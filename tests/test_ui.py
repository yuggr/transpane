from __future__ import annotations

import asyncio
import json
from pathlib import Path

import httpx
import pytest
from textual.widgets import Static

from codex_ja.config import load_config
from codex_ja.models import TranslationState
from codex_ja.session import discover_sessions, select_session
from codex_ja.ui import CodexJaApp, MessageView, Monitor

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


def make_config(tmp_path):
    config_dir = tmp_path / "config" / "codex-ja"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text(
        '[translation]\nbase_url = "http://127.0.0.1:1234/v1"\nmodel = "plamo-3-translate-2b"\n',
        encoding="utf-8",
    )
    env = {
        "CODEX_HOME": str(tmp_path / "codex"),
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


def screen_text(app: CodexJaApp) -> str:
    """Return what the terminal compositor actually paints."""
    strips = app.screen._compositor.render_strips()
    return "\n".join(strip.text.rstrip() for strip in strips)


@pytest.fixture
def session_file(tmp_path):
    config = make_config(tmp_path)
    path = write_session(
        config.sessions_root,
        "thread-1",
        [
            meta("thread-1"),
            user("thread-1", "保存処理を調べて"),
            agent("thread-1", "a1", "I will inspect the storage layer."),
        ],
    )
    sessions = discover_sessions(config.sessions_root)
    return config, path, select_session(sessions, "thread-1")


async def test_monitor_translates_and_preserves_order(tmp_path):
    config = make_config(tmp_path)
    path = write_session(
        config.sessions_root,
        "t",
        [
            meta("t"),
            user("t", "保存処理を調べて"),
            agent("t", "a1", "I will inspect the storage layer."),
            agent("t", "a2", "The tests passed.", phase="final_answer"),
        ],
    )
    session = select_session(discover_sessions(config.sessions_root), "t")
    before_bytes = path.read_bytes()
    before_mtime = path.stat().st_mtime_ns
    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: all(not view.pending for view in monitor.views))
    views = monitor.views
    assert [view.message.message_id for view in views] == ["u-t", "a1", "a2"]
    assert views[0].state is TranslationState.SKIPPED
    assert views[0].display_text() == "保存処理を調べて"
    assert views[1].state is TranslationState.TRANSLATED
    assert "訳:I will inspect the storage layer." in views[1].display_text()
    assert views[2].message.kind.value == "final"
    await monitor.close()
    assert path.read_bytes() == before_bytes
    assert path.stat().st_mtime_ns == before_mtime


async def test_monitor_deduplicates_revision_and_updates_in_place(tmp_path):
    config = make_config(tmp_path)
    path = write_session(config.sessions_root, "t", [meta("t"), agent("t", "a1", "Version one.")])
    session = select_session(discover_sessions(config.sessions_root), "t")
    calls = []

    def handler(request):
        calls.append(1)
        return translating_handler(request)

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: monitor.views[0].state is TranslationState.TRANSLATED)
    assert len(calls) == 1

    append(path, [agent("t", "a1", "Version one.")])
    await asyncio.sleep(0.6)
    assert len(calls) == 1
    assert len(monitor.views) == 1

    append(path, [agent("t", "a1", "Version two.")])
    await wait_until(
        lambda: "訳:Version two." in monitor.views[0].display_text(),
        timeout=5,
    )
    assert len(monitor.views) == 1
    assert monitor.views[0].message.original == "Version two."
    assert len(calls) == 2
    await monitor.close()


async def test_monitor_initial_window_and_history(tmp_path):
    config = make_config(tmp_path)
    records = [meta("t")]
    for index in range(60):
        records.append(agent("t", f"a{index}", f"Message number {index}."))
    write_session(config.sessions_root, "t", records)
    session = select_session(discover_sessions(config.sessions_root), "t")
    calls = []

    def handler(request):
        calls.append(1)
        return translating_handler(request)

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: len(calls) == 50)
    assert len(monitor.views) == 50
    assert monitor.views[-1].message.message_id == "a59"
    assert monitor.views[0].message.message_id == "a10"

    await monitor.add_history()
    await wait_until(lambda: len(calls) == 60)
    assert len(monitor.views) == 60
    assert monitor.views[0].message.message_id == "a0"
    await monitor.close()


