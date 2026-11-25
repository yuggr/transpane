from __future__ import annotations

from pathlib import Path

from transpane.models import AgentProvider, MessageKind
from transpane.session import SessionReader

FIXTURES = Path(__file__).parent / "fixtures"


def test_legacy_fixture_extracts_only_allowlisted_events():
    reader = SessionReader(FIXTURES / "legacy.jsonl", "legacy-thread")
    result = reader.scan_all()
    assert result.history_mode == "legacy"
    assert result.stats.extracted == 2
    assert result.stats.ignored == 1
    messages = reader.read_messages(result.entries)
    assert [message.kind for message in messages] == [
        MessageKind.USER,
        MessageKind.COMMENTARY,
    ]
    assert messages[1].original == "I will inspect the storage layer."


def test_paginated_fixture_preserves_order_and_phases():
    reader = SessionReader(FIXTURES / "paginated.jsonl", "new-thread")
    result = reader.scan_all()
    assert result.history_mode == "paginated"
    messages = reader.read_messages(result.entries)
    assert [message.message_id for message in messages] == ["u1", "a1", "a2"]
    assert [message.kind for message in messages] == [
        MessageKind.USER,
        MessageKind.COMMENTARY,
        MessageKind.FINAL,
    ]


def test_claude_fixture_extracts_only_public_text():
    reader = SessionReader(
        FIXTURES / "claude.jsonl",
        "11111111-1111-4111-8111-111111111111",
        provider=AgentProvider.CLAUDE,
    )
    result = reader.scan_all()
    assert result.history_mode == "claude-jsonl"
    assert result.stats.extracted == 4
    assert result.stats.ignored == 3
    messages = reader.read_messages(result.entries)
    assert [message.kind for message in messages] == [
        MessageKind.USER,
        MessageKind.ASSISTANT,
        MessageKind.ASSISTANT,
        MessageKind.ASSISTANT,
    ]
    assert [message.original for message in messages] == [
        "保存処理を確認して",
        "I will inspect the storage layer.",
        "The storage layer uses atomic writes.",
        "The targeted tests passed.",
    ]
    assert all("DO_NOT_SEND" not in message.original for message in messages)
