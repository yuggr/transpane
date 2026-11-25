from __future__ import annotations

import asyncio
import json
import os
import threading
from pathlib import Path

import httpx
import pytest

from support import (
    FakeListener,
    agent,
    append,
    claude_agent,
    claude_user,
    make_config,
    meta,
    ok_response,
    screen_text,
    translating_handler,
    wait_until,
    write_claude_session,
    write_session,
)
from transpane.models import AgentProvider, TranslationState
from transpane.session import (
    SessionReader,
    SessionReadError,
    discover_all_sessions,
    select_session,
)
from transpane.ui import Monitor, TranspaneApp

CLAUDE_ID = "11111111-1111-4111-8111-111111111111"


async def test_monitor_claude_translates_and_reports_provider(tmp_path):
    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [
            claude_user(CLAUDE_ID, "保存処理を調べて"),
            claude_agent(CLAUDE_ID, "I will inspect the storage layer."),
        ],
    )
    before_bytes = path.read_bytes()
    before_mtime = path.stat().st_mtime_ns
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    sent = []

    def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        body = json.loads(request.content)
        sent.append(body["messages"][1]["content"])
        return translating_handler(request)

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 2 and not monitor.views[1].pending)
    assert sent == ["I will inspect the storage layer."]
    assert monitor.views[0].state is TranslationState.SKIPPED
    assert monitor.views[1].message.kind.value == "assistant"
    assert "訳:I will inspect" in monitor.views[1].display_text()
    assert len(monitor._entries) == 2
    assert any(status.session == "Claude Code / 11111111…" for status in listener.statuses)
    await monitor.close()
    assert path.read_bytes() == before_bytes
    assert path.stat().st_mtime_ns == before_mtime


async def test_monitor_claude_history_includes_polled_messages(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(CLAUDE_ID, f"Initial {index}.", uuid=f"00000000-0000-4000-8000-{index:012d}")
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 50)

    append(
        path,
        [
            claude_agent(
                CLAUDE_ID,
                f"New {index}.",
                uuid=f"00000000-0000-4000-8000-{100 + index:012d}",
            )
            for index in range(10)
        ],
    )
    await wait_until(lambda: len(monitor.views) == 60)
    await monitor.add_history()
    assert len(monitor.views) == 70
    assert len({view.message.message_id for view in monitor.views}) == 70
    assert monitor.views[0].message.message_id == "00000000-0000-4000-8000-000000000000"
    await monitor.close()


async def test_monitor_claude_excluded_markers_never_reach_http(tmp_path):
    config = make_config(tmp_path)
    fixture = Path(__file__).parent / "fixtures" / "claude.jsonl"
    records = [json.loads(line) for line in fixture.read_text(encoding="utf-8").splitlines()]
    write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    sent = []
    bodies = []

    def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        body = request.content.decode("utf-8")
        bodies.append(body)
        sent.append(json.loads(body)["messages"][1]["content"])
        return translating_handler(request)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(
        lambda: len(monitor.views) == 4 and all(not view.pending for view in monitor.views)
    )
    assert set(sent) == {
        "I will inspect the storage layer.",
        "The storage layer uses atomic writes.",
        "The targeted tests passed.",
    }
    assert all("DO_NOT_SEND" not in body for body in bodies)
    await monitor.close()


async def test_monitor_rejects_claude_initial_session_id_mismatch_without_post(tmp_path):
    config = make_config(tmp_path)
    write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [
            claude_user(CLAUDE_ID, "hello"),
            claude_agent(CLAUDE_ID, "other", uuid="00000000-0000-4000-8000-000000000002")
            | {"sessionId": "other-session"},
        ],
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    calls = []

    def handler(request):
        calls.append(1)
        return translating_handler(request)

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    assert monitor.views == []
    assert calls == []
    assert monitor._stopped
    assert any("セッションID" in (status.note or "") for status in listener.statuses)

    queue_before = (
        len(monitor._scheduler._queue),
        len(monitor._scheduler._deferred),
        monitor._scheduler._active,
    )
    await monitor.retry()
    await monitor.add_history()
    assert (
        len(monitor._scheduler._queue),
        len(monitor._scheduler._deferred),
        monitor._scheduler._active,
    ) == queue_before
    assert calls == []
    await monitor.close()


async def test_monitor_index_includes_polled_messages(tmp_path):
    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root, CLAUDE_ID, [claude_user(CLAUDE_ID, "hello")]
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 1)
    append(path, [claude_agent(CLAUDE_ID, "New answer.")])
    await wait_until(lambda: len(monitor.views) == 2)
    assert len(monitor._entries) == 2
    await monitor.close()


