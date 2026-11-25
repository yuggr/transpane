from __future__ import annotations

import pytest

from transpane.events import (
    Extractor,
    HistoryMode,
    UnsupportedHistoryMode,
    parse_history_mode,
)
from transpane.models import MessageKind


def test_legacy_extraction_skips_response_items():
    extractor = Extractor("legacy-thread", HistoryMode.LEGACY)
    user = extractor.extract(
        {
            "timestamp": "2026-11-25T01:00:01Z",
            "type": "event_msg",
            "payload": {"type": "user_message", "message": "保存処理を調べて"},
        },
        10,
    )
    assert user.message is not None
    assert user.message.kind is MessageKind.USER
    assert user.message.timestamp == "2026-11-25T01:00:01Z"
    assert user.message.sequence == 10

    agent = extractor.extract(
        {
            "timestamp": "2026-11-25T01:00:02Z",
            "type": "event_msg",
            "payload": {
                "type": "agent_message",
                "message": "I will inspect the storage layer.",
                "phase": "commentary",
            },
        },
        20,
    )
    assert agent.message is not None
    assert agent.message.kind is MessageKind.COMMENTARY

    response_item = extractor.extract(
        {
            "type": "response_item",
            "payload": {
                "type": "message",
                "role": "assistant",
                "content": [{"type": "output_text", "text": "duplicate"}],
            },
        },
        30,
    )
    assert response_item.message is None
    assert response_item.reason == "not_allowed"


def test_paginated_extraction_and_phases():
    extractor = Extractor("new-thread", HistoryMode.PAGINATED)
    user = extractor.extract(
        {
            "timestamp": "2026-11-25T01:00:01Z",
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "turn_id": "turn-1",
                "item": {
                    "type": "UserMessage",
                    "id": "u1",
                    "content": [
                        {"type": "text", "text": "保存処理を"},
                        {"type": "text", "text": "調べて"},
                    ],
                },
            },
        },
        40,
    )
    assert user.message is not None
    assert user.message.message_id == "u1"
    assert user.message.original == "保存処理を調べて"

    for phase, expected in (
        ("commentary", MessageKind.COMMENTARY),
        ("final_answer", MessageKind.FINAL),
        ("other", MessageKind.ASSISTANT),
    ):
        result = extractor.extract(
            {
                "type": "event_msg",
                "payload": {
                    "type": "item_completed",
                    "item": {
                        "type": "AgentMessage",
                        "id": f"a-{phase}",
                        "phase": phase,
                        "content": [{"type": "Text", "text": "Done."}],
                    },
                },
            },
            50,
        )
        assert result.message is not None
        assert result.message.kind is expected


def test_plan_is_extracted_in_both_modes():
    for mode in (HistoryMode.LEGACY, HistoryMode.PAGINATED):
        extractor = Extractor("t", mode)
        result = extractor.extract(
            {
                "type": "event_msg",
                "payload": {
                    "type": "item_completed",
                    "item": {"type": "Plan", "id": "plan-1", "text": "- step one\n- step two"},
                },
            },
            60,
        )
        assert result.message is not None
        assert result.message.kind is MessageKind.PLAN
        assert result.message.message_id == "plan-1"


def test_plan_without_id_uses_offset():
    extractor = Extractor("t", HistoryMode.PAGINATED)
    result = extractor.extract(
        {
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "item": {"type": "Plan", "text": "Step one"},
            },
        },
        70,
    )
    assert result.message is not None
    assert result.message.message_id == "t:70"


def test_strict_mode_separation():
    legacy = Extractor("t", HistoryMode.LEGACY)
    result = legacy.extract(
        {
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "item": {
                    "type": "AgentMessage",
                    "id": "a1",
                    "content": [{"type": "Text", "text": "x"}],
                },
            },
        },
        0,
    )
    assert result.message is None

    paginated = Extractor("t", HistoryMode.PAGINATED)
    result = paginated.extract(
        {"type": "event_msg", "payload": {"type": "agent_message", "message": "x"}},
        0,
    )
    assert result.message is None


def test_missing_item_id_and_empty_text():
    extractor = Extractor("t", HistoryMode.PAGINATED)
    result = extractor.extract(
        {
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "item": {"type": "AgentMessage", "content": [{"type": "Text", "text": "x"}]},
            },
        },
        0,
    )
    assert result.message is None
    assert result.reason == "missing_id"

    result = extractor.extract(
        {
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "item": {
                    "type": "AgentMessage",
                    "id": "a",
                    "content": [{"type": "Text", "text": "  "}],
                },
            },
        },
        0,
    )
    assert result.message is None
    assert result.reason == "empty"


def test_unknown_history_mode():
    with pytest.raises(UnsupportedHistoryMode):
        parse_history_mode("chunked")
    assert parse_history_mode(None) is HistoryMode.LEGACY
    assert parse_history_mode("legacy") is HistoryMode.LEGACY
    assert parse_history_mode("paginated") is HistoryMode.PAGINATED


def test_revision_is_content_hash():
    extractor = Extractor("t", HistoryMode.PAGINATED)
    record = {
        "type": "event_msg",
        "payload": {
            "type": "item_completed",
            "item": {
                "type": "AgentMessage",
                "id": "a",
                "content": [{"type": "Text", "text": "hello"}],
            },
        },
    }
    first = extractor.extract(record, 0).message
    second = extractor.extract(record, 0).message
    assert first is not None and second is not None
    assert first.revision == second.revision
    assert len(first.revision) == 64
