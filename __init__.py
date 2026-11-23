"""ComfyUI KK Go Agent: independent nodes backed by one local Go process."""
from __future__ import annotations

import atexit
from .nodes import NODE_CLASS_MAPPINGS, NODE_DISPLAY_NAME_MAPPINGS
from .kkagent_bridge import BRIDGE
from .kkagent_bridge.go_backend import shutdown_owned_service
from .routes.api import ensure_registered

WEB_DIRECTORY = "./web"
ensure_registered()


def _shutdown():
    BRIDGE.close()
    shutdown_owned_service()


atexit.register(_shutdown)
print("[KK Go Agent] loaded; standalone Go backend; nodes=" + ",".join(NODE_CLASS_MAPPINGS))

__all__ = ["NODE_CLASS_MAPPINGS", "NODE_DISPLAY_NAME_MAPPINGS", "WEB_DIRECTORY"]