async def test_app_picker_shows_provider_and_applies_source_filter(tmp_path):
    config = make_config(tmp_path)
    write_session(config.sessions_root, "thread-1", [meta("thread-1")])
    write_claude_session(config.claude_projects_root, CLAUDE_ID, [claude_user(CLAUDE_ID, "hello")])

    app = TranspaneApp(
        config,
        source=AgentProvider.CLAUDE,
        transport=httpx.MockTransport(translating_handler),
    )
    async with app.run_test(size=(120, 40)) as pilot:
        from transpane.ui import SessionScreen

        await wait_until(lambda: isinstance(app.screen, SessionScreen))
        await pilot.pause()
        assert [session.provider for session in app._sessions] == [AgentProvider.CLAUDE]
        rendered = screen_text(app)
        assert "Claude Code" in rendered
        assert "claude-jsonl" in rendered
        await pilot.press("enter")
        await wait_until(lambda: app._monitor.session is not None)
        assert app._monitor.session.provider is AgentProvider.CLAUDE


async def test_app_claude_renders_and_translates_many_messages(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            f"Rendered Claude body {index}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(50)
    ]
    write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )

    app = TranspaneApp(
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
        last_id = f"00000000-0000-4000-8000-{49:012d}"
        last = app._widgets[(CLAUDE_ID, last_id)]
        body = last.query_one("Markdown")
        assert last.size.height > 1
        assert last.region.contains_region(body.region)
        rendered = screen_text(app)
        assert "訳:Rendered Claude body 49." in rendered
        assert "応答" in rendered

        await pilot.press("o")
        await pilot.pause()
        rendered = screen_text(app)
        assert "Rendered Claude body 49." in rendered
        assert "訳:Rendered Claude body 49." not in rendered
        assert "原文" in rendered


async def test_app_switch_between_providers_with_same_ids_drops_stale_results(tmp_path):
    config = make_config(tmp_path)
    shared = "88888888-8888-4888-8888-888888888888"
    write_session(
        config.sessions_root,
        shared,
        [meta(shared), agent(shared, "shared-msg", "Slow codex payload.")],
    )
    write_claude_session(
        config.claude_projects_root,
        shared,
        [
            claude_user(shared, "hello", uuid="00000000-0000-4000-8000-000000000001"),
            claude_agent(shared, "Claude payload.", uuid="shared-msg"),
        ],
    )
    sessions = discover_all_sessions(config.sessions_root, config.claude_projects_root)
    codex_session = select_session(sessions, f"codex:{shared}")
    claude_session = select_session(sessions, f"claude:{shared}")

    gate = asyncio.Event()
    started = asyncio.Event()

    async def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        text = json.loads(request.content)["messages"][1]["content"]
        if "Slow codex" in text:
            started.set()
            await gate.wait()
            return ok_response("訳:codex")
        return ok_response("訳:" + text)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(codex_session)
    await asyncio.wait_for(started.wait(), timeout=3)
    await monitor.switch(claude_session)
    gate.set()
    await wait_until(
        lambda: any("訳:Claude payload." in view.display_text() for view in monitor.views),
        timeout=5,
    )
    await asyncio.sleep(0.05)
    assert monitor.session.provider is AgentProvider.CLAUDE
    assert all("Slow codex" not in view.display_text() for view in monitor.views)
    await monitor.close()


async def test_cache_is_shared_between_providers(tmp_path):
    config = make_config(tmp_path)
    text = "Shared provider sentence."
    write_session(config.sessions_root, "shared", [meta("shared"), agent("shared", "a1", text)])
    write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [claude_agent(CLAUDE_ID, text)],
    )
    sessions = discover_all_sessions(config.sessions_root, config.claude_projects_root)
    codex_session = select_session(sessions, "shared")
    claude_session = select_session(sessions, f"claude:{CLAUDE_ID}")
    calls = []

    def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        calls.append(1)
        return ok_response("訳:" + text)

    first = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await first.start(codex_session)
    await wait_until(lambda: first.views and not first.views[0].pending)
    await first.close()

    second = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await second.start(claude_session)
    await wait_until(lambda: second.views and not second.views[0].pending)
    assert second.views[0].state is TranslationState.TRANSLATED
    assert "訳:Shared provider sentence." in second.views[0].display_text()
    assert len(calls) == 1
    await second.close()


