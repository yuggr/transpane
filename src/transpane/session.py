"""Session discovery and bounded, incremental reading of rollout JSONL files."""

from __future__ import annotations

import json
import os
from collections.abc import Iterator
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .config import INITIAL_SCAN_CHUNK_BYTES, MAX_LINE_BYTES, MAX_POLL_BYTES
from .events import (
    Extractor,
    HistoryMode,
    UnsupportedHistoryMode,
    parse_history_mode,
)
from .models import Message, MessageKind, SessionInfo

META_READ_LIMIT = 1024 * 1024


class SessionError(Exception):
    """Session selection or access failed."""


@dataclass(frozen=True, slots=True)
class IndexEntry:
    message_id: str
    sequence: int
    end: int
    timestamp: str
    kind: MessageKind
    revision: str


@dataclass
class ScanStats:
    records: int = 0
    events: int = 0
    extracted: int = 0
    ignored: int = 0
    invalid_lines: int = 0
    oversized_lines: int = 0


@dataclass
class ScanResult:
    thread_id: str
    history_mode: str | None
    unsupported_mode: str | None
    thread_changed: str | None
    entries: list[IndexEntry]
    stats: ScanStats


@dataclass
class PollResult:
    messages: list[Message] = field(default_factory=list)
    stats: ScanStats = field(default_factory=ScanStats)
    missing: bool = False
    rebuilt: bool = False
    thread_changed: str | None = None
    unsupported_mode: str | None = None


def iter_rollout_files(sessions_root: Path) -> Iterator[Path]:
    if not sessions_root.is_dir():
        return
    try:
        root_real = sessions_root.resolve()
    except OSError:
        return
    for dirpath, dirnames, filenames in os.walk(sessions_root, followlinks=False):
        dirnames[:] = sorted(
            name
            for name in dirnames
            if name != "archived_sessions" and not os.path.islink(os.path.join(dirpath, name))
        )
        for name in sorted(filenames):
            if not (name.startswith("rollout-") and name.endswith(".jsonl")):
                continue
            path = Path(dirpath) / name
            if path.is_symlink():
                continue
            try:
                path.resolve().relative_to(root_real)
            except (OSError, ValueError):
                continue
            yield path


def read_session_meta(path: Path) -> dict[str, Any] | None:
    """Return the first valid session_meta payload, reading only the file head."""
    consumed = 0
    try:
        with path.open("rb") as handle:
            while consumed < META_READ_LIMIT:
                line = handle.readline(256 * 1024)
                if not line:
                    return None
                consumed += len(line)
                if not line.endswith(b"\n") and len(line) >= 256 * 1024:
                    while consumed < META_READ_LIMIT:
                        rest = handle.readline(256 * 1024)
                        if not rest:
                            return None
                        consumed += len(rest)
                        if rest.endswith(b"\n"):
                            break
                    continue
                try:
                    record = json.loads(line)
                except (json.JSONDecodeError, UnicodeDecodeError):
                    continue
                if not isinstance(record, dict) or record.get("type") != "session_meta":
                    continue
                payload = record.get("payload")
                if not isinstance(payload, dict):
                    continue
                thread_id = payload.get("id")
                if isinstance(thread_id, str) and thread_id:
                    return payload
    except OSError:
        return None
    return None


