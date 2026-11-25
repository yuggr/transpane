from __future__ import annotations

import asyncio
import json
from pathlib import Path

import httpx

from transpane.config import load_config
from transpane.ui import MessageView, TranspaneApp

BASE = "http://127.0.0.1:1234/v1"


def meta(thread_id: str, mode: str = "paginated") -> dict:
    return {
        "timestamp": "2026-11-25T01:00:00Z",
        "type": "session_meta",
        "payload": {
            "id": thread_id,
            "cwd": "/tmp/demo",
            "cli_version": "0.169.0",
            "history_mode": mode,
        },
    }


def user(thread_id: str, text: str) -> dict:
    return {
        "timestamp": "2026-11-25T01:00:01Z",
        "type": "event_msg",
        "payload": {
            "type": "item_completed",
            "item": {
                "type": "UserMessage",
                "id": f"u-{thread_id}",
                "content": [{"type": "text", "text": text}],
            },
        },
    }


def agent(thread_id: str, item_id: str, text: str, phase: str = "commentary") -> dict:
    return {
        "timestamp": "2026-11-25T01:00:02Z",
        "type": "event_msg",
        "payload": {
            "type": "item_completed",
            "item": {
                "type": "AgentMessage",
                "id": item_id,
                "phase": phase,
                "content": [{"type": "Text", "text": text}],
            },
        },
    }


def write_session(root: Path, thread_id: str, records: list[dict]) -> Path:
    path = root / "2026" / "11" / "25" / f"rollout-{thread_id}.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb") as handle:
        for record in records:
            handle.write(json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n")
    return path


def append(path: Path, records: list[dict]) -> None:
    with path.open("ab") as handle:
        for record in records:
            handle.write(json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n")


def claude_user(
    session: str, text: str, uuid: str = "00000000-0000-4000-8000-000000000001"
) -> dict:
    return {
        "type": "user",
        "sessionId": session,
        "uuid": uuid,
        "timestamp": "2026-11-25T01:00:01Z",
        "cwd": "/tmp/demo",
        "version": "2.1.285",
        "message": {"role": "user", "content": text},
    }


def claude_agent(
    session: str, text: str, uuid: str = "00000000-0000-4000-8000-000000000002"
) -> dict:
    return {
        "type": "assistant",
        "sessionId": session,
        "uuid": uuid,
        "timestamp": "2026-11-25T01:00:02Z",
        "cwd": "/tmp/demo",
        "version": "2.1.285",
        "message": {
            "id": f"msg-{uuid}",
            "role": "assistant",
            "model": "claude-sonnet-5",
            "content": [{"type": "text", "text": text}],
        },
    }


def write_claude_session(root: Path, session: str, records: list[dict]) -> Path:
    path = root / "demo" / f"{session}.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb") as handle:
        for record in records:
            handle.write(json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n")
    return path


def make_config(tmp_path):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text(
        '[translation]\nbase_url = "http://127.0.0.1:1234/v1"\nmodel = "plamo-3-translate-2b"\n',
        encoding="utf-8",
    )
    env = {
        "CODEX_HOME": str(tmp_path / "codex"),
        "CLAUDE_CONFIG_DIR": str(tmp_path / "claude"),
        "XDG_CONFIG_HOME": str(tmp_path / "config"),
        "XDG_CACHE_HOME": str(tmp_path / "cache"),
    }
    return load_config(env)


def ok_response(text: str) -> httpx.Response:
    return httpx.Response(
        200,
        json={
            "choices": [
                {"message": {"role": "assistant", "content": text}, "finish_reason": "stop"}
            ]
        },
    )


def translating_handler(request: httpx.Request) -> httpx.Response:
    if request.method == "GET":
        return httpx.Response(200, json={"data": [{"id": "plamo-3-translate-2b"}]})
    body = json.loads(request.content)
    return ok_response("訳:" + body["messages"][1]["content"])


class FakeListener:
    def __init__(self) -> None:
        self.resets: list[tuple] = []
        self.added: list[MessageView] = []
        self.prepended: list[MessageView] = []
        self.updated: list[MessageView] = []
        self.statuses: list = []

    def on_reset(self, session, views):
        self.resets.append((session, list(views)))

    def on_added(self, view):
        self.added.append(view)

    def on_prepended(self, views):
        self.prepended.extend(views)

    def on_updated(self, view):
        self.updated.append(view)

    def on_status(self, status):
        self.statuses.append(status)


async def wait_until(predicate, timeout: float = 5.0) -> None:
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while loop.time() < deadline:
        if predicate():
            return
        await asyncio.sleep(0.01)
    raise AssertionError("condition not met in time")


def screen_text(app: TranspaneApp) -> str:
    """Return what the terminal compositor actually paints."""
    strips = app.screen._compositor.render_strips()
    return "\n".join(strip.text.rstrip() for strip in strips)