async def test_monitor_claude_rebuilds_after_replacement(tmp_path):
    import os

    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [claude_agent(CLAUDE_ID, "Original Claude text.")],
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: monitor.views[0].state is TranslationState.TRANSLATED)

    replacement = tmp_path / "replacement.jsonl"
    replacement.write_bytes(
        b"".join(
            json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n"
            for record in [claude_agent(CLAUDE_ID, "Replacement Claude text.")]
        )
    )
    os.replace(replacement, path)
    await wait_until(
        lambda: monitor.views and "訳:Replacement Claude text." in monitor.views[0].display_text(),
        timeout=5,
    )
    assert any("再構築" in (status.note or "") for status in listener.statuses)
    await monitor.close()


async def test_monitor_keeps_claude_views_when_file_disappears(tmp_path):
    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [claude_agent(CLAUDE_ID, "Safe text.")],
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: monitor.views[0].state is TranslationState.TRANSLATED)
    path.unlink()
    await wait_until(
        lambda: any(
            "ファイルが見つかりません" in (status.note or "") for status in listener.statuses
        )
    )
    assert len(monitor.views) == 1
    assert not any("許可範囲外" in (status.note or "") for status in listener.statuses)
    await monitor.close()


async def test_monitor_claude_stops_on_symlink_replacement(tmp_path):
    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [claude_agent(CLAUDE_ID, "Safe text.")],
    )
    outside = tmp_path / "outside.jsonl"
    outside.write_bytes(path.read_bytes())
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: monitor.views[0].state is TranslationState.TRANSLATED)
    path.unlink()
    path.symlink_to(outside)
    await wait_until(
        lambda: any("許可範囲外" in (status.note or "") for status in listener.statuses)
    )
    assert len(monitor.views) == 1
    await monitor.close()


async def test_monitor_reports_when_no_claude_events_are_extracted(tmp_path):
    config = make_config(tmp_path)
    write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [
            claude_user(CLAUDE_ID, "internal", uuid="00000000-0000-4000-8000-000000000001")
            | {"isMeta": True},
            claude_agent(CLAUDE_ID, "synthetic")
            | {
                "message": {
                    "id": "msg",
                    "role": "assistant",
                    "model": "<synthetic>",
                    "content": [{"type": "text", "text": "synthetic"}],
                }
            },
        ],
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(
        lambda: any("対象イベント未検出" in (status.note or "") for status in listener.statuses)
    )
    assert monitor.views == []
    await monitor.close()


async def test_monitor_stops_on_claude_session_id_mismatch(tmp_path):
    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [claude_user(CLAUDE_ID, "hello"), claude_agent(CLAUDE_ID, "Slow answer.")],
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    gate = asyncio.Event()
    started = asyncio.Event()

    async def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        started.set()
        await gate.wait()
        return ok_response("訳:late")

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await asyncio.wait_for(started.wait(), timeout=3)
    append(path, [claude_user("other-session", "mismatch")])
    await wait_until(
        lambda: any("セッションID" in (status.note or "") for status in listener.statuses)
    )
    assert not monitor.views[1].pending
    assert monitor.views[1].state not in (
        TranslationState.QUEUED,
        TranslationState.TRANSLATING,
    )
    gate.set()
    await asyncio.sleep(0.1)
    assert all("訳:late" not in view.display_text() for view in monitor.views)
    assert len(monitor.views) == 2
    await monitor.close()


@pytest.mark.parametrize("stop_mode", ["mismatch", "denied"])
async def test_monitor_stop_blocks_retry_and_history(tmp_path, stop_mode):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            "Fail me." if index == 59 else f"Message {index}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    requests = []
    unexpected = asyncio.Event()

    def handler(request):
        requests.append(request.method)
        unexpected.set()
        if request.method == "GET":
            return translating_handler(request)
        text = json.loads(request.content)["messages"][1]["content"]
        if text == "Fail me.":
            return httpx.Response(400, json={})
        return ok_response("訳:" + text)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(
        lambda: len(monitor.views) == 50 and all(not view.pending for view in monitor.views)
    )
    assert any(view.state is TranslationState.FAILED for view in monitor.views)

    if stop_mode == "mismatch":
        append(path, [claude_user("other-session", "mismatch")])
        note = "セッションID"
    else:
        outside = tmp_path / "outside.jsonl"
        outside.write_bytes(path.read_bytes())
        path.unlink()
        path.symlink_to(outside)
        note = "許可範囲外"
    await wait_until(lambda: monitor._note is not None and note in monitor._note)
    assert monitor._stopped
    assert monitor._scheduler._cancelled_through >= monitor._generation
    unexpected.clear()
    before = len(requests)
    views_before = len(monitor.views)
    queue_before = (
        len(monitor._scheduler._queue),
        len(monitor._scheduler._deferred),
        monitor._scheduler._active,
    )

    await monitor.retry()
    await monitor.add_history()
    assert (
        len(monitor._scheduler._queue),
        len(monitor._scheduler._deferred),
        monitor._scheduler._active,
    ) == queue_before
    for _ in range(5):
        await asyncio.sleep(0)
    assert len(requests) == before
    assert not unexpected.is_set()
    assert len(monitor.views) == views_before
    await monitor.close()


