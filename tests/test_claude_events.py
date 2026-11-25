from __future__ import annotations

from typing import Any

import pytest

from transpane.events import ClaudeExtractor
from transpane.models import MessageKind

SESSION = "11111111-1111-4111-8111-111111111111"


def record(
    kind: str = "user",
    content: Any = "hello",
    *,
    uuid: str = "00000000-0000-4000-8000-000000000010",
    session: str = SESSION,
    **extra: Any,
) -> dict[str, Any]:
    body: dict[str, Any] = {
        "type": kind,
        "sessionId": session,
        "uuid": uuid,
        "timestamp": "2026-11-25T03:00:00Z",
        "message": {"role": kind, "content": content},
        **extra,
    }
    return body


def extract(record: dict[str, Any], offset: int = 0):
    return ClaudeExtractor(SESSION).extract(record, offset)


def test_user_string_and_text_blocks_are_displayed():
    string_result = extract(record("user", "保存処理を確認して"), 10)
    assert string_result.message is not None
    assert string_result.message.kind is MessageKind.USER
    assert string_result.message.original == "保存処理を確認して"
    assert string_result.message.thread_id == SESSION
    assert string_result.message.sequence == 10

    blocks = extract(
        record(
            "user",
            [
                {"type": "text", "text": "first line"},
                {"type": "image", "source": {"type": "base64", "data": "AAAA"}},
                {"type": "text", "text": "second line"},
            ],
        )
    )
    assert blocks.message is not None
    assert blocks.message.original == "first line\n\nsecond line"


def test_assistant_text_blocks_join_and_hidden_blocks_are_dropped():
    result = extract(
        record(
            "assistant",
            [
                {"type": "thinking", "thinking": "DO_NOT_SEND_THINKING"},
                {"type": "redacted_thinking", "data": "DO_NOT_SEND_REDACTED"},
                {"type": "tool_use", "id": "tool_a", "name": "Read", "input": {}},
                {"type": "text", "text": "I will inspect the storage layer."},
                {"type": "text", "text": "The tests passed."},
            ],
        ),
        20,
    )
    assert result.message is not None
    assert result.message.kind is MessageKind.ASSISTANT
    assert result.message.original == "I will inspect the storage layer.\n\nThe tests passed."


@pytest.mark.parametrize(
    "kind,content",
    [
        ("user", [{"type": "text", "text": "keep"}, {"type": "tool_result", "content": "drop"}]),
        ("user", [{"type": "unknown", "text": "drop"}]),
        ("user", [{"type": "text", "text": "drop"}, "not-an-object"]),
        ("assistant", [{"type": "text", "text": "drop"}, {"type": "unknown"}]),
        ("assistant", "a string assistant content is unverified"),
        ("assistant", [{"type": "text", "text": "  "}]),
        ("user", ""),
        ("user", []),
    ],
)
def test_unverified_or_empty_shapes_are_not_adopted(kind, content):
    result = extract(record(kind, content))
    assert result.message is None


@pytest.mark.parametrize(
    "flag",
    [
        "isSidechain",
        "isMeta",
        "isCompactSummary",
        "isVisibleInTranscriptOnly",
        "isApiErrorMessage",
    ],
)
def test_exclusion_flags_and_invalid_types(flag):
    assert extract(record("user", "hidden", **{flag: True})).message is None
    assert extract(record("user", "shown", **{flag: False})).message is not None
    assert extract(record("user", "hidden", **{flag: "true"})).message is None


def test_tool_result_and_tool_use_result_are_excluded():
    with_result = record(
        "user", [{"type": "text", "text": "drop"}, {"type": "tool_result", "content": "x"}]
    )
    assert extract(with_result).message is None
    assert extract(record("user", "drop", toolUseResult={"stdout": "x"})).message is None
    assert extract(record("user", "keep", toolUseResult=None)).message is not None


@pytest.mark.parametrize(
    "tag",
    [
        "<system-reminder>",
        "<local-command-caveat>",
        "<local-command-stdout>",
        "<local-command-stderr>",
        "<command-message>",
        "<command-name>",
        "<command-args>",
        "<task-notification>",
        "<persisted-output>",
        "<bash-stdout>",
    ],
)
def test_internal_wrappers_are_excluded_for_user_and_assistant(tag):
    text = f"{tag} internal"
    assert extract(record("user", text)).message is None
    assert extract(record("assistant", [{"type": "text", "text": text}])).message is None


def test_exclusion_flags_take_priority_over_session_mismatch():
    sidechain = extract(record("user", "hidden", session="other", isSidechain=True))
    assert sidechain.message is None
    assert sidechain.reason == "excluded"

    mismatch = extract(record("user", "visible", session="other"))
    assert mismatch.message is None
    assert mismatch.reason == "thread_mismatch"


def test_identity_and_role_contract():
    assert extract(record("user", "x", session="other")).reason == "thread_mismatch"
    assert extract(record("user", "x", uuid="")).reason == "missing_id"
    assert extract({"type": "system", "sessionId": SESSION}).message is None

    wrong_role = record("user", "x")
    wrong_role["message"]["role"] = "assistant"
    assert extract(wrong_role).message is None

    synthetic = record("assistant", [{"type": "text", "text": "x"}])
    synthetic["message"]["model"] = "<synthetic>"
    assert extract(synthetic).message is None


def test_synthetic_sentinel_is_excluded_for_both_record_types():
    for kind in ("user", "assistant"):
        content = "x" if kind == "user" else [{"type": "text", "text": "x"}]
        synthetic = record(kind, content)
        synthetic["message"]["model"] = "<synthetic>"
        assert extract(synthetic).message is None


def test_same_message_id_with_different_uuids_stays_separate():
    first = record("assistant", [{"type": "text", "text": "first"}], uuid="u-1")
    second = record("assistant", [{"type": "text", "text": "second"}], uuid="u-2")
    first["message"]["id"] = "msg_a"
    second["message"]["id"] = "msg_a"
    one = extract(first)
    two = extract(second)
    assert one.message is not None and two.message is not None
    assert one.message.message_id == "u-1"
    assert two.message.message_id == "u-2"


def test_timestamp_missing_and_revision_is_content_hash():
    no_timestamp = record("assistant", [{"type": "text", "text": "hello"}])
    no_timestamp.pop("timestamp")
    result = extract(no_timestamp)
    assert result.message is not None
    assert result.message.timestamp == ""
    assert len(result.message.revision) == 64