def _optional_str(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


def discover_sessions(sessions_root: Path) -> list[SessionInfo]:
    found: list[SessionInfo] = []
    seen_parents: set[str] = set()

    raw: list[tuple[SessionInfo, str | None]] = []
    for path in iter_rollout_files(sessions_root):
        payload = read_session_meta(path)
        if payload is None:
            continue
        thread_id = payload.get("id")
        if not isinstance(thread_id, str) or not thread_id:
            continue
        try:
            updated_at = path.stat().st_mtime
        except OSError:
            continue
        parent = _optional_str(payload.get("parent_thread_id"))
        info = SessionInfo(
            thread_id=thread_id,
            path=str(path),
            cwd=_optional_str(payload.get("cwd")),
            updated_at=updated_at,
            history_mode=_optional_str(payload.get("history_mode")),
            cli_version=_optional_str(payload.get("cli_version")),
            parent_thread_id=parent,
            source=_optional_str(payload.get("source")),
        )
        raw.append((info, parent))

    seen_parents = {info.thread_id for info, _ in raw}

    for info, parent in raw:
        # Known child sessions are excluded. A child whose parent is not present
        # is kept and labelled by the UI.
        if parent is not None and parent in seen_parents:
            continue
        found.append(info)

    found.sort(key=lambda item: (-item.updated_at, item.thread_id))
    return found


def normalize_cwd(cwd: str) -> str:
    return os.path.normpath(os.path.abspath(os.path.expanduser(cwd)))


def select_session(sessions: list[SessionInfo], thread_id: str) -> SessionInfo:
    matches = [session for session in sessions if session.thread_id == thread_id]
    if not matches:
        raise SessionError(f"セッションが見つかりません: {thread_id}")
    if len(matches) > 1:
        raise SessionError(f"セッションIDが重複しています: {thread_id}")
    return matches[0]


def sessions_for_cwd(sessions: list[SessionInfo], cwd: str) -> list[SessionInfo]:
    wanted = normalize_cwd(cwd)
    return [
        session
        for session in sessions
        if session.cwd is not None and normalize_cwd(session.cwd) == wanted
    ]


class _LineSplitter:
    """Byte-offset aware line buffer that bounds memory for oversized lines."""

    def __init__(self, start: int = 0) -> None:
        self.pos = start
        self.buf = b""
        self.dropping = False

    def feed(self, chunk: bytes) -> tuple[list[tuple[int, bytes]], int]:
        self.buf += chunk
        lines: list[tuple[int, bytes]] = []
        oversized = 0
        while True:
            if self.dropping:
                index = self.buf.find(b"\n")
                if index < 0:
                    self.pos += len(self.buf)
                    self.buf = b""
                    return lines, oversized
                self.pos += index + 1
                self.buf = self.buf[index + 1 :]
                oversized += 1
                self.dropping = False
                continue

            index = self.buf.find(b"\n")
            if index < 0:
                if len(self.buf) > MAX_LINE_BYTES:
                    self.pos += len(self.buf)
                    self.buf = b""
                    self.dropping = True
                return lines, oversized

            line = self.buf[: index + 1]
            if len(line) > MAX_LINE_BYTES:
                self.pos += len(line)
                self.buf = self.buf[index + 1 :]
                oversized += 1
                continue
            lines.append((self.pos, line))
            self.pos += len(line)
            self.buf = self.buf[index + 1 :]

    @property
    def consumed(self) -> int:
        return self.pos + len(self.buf)


class SessionReader:
    """Reads one rollout file from the start, then only appended complete lines."""

    def __init__(
        self,
        path: Path,
        thread_id: str,
        max_poll_bytes: int = MAX_POLL_BYTES,
    ) -> None:
        self.path = path
        self.thread_id = thread_id
        self.max_poll_bytes = max_poll_bytes
        self._splitter = _LineSplitter()
        self._mode: HistoryMode = HistoryMode.LEGACY
        self._extractor = Extractor(thread_id, self._mode)
        self._unsupported: str | None = None
        self._halted = False
        self._device: int | None = None
        self._inode: int | None = None
        self._scan_stats = ScanStats()

    def scan_all(self) -> ScanResult:
        self._splitter = _LineSplitter()
        self._mode = HistoryMode.LEGACY
        self._extractor = Extractor(self.thread_id, self._mode)
        self._unsupported = None
        self._halted = False
        self._scan_stats = ScanStats()
        entries: list[IndexEntry] = []
        thread_changed: str | None = None

        with self.path.open("rb") as handle:
            while True:
                chunk = handle.read(INITIAL_SCAN_CHUNK_BYTES)
                if not chunk:
                    break
                lines, oversized = self._splitter.feed(chunk)
                self._scan_stats.oversized_lines += oversized
                for offset, raw in lines:
                    message, changed = self._process_line(offset, raw, self._scan_stats)
                    if changed is not None and thread_changed is None:
                        thread_changed = changed
                    if message is not None:
                        entries.append(
                            IndexEntry(
                                message_id=message.message_id,
                                sequence=message.sequence,
                                end=message.sequence + len(raw),
                                timestamp=message.timestamp,
                                kind=message.kind,
                                revision=message.revision,
                            )
                        )

        stat = self.path.stat()
        self._device = stat.st_dev
        self._inode = stat.st_ino
        return ScanResult(
            thread_id=self.thread_id,
            history_mode=None if self._unsupported else self._mode.value,
            unsupported_mode=self._unsupported,
            thread_changed=thread_changed,
            entries=entries,
            stats=self._scan_stats,
        )

    def poll(self) -> PollResult:
        try:
            stat = self.path.stat()
        except OSError:
            return PollResult(missing=True)

        if self._device is not None and (stat.st_dev != self._device or stat.st_ino != self._inode):
            return PollResult(rebuilt=True)
        consumed = self._splitter.consumed
        if stat.st_size < consumed:
            return PollResult(rebuilt=True)

        remaining = stat.st_size - consumed
        if remaining <= 0:
            return PollResult()

        with self.path.open("rb") as handle:
            handle.seek(consumed)
            chunk = handle.read(min(self.max_poll_bytes, remaining))

        stats = ScanStats()
        lines, oversized = self._splitter.feed(chunk)
        stats.oversized_lines += oversized
        messages: list[Message] = []
        thread_changed: str | None = None
        for offset, raw in lines:
            message, changed = self._process_line(offset, raw, stats)
            if changed is not None and thread_changed is None:
                thread_changed = changed
            if message is not None:
                messages.append(message)

        return PollResult(
            messages=messages,
            stats=stats,
            thread_changed=thread_changed,
            unsupported_mode=self._unsupported,
        )

    def read_messages(self, entries: list[IndexEntry]) -> list[Message]:
        if self._unsupported is not None:
            return []
        messages: list[Message] = []
        with self.path.open("rb") as handle:
            for entry in entries:
                handle.seek(entry.sequence)
                raw = handle.read(entry.end - entry.sequence)
                try:
                    record = json.loads(raw)
                except (json.JSONDecodeError, UnicodeDecodeError):
                    continue
                if not isinstance(record, dict):
                    continue
                message = self._extract_record(record, entry.sequence)
                if message is None or message.message_id != entry.message_id:
                    continue
                messages.append(message)
        return messages

    @property
    def unsupported_mode(self) -> str | None:
        return self._unsupported

    def _extract_record(self, record: dict[str, Any], offset: int) -> Message | None:
        if self._unsupported is not None or self._halted:
            return None
        if record.get("type") == "session_meta":
            return None
        return self._extractor.extract(record, offset).message

    def _process_line(
        self, offset: int, raw: bytes, stats: ScanStats
    ) -> tuple[Message | None, str | None]:
        stats.records += 1
        try:
            record = json.loads(raw)
        except (json.JSONDecodeError, UnicodeDecodeError):
            stats.invalid_lines += 1
            return None, None
        if not isinstance(record, dict):
            stats.invalid_lines += 1
            return None, None

        if record.get("type") == "session_meta":
            payload = record.get("payload")
            if isinstance(payload, dict):
                seen_id = payload.get("id")
                if isinstance(seen_id, str) and seen_id and seen_id != self.thread_id:
                    self._halted = True
                    return None, seen_id
                try:
                    mode = parse_history_mode(payload.get("history_mode"))
                except UnsupportedHistoryMode as exc:
                    self._unsupported = repr(exc.mode)
                    self._halted = True
                    return None, None
                self._mode = mode
                self._extractor = Extractor(self.thread_id, mode)
            return None, None

        stats.events += 1
        if self._halted:
            stats.ignored += 1
            return None, None

        message = self._extract_record(record, offset)
        if message is None:
            stats.ignored += 1
            return None, None
        stats.extracted += 1
        return message, None
