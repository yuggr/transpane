"""Allowlisted conversion of Codex rollout records into display messages.

Only the explicitly supported event forms are converted.  Everything else,
including ``response_item`` (the model-facing history), is counted as ignored
and never reaches the translation server.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Any

from .models import Message, MessageKind, make_revision


class HistoryMode(StrEnum):
    LEGACY = "legacy"
    PAGINATED = "paginated"


class UnsupportedHistoryMode(Exception):
    def __init__(self, mode: Any) -> None:
        super().__init__(f"未対応の history_mode: {mode!r}")
        self.mode = mode


def parse_history_mode(value: Any) -> HistoryMode:
    """Map the metadata value to a supported mode (absent means legacy)."""
    if value is None or value == "":
        return HistoryMode.LEGACY
    if value == "legacy":
        return HistoryMode.LEGACY
    if value == "paginated":
        return HistoryMode.PAGINATED
    raise UnsupportedHistoryMode(value)


@dataclass(frozen=True, slots=True)
class ExtractResult:
    message: Message | None = None
    reason: str = "not_allowed"


def kind_for_phase(phase: Any) -> MessageKind:
    if phase == "commentary":
        return MessageKind.COMMENTARY
    if phase == "final_answer":
        return MessageKind.FINAL
    return MessageKind.ASSISTANT


def _as_dict(value: Any) -> dict[str, Any]:
    return value if isinstance(value, dict) else {}


def _join_content(content: Any, element_type: str) -> str:
    if not isinstance(content, list):
        return ""
    chunks: list[str] = []
    for element in content:
        if not isinstance(element, dict):
            continue
        if element.get("type") != element_type:
            continue
        text = element.get("text")
        if isinstance(text, str):
            chunks.append(text)
    return "".join(chunks)


def _timestamp(record: dict[str, Any]) -> str:
    value = record.get("timestamp")
    return value if isinstance(value, str) else ""


class Extractor:
    """Stateful extractor for one session's history mode."""

    def __init__(self, thread_id: str, mode: HistoryMode) -> None:
        self.thread_id = thread_id
        self.mode = mode

    def extract(self, record: dict[str, Any], offset: int) -> ExtractResult:
        record_type = record.get("type")
        payload = _as_dict(record.get("payload"))

        if record_type == "event_msg":
            payload_type = payload.get("type")

            if payload_type == "item_completed":
                item = payload.get("item")
                if isinstance(item, dict):
                    result = self._extract_item(item, record, offset)
                    if result is not None:
                        return result
                return ExtractResult(reason="not_allowed")

            if self.mode is HistoryMode.LEGACY:
                if payload_type == "user_message":
                    return self._from_text(payload.get("message"), MessageKind.USER, record, offset)
                if payload_type == "agent_message":
                    return self._from_text(
                        payload.get("message"),
                        kind_for_phase(payload.get("phase")),
                        record,
                        offset,
                    )

        return ExtractResult(reason="not_allowed")

    def _extract_item(
        self, item: dict[str, Any], record: dict[str, Any], offset: int
    ) -> ExtractResult | None:
        item_type = item.get("type")

        if item_type == "Plan":
            text = item.get("text")
            if not isinstance(text, str) or not text.strip():
                return ExtractResult(reason="empty")
            message_id = item.get("id")
            if not isinstance(message_id, str) or not message_id:
                message_id = f"{self.thread_id}:{offset}"
            return ExtractResult(self._build(message_id, text, MessageKind.PLAN, record, offset))

        if self.mode is not HistoryMode.PAGINATED:
            return None

        if item_type == "UserMessage":
            text = _join_content(item.get("content"), "text")
            kind = MessageKind.USER
        elif item_type == "AgentMessage":
            text = _join_content(item.get("content"), "Text")
            kind = kind_for_phase(item.get("phase"))
        else:
            return None

        message_id = item.get("id")
        if not isinstance(message_id, str) or not message_id:
            return ExtractResult(reason="missing_id")
        if not text.strip():
            return ExtractResult(reason="empty")
        return ExtractResult(self._build(message_id, text, kind, record, offset))

    def _from_text(
        self, text: Any, kind: MessageKind, record: dict[str, Any], offset: int
    ) -> ExtractResult:
        if not isinstance(text, str):
            return ExtractResult(reason="bad_payload")
        if not text.strip():
            return ExtractResult(reason="empty")
        message_id = f"{self.thread_id}:{offset}"
        return ExtractResult(self._build(message_id, text, kind, record, offset))

    def _build(
        self,
        message_id: str,
        text: str,
        kind: MessageKind,
        record: dict[str, Any],
        offset: int,
    ) -> Message:
        return Message(
            thread_id=self.thread_id,
            message_id=message_id,
            revision=make_revision(text),
            sequence=offset,
            timestamp=_timestamp(record),
            kind=kind,
            original=text,
        )


