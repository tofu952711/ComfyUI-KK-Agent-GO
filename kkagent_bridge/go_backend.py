"""KK Go Agent local service adapter; no DSH or Harness dependency."""
from __future__ import annotations

import contextlib
import hashlib
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

try:
    from .errors import BridgeError
except ImportError:
    class BridgeError(RuntimeError):
        def __init__(self, code, message):
            self.code = code
            super().__init__(message)

_SERVICE_LOCK = threading.Lock()
_PROCESS = None


_CONFIG_LOCK = threading.Lock()


def _home():
    # The distributable carries its own runtime; no environment setup required.
    return Path(__file__).resolve().parents[1] / "runtime"


def _settings():
    root = _home()
    path = root / "config.local.json"
    with _CONFIG_LOCK:
        if not path.is_file():
            with _startup_file_lock(root):
                if not path.is_file():
                    from .paths import bridge_data_dir
                    config = json.loads((root / "config.example.json").read_text(encoding="utf-8-sig"))
                    config["data_dir"] = str(bridge_data_dir() / "go-runtime")
                    config["auth_token"] = uuid.uuid4().hex
                    temporary = path.with_name(".config-" + uuid.uuid4().hex)
                    temporary.write_text(json.dumps(config, ensure_ascii=False, indent=2), encoding="utf-8")
                    os.replace(temporary, path)
        config = json.loads(path.read_text(encoding="utf-8-sig"))
    address = config.get("listen", "127.0.0.1:8191")
    parsed = urllib.parse.urlsplit("http://" + address)
    if parsed.hostname not in ("127.0.0.1", "::1"):
        raise BridgeError("go_config", "Go 服务必须使用本机回环地址")
    return root, config, "http://" + address, config.get("auth_token", "")


def _request(method, path, body=None, timeout=10):
    _, _, base, token = _settings()
    raw = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(base + path, raw, headers, method=method)
    # Local communication must not use the user's internet proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=timeout) as response:
            return json.load(response)
    except urllib.error.HTTPError as exc:
        try:
            message = json.loads(exc.read(8192)).get("error", f"HTTP {exc.code}")
        except (ValueError, AttributeError):
            message = f"HTTP {exc.code}"
        raise BridgeError("go_service", message) from None


def _healthy():
    try:
        status = _request("GET", "/health", timeout=1)
        return status.get("name") == "KKagent-go" and status.get("api_version") == 1
    except (OSError, ValueError, BridgeError):
        return False


@contextlib.contextmanager
def _startup_file_lock(root):
    lock_path = root / "data" / "startup.lock"
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a+b") as handle:
        handle.seek(0, 2)
        if handle.tell() == 0:
            handle.write(b"0")
            handle.flush()
        if os.name == "nt":
            import msvcrt
            deadline = time.monotonic() + 20
            while True:
                try:
                    handle.seek(0)
                    msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
                    break
                except OSError:
                    if time.monotonic() >= deadline:
                        raise BridgeError("go_startup", "等待 Go 服务启动锁超时")
                    time.sleep(.1)
            try:
                yield
            finally:
                handle.seek(0)
                msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
        else:
            import fcntl
            fcntl.flock(handle, fcntl.LOCK_EX)
            try:
                yield
            finally:
                fcntl.flock(handle, fcntl.LOCK_UN)


