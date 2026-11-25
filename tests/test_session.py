from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from codex_ja import session as session_module
from codex_ja.session import (
    SessionError,
    SessionReader,
    discover_sessions,
    normalize_cwd,
    read_session_meta,
    select_session,
    sessions_for_cwd,
)


def dump(records: list[dict]) -> bytes:
    return b"".join(
        json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n" for record in records
    )


def meta(thread_id: str, mode: str | None = "legacy", **extra) -> dict:
    payload = {"id": thread_id, "cwd": "/tmp/demo", "cli_version": "0.169.0", **extra}
    if mode is not None:
        payload["history_mode"] = mode
    else:
        payload.pop("history_mode", None)
    return {"timestamp": "2026-11-25T01:00:00Z", "type": "session_meta", "payload": payload}


def legacy_user(offset_text: str) -> dict:
    return {
        "timestamp": "2026-11-25T01:00:01Z",
        "type": "event_msg",
        "payload": {"type": "user_message", "message": offset_text},
    }


def paginated_agent(item_id: str, text: str) -> dict:
    return {
        "timestamp": "2026-11-25T01:00:02Z",
        "type": "event_msg",
        "payload": {
            "type": "item_completed",
            "item": {
                "type": "AgentMessage",
                "id": item_id,
                "phase": "commentary",
                "content": [{"type": "Text", "text": text}],
            },
        },
    }


def write_rollout(root: Path, relative: str, records: list[dict]) -> Path:
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(dump(records))
    return path


def test_discovery_sorts_and_filters_children(tmp_path):
    root = tmp_path / "sessions"
    parent = write_rollout(
        root, "2026/11/25/rollout-parent.jsonl", [meta("parent"), legacy_user("hi")]
    )
    write_rollout(
        root,
        "2026/11/25/rollout-child.jsonl",
        [meta("child", parent_thread_id="parent"), legacy_user("child")],
    )
    write_rollout(
        root,
        "2026/11/25/rollout-orphan.jsonl",
        [meta("orphan", parent_thread_id="missing-parent"), legacy_user("orphan")],
    )
    write_rollout(root, "2026/11/25/rollout-no-meta.jsonl", [legacy_user("x")])
    write_rollout(root, "archived_sessions/2026/11/25/rollout-old.jsonl", [meta("archived")])

    os.utime(parent, (1000, 1000))
    found = discover_sessions(root)
    ids = [item.thread_id for item in found]
    assert "parent" in ids
    assert "child" not in ids
    assert "orphan" in ids
    assert "archived" not in ids
    assert found[-1].thread_id == "parent"
    assert found[-1].history_mode == "legacy"


def test_discovery_tie_breaks_by_id(tmp_path):
    root = tmp_path / "sessions"
    first = write_rollout(root, "2026/11/25/rollout-b.jsonl", [meta("b")])
    second = write_rollout(root, "2026/11/25/rollout-a.jsonl", [meta("a")])
    os.utime(first, (500, 500))
    os.utime(second, (500, 500))
    assert [item.thread_id for item in discover_sessions(root)] == ["a", "b"]


def test_missing_sessions_root_is_empty(tmp_path):
    assert discover_sessions(tmp_path / "nope") == []


def test_read_session_meta_ignores_invalid_head(tmp_path):
    path = tmp_path / "rollout-x.jsonl"
    path.write_bytes(b"not json\n" + dump([meta("real")]))
    payload = read_session_meta(path)
    assert payload is not None
    assert payload["id"] == "real"


def test_duplicate_and_unknown_selection(tmp_path):
    root = tmp_path / "sessions"
    write_rollout(root, "2026/11/25/rollout-1.jsonl", [meta("dup")])
    write_rollout(root, "2026/11/25/rollout-2.jsonl", [meta("dup")])
    write_rollout(root, "2026/11/25/rollout-3.jsonl", [meta("other")])
    found = discover_sessions(root)
    with pytest.raises(SessionError, match="重複"):
        select_session(found, "dup")
    with pytest.raises(SessionError, match="見つかりません"):
        select_session(found, "missing")
    assert select_session(found, "other").thread_id == "other"


def test_cwd_filter_is_exact(tmp_path):
    root = tmp_path / "sessions"
    write_rollout(root, "2026/11/25/rollout-1.jsonl", [meta("a", cwd="/tmp/project")])
    write_rollout(root, "2026/11/25/rollout-2.jsonl", [meta("b", cwd="/tmp/project/sub")])
    found = discover_sessions(root)
    assert [item.thread_id for item in sessions_for_cwd(found, "/tmp/project")] == ["a"]
    assert normalize_cwd("/tmp//project/../project") == "/tmp/project"


