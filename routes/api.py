from __future__ import annotations
import asyncio
from ..kkagent_bridge import BRIDGE

try:
    from aiohttp import web
    from server import PromptServer
except ImportError:
    web = PromptServer = None

_REGISTERED = False
_LOCAL = {"127.0.0.1", "::1", "::ffff:127.0.0.1", "localhost", None}


def ensure_registered():
    global _REGISTERED
    if _REGISTERED:
        return True
    if PromptServer is None or getattr(PromptServer, "instance", None) is None:
        return False
    from ..kkagent_bridge.cancellation import install_interrupt_hook
    install_interrupt_hook(PromptServer.instance)
    routes = PromptServer.instance.routes

    @routes.get("/kkagent_go/status")
    async def status(request):
        if request.remote not in _LOCAL:
            return web.json_response({"error": "forbidden"}, status=403)
        from ..kkagent_bridge.go_backend import _healthy
        return web.json_response({"backend": "go", "runtime_ready": await asyncio.to_thread(_healthy),
                                  "status": BRIDGE.status(), "node_ids": ["KKGO_FullVideoAgent", "KKGO_VideoSegments", "KKGO_VideoReverse"]})

    @routes.get("/kkagent_go/token_speed")
    async def token_speed(request):
        if request.remote not in _LOCAL:
            return web.json_response({"error": "forbidden"}, status=403)
        from ..kkagent_bridge.token_speed import latest_snapshot
        value = await asyncio.to_thread(latest_snapshot, request.query.get("node_id", ""))
        return web.json_response({"result": value})

    @routes.get("/kkagent_go/last")
    async def last(request):
        if request.remote not in _LOCAL:
            return web.json_response({"error": "forbidden"}, status=403)
        result = BRIDGE.last(request.query.get("node_id", ""))
        return web.json_response({"found": bool(result), "result": result})

    _REGISTERED = True
    return True