async def test_monitor_rebuild_failure_blocks_retry_and_history(tmp_path):
    import os

    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            "Fail me." if index == 59 else f"Message {index}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    requests = []
    unexpected = asyncio.Event()

    def handler(request):
        requests.append(request.method)
        unexpected.set()
        if request.method == "GET":
            return translating_handler(request)
        text = json.loads(request.content)["messages"][1]["content"]
        if text == "Fail me.":
            return httpx.Response(400, json={})
        return ok_response("訳:" + text)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(
        lambda: len(monitor.views) == 50 and all(not view.pending for view in monitor.views)
    )

    replacement = tmp_path / "replacement.jsonl"
    replacement.write_bytes(
        b"".join(
            json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n"
            for record in [claude_agent("other-session", "Replacement.")]
        )
    )
    os.replace(replacement, path)
    await wait_until(lambda: monitor._note is not None and "セッションID" in monitor._note)
    unexpected.clear()
    before = len(requests)
    queue_before = (
        len(monitor._scheduler._queue),
        len(monitor._scheduler._deferred),
        monitor._scheduler._active,
    )

    await monitor.retry()
    await monitor.add_history()
    assert (
        len(monitor._scheduler._queue),
        len(monitor._scheduler._deferred),
        monitor._scheduler._active,
    ) == queue_before
    for _ in range(5):
        await asyncio.sleep(0)
    assert len(requests) == before
    assert not unexpected.is_set()
    await monitor.close()