async def test_monitor_switch_drops_stale_results(tmp_path):
    config = make_config(tmp_path)
    write_session(config.sessions_root, "a", [meta("a"), agent("a", "a1", "Slow A payload.")])
    write_session(config.sessions_root, "b", [meta("b"), agent("b", "b1", "B payload.")])
    sessions = discover_sessions(config.sessions_root)
    session_a = select_session(sessions, "a")
    session_b = select_session(sessions, "b")

    gate = asyncio.Event()
    started = asyncio.Event()

    async def handler(request):
        body = json.loads(request.content)
        text = body["messages"][1]["content"]
        if "Slow A" in text:
            started.set()
            await gate.wait()
            return ok_response("訳:A")
        return ok_response("訳:B")

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session_a)
    await asyncio.wait_for(started.wait(), timeout=5)

    await monitor.switch(session_b)
    gate.set()
    await wait_until(lambda: monitor.views and "訳:B" in monitor.views[0].display_text(), timeout=5)
    await asyncio.sleep(0.05)
    assert monitor.session.thread_id == "b"
    assert len(monitor.views) == 1
    assert "訳:A" not in monitor.views[0].display_text()
    await monitor.close()


async def test_monitor_unsupported_mode_sends_nothing(tmp_path):
    config = make_config(tmp_path)
    write_session(
        config.sessions_root, "t", [meta("t", mode="chunked"), agent("t", "a1", "Secret.")]
    )
    session = select_session(discover_sessions(config.sessions_root), "t")
    calls = []

    def handler(request):
        calls.append(1)
        return translating_handler(request)

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await asyncio.sleep(0.1)
    assert monitor.views == []
    assert calls == []
    assert any("未対応" in (status.note or "") for status in listener.statuses)
    await monitor.close()


async def test_monitor_retry_only_resends_failed_units(tmp_path):
    config = make_config(tmp_path)
    write_session(config.sessions_root, "t", [meta("t"), agent("t", "a1", "Retry me.")])
    session = select_session(discover_sessions(config.sessions_root), "t")
    fail = {"value": True}
    calls = []

    def handler(request):
        if request.method != "POST":
            return translating_handler(request)
        calls.append(1)
        if fail["value"]:
            return httpx.Response(500, json={})
        return translating_handler(request)

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: monitor.views[0].state is TranslationState.FAILED)
    assert len(calls) == 1

    fail["value"] = False
    await monitor.retry()
    await wait_until(lambda: monitor.views[0].state is TranslationState.TRANSLATED)
    assert len(calls) == 2
    await monitor.close()


async def test_app_shows_original_first_then_translation(session_file):
    config, _path, session = session_file
    gate = asyncio.Event()

    async def handler(request):
        if request.method == "GET":
            return httpx.Response(200, json={"data": [{"id": "plamo-3-translate-2b"}]})
        await gate.wait()
        body = json.loads(request.content)
        return ok_response("訳:" + body["messages"][1]["content"])

    app = CodexJaApp(config, initial_session=session, transport=httpx.MockTransport(handler))
    async with app.run_test(size=(100, 40)) as pilot:
        await pilot.pause()
        await wait_until(lambda: len(app._monitor.views) == 2)
        agent_view = app._monitor.views[1]
        widget = app._widgets[("thread-1", "a1")]
        body = widget.query_one("Markdown")
        assert body._open_links is False
        assert agent_view.state in (TranslationState.QUEUED, TranslationState.TRANSLATING)

        # The body must fit inside the message widget and be painted on screen.
        assert widget.size.height >= body.size.height
        assert widget.region.contains_region(body.region)
        assert "I will inspect the storage layer." in screen_text(app)

        gate.set()
        await wait_until(lambda: agent_view.state is TranslationState.TRANSLATED)
        await pilot.pause()
        assert "訳:I will inspect" in screen_text(app)

        await pilot.press("o")
        await pilot.pause()
        rendered = screen_text(app)
        assert "訳:" not in rendered
        assert "I will inspect the storage layer." in rendered


