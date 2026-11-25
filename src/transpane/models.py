"""Core data structures shared by the reader, extractor, UI and scheduler."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum


class AgentProvider(StrEnum):
    CODEX = "codex"
    CLAUDE = "claude"

    @property
    def label(self) -> str:
        labels = {
            AgentProvider.CODEX: "Codex",
            AgentProvider.CLAUDE: "Claude Code",
        }
        return labels[self]


class MessageKind(StrEnum):
    USER = "user"
    COMMENTARY = "commentary"
    FINAL = "final"
    PLAN = "plan"
    ASSISTANT = "assistant"

    @property
    def label(self) -> str:
        labels = {
            MessageKind.USER: "あなた",
            MessageKind.COMMENTARY: "進捗報告",
            MessageKind.FINAL: "最終回答",
            MessageKind.PLAN: "計画",
            MessageKind.ASSISTANT: "応答",
        }
        return labels[self]


class TranslationState(StrEnum):
    ORIGINAL = "original"
    SKIPPED = "skipped"
    QUEUED = "queued"
    TRANSLATING = "translating"
    TRANSLATED = "translated"
    PARTIAL = "partial"
    FAILED = "failed"

    @property
    def label(self) -> str:
        labels = {
            TranslationState.ORIGINAL: "未翻訳",
            TranslationState.SKIPPED: "",
            TranslationState.QUEUED: "待機",
            TranslationState.TRANSLATING: "翻訳中",
            TranslationState.TRANSLATED: "",
            TranslationState.PARTIAL: "部分翻訳",
            TranslationState.FAILED: "翻訳失敗",
        }
        return labels[self]


@dataclass(frozen=True, slots=True)
class Message:
    thread_id: str
    message_id: str
    revision: str
    sequence: int
    timestamp: str
    kind: MessageKind
    original: str


def make_revision(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


@dataclass(frozen=True, slots=True)
class SessionInfo:
    thread_id: str
    path: str
    cwd: str | None
    updated_at: float
    history_mode: str | None
    cli_version: str | None
    parent_thread_id: str | None
    source: str | None
    provider: AgentProvider = AgentProvider.CODEX

    @property
    def display_id(self) -> str:
        if len(self.thread_id) <= 8:
            return self.thread_id
        return f"{self.thread_id[:8]}…"

    @property
    def is_subagent(self) -> bool:
        return self.parent_thread_id is not None

    @property
    def origin_label(self) -> str:
        return "子セッション(親不明)" if self.parent_thread_id is not None else ""

    @property
    def mode_label(self) -> str:
        if self.history_mode == "claude-jsonl":
            return "claude-jsonl"
        if self.history_mode in (None, "", "legacy"):
            return "legacy"
        if self.history_mode == "paginated":
            return "paginated"
        return "未対応"


def format_timestamp(timestamp: str) -> str:
    """Render an RFC3339 timestamp as local HH:MM:SS, or an explicit unknown label."""
    try:
        parsed = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    except (TypeError, ValueError):
        return "時刻不明"
    if parsed.tzinfo is None:
        return parsed.strftime("%H:%M:%S")
    return parsed.astimezone().strftime("%H:%M:%S")