def ensure_service():
    global _PROCESS
    if _healthy():
        return
    root, _, _, _ = _settings()
    with _SERVICE_LOCK, _startup_file_lock(root):
        if _healthy():
            return
        executable = root / "bin" / ("kkagent.exe" if os.name == "nt" else "kkagent")
        if not executable.is_file():
            raise BridgeError("go_missing", "Go 可执行文件缺失，请使用完整插件包")
        args = [str(executable)]
        config = root / "config.local.json"
        if config.is_file():
            args += ["-config", str(config)]
        log_path = root / "data" / "service.log"
        with log_path.open("ab") as log:
            _PROCESS = subprocess.Popen(args, cwd=root, stdin=subprocess.DEVNULL,
                                        stdout=log, stderr=log,
                                        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if _healthy():
                return
            if _PROCESS.poll() is not None:
                raise BridgeError("go_startup", "Go 服务启动失败，请查看 data/service.log")
            time.sleep(.1)
        raise BridgeError("go_startup", "Go 服务启动超时")


def sdk_info():
    return {"installed": True, "compatible": True, "backend": "go", "version": "0.1.4", "bundled": False}


class SDKRuntime:
    external = True

    def __init__(self, cfg):
        self.cfg = cfg
        # Match DSH's per-workflow home, not the shared Go service data directory.
        workflow_home = cfg.get("kkgo_home") or cfg.get("dsh_home")
        self._session_namespace = (
            "home-" + hashlib.sha256(os.path.normcase(os.path.abspath(workflow_home)).encode("utf-8")).hexdigest()
            if workflow_home else "runtime-" + uuid.uuid4().hex
        )
        self._alive = False
        self._active = ""
        self._run_id = ""
        self._turn_lock = threading.Lock()
        self._cancelled = threading.Event()
        self.last_diagnostics = {}

    @property
    def alive(self):
        return self._alive

    @property
    def pid(self):
        return _PROCESS.pid if _PROCESS is not None and _PROCESS.poll() is None else None

    @property
    def active_session(self):
        return self._active

    def start(self):
        ensure_service()
        self._alive = True

    def stop(self, expected=None):
        # Closing one Bridge must not stop the service shared by other agents.
        self.cancel()
        self._alive = False

    def cancel(self):
        if not self._active:
            return False
        self._cancelled.set()
        if self._run_id:
            try:
                _request("POST", f"/v1/runs/{self._run_id}/cancel", {}, timeout=2)
            except (OSError, BridgeError):
                pass
        return True

    def run(self, session, blocks, timeout, on_progress=None):
        if not self._turn_lock.acquire(blocking=False):
            raise BridgeError("busy", "当前会话已有任务运行")
        deadline = time.monotonic() + timeout
        try:
            self._cancelled.clear()
            self._active = session
            self.start()
            cfg = self.cfg
            if not cfg.get("video_system_prompt", "").strip():
                raise BridgeError("go_scope", "请填写本阶段系统提示词；A/B规则分别填写在节点对应文本框")
            protocol = cfg.get("api_protocol", "openai-completions")
            base = cfg.get("base_url", "")
            role = "developer" if (urllib.parse.urlsplit(base).hostname == "api.zdjapi.com"
                                     and cfg.get("model") == "gpt-6.1-sol"
                                     and protocol in ("openai-completions", "codez")) else "system"
            key = cfg.get("api_key", "")
            if not key:
                try:
                    from .config import environment_api_key
                    key = environment_api_key(cfg)
                except ImportError:
                    pass
            images = [{"type": "image", "data": b["data"], "mimeType": b.get("mimeType", "image/png")}
                      for b in blocks if b.get("type") == "image"]
            task = (cfg["video_user_prompt"] if "video_user_prompt" in cfg else
                    "\n".join(b.get("text", "") for b in blocks if b.get("type") == "text"))
            spec = {"client_request_id": uuid.uuid4().hex, "session_id": session,
                    "session_namespace": self._session_namespace,
                    "system_rules": cfg["video_system_prompt"], "task": task,
                    "media_evidence": cfg.get("video_media_evidence", ""), "content": images,
                    "provider": {"protocol": protocol, "base_url": base, "model": cfg["model"],
                                 "api_key": key, "max_tokens": int(cfg.get("max_tokens", 32768)),
                                 "reasoning_effort": cfg.get("reasoning_effort", "auto"),
                                 "instruction_role": role, "stream_usage": bool(cfg.get("stream_usage")),
                                 "request_timeout_seconds": max(1, int(timeout)), "retry_count": 3},
                    "limits": {"timeout_seconds": max(1, min(7200, int(timeout))),
                               "max_model_rounds": int(cfg.get("go_max_rounds", 20)),
                               "max_tool_calls": int(cfg.get("go_max_tool_calls", 40)),
                               "tool_timeout_seconds": max(1, int(timeout))}}
            # Match the existing DSH exact-inputs chat pipeline at the wire boundary.
            if protocol == "openai-completions" or (protocol == "codez" and
                    (urllib.parse.urlsplit(base).hostname or "").removeprefix("api.") == "zdjapi.com"):
                spec["provider"]["wire_compatibility"] = "dsh-video"
            mcp = cfg.get("media_mcp")
            if mcp:
                allowed_keys = ("serverName", "command", "args", "env", "cwd", "startupTimeoutMs", "toolCallTimeoutMs")
                media = {k: mcp[k] for k in allowed_keys if k in mcp}
                phase = "reverse"
                args = media.get("args", [])
                try:
                    output = Path(args[args.index("--output") + 1])
                    policy_file = output / "duration_policy.json"
                    if policy_file.is_file():
                        phase = json.loads(policy_file.read_text(encoding="utf-8")).get("phase", "reverse")
                except (ValueError, IndexError, OSError):
                    pass
                if any(str(arg).endswith("b_media_server.py") for arg in args):
                    phase = "reverse"
                spec["tools_enabled"] = True
                spec["tool_scope"] = {"id": hashlib.sha256(json.dumps(media, sort_keys=True).encode()).hexdigest(),
                                      "phase": phase, "mcp": media}
            if self._cancelled.is_set():
                raise BridgeError("cancelled", "任务已停止")
            # A fixed client ID makes a transport retry safe if creation succeeded.
            try:
                snapshot = _request("POST", "/v1/runs", spec, timeout=min(60, max(1, timeout)))
            except (OSError, TimeoutError):
                if self._cancelled.is_set():
                    raise BridgeError("cancelled", "任务已停止")
                snapshot = _request("POST", "/v1/runs", spec, timeout=min(60, max(1, timeout)))
            self._run_id = snapshot["run_id"]
            cursor = 0
            monitor = cfg.get("_token_monitor")
            while True:
                if self._cancelled.is_set() or time.monotonic() >= deadline:
                    _request("POST", f"/v1/runs/{self._run_id}/cancel", {}, timeout=2)
                    raise BridgeError("cancelled" if self._cancelled.is_set() else "timeout", "任务已停止或超时")
                events = _request("GET", f"/v1/runs/{self._run_id}/events?format=json&after={cursor}")["events"]
                for event in events:
                    cursor = event["seq"]
                    if on_progress:
                        on_progress(event["type"])
                    if event["type"] == "llm/usage" and monitor:
                        monitor.handle(event["data"]["record"])
                snapshot = _request("GET", f"/v1/runs/{self._run_id}")
                if snapshot["status"] in ("completed", "failed", "cancelled", "timed_out"):
                    if cursor < snapshot.get("last_event_seq", 0):
                        continue
                    break
                time.sleep(.2)
            if snapshot["status"] != "completed":
                raise BridgeError(snapshot["status"], snapshot.get("error", "Go任务失败"))
            result = snapshot["result"]
            result["kind"] = "prompt"
            self.last_diagnostics = {"finish_reason": result["finish_reason"],
                                     "token_speed": result.get("token_speed", []), "backend": "go"}
            return result
        finally:
            if self._run_id:
                try:
                    _request("POST", f"/v1/runs/{self._run_id}/cancel", {}, timeout=2)
                except (OSError, BridgeError):
                    pass
            self._active = self._run_id = ""
            self._turn_lock.release()


class SDKChannel:
    def __init__(self, name, cfg, proc):
        self.name, self.cfg, self.proc = name, cfg, proc
        self._connected = False

    def connect(self):
        self.proc.start()
        self._connected = True

    @property
    def ready(self):
        return self._connected and self.proc.alive

    def ask(self, session_id, blocks, timeout=None, on_progress=None):
        self._connected = True
        return self.proc.run(session_id, blocks, float(timeout or self.cfg.get("request_timeout_s", 300)), on_progress)

    def close(self):
        self._connected = False


def shutdown_owned_service():
    global _PROCESS
    process = _PROCESS
    if process is None or process.poll() is not None:
        return
    if os.name == "nt":
        subprocess.run(["taskkill.exe", "/PID", str(process.pid), "/T", "/F"],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0), timeout=10)
    else:
        process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
    _PROCESS = None
