"""Session discovery and bounded, incremental reading of rollout JSONL files."""

from __future__ import annotations

import json
import os
from collections.abc import Iterator
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, BinaryIO

from .claude import CLAUDE_HISTORY_MODE, discover_claude_sessions
from .config import INITIAL_SCAN_CHUNK_BYTES, MAX_LINE_BYTES, MAX_POLL_BYTES
from .events import (
    ClaudeExtractor,
    Extractor,
    HistoryMode,
    UnsupportedHistoryMode,
    parse_history_mode,
)
from .models import AgentProvider, Message, MessageKind, SessionInfo

META_READ_LIMIT = 1024 * 1024


class SessionError(Exception):
    """Session selection or access failed."""


class SessionReadError(SessionError):
    """The transcript could not be read safely."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class SessionIndexMismatch(SessionReadError):
    """The on-disk record no longer matches the scan index."""

    def __init__(self) -> None:
        super().__init__("index_mismatch")


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
    entries: list[IndexEntry] = field(default_factory=list)
    stats: ScanStats = field(default_factory=ScanStats)
    missing: bool = False
    rebuilt: bool = False
    denied: bool = False
    thread_changed: str | None = None
    unsupported_mode: str | None = None


def _is_rollout_file_name(name: str) -> bool:
    return name.startswith("rollout-") and name.endswith(".jsonl")


def _within_root(path: Path, root_real: Path) -> bool:
    try:
        path.resolve().relative_to(root_real)
    except (OSError, ValueError):
        return False
    return True


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
            if not _is_rollout_file_name(name):
                continue
            path = Path(dirpath) / name
            if path.is_symlink():
                continue
            if not _within_root(path, root_real):
                continue
            yield path


def _discard_rest_of_oversized_line(handle: BinaryIO, consumed: int) -> int | None:
    while consumed < META_READ_LIMIT:
        rest = handle.readline(256 * 1024)
        if not rest:
            return None
        consumed += len(rest)
        if rest.endswith(b"\n"):
            break
    return consumed


def _session_meta_payload(record: object) -> dict[str, Any] | None:
    if not isinstance(record, dict) or record.get("type") != "session_meta":
        return None
    payload = record.get("payload")
    if not isinstance(payload, dict):
        return None
    thread_id = payload.get("id")
    if isinstance(thread_id, str) and thread_id:
        return payload
    return None


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
                    discarded = _discard_rest_of_oversized_line(handle, consumed)
                    if discarded is None:
                        return None
                    consumed = discarded
                    continue
                try:
                    record = json.loads(line)
                except (json.JSONDecodeError, UnicodeDecodeError):
                    continue
                payload = _session_meta_payload(record)
                if payload is not None:
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
            provider=AgentProvider.CODEX,
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


def discover_all_sessions(
    codex_root: Path,
    claude_projects_root: Path,
    provider: AgentProvider | None = None,
) -> list[SessionInfo]:
    found: list[SessionInfo] = []
    if provider in (None, AgentProvider.CODEX):
        found.extend(discover_sessions(codex_root))
    if provider in (None, AgentProvider.CLAUDE):
        found.extend(discover_claude_sessions(claude_projects_root))
    found.sort(key=lambda item: (-item.updated_at, item.provider.value, item.thread_id, item.path))
    return found


def normalize_cwd(cwd: str) -> str:
    return os.path.normpath(os.path.abspath(os.path.expanduser(cwd)))


def parse_session_ref(ref: str) -> tuple[AgentProvider | None, str]:
    prefix, separator, rest = ref.partition(":")
    if separator and rest and prefix in (AgentProvider.CODEX.value, AgentProvider.CLAUDE.value):
        return AgentProvider(prefix), rest
    return None, ref


def reader_for_session(
    session: SessionInfo, codex_root: Path, claude_projects_root: Path
) -> SessionReader:
    root = claude_projects_root if session.provider is AgentProvider.CLAUDE else codex_root
    return SessionReader(
        Path(session.path),
        session.thread_id,
        provider=session.provider,
        allowed_root=root,
    )


def select_session(
    sessions: list[SessionInfo],
    ref: str,
    *,
    source: AgentProvider | None = None,
) -> SessionInfo:
    wanted_provider, thread_id = parse_session_ref(ref)
    if wanted_provider is not None and source is not None and wanted_provider is not source:
        raise SessionError("--source とセッションIDの接頭辞が矛盾しています")
    matches = [
        session
        for session in sessions
        if session.thread_id == thread_id and (source is None or session.provider is source)
    ]
    if wanted_provider is not None:
        matches = [session for session in matches if session.provider is wanted_provider]
    if not matches:
        raise SessionError(f"セッションが見つかりません: {ref}")
    if len(matches) > 1:
        raise SessionError(f"セッションIDが重複しています: {ref}")
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
        *,
        provider: AgentProvider = AgentProvider.CODEX,
        allowed_root: Path | None = None,
    ) -> None:
        self.path = path
        self.thread_id = thread_id
        self.max_poll_bytes = max_poll_bytes
        self.provider = provider
        self.allowed_root = allowed_root
        self._splitter = _LineSplitter()
        self._mode: HistoryMode | None = None
        self._extractor: Extractor | ClaudeExtractor = ClaudeExtractor(thread_id)
        self._reset_extractor()
        self._unsupported: str | None = None
        self._halted = False
        self._device: int | None = None
        self._inode: int | None = None
        self._scan_stats = ScanStats()

    def _reset_extractor(self) -> None:
        if self.provider is AgentProvider.CLAUDE:
            self._mode = None
            self._extractor = ClaudeExtractor(self.thread_id)
        else:
            self._mode = HistoryMode.LEGACY
            self._extractor = Extractor(self.thread_id, self._mode)

    def _source_allowed(self) -> bool:
        if self.allowed_root is None:
            return True
        try:
            if self.path.is_symlink() or not self.path.is_file():
                return False
            self.path.resolve().relative_to(self.allowed_root.resolve())
        except (OSError, ValueError):
            return False
        return not (self.provider is AgentProvider.CLAUDE and self.path.stem != self.thread_id)

    def scan_all(self) -> ScanResult:
        if not self._source_allowed():
            raise SessionError("セッションのパスが許可範囲外です")
        self._splitter = _LineSplitter()
        self._reset_extractor()
        self._unsupported = None
        self._halted = False
        self._scan_stats = ScanStats()

        with self.path.open("rb") as handle:
            entries, thread_changed = self._scan_file(handle)

        stat = self.path.stat()
        self._device = stat.st_dev
        self._inode = stat.st_ino
        return ScanResult(
            thread_id=self.thread_id,
            history_mode=self._history_mode_label(),
            unsupported_mode=self._unsupported,
            thread_changed=thread_changed,
            entries=entries,
            stats=self._scan_stats,
        )

    def _scan_file(self, handle: BinaryIO) -> tuple[list[IndexEntry], str | None]:
        entries: list[IndexEntry] = []
        positions: dict[str, int] = {}
        thread_changed: str | None = None
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
                if message is None:
                    continue
                entry = self._make_entry(message, len(raw))
                if self.provider is AgentProvider.CLAUDE:
                    self._append_scan_entry(entries, positions, entry)
                else:
                    entries.append(entry)
        return entries, thread_changed

    @staticmethod
    def _append_scan_entry(
        entries: list[IndexEntry], positions: dict[str, int], entry: IndexEntry
    ) -> None:
        position = positions.get(entry.message_id)
        if position is None:
            positions[entry.message_id] = len(entries)
            entries.append(entry)
        elif entries[position].revision != entry.revision:
            entries[position] = entry

    def _history_mode_label(self) -> str | None:
        if self.provider is AgentProvider.CLAUDE:
            return CLAUDE_HISTORY_MODE
        return None if self._unsupported or self._mode is None else self._mode.value

    @staticmethod
    def _make_entry(message: Message, raw_length: int) -> IndexEntry:
        return IndexEntry(
            message_id=message.message_id,
            sequence=message.sequence,
            end=message.sequence + raw_length,
            timestamp=message.timestamp,
            kind=message.kind,
            revision=message.revision,
        )

    def _poll_precheck(self) -> tuple[PollResult | None, os.stat_result | None]:
        if self.path.is_symlink():
            return PollResult(denied=True), None
        if not self.path.exists():
            return PollResult(missing=True), None
        if not self._source_allowed():
            return PollResult(denied=True), None
        try:
            stat = self.path.stat()
        except OSError:
            return PollResult(missing=True), None

        if self._device is not None and (stat.st_dev != self._device or stat.st_ino != self._inode):
            return PollResult(rebuilt=True), None
        if stat.st_size < self._splitter.consumed:
            return PollResult(rebuilt=True), None
        return None, stat

    def poll(self) -> PollResult:
        precheck, stat = self._poll_precheck()
        if precheck is not None:
            return precheck
        assert stat is not None

        consumed = self._splitter.consumed
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
        entries: list[IndexEntry] = []
        thread_changed: str | None = None
        for offset, raw in lines:
            message, changed = self._process_line(offset, raw, stats)
            if changed is not None and thread_changed is None:
                thread_changed = changed
            if message is not None:
                messages.append(message)
                entries.append(self._make_entry(message, len(raw)))

        return PollResult(
            messages=messages,
            entries=entries,
            stats=stats,
            thread_changed=thread_changed,
            unsupported_mode=self._unsupported,
        )

    def _ensure_readable(self) -> None:
        if self._unsupported is not None:
            raise SessionReadError("unsupported")
        if self._halted:
            raise SessionReadError("halted")
        if self.path.is_symlink():
            raise SessionReadError("denied")
        if not self.path.exists():
            raise SessionReadError("missing")
        if not self._source_allowed():
            raise SessionReadError("denied")

    def _read_entry(self, handle: BinaryIO, entry: IndexEntry) -> Message:
        handle.seek(entry.sequence)
        raw = handle.read(entry.end - entry.sequence)
        try:
            record = json.loads(raw)
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            raise SessionIndexMismatch() from exc
        if not isinstance(record, dict):
            raise SessionIndexMismatch()
        message = self._extract_record(record, entry.sequence)
        if (
            message is None
            or message.message_id != entry.message_id
            or message.revision != entry.revision
        ):
            raise SessionIndexMismatch()
        return message

    def read_messages(self, entries: list[IndexEntry]) -> list[Message]:
        if not entries:
            return []
        self._ensure_readable()

        messages: list[Message] = []
        try:
            with self.path.open("rb") as handle:
                for entry in entries:
                    messages.append(self._read_entry(handle, entry))
        except OSError as exc:
            raise SessionReadError("read") from exc
        return messages

    @property
    def unsupported_mode(self) -> str | None:
        return self._unsupported

    def _extract_record(self, record: dict[str, Any], offset: int) -> Message | None:
        if self._unsupported is not None or self._halted:
            return None
        if self.provider is AgentProvider.CODEX and record.get("type") == "session_meta":
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

        if self.provider is AgentProvider.CLAUDE:
            return self._process_claude_line(record, offset, stats)
        if record.get("type") == "session_meta":
            return self._process_session_meta(record, stats)
        return self._process_codex_line(record, offset, stats)

    def _process_claude_line(
        self, record: dict[str, Any], offset: int, stats: ScanStats
    ) -> tuple[Message | None, str | None]:
        stats.events += 1
        if self._halted:
            stats.ignored += 1
            return None, None
        result = self._extractor.extract(record, offset)
        if result.reason == "thread_mismatch":
            self._halted = True
            session_id = record.get("sessionId")
            return None, session_id if isinstance(session_id, str) else None
        if result.message is None:
            stats.ignored += 1
            return None, None
        stats.extracted += 1
        return result.message, None

    def _process_session_meta(
        self, record: dict[str, Any], stats: ScanStats
    ) -> tuple[Message | None, str | None]:
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

    def _process_codex_line(
        self, record: dict[str, Any], offset: int, stats: ScanStats
    ) -> tuple[Message | None, str | None]:
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
