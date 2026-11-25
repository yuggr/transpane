"""Claude Code transcript discovery and metadata interpretation."""

from __future__ import annotations

import json
import os
import re
from collections.abc import Iterator
from pathlib import Path
from typing import BinaryIO

from .models import AgentProvider, SessionInfo

META_READ_LIMIT = 1024 * 1024
CLAUDE_HISTORY_MODE = "claude-jsonl"
_UUID_RE = re.compile(
    r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"
)


def _is_contained(path: Path, root_real: Path) -> bool:
    """Return whether ``path`` resolves inside ``root_real``."""
    try:
        path.resolve().relative_to(root_real)
    except (OSError, ValueError):
        return False
    return True


def _is_transcript_child(path: Path, root_real: Path) -> bool:
    """Return whether ``path`` is a contained regular ``<uuid>.jsonl`` file."""
    if path.suffix != ".jsonl" or not _UUID_RE.match(path.stem):
        return False
    if path.is_symlink() or not path.is_file():
        return False
    return _is_contained(path, root_real)


def _iter_project_transcripts(project: Path, root_real: Path) -> Iterator[Path]:
    """Yield eligible ``<uuid>.jsonl`` children of one validated project directory."""
    try:
        children = sorted(project.iterdir())
    except OSError:
        return
    for path in children:
        if _is_transcript_child(path, root_real):
            yield path


def _iter_projects(projects_root: Path, root_real: Path) -> Iterator[Path]:
    """Yield sorted direct project directories contained in the resolved root."""
    try:
        projects = sorted(projects_root.iterdir())
    except OSError:
        return
    for project in projects:
        if project.is_symlink() or not project.is_dir():
            continue
        if _is_contained(project, root_real):
            yield project


def iter_claude_files(projects_root: Path) -> Iterator[Path]:
    """Yield only direct ``<project>/<uuid>.jsonl`` regular files."""
    if not projects_root.is_dir():
        return
    try:
        root_real = projects_root.resolve()
    except OSError:
        return
    for project in _iter_projects(projects_root, root_real):
        yield from _iter_project_transcripts(project, root_real)


def _read_head_chunk(handle: BinaryIO, limit: int, consumed: int) -> tuple[bytes | None, int, int]:
    """Read one bounded chunk; ``None`` bytes means EOF."""
    chunk_size = min(256 * 1024, limit - consumed)
    chunk = handle.readline(chunk_size)
    if not chunk:
        return None, consumed, chunk_size
    return chunk, consumed + len(chunk), chunk_size


def _read_head_line(handle: BinaryIO, limit: int, consumed: int) -> tuple[bytes | None, int]:
    """Read one complete line within the byte budget; a ``None`` line means stop."""
    buffer = b""
    while consumed < limit:
        chunk, consumed, chunk_size = _read_head_chunk(handle, limit, consumed)
        if chunk is None:
            return None, consumed
        buffer += chunk
        if buffer.endswith(b"\n"):
            return buffer, consumed
        if len(chunk) < chunk_size:
            return None, consumed
    return None, consumed


def _iter_complete_lines(handle: BinaryIO, limit: int) -> Iterator[bytes]:
    """Yield complete lines from ``handle`` while staying within ``limit`` bytes."""
    consumed = 0
    while consumed < limit:
        line, consumed = _read_head_line(handle, limit, consumed)
        if line is None:
            return
        yield line


def _iter_head_lines(path: Path, limit: int) -> Iterator[bytes]:
    """Yield complete lines from the head of a file, bounded by ``limit`` bytes."""
    try:
        with path.open("rb") as handle:
            yield from _iter_complete_lines(handle, limit)
    except OSError:
        return


def _is_eligible_record(record: dict, stem: str) -> bool:
    """Return whether ``record`` carries the confirmed session metadata."""
    if record.get("type") not in ("user", "assistant"):
        return False
    if record.get("sessionId") != stem:
        return False
    sidechain = record.get("isSidechain")
    if isinstance(sidechain, bool) and sidechain:
        return False
    return "isSidechain" not in record or isinstance(sidechain, bool)


def _meta_values(record: dict) -> tuple[str | None, str | None]:
    """Normalize the ``cwd`` and ``version`` fields of an eligible record."""
    cwd = record.get("cwd")
    cli_version = record.get("version")
    return (
        cwd if isinstance(cwd, str) and os.path.isabs(cwd) else None,
        cli_version if isinstance(cli_version, str) else None,
    )


def read_claude_meta(path: Path) -> tuple[str | None, str | None] | None:
    """Confirm the session ID from the first eligible record and read its metadata."""
    for line in _iter_head_lines(path, META_READ_LIMIT):
        try:
            record = json.loads(line)
        except (json.JSONDecodeError, UnicodeDecodeError):
            continue
        if not isinstance(record, dict):
            continue
        if not _is_eligible_record(record, path.stem):
            continue
        return _meta_values(record)
    return None


def claude_transcript_counts(projects_root: Path) -> tuple[int, int]:
    """Return ``(confirmed, unconfirmed)`` direct transcript counts."""
    confirmed = 0
    unconfirmed = 0
    for path in iter_claude_files(projects_root):
        if read_claude_meta(path) is None:
            unconfirmed += 1
        else:
            confirmed += 1
    return confirmed, unconfirmed


def discover_claude_sessions(projects_root: Path) -> list[SessionInfo]:
    found: list[SessionInfo] = []
    for path in iter_claude_files(projects_root):
        meta = read_claude_meta(path)
        if meta is None:
            continue
        try:
            updated_at = path.stat().st_mtime
        except OSError:
            continue
        cwd, cli_version = meta
        found.append(
            SessionInfo(
                thread_id=path.stem,
                path=str(path),
                cwd=cwd,
                updated_at=updated_at,
                history_mode=CLAUDE_HISTORY_MODE,
                cli_version=cli_version,
                parent_thread_id=None,
                source=None,
                provider=AgentProvider.CLAUDE,
            )
        )
    found.sort(key=lambda item: (-item.updated_at, item.provider.value, item.thread_id, item.path))
    return found