async def test_app_renders_bodies_with_many_messages(tmp_path):
    config = make_config(tmp_path)
    records = [meta("t")]
    for index in range(50):
        records.append(agent("t", f"a{index}", f"Rendered message body {index}."))
    write_session(config.sessions_root, "t", records)
    session = select_session(discover_sessions(config.sessions_root), "t")

    app = CodexJaApp(
        config, initial_session=session, transport=httpx.MockTransport(translating_handler)
    )
    async with app.run_test(size=(80, 24)) as pilot:
        await wait_until(
            lambda: (
                len(app._monitor.views) == 50
                and all(not view.pending for view in app._monitor.views)
            )
        )
        await pilot.pause()
        await pilot.pause()

        last = app._widgets[("t", "a49")]
        body = last.query_one("Markdown")
        assert last.size.height > 1
        assert last.region.contains_region(body.region)

        rendered = screen_text(app)
        assert "訳:Rendered message body 49." in rendered
        assert "進捗報告" in rendered

        await pilot.press("o")
        await pilot.pause()
        rendered = screen_text(app)
        assert "Rendered message body 49." in rendered
        assert "訳:Rendered message body 49." not in rendered
        assert "原文" in rendered


async def test_app_status_and_keys_render(session_file):
    config, _path, session = session_file
    app = CodexJaApp(
        config, initial_session=session, transport=httpx.MockTransport(translating_handler)
    )
    async with app.run_test(size=(120, 40)) as pilot:
        await wait_until(lambda: len(app._monitor.views) == 2)
        await pilot.pause()
        status = str(app.query_one("#status", Static).content)
        assert "PLaMo" in status
        assert "最終更新" in status
        footer = str(app.query_one("#footer", Static).content)
        assert "s:" in footer and "q:" in footer


async def test_app_status_distinguishes_unknown_connected_and_failed(tmp_path):
    config = make_config(tmp_path)
    path = write_session(config.sessions_root, "t", [meta("t"), agent("t", "a1", "First.")])
    session = select_session(discover_sessions(config.sessions_root), "t")
    gate = asyncio.Event()
    fail = {"value": False}

    async def handler(request):
        if request.method == "GET":
            return httpx.Response(200, json={"data": [{"id": "plamo-3-translate-2b"}]})
        await gate.wait()
        if fail["value"]:
            return httpx.Response(503, json={})
        return translating_handler(request)

    app = CodexJaApp(config, initial_session=session, transport=httpx.MockTransport(handler))
    async with app.run_test(size=(120, 40)) as pilot:
        await wait_until(lambda: len(app._monitor.views) == 1)
        await pilot.pause()
        assert "PLaMo: 未確認" in str(app.query_one("#status", Static).content)

        gate.set()
        await wait_until(lambda: app._monitor.views[0].state is TranslationState.TRANSLATED)
        await pilot.pause()
        assert "PLaMo: 接続成功" in str(app.query_one("#status", Static).content)

        fail["value"] = True
        append(path, [agent("t", "a2", "Second.")])
        await wait_until(
            lambda: (
                len(app._monitor.views) == 2
                and app._monitor.views[1].state is TranslationState.FAILED
            )
        )
        await pilot.pause()
        assert "PLaMo: 通信失敗" in str(app.query_one("#status", Static).content)


