"""Install the complete Windows GO bundle without network downloads."""
from pathlib import Path
import hashlib
import json
import os
import platform
import subprocess
import sys

ROOT = Path(__file__).resolve().parent


def main():
    if sys.platform != "win32" or platform.machine().lower() not in ("amd64", "x86_64"):
        raise RuntimeError("This full bundle requires Windows x64.")
    if sys.implementation.name != "cpython" or sys.version_info[:2] not in ((3, 10), (3, 11), (3, 12), (3, 13)):
        raise RuntimeError("Offline wheels support CPython 3.10-3.13 x64.")
    manifest = ROOT / "bundle-manifest.json"
    if not manifest.is_file():
        raise RuntimeError("Use the complete Windows ZIP from Releases; the source ZIP omits tool binaries and models.")
    for name, metadata in json.loads(manifest.read_text(encoding="utf-8"))["files"].items():
        path = ROOT / name
        if not path.is_file():
            raise RuntimeError("Missing bundle file: " + name)
        with path.open("rb") as stream:
            digest = hashlib.file_digest(stream, "sha256").hexdigest() if hasattr(hashlib, "file_digest") else None
        if digest is None:
            sha = hashlib.sha256()
            with path.open("rb") as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    sha.update(chunk)
            digest = sha.hexdigest()
        if digest != metadata["sha256"]:
            raise RuntimeError("Bundle checksum mismatch: " + name)
    env = os.environ.copy()
    env.update(PIP_NO_INDEX="1", PIP_DISABLE_PIP_VERSION_CHECK="1", PIP_CONFIG_FILE=os.devnull)
    subprocess.run([sys.executable, "-m", "pip", "--isolated", "install", "--no-index",
                    "--find-links", str(ROOT / "vendor" / "wheels"), "--only-binary=:all:",
                    "--disable-pip-version-check", "-r", str(ROOT / "requirements.txt")], env=env, check=True)
    for name in ("ffmpeg.exe", "ffprobe.exe"):
        subprocess.run([str(ROOT / "vendor" / "ffmpeg" / "windows-x64" / name), "-version"],
                       stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=15, check=True,
                       creationflags=subprocess.CREATE_NO_WINDOW)
    import sherpa_onnx
    print("Windows GO offline bundle is ready. Restart ComfyUI to load the plugin.")


if __name__ == "__main__":
    main()