async def test_monitor_index_mismatch_rebuilds_on_history(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            f"Message {index:02d}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 50)
    before_size = path.stat().st_size

    changed = [dict(record) for record in records]
    changed[0] = claude_agent(
        CLAUDE_ID,
        "Changed 00.",
        uuid="00000000-0000-4000-8000-000000000000",
    )
    path.write_bytes(
        b"".join(
            json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n" for record in changed
        )
    )
    assert path.stat().st_size == before_size

    await monitor.add_history()
    assert monitor._note is not None and "索引不一致" in monitor._note
    assert len(monitor.views) == 50

    await monitor.add_history()
    assert len(monitor.views) == 60
    assert monitor.views[0].message.original == "Changed 00."
    assert all(view.message.original != "Message 00." for view in monitor.views)
    await monitor.close()


async def test_rebuild_blocks_retry_until_success(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            "Fail me." if index == 59 else f"Message {index}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    requests = []

    def handler(request):
        requests.append(request.method)
        if request.method == "GET":
            return translating_handler(request)
        text = json.loads(request.content)["messages"][1]["content"]
        if text == "Fail me.":
            return httpx.Response(400, json={})
        return ok_response("訳:" + text)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(
        lambda: len(monitor.views) == 50 and all(not view.pending for view in monitor.views)
    )

    original_scan = monitor._reader.scan_all
    entered = threading.Event()
    release = threading.Event()

    def blocking_scan():
        entered.set()
        release.wait(timeout=5)
        return original_scan()

    monitor._reader.scan_all = blocking_scan
    replacement = tmp_path / "replacement.jsonl"
    replacement.write_bytes(path.read_bytes())
    try:
        os.replace(replacement, path)
        await wait_until(entered.is_set)
        assert monitor._stopped
        before = len(requests)
        queue_before = (
            len(monitor._scheduler._queue),
            len(monitor._scheduler._deferred),
            monitor._scheduler._active,
        )

        await monitor.retry()
        assert (
            len(monitor._scheduler._queue),
            len(monitor._scheduler._deferred),
            monitor._scheduler._active,
        ) == queue_before
        assert len(requests) == before
    finally:
        release.set()
    await wait_until(
        lambda: not monitor._stopped and monitor._note is not None and "再構築" in monitor._note
    )
    await monitor.close()


async def test_history_denied_stop_survives_replacement_until_switch(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            f"Message {index:02d}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    requests = []

    def handler(request):
        requests.append(request.method)
        return translating_handler(request)

    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 50)

    original_read = monitor._reader.read_messages

    def denied(entries):
        raise SessionReadError("denied")

    monitor._reader.read_messages = denied
    await monitor.add_history()
    monitor._reader.read_messages = original_read

    assert monitor._stopped
    assert monitor._note is not None and "許可範囲外" in monitor._note
    poll_task = monitor._poll_task
    assert poll_task is not None
    await asyncio.wait_for(poll_task, timeout=2)

    before = len(requests)
    replacement = tmp_path / "replacement.jsonl"
    replacement.write_bytes(path.read_bytes())
    os.replace(replacement, path)
    for _ in range(5):
        await asyncio.sleep(0)
    assert len(requests) == before
    assert monitor._stopped
    assert monitor._note is not None and "許可範囲外" in monitor._note
    assert len(monitor.views) == 50

    await monitor.switch(session)
    await wait_until(lambda: not monitor._stopped and len(monitor.views) == 50)
    await monitor.add_history()
    await wait_until(lambda: len(monitor.views) == 60)
    await monitor.close()


async def test_history_read_failure_does_not_advance_visible_and_recovers(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            f"Message {index:02d}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 50)
    if monitor._poll_task is not None:
        monitor._poll_task.cancel()
        monitor._poll_task = None

    original = path.read_bytes()
    path.unlink()
    await monitor.add_history()
    assert len(monitor.views) == 50
    assert monitor._visible == 50
    assert monitor._note is not None and "ファイルが見つかりません" in monitor._note
    assert not monitor._stopped

    path.write_bytes(original)
    await monitor.add_history()
    assert len(monitor.views) == 60
    assert monitor._visible == 60
    assert monitor.views[0].message.message_id == "00000000-0000-4000-8000-000000000000"
    await monitor.close()


async def test_switch_read_error_is_handled(tmp_path, monkeypatch):
    config = make_config(tmp_path)
    write_claude_session(config.claude_projects_root, CLAUDE_ID, [claude_user(CLAUDE_ID, "hello")])
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )

    def fail_read(self, entries):
        raise SessionReadError("read")

    monkeypatch.setattr(SessionReader, "read_messages", fail_read)
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    assert monitor._stopped
    assert monitor._note is not None and "ログを読み取れません" in monitor._note
    await monitor.close()


async def test_rebuild_read_error_is_handled(tmp_path):
    config = make_config(tmp_path)
    records = [
        claude_agent(
            CLAUDE_ID,
            f"Message {index}.",
            uuid=f"00000000-0000-4000-8000-{index:012d}",
        )
        for index in range(60)
    ]
    path = write_claude_session(config.claude_projects_root, CLAUDE_ID, records)
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    await wait_until(lambda: len(monitor.views) == 50)

    def fail_read(entries):
        raise SessionReadError("read")

    monitor._reader.read_messages = fail_read
    replacement = tmp_path / "replacement.jsonl"
    replacement.write_bytes(path.read_bytes())
    os.replace(replacement, path)
    await wait_until(lambda: monitor._stopped)
    assert monitor._note is not None and "ログを読み取れません" in monitor._note
    await monitor.close()


async def test_monitor_denied_path_cancels_pending_translation(tmp_path):
    config = make_config(tmp_path)
    path = write_claude_session(
        config.claude_projects_root,
        CLAUDE_ID,
        [claude_agent(CLAUDE_ID, "Slow answer.")],
    )
    outside = tmp_path / "outside.jsonl"
    outside.write_bytes(path.read_bytes())
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    gate = asyncio.Event()
    started = asyncio.Event()
    calls = []

    async def handler(request):
        if request.method == "GET":
            return translating_handler(request)
        calls.append(1)
        started.set()
        await gate.wait()
        return ok_response("訳:late")

    listener = FakeListener()
    monitor = Monitor(config, listener, transport=httpx.MockTransport(handler))
    await monitor.start(session)
    await asyncio.wait_for(started.wait(), timeout=3)
    path.unlink()
    path.symlink_to(outside)
    await wait_until(
        lambda: any("許可範囲外" in (status.note or "") for status in listener.statuses)
    )
    view = monitor.views[0]
    assert not view.pending
    assert view.state not in (TranslationState.QUEUED, TranslationState.TRANSLATING)
    gate.set()
    await asyncio.sleep(0.1)
    assert len(calls) == 1
    assert "訳:late" not in view.display_text()
    assert view.state is not TranslationState.TRANSLATED
    await monitor.close()