async def test_app_scroll_holds_position_until_end(tmp_path):
    config = make_config(tmp_path)
    records = [meta("t")]
    for index in range(40):
        records.append(agent("t", f"a{index}", f"Message {index}."))
    path = write_session(config.sessions_root, "t", records)
    session = select_session(discover_sessions(config.sessions_root), "t")
    app = CodexJaApp(
        config, initial_session=session, transport=httpx.MockTransport(translating_handler)
    )
    async with app.run_test(size=(80, 24)) as pilot:
        await wait_until(lambda: len(app._monitor.views) == 40)
        await pilot.pause()
        container = app.query_one("#messages")
        container.scroll_to(y=0, animate=False)
        await pilot.pause()
        assert app._auto_scroll is False

        append(path, [agent("t", "a-new", "New message while reading.")])
        await wait_until(lambda: len(app._monitor.views) == 41, timeout=5)
        await pilot.pause()
        assert container.scroll_y == 0

        await pilot.press("end")
        await pilot.pause()
        assert app._auto_scroll is True
        assert container.is_vertical_scroll_end


def test_initial_state_of_plain_and_secret_documents():
    from codex_ja.markdown import protect_document
    from codex_ja.ui import initial_state

    assert initial_state(protect_document("Hello world.")) is TranslationState.ORIGINAL
    assert initial_state(protect_document("これは日本語です。")) is TranslationState.SKIPPED
    assert (
        initial_state(protect_document("token sk-abcdefghijklmnopqrstuvwx"))
        is TranslationState.SKIPPED
    )
    long_line = "x" * 1500
    assert initial_state(protect_document(long_line)) is TranslationState.PARTIAL


async def test_message_widget_sanitizes_control_codes():
    from codex_ja.markdown import protect_document
    from codex_ja.models import Message, MessageKind

    message = Message(
        thread_id="t",
        message_id="m",
        revision="r",
        sequence=0,
        timestamp="bad",
        kind=MessageKind.ASSISTANT,
        original="Safe \x1b[31mred\x1b[0m \x1b]0;title\x07text",
    )
    view = MessageView(
        message=message,
        doc=protect_document(message.original),
        state=TranslationState.SKIPPED,
    )
    # The sanitizer is pure; the widget is exercised through the app tests.
    from codex_ja.ui import sanitize_for_display

    cleaned = sanitize_for_display(view.display_text())
    assert "\x1b" not in cleaned
    assert "Safe" in cleaned and "red" in cleaned


async def test_monitor_never_sends_response_items(tmp_path):
    config = make_config(tmp_path)
    write_session(
        config.sessions_root,
        "t",
        [
            meta("t"),
            {
                "type": "response_item",
                "payload": {
                    "type": "message",
                    "role": "assistant",
                    "content": [{"type": "output_text", "text": "Model-facing only."}],
                },
            },
        ],
    )
    session = select_session(discover_sessions(config.sessions_root), "t")
    calls = []

    def handler(request):
        calls.append(1)
        return translating_handler(request)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await asyncio.sleep(0.1)
    assert calls == []
    assert monitor.views == []
    await monitor.close()


async def test_monitor_shares_cache_for_repeated_text_with_different_ids(tmp_path):
    config = make_config(tmp_path)
    write_session(
        config.sessions_root,
        "t",
        [meta("t"), agent("t", "a1", "Repeated sentence."), agent("t", "a2", "Repeated sentence.")],
    )
    session = select_session(discover_sessions(config.sessions_root), "t")
    calls = []

    def handler(request):
        if request.method != "POST":
            return translating_handler(request)
        calls.append(1)
        return translating_handler(request)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(
        lambda: (
            len(monitor.views) == 2
            and all(view.state is TranslationState.TRANSLATED for view in monitor.views)
        )
    )
    assert len(calls) == 1
    assert [view.message.message_id for view in monitor.views] == ["a1", "a2"]
    await monitor.close()


