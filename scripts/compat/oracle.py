"""Development-only Python oracle for synthetic Transpane fixtures.

Run from the repository root with:
    uv run python scripts/compat/oracle.py > testdata/compat/golden.json
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

from transpane.cache import cache_key
from transpane.markdown import protect_document
from transpane.models import AgentProvider
from transpane.session import SessionReader

ROOT = Path(__file__).resolve().parents[2]
CLAUDE_SESSION = "11111111-1111-4111-8111-111111111111"


def message_json(message):
    return {
        "id": message.message_id,
        "revision": message.revision,
        "kind": message.kind.value,
        "offset": message.sequence,
        "text": message.original,
        "timestamp": message.timestamp,
    }


def scan(path: Path, thread: str, provider):
    reader = SessionReader(path, thread, provider=provider, allowed_root=path.parent)
    result = reader.scan_all()
    messages = reader.read_messages(result.entries)
    return {
        "history_mode": result.history_mode,
        "unsupported": result.unsupported_mode,
        "thread_changed": result.thread_changed,
        "stats": {
            "records": result.stats.records,
            "events": result.stats.events,
            "extracted": result.stats.extracted,
            "ignored": result.stats.ignored,
            "invalid_lines": result.stats.invalid_lines,
            "oversized_lines": result.stats.oversized_lines,
        },
        "entries": [
            {
                "id": entry.message_id,
                "revision": entry.revision,
                "offset": entry.sequence,
                "end": entry.end,
                "kind": entry.kind.value,
            }
            for entry in result.entries
        ],
        "messages": [message_json(message) for message in messages],
    }


def protection():
    source = (ROOT / "testdata/compat/markdown.md").read_text(encoding="utf-8")
    doc = protect_document(source)
    return {
        "units": [
            {
                "id": unit.unit_id,
                "line": unit.line_index,
                "prefix": unit.prefix,
                "suffix": unit.suffix,
                "source": unit.source_text,
                "markers": list(unit.markers),
                "skip": unit.skip_reason,
                "conservative": unit.conservative,
                "segments": [
                    {
                        "text": segment.text,
                        "markers": list(segment.markers),
                        "trailing": segment.trailing,
                    }
                    for segment in unit.segments
                ],
            }
            for unit in doc.units
        ],
        "rendered": doc.render({}),
    }


def main() -> int:
    codex = scan(
        ROOT / "testdata/compat/codex_paginated.jsonl", "compat-codex", AgentProvider.CODEX
    )
    claude = scan(
        ROOT / "testdata/compat/11111111-1111-4111-8111-111111111111.jsonl",
        CLAUDE_SESSION,
        AgentProvider.CLAUDE,
    )
    payload = {
        "codex": codex,
        "claude": claude,
        "protection": protection(),
        "cache_keys": {
            "hello": cache_key(
                "http://127.0.0.1:1234/v1", "plamo-3-translate-2b", "polite", "hello"
            ),
            "japanese": cache_key(
                "http://127.0.0.1:1234/v1", "plamo-3-translate-2b", "polite", "保存 @@CJ0@@"
            ),
        },
    }
    json.dump(payload, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