_CLAUDE_FLAGS = (
    "isSidechain",
    "isMeta",
    "isCompactSummary",
    "isVisibleInTranscriptOnly",
    "isApiErrorMessage",
)
_CLAUDE_INTERNAL_TAGS = (
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
)
_CLAUDE_HIDDEN_ASSISTANT_BLOCKS = frozenset({"thinking", "redacted_thinking", "tool_use"})
_CLAUDE_IGNORED_USER_BLOCKS = frozenset({"image"})
_CLAUDE_SYNTHETIC_MODEL = "<synthetic>"


class ClaudeExtractor:
    """Allowlisted conversion of Claude Code transcript records."""

    def __init__(self, thread_id: str) -> None:
        self.thread_id = thread_id

    def extract(self, record: dict[str, Any], offset: int) -> ExtractResult:
        record_type = record.get("type")
        if record_type not in ("user", "assistant"):
            return ExtractResult(reason="not_allowed")
        # Internal/excluded records must not be mistaken for a session change.
        if self._excluded_by_flag(record):
            return ExtractResult(reason="excluded")

        session_id = record.get("sessionId")
        if not isinstance(session_id, str) or not session_id:
            return ExtractResult(reason="not_allowed")
        if session_id != self.thread_id:
            return ExtractResult(reason="thread_mismatch")

        message_id = record.get("uuid")
        if not isinstance(message_id, str) or not message_id:
            return ExtractResult(reason="missing_id")

        message = record.get("message")
        if not isinstance(message, dict):
            return ExtractResult(reason="bad_payload")
        if message.get("role") != record_type:
            return ExtractResult(reason="role_mismatch")
        if message.get("model") == _CLAUDE_SYNTHETIC_MODEL:
            return ExtractResult(reason="synthetic")

        content = message.get("content")
        if record_type == "user":
            if record.get("toolUseResult") is not None:
                return ExtractResult(reason="excluded")
            text = self._user_text(content)
        else:
            text = self._assistant_text(content)

        if text is None:
            return ExtractResult(reason="excluded")
        if not text.strip():
            return ExtractResult(reason="empty")
        if any(tag in text for tag in _CLAUDE_INTERNAL_TAGS):
            return ExtractResult(reason="excluded")

        return ExtractResult(
            Message(
                thread_id=self.thread_id,
                message_id=message_id,
                revision=make_revision(text),
                sequence=offset,
                timestamp=_timestamp(record),
                kind=MessageKind.USER if record_type == "user" else MessageKind.ASSISTANT,
                original=text,
            )
        )

    def _excluded_by_flag(self, record: dict[str, Any]) -> bool:
        for name in _CLAUDE_FLAGS:
            if name not in record:
                continue
            value = record[name]
            if not isinstance(value, bool) or value:
                return True
        return False

    def _user_text(self, content: Any) -> str | None:
        if isinstance(content, str):
            return content
        if not isinstance(content, list):
            return None
        texts: list[str] = []
        for block in content:
            if not isinstance(block, dict):
                return None
            block_type = block.get("type")
            if block_type == "text":
                text = block.get("text")
                if not isinstance(text, str):
                    return None
                if text.strip():
                    texts.append(text)
            elif block_type in _CLAUDE_IGNORED_USER_BLOCKS:
                continue
            else:
                return None
        return "\n\n".join(texts)

    def _assistant_text(self, content: Any) -> str | None:
        if not isinstance(content, list):
            return None
        texts: list[str] = []
        for block in content:
            if not isinstance(block, dict):
                return None
            block_type = block.get("type")
            if block_type == "text":
                text = block.get("text")
                if not isinstance(text, str):
                    return None
                if text.strip():
                    texts.append(text)
            elif block_type in _CLAUDE_HIDDEN_ASSISTANT_BLOCKS:
                continue
            else:
                return None
        return "\n\n".join(texts)
