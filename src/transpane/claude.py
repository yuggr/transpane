"""Claude Code transcript discovery and metadata interpretation."""

from __future__ import annotations

import json
import os
import re
from collections.abc import Iterator
from pathlib import Path

from .models import AgentProvider, SessionInfo

META_READ_LIMIT = 1024 * 1024
CLAUDE_HISTORY_MODE = "claude-jsonl"
_UUID_RE = re.compile(
    r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"
)


def iter_claude_files(projects_root: Path) -> Iterator[Path]:
    """Yield only direct ``<project>/<uuid>.jsonl`` regular files."""
    if not projects_root.is_dir():
        return
    try:
        root_real = projects_root.resolve()
    except OSError:
        return
    try:
        projects = sorted(projects_root.iterdir())
    except OSError:
        return

    for project in projects:
        if project.is_symlink() or not project.is_dir():
            continue
        try:
            project.resolve().relative_to(root_real)
        except (OSError, ValueError):
            continue
        try:
            children = sorted(project.iterdir())
        except OSError:
            continue
        for path in children:
            if path.suffix != ".jsonl" or not _UUID_RE.match(path.stem):
                continue
            if path.is_symlink() or not path.is_file():
                continue
            try:
                path.resolve().relative_to(root_real)
            except (OSError, ValueError):
                continue
            yield path


def _iter_head_lines(path: Path, limit: int) -> Iterator[bytes]:
    """Yield complete lines from the head of a file, bounded by ``limit`` bytes."""
    consumed = 0
    buffer = b""
    try:
        with path.open("rb") as handle:
            while consumed < limit:
                chunk_size = min(256 * 1024, limit - consumed)
                chunk = handle.readline(chunk_size)
                if not chunk:
                    return
                consumed += len(chunk)
                buffer += chunk
                if buffer.endswith(b"\n"):
                    yield buffer
                    buffer = b""
                    continue
                if len(chunk) < chunk_size:
                    return
    except OSError:
        return


def read_claude_meta(path: Path) -> tuple[str | None, str | None] | None:
    """Confirm the session ID from the first eligible record and read its metadata."""
    for line in _iter_head_lines(path, META_READ_LIMIT):
        try:
            record = json.loads(line)
        except (json.JSONDecodeError, UnicodeDecodeError):
            continue
        if not isinstance(record, dict):
            continue
        if record.get("type") not in ("user", "assistant"):
            continue
        if record.get("sessionId") != path.stem:
            continue
        sidechain = record.get("isSidechain")
        if isinstance(sidechain, bool) and sidechain:
            continue
        if "isSidechain" in record and not isinstance(sidechain, bool):
            continue

        cwd = record.get("cwd")
        cli_version = record.get("version")
        return (
            cwd if isinstance(cwd, str) and os.path.isabs(cwd) else None,
            cli_version if isinstance(cli_version, str) else None,
        )
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
