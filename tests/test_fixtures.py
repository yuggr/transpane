from __future__ import annotations

from pathlib import Path

from transpane.models import MessageKind
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
