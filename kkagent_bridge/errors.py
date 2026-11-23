"""Stable error codes for the ComfyUI <-> DeepSeek Harness bridge."""

from __future__ import annotations

# The node's status socket is read by people, not by code, so every code gets a
# plain-Chinese label. The raw code stays in brackets for log correlation.
_CODE_LABELS = {
    "missing_sdk": "本地运行时不完整，请重新解压 Windows x64 免安装包",
    "sdk_version": "运行时版本不匹配，请重新解压免安装包",
    "sdk_worker": "本地运行时通信异常，请查看面板详情",
    "missing_api_key": "请先在 KKGO 连接设置中填写 API Key",
    "busy": "已有任务运行，请等待或停止",
    "cancelled": "任务已停止",
    "model_error": "模型请求失败，请查看面板错误详情",
    "empty_response": "模型未返回内容",
    "output_limit": "模型输出达到上限，结果不完整",
    "disconnected": "与 harness 断开",
    "pipe_unavailable": "找不到 harness 管道",
    "process_exit": "harness 已退出",
    "not_running": "harness 未运行",
    "not_ready": "harness 尚未就绪",
    "spawn_failed": "harness 启动失败",
    "missing_cli": "找不到 harness 程序",
    "missing_node": "找不到 node 可执行文件",
    "timeout": "等待 harness 响应超时",
    "write_failed": "写入 harness 失败",
    "bad_handshake": "harness 握手异常",
    "empty_prompt": "请求内容为空",
    "ask_failed": "请求 harness 失败",
    "rpc_error": "harness 返回错误",
    "bad_image": "图片数据格式不对",
    "no_frames": "没有可用的画面",
    "no_request": "没有请求内容",
    "purge_op": "清洗操作名无法识别",
    "purge_timeout": "清洗操作超时",
    "purge_spawn": "清洗进程启动失败",
}


class BridgeError(RuntimeError):
    """A bridge failure carrying a stable machine-readable code."""

    def __init__(self, code: str, message: str = "") -> None:
        self.code = code
        super().__init__(f"{code}: {message}" if message else code)

    def __str__(self) -> str:  # pragma: no cover - trivial
        return self.args[0]


def error_label(code: str) -> str:
    """Plain-Chinese label for one error code, falling back to the code itself."""
    return _CODE_LABELS.get(code, code)


def error_status(exc: BaseException) -> str:
    """Render any exception as a node `status` string, readable at a glance."""
    if isinstance(exc, BridgeError):
        return f"失败：{error_label(exc.code)}（{exc.code}）"
    return f"失败：{type(exc).__name__}"