def test_scan_and_read_messages(tmp_path):
    path = write_rollout(
        tmp_path / "rollout-a.jsonl",
        "",
        [
            meta("thread-1"),
            legacy_user("保存処理を調べて"),
            {
                "type": "response_item",
                "payload": {"type": "message", "role": "assistant", "content": []},
            },
            {"type": "event_msg", "payload": {"type": "agent_message", "message": "I will look."}},
        ],
    )
    reader = SessionReader(path, "thread-1")
    result = reader.scan_all()
    assert result.history_mode == "legacy"
    assert result.stats.records == 4
    assert result.stats.extracted == 2
    assert result.stats.ignored == 1
    assert [entry.kind.value for entry in result.entries] == ["user", "assistant"]

    messages = reader.read_messages(result.entries)
    assert [message.original for message in messages] == ["保存処理を調べて", "I will look."]
    assert messages[0].sequence == result.entries[0].sequence


def test_incremental_read_waits_for_complete_lines(tmp_path):
    path = write_rollout(tmp_path / "rollout-a.jsonl", "", [meta("t"), legacy_user("first")])
    reader = SessionReader(path, "t")
    reader.scan_all()

    payload = dump([legacy_user("second")])
    split = len(payload) // 2
    with path.open("ab") as handle:
        handle.write(payload[:split])
    assert reader.poll().messages == []

    with path.open("ab") as handle:
        handle.write(payload[split:])
    polled = reader.poll()
    assert [message.original for message in polled.messages] == ["second"]
    assert reader.poll().messages == []


def test_incremental_read_waits_for_partial_utf8(tmp_path):
    path = write_rollout(tmp_path / "rollout-a.jsonl", "", [meta("t")])
    reader = SessionReader(path, "t")
    reader.scan_all()

    payload = dump([legacy_user("日本語の途中")])
    with path.open("ab") as handle:
        handle.write(payload[: len(payload) // 2 - 1])
    reader.poll()
    # Split inside the multibyte character by taking the first half of the bytes.
    with path.open("ab") as handle:
        handle.write(payload[len(payload) // 2 - 1 :])
    polled = reader.poll()
    assert [message.original for message in polled.messages] == ["日本語の途中"]


def test_invalid_and_oversized_lines_are_bounded(tmp_path, monkeypatch):
    monkeypatch.setattr(session_module, "MAX_LINE_BYTES", 300)
    path = tmp_path / "rollout-a.jsonl"
    path.write_bytes(
        dump([meta("t")])
        + b'{"type": "event_msg", "payload": {"type": "agent_message", "message": "'
        + b"x" * 400
        + b'"}}\n'
        + b"{invalid json\n"
        + dump([legacy_user("after")])
    )
    reader = SessionReader(path, "t")
    result = reader.scan_all()
    assert result.stats.oversized_lines == 1
    assert result.stats.invalid_lines == 1
    assert result.stats.extracted == 1
    messages = reader.read_messages(result.entries)
    assert [message.original for message in messages] == ["after"]


def test_unsupported_mode_reads_nothing(tmp_path):
    path = write_rollout(
        tmp_path / "rollout-a.jsonl", "", [meta("t", mode="chunked"), legacy_user("secret")]
    )
    reader = SessionReader(path, "t")
    result = reader.scan_all()
    assert result.unsupported_mode == "'chunked'"
    assert result.history_mode is None
    assert result.entries == []
    assert reader.read_messages([]) == []


def test_thread_id_change_halts_processing(tmp_path):
    path = write_rollout(tmp_path / "rollout-a.jsonl", "", [meta("t")])
    reader = SessionReader(path, "t")
    reader.scan_all()
    with path.open("ab") as handle:
        handle.write(dump([meta("other"), legacy_user("should not appear")]))
    polled = reader.poll()
    assert polled.thread_changed == "other"
    assert polled.messages == []


def test_missing_and_rebuilt_detection(tmp_path):
    path = write_rollout(tmp_path / "rollout-a.jsonl", "", [meta("t"), legacy_user("one")])
    reader = SessionReader(path, "t")
    reader.scan_all()

    path.unlink()
    assert reader.poll().missing is True

    path.write_bytes(dump([meta("t"), legacy_user("one")]))
    reader = SessionReader(path, "t")
    reader.scan_all()
    with path.open("ab") as handle:
        handle.write(dump([legacy_user("two")]))
    replacement = tmp_path / "rollout-b.jsonl"
    replacement.write_bytes(dump([meta("t"), legacy_user("replaced")]))
    os.replace(replacement, path)
    assert reader.poll().rebuilt is True


def test_shrink_detection(tmp_path):
    path = write_rollout(
        tmp_path / "rollout-a.jsonl", "", [meta("t"), legacy_user("one"), legacy_user("two")]
    )
    reader = SessionReader(path, "t")
    reader.scan_all()
    path.write_bytes(dump([meta("t")]))
    assert reader.poll().rebuilt is True