async def test_monitor_secret_lines_are_not_sent(tmp_path):
    config = make_config(tmp_path)
    write_session(
        config.sessions_root,
        "t",
        [
            meta("t"),
            agent(
                "t",
                "a1",
                "Please translate this sentence.\n\nThe token is sk-abcdefghijklmnopqrstuvwx.",
            ),
        ],
    )
    session = select_session(discover_sessions(config.sessions_root), "t")
    sent = []

    def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        body = json.loads(request.content)
        sent.append(body["messages"][1]["content"])
        return translating_handler(request)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: not monitor.views[0].pending)
    assert sent == ["Please translate this sentence."]
    assert "sk-" not in " ".join(sent)
    await monitor.close()


async def test_monitor_rebuilds_after_file_replacement(tmp_path):
    import os

    config = make_config(tmp_path)
    path = write_session(config.sessions_root, "t", [meta("t"), agent("t", "a1", "Original one.")])
    session = select_session(discover_sessions(config.sessions_root), "t")
    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: monitor.views[0].state is TranslationState.TRANSLATED)

    replacement = tmp_path / "replacement.jsonl"
    replacement.write_bytes(
        b"".join(
            json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n"
            for record in [meta("t"), agent("t", "a2", "Replacement text.")]
        )
    )
    os.replace(replacement, path)
    await wait_until(
        lambda: monitor.views and "訳:Replacement text." in monitor.views[0].display_text(),
        timeout=5,
    )
    assert any("再構築" in (status.note or "") for status in listener.statuses)
    await monitor.close()


async def test_user_messages_are_never_translated(tmp_path):
    config = make_config(tmp_path)
    write_session(
        config.sessions_root,
        "t",
        [
            meta("t"),
            user("t", "Please inspect the storage layer."),
            agent("t", "a1", "I will inspect the storage layer."),
        ],
    )
    session = select_session(discover_sessions(config.sessions_root), "t")
    sent = []

    def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        body = json.loads(request.content)
        sent.append(body["messages"][1]["content"])
        return translating_handler(request)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: not monitor.views[1].pending)
    assert sent == ["I will inspect the storage layer."]
    assert monitor.views[0].state is TranslationState.SKIPPED
    assert monitor.views[0].display_text() == "Please inspect the storage layer."
    await monitor.close()


async def test_app_startup_shows_picker_before_sending(tmp_path):
    config = make_config(tmp_path)
    write_session(config.sessions_root, "t", [meta("t"), agent("t", "a1", "Initial message.")])
    calls = []

    def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        calls.append(1)
        return translating_handler(request)

    app = CodexJaApp(config, transport=httpx.MockTransport(handler))
    async with app.run_test(size=(120, 40)) as pilot:
        from codex_ja.ui import SessionScreen

        await wait_until(lambda: isinstance(app.screen, SessionScreen))
        assert calls == []
        await pilot.press("enter")
        await wait_until(lambda: app._monitor.session is not None)
        await wait_until(lambda: len(app._monitor.views) == 1)
        assert app._monitor.session.thread_id == "t"
        assert len(calls) == 1


async def test_app_s_key_switches_session(tmp_path):
    config = make_config(tmp_path)
    write_session(config.sessions_root, "a", [meta("a"), agent("a", "a1", "First session.")])
    sessions = discover_sessions(config.sessions_root)
    initial = select_session(sessions, "a")
    write_session(config.sessions_root, "b", [meta("b"), agent("b", "b1", "Second session.")])

    app = CodexJaApp(
        config, initial_session=initial, transport=httpx.MockTransport(translating_handler)
    )
    async with app.run_test(size=(120, 40)) as pilot:
        from codex_ja.ui import SessionScreen

        await wait_until(lambda: len(app._monitor.views) == 1)
        await pilot.press("s")
        await wait_until(lambda: isinstance(app.screen, SessionScreen))
        await pilot.press("enter")
        await wait_until(lambda: app._monitor.session.thread_id == "b", timeout=5)
        await wait_until(
            lambda: (
                app._monitor.views and "訳:Second session." in app._monitor.views[0].display_text()
            ),
            timeout=5,
        )
