from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from transpane import session as session_module
from transpane.claude import claude_transcript_counts, discover_claude_sessions
from transpane.models import AgentProvider
from transpane.session import (
    SessionError,
    SessionIndexMismatch,
    SessionReader,
    SessionReadError,
    discover_all_sessions,
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


CLAUDE_SESSION = "11111111-1111-4111-8111-111111111111"


def claude_record(
    text: str = "hello",
    *,
    session: str = CLAUDE_SESSION,
    uuid: str = "00000000-0000-4000-8000-000000000001",
    kind: str = "user",
    **extra,
) -> dict:
    content: object = text if kind == "user" else [{"type": "text", "text": text}]
    return {
        "type": kind,
        "sessionId": session,
        "uuid": uuid,
        "timestamp": "2026-11-25T03:00:00Z",
        "cwd": "/tmp/demo",
        "version": "2.1.285",
        "message": {"role": kind, "content": content},
        **extra,
    }


def write_claude(root: Path, project: str, session: str, records: list[dict]) -> Path:
    path = root / project / f"{session}.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(dump(records))
    return path


def test_claude_discovery_reads_only_direct_uuid_transcripts(tmp_path):
    root = tmp_path / "claude" / "projects"
    valid = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    write_claude(
        root, "demo/subagents", "22222222-2222-4222-8222-222222222222", [claude_record("sub")]
    )
    write_claude(
        root, "demo/tool-results", "33333333-3333-4333-8333-333333333333", [claude_record("tool")]
    )
    write_claude(root, "demo", "not-a-uuid", [claude_record("bad-name")])
    (root / "demo" / "sessions-index.json").write_text("{}", encoding="utf-8")
    (root / "history.jsonl").write_text("{}\n", encoding="utf-8")

    outside = tmp_path / "outside.jsonl"
    outside.write_bytes(dump([claude_record("outside")]))
    (root / "demo" / "44444444-4444-4444-8444-444444444444.jsonl").symlink_to(outside)
    (root / "linked-project").symlink_to(root / "demo", target_is_directory=True)

    found = discover_claude_sessions(root)
    assert [item.thread_id for item in found] == [CLAUDE_SESSION]
    assert found[0].path == str(valid)
    assert found[0].provider is AgentProvider.CLAUDE
    assert found[0].history_mode == "claude-jsonl"
    assert found[0].cli_version == "2.1.285"
    assert found[0].cwd == "/tmp/demo"


def test_claude_discovery_reads_complete_meta_line_above_256k(tmp_path):
    root = tmp_path / "claude" / "projects"
    write_claude(root, "demo", CLAUDE_SESSION, [claude_record("x" * 300_000)])

    found = discover_claude_sessions(root)
    assert [item.thread_id for item in found] == [CLAUDE_SESSION]
    assert claude_transcript_counts(root) == (1, 0)


def test_claude_transcript_counts_report_unconfirmed_files(tmp_path):
    root = tmp_path / "claude" / "projects"
    write_claude(root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    write_claude(
        root,
        "demo",
        "22222222-2222-4222-8222-222222222222",
        [claude_record("wrong", session=CLAUDE_SESSION)],
    )
    write_claude(
        root,
        "demo",
        "33333333-3333-4333-8333-333333333333",
        [claude_record("side", session="33333333-3333-4333-8333-333333333333", isSidechain=True)],
    )
    write_claude(root, "demo", "not-a-uuid", [claude_record("ignored-name")])

    assert claude_transcript_counts(root) == (1, 2)


def test_claude_discovery_requires_matching_session_id_and_handles_unknown_cwd(tmp_path):
    root = tmp_path / "claude" / "projects"
    write_claude(
        root,
        "demo",
        CLAUDE_SESSION,
        [claude_record("wrong", session="99999999-9999-4999-8999-999999999999")],
    )
    other = "55555555-5555-4555-8555-555555555555"
    path = write_claude(
        root,
        "demo",
        other,
        [
            claude_record("side", session=other, isSidechain=True),
            claude_record("ok", session=other),
        ],
    )
    record = json.loads(path.read_text(encoding="utf-8").splitlines()[1])
    record["cwd"] = "relative/path"
    path.write_bytes(dump([record]))

    found = discover_claude_sessions(root)
    assert [item.thread_id for item in found] == [other]
    assert found[0].cwd is None


def test_discover_all_sessions_mixes_sources_and_filters(tmp_path):
    codex_root = tmp_path / "codex" / "sessions"
    claude_root = tmp_path / "claude" / "projects"
    codex_path = write_rollout(codex_root, "2026/11/25/rollout-codex.jsonl", [meta("codex-1")])
    claude_path = write_claude(claude_root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    os.utime(codex_path, (1000, 1000))
    os.utime(claude_path, (2000, 2000))

    both = discover_all_sessions(codex_root, claude_root)
    assert [item.thread_id for item in both] == [CLAUDE_SESSION, "codex-1"]
    assert [item.provider for item in both] == [AgentProvider.CLAUDE, AgentProvider.CODEX]

    only_claude = discover_all_sessions(codex_root, claude_root, AgentProvider.CLAUDE)
    assert [item.thread_id for item in only_claude] == [CLAUDE_SESSION]
    assert discover_all_sessions(tmp_path / "missing", tmp_path / "missing") == []


def test_discover_all_sessions_tie_breaks_by_provider_and_id(tmp_path):
    codex_root = tmp_path / "codex" / "sessions"
    claude_root = tmp_path / "claude" / "projects"
    codex_path = write_rollout(codex_root, "2026/11/25/rollout-z.jsonl", [meta("z")])
    claude_path = write_claude(claude_root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    os.utime(codex_path, (1000, 1000))
    os.utime(claude_path, (1000, 1000))

    found = discover_all_sessions(codex_root, claude_root)
    assert [item.provider.value for item in found] == ["claude", "codex"]


def test_select_session_supports_provider_prefix_and_source(tmp_path):
    codex_root = tmp_path / "codex" / "sessions"
    claude_root = tmp_path / "claude" / "projects"
    shared = "77777777-7777-4777-8777-777777777777"
    write_rollout(codex_root, "2026/11/25/rollout-same.jsonl", [meta(shared)])
    write_claude(claude_root, "demo", shared, [claude_record("ok", session=shared)])
    sessions = discover_all_sessions(codex_root, claude_root)

    with pytest.raises(SessionError, match="重複"):
        select_session(sessions, shared)
    assert select_session(sessions, f"codex:{shared}").provider is AgentProvider.CODEX
    assert select_session(sessions, f"claude:{shared}").provider is AgentProvider.CLAUDE
    assert (
        select_session(sessions, shared, source=AgentProvider.CODEX).provider is AgentProvider.CODEX
    )
    with pytest.raises(SessionError, match="矛盾"):
        select_session(sessions, f"claude:{shared}", source=AgentProvider.CODEX)
    with pytest.raises(SessionError, match="見つかりません"):
        select_session(sessions, "missing", source=AgentProvider.CLAUDE)


def test_duplicate_ids_within_a_provider_still_fail_cli_selection(tmp_path):
    root = tmp_path / "claude" / "projects"
    write_claude(root, "one", CLAUDE_SESSION, [claude_record("one")])
    write_claude(root, "two", CLAUDE_SESSION, [claude_record("two")])
    sessions = discover_claude_sessions(root)
    assert len(sessions) == 2
    with pytest.raises(SessionError, match="重複"):
        select_session(sessions, CLAUDE_SESSION)


def test_claude_reader_dedupes_initial_index_and_keeps_latest_revision(tmp_path):
    root = tmp_path / "claude" / "projects"
    path = write_claude(
        root,
        "demo",
        CLAUDE_SESSION,
        [
            claude_record("first", uuid="u-1", kind="assistant"),
            claude_record("duplicate", uuid="u-1", kind="assistant"),
            claude_record("updated", uuid="u-1", kind="assistant"),
            claude_record("updated", uuid="u-1", kind="assistant"),
            claude_record("other", uuid="u-2", kind="assistant"),
        ],
    )
    offsets = []
    position = 0
    for line in path.read_bytes().splitlines(keepends=True):
        offsets.append(position)
        position += len(line)
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    result = reader.scan_all()
    assert [entry.message_id for entry in result.entries] == ["u-1", "u-2"]
    assert result.entries[0].sequence == offsets[2]
    assert result.entries[1].sequence == offsets[4]
    messages = reader.read_messages(result.entries)
    assert [message.original for message in messages] == ["updated", "other"]


def test_claude_reader_reports_session_id_mismatch(tmp_path):
    root = tmp_path / "claude" / "projects"
    path = write_claude(
        root,
        "demo",
        CLAUDE_SESSION,
        [claude_record("ok"), claude_record("other", session="other")],
    )
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    result = reader.scan_all()
    assert result.thread_changed == "other"
    with pytest.raises(SessionReadError) as excinfo:
        reader.read_messages(result.entries)
    assert excinfo.value.reason == "halted"

    path2 = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    reader2 = SessionReader(path2, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    reader2.scan_all()
    with path2.open("ab") as handle:
        handle.write(dump([claude_record("other", session="other")]))
    polled = reader2.poll()
    assert polled.thread_changed == "other"
    assert polled.messages == []


def test_claude_reader_ignores_sidechain_with_other_session_id(tmp_path):
    root = tmp_path / "claude" / "projects"
    path = write_claude(
        root,
        "demo",
        CLAUDE_SESSION,
        [
            claude_record("ok"),
            claude_record("side", session="other", isSidechain=True),
            claude_record("after", uuid="u-2", kind="assistant"),
        ],
    )
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    result = reader.scan_all()
    assert result.thread_changed is None
    assert [message.original for message in reader.read_messages(result.entries)] == [
        "ok",
        "after",
    ]


def test_claude_poll_returns_entries_for_history(tmp_path):
    root = tmp_path / "claude" / "projects"
    path = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("one")])
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    reader.scan_all()
    with path.open("ab") as handle:
        handle.write(dump([claude_record("two", uuid="u-2", kind="assistant")]))
    polled = reader.poll()
    assert [message.original for message in polled.messages] == ["two"]
    assert [entry.message_id for entry in polled.entries] == ["u-2"]
    assert polled.entries[0].end > polled.entries[0].sequence


def test_claude_reader_handles_partial_invalid_and_oversized_lines(tmp_path, monkeypatch):
    monkeypatch.setattr(session_module, "MAX_LINE_BYTES", 300)
    root = tmp_path / "claude" / "projects"
    path = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("first")])
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    reader.scan_all()

    payload = dump([claude_record("日本語の途中", uuid="u-2", kind="assistant")])
    with path.open("ab") as handle:
        handle.write(payload[: len(payload) // 2 - 1])
    assert reader.poll().messages == []
    with path.open("ab") as handle:
        handle.write(payload[len(payload) // 2 - 1 :])
    polled = reader.poll()
    assert [message.original for message in polled.messages] == ["日本語の途中"]

    with path.open("ab") as handle:
        handle.write(
            b'{"type":"assistant","sessionId":"'
            + CLAUDE_SESSION.encode()
            + b'","uuid":"big","message":{"role":"assistant","content":[{"type":"text","text":"'
            + b"x" * 400
            + b'"}]}}\n'
        )
        handle.write(b"{invalid json\n")
        handle.write(dump([claude_record("after", uuid="u-3", kind="assistant")]))
    polled = reader.poll()
    assert [message.original for message in polled.messages] == ["after"]
    assert polled.stats.oversized_lines == 1
    assert polled.stats.invalid_lines == 1


def test_claude_read_messages_rejects_same_size_rewrite(tmp_path):
    root = tmp_path / "claude" / "projects"
    path = write_claude(
        root, "demo", CLAUDE_SESSION, [claude_record("one", uuid="u-1", kind="assistant")]
    )
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    result = reader.scan_all()
    before_size = path.stat().st_size
    path.write_bytes(dump([claude_record("two", uuid="u-1", kind="assistant")]))
    assert path.stat().st_size == before_size
    assert reader.poll().messages == []
    with pytest.raises(SessionIndexMismatch):
        reader.read_messages(result.entries)


def test_read_messages_distinguishes_missing_denied_and_read_failure(tmp_path, monkeypatch):
    root = tmp_path / "claude" / "projects"
    path = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("one")])
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE)
    result = reader.scan_all()
    original = path.read_bytes()

    path.unlink()
    with pytest.raises(SessionReadError) as excinfo:
        reader.read_messages(result.entries)
    assert excinfo.value.reason == "missing"

    path.write_bytes(original)
    assert [message.original for message in reader.read_messages(result.entries)] == ["one"]

    outside = tmp_path / "outside.jsonl"
    outside.write_bytes(original)
    path.unlink()
    path.symlink_to(outside)
    with pytest.raises(SessionReadError) as excinfo:
        reader.read_messages(result.entries)
    assert excinfo.value.reason == "denied"

    path.unlink()
    path.write_bytes(original)

    def fail_open(self, *args, **kwargs):
        raise PermissionError("denied")

    monkeypatch.setattr(Path, "open", fail_open)
    with pytest.raises(SessionReadError) as excinfo:
        reader.read_messages(result.entries)
    assert excinfo.value.reason == "read"


def test_reader_missing_file_with_allowed_root_is_missing_not_denied(tmp_path):
    root = tmp_path / "claude" / "projects"
    path = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    reader = SessionReader(path, CLAUDE_SESSION, provider=AgentProvider.CLAUDE, allowed_root=root)
    reader.scan_all()
    path.unlink()
    result = reader.poll()
    assert result.missing is True
    assert result.denied is False


def test_claude_reader_rejects_symlinks_and_paths_outside_root(tmp_path):
    root = tmp_path / "claude" / "projects"
    valid = write_claude(root, "demo", CLAUDE_SESSION, [claude_record("ok")])
    outside = tmp_path / "outside.jsonl"
    outside.write_bytes(valid.read_bytes())

    link = root / "demo" / "66666666-6666-4666-8666-666666666666.jsonl"
    link.symlink_to(outside)
    with pytest.raises(SessionError):
        SessionReader(
            link,
            "66666666-6666-4666-8666-666666666666",
            provider=AgentProvider.CLAUDE,
            allowed_root=root,
        ).scan_all()

    outside_reader = SessionReader(
        outside, CLAUDE_SESSION, provider=AgentProvider.CLAUDE, allowed_root=root
    )
    with pytest.raises(SessionError):
        outside_reader.scan_all()

    reader = SessionReader(valid, CLAUDE_SESSION, provider=AgentProvider.CLAUDE, allowed_root=root)
    reader.scan_all()
    target = tmp_path / "target.jsonl"
    target.write_bytes(valid.read_bytes())
    valid.unlink()
    valid.symlink_to(target)
    assert reader.poll().denied is True


def test_read_session_meta_accepts_unterminated_final_record(tmp_path):
    path = tmp_path / "rollout-x.jsonl"
    path.write_bytes(dump([meta("eof")]).rstrip(b"\n"))
    payload = read_session_meta(path)
    assert payload is not None
    assert payload["id"] == "eof"


def test_read_session_meta_skips_line_at_256k_boundary(tmp_path):
    path = tmp_path / "rollout-x.jsonl"
    path.write_bytes(b"x" * (256 * 1024) + b"\n" + dump([meta("after")]))
    payload = read_session_meta(path)
    assert payload is not None
    assert payload["id"] == "after"
