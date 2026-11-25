"""Markdown structure protection, translation segmentation and restoration.

The document is treated as a list of physical lines.  Only single-line prose
inside top-level paragraphs, simple list items, simple block quotes and ATX
headings is translated.  Everything else (fences, indented code, HTML, tables,
nested lists, multi-line blocks, images) keeps its original bytes.

Protected inline spans are replaced with ASCII markers; a translation is only
accepted when every marker appears exactly once, in the original order.  The
original bytes are kept in the document and are only restored for display.
"""

from __future__ import annotations

import re
from collections.abc import Iterable, Mapping
from dataclasses import dataclass

from markdown_it import MarkdownIt

from .config import MAX_SEGMENT_CHARS

_MARKER_RE = re.compile(r"@@CJ\d+@@")
_MARKER_PREFIX = "@@CJ"

_JAPANESE_RE = re.compile(r"[\u3000-\u303f\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uff66-\uff9f]")
_ASCII_LETTER_RE = re.compile(r"[A-Za-z]")

_SECRET_PATTERNS = (
    re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----"),
    re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{10,}"),
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{20,}\b"),
    re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{10,}\b"),
    re.compile(r"\bsk-[A-Za-z0-9_-]{16,}\b"),
    re.compile(r"https?://[^\s/@:]+:[^\s/@]+@"),
)

_INLINE_PATTERNS = (
    re.compile(r"(?P<fence>`+)(?P<body>.+?)(?P=fence)"),
    re.compile(r"!?\[[^\]\n]*\]\([^)\n]*\)"),
    re.compile(r"<https?://[^>\s]+>"),
    re.compile(r"</?[A-Za-z][^>\n]*>"),
    re.compile(r"\$\$?[^$\n]+\$\$?"),
    re.compile(r"https?://[^\s<>()\[\]]+"),
    re.compile(r"(?<![\w.])\.{0,2}/[\w./%~+-]+"),
    re.compile(r"\b[\w.-]+(?:/[\w.-]+)+\b"),
    re.compile(
        r"\b[\w-]+\.(?:py|pyi|ts|tsx|js|jsx|mjs|cjs|json|jsonl|toml|yaml|yml|md|txt|"
        r"rs|go|java|kt|c|h|cc|cpp|hpp|sh|bash|zsh|fish|sql|html|css|scss|ini|cfg|"
        r"conf|lock|xml|gradle|mod|sum|env)\b"
    ),
    re.compile(r"\b[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\(\)"),
    re.compile(r"\b[A-Za-z]\w*_[A-Za-z0-9_]+\b"),
    re.compile(r"\b[a-z]+[A-Z][A-Za-z0-9]*\b"),
    re.compile(r"\b[a-z_]\w*(?:\.[a-z_]\w*)+\b"),
    re.compile(r"\b[A-Z][A-Z0-9_]{2,}\b"),
    re.compile(r"(?<![\w-])--?[A-Za-z][\w-]*"),
    re.compile(r"(?<![\w.])v?\d+\.\d+(?:\.\d+)?\b"),
)

_ADDED_STRUCTURE_RE = re.compile(r"^(?:#{1,6}\s|[-*+]\s|\d{1,9}[.)]\s|```|~~~|>|\|)")


@dataclass(frozen=True, slots=True)
class Segment:
    text: str
    markers: tuple[str, ...]
    trailing: str


@dataclass(frozen=True, slots=True)
class Unit:
    unit_id: int
    line_index: int
    prefix: str
    suffix: str
    source_text: str
    segments: tuple[Segment, ...]
    markers: tuple[str, ...]
    skip_reason: str | None = None
    conservative: bool = False


@dataclass
class ProtectedDoc:
    source: str
    lines: list[str]
    units: list[Unit]

    @property
    def translatable_units(self) -> list[Unit]:
        return [unit for unit in self.units if unit.skip_reason is None]

    @property
    def has_conservative_exclusion(self) -> bool:
        return any(unit.conservative for unit in self.units)

    def render(self, translations: Mapping[int, str | None]) -> str:
        lines = list(self.lines)
        for unit in self.units:
            if unit.unit_id not in translations:
                continue
            translated = translations[unit.unit_id]
            if translated is None:
                continue
            restored = restore_markers(translated, unit.markers)
            if restored is None:
                continue
            lines[unit.line_index] = unit.prefix + restored + unit.suffix
        return "\n".join(lines)


def marker_for(index: int) -> str:
    return f"{_MARKER_PREFIX}{index}@@"


def _new_markdown() -> MarkdownIt:
    parser = MarkdownIt("commonmark", {"html": False})
    parser.enable("table")
    return parser


_MARKDOWN = _new_markdown()


def _allowed_lines(source: str) -> dict[int, str]:
    """Map line index to a container kind for blocks we are willing to translate."""
    try:
        tokens = _MARKDOWN.parse(source)
    except Exception:  # defensive: malformed input keeps everything original
        return {}

    stack: list[str] = []
    allowed: dict[int, str] = {}
    for token in tokens:
        if token.nesting == 1:
            container_depth = sum(
                1 for name in stack if name in ("list_item_open", "blockquote_open")
            )
            block_map = token.map
            if token.type == "heading_open":
                if (
                    block_map is not None
                    and block_map[1] - block_map[0] == 1
                    and container_depth == 0
                    and isinstance(token.markup, str)
                    and token.markup.startswith("#")
                ):
                    allowed[block_map[0]] = "heading"
            elif (
                token.type == "paragraph_open"
                and block_map is not None
                and block_map[1] - block_map[0] == 1
                and container_depth <= 1
            ):
                if "list_item_open" in stack:
                    allowed[block_map[0]] = "list_item"
                elif "blockquote_open" in stack:
                    allowed[block_map[0]] = "blockquote"
                else:
                    allowed[block_map[0]] = "paragraph"
            stack.append(token.type)
        elif token.nesting == -1 and stack:
            stack.pop()

    list_items = [
        (token.map[0], token.map[1])
        for token in tokens
        if token.type == "list_item_open" and token.map is not None
    ]
    complex_ranges: list[tuple[int, int]] = []
    for index, (start, end) in enumerate(list_items):
        for other_index, (other_start, other_end) in enumerate(list_items):
            if index == other_index:
                continue
            contains_other = start <= other_start and other_end <= end
            if contains_other and (start, end) != (other_start, other_end):
                complex_ranges.append((start, end))
                break
    for start, end in complex_ranges:
        for line_index in range(start, end):
            allowed.pop(line_index, None)

    return allowed


_HEADING_PREFIX_RE = re.compile(r"^( {0,3}#{1,6}[ \t]+)")
_LIST_PREFIX_RE = re.compile(r"^([ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+(?:\[[ xX]\][ \t]+)?)")
_BLOCKQUOTE_PREFIX_RE = re.compile(r"^([ \t]*>[ \t]?)")
_PARAGRAPH_PREFIX_RE = re.compile(r"^([ \t]*)")
_HEADING_CLOSE_RE = re.compile(r"([ \t]+#+[ \t]*)$")


def _split_line(line: str, kind: str) -> tuple[str, str, str] | None:
    if kind == "heading":
        match = _HEADING_PREFIX_RE.match(line)
        if match is None:
            return None
        prefix = match.group(1)
        rest = line[match.end() :]
        close = _HEADING_CLOSE_RE.search(rest)
        closing = ""
        if close is not None:
            closing = close.group(1)
            rest = rest[: close.start()]
        core = rest.rstrip()
        return prefix, core, rest[len(core) :] + closing

    if kind == "list_item":
        pattern = _LIST_PREFIX_RE
    elif kind == "blockquote":
        pattern = _BLOCKQUOTE_PREFIX_RE
    else:
        pattern = _PARAGRAPH_PREFIX_RE

    match = pattern.match(line)
    if match is None:
        return None
    prefix = match.group(1)
    rest = line[match.end() :]
    core = rest.rstrip()
    return prefix, core, rest[len(core) :]


def _collect_spans(text: str) -> list[tuple[int, int]]:
    spans: list[tuple[int, int]] = []
    for pattern in _INLINE_PATTERNS:
        for match in pattern.finditer(text):
            if match.end() > match.start():
                spans.append((match.start(), match.end()))
    spans.sort(key=lambda span: (span[0], -(span[1] - span[0])))
    merged: list[tuple[int, int]] = []
    last_end = -1
    for start, end in spans:
        if start >= last_end:
            merged.append((start, end))
            last_end = end
    return merged


def _apply_markers(text: str, spans: Iterable[tuple[int, int]]) -> tuple[str, list[str]]:
    chunks: list[str] = []
    values: list[str] = []
    last = 0
    for index, (start, end) in enumerate(spans):
        chunks.append(text[last:start])
        chunks.append(marker_for(index))
        values.append(text[start:end])
        last = end
    chunks.append(text[last:])
    return "".join(chunks), values


def _classify_skip(core: str) -> str | None:
    if any(pattern.search(core) for pattern in _SECRET_PATTERNS):
        return "secret"
    if _JAPANESE_RE.search(core):
        return "japanese"
    if not _ASCII_LETTER_RE.search(core):
        return "no_letters"
    stripped = core.strip()
    if stripped.startswith("$ "):
        return "command"
    if stripped.startswith("|"):
        return "structured"
    if stripped.startswith(("{", "[")) and stripped.endswith(("}", "]")):
        return "structured"
    if stripped.startswith(("```", "~~~")):
        return "code"
    return None


def _inside_marker(text: str, index: int, spans: list[tuple[int, int]]) -> bool:
    return any(start < index < end for start, end in spans)


def split_protected(text: str, limit: int = MAX_SEGMENT_CHARS) -> list[tuple[str, str]] | None:
    """Split protected text into <= limit chunks without cutting markers.

    Returns (segment, trailing_separator) pairs, or None when no safe boundary
    exists.  Separators are returned so the original spacing can be restored.
    """
    if len(text) <= limit:
        return [(text, "")]
    marker_spans = [(match.start(), match.end()) for match in _MARKER_RE.finditer(text)]

    pieces: list[tuple[str, str]] = []
    start = 0
    while len(text) - start > limit:
        window_end = start + limit
        cut = None
        for index in range(window_end, start, -1):
            if _inside_marker(text, index, marker_spans):
                continue
            if text[index - 1] in ".!?;:," and index < len(text) and text[index] in " \t":
                cut = index
                break
        if cut is None:
            for index in range(window_end, start, -1):
                if _inside_marker(text, index, marker_spans):
                    continue
                if text[index] in " \t":
                    cut = index
                    break
        if cut is None or cut <= start:
            return None

        segment = text[start:cut]
        end = cut
        while end < len(text) and text[end] in " \t":
            end += 1
        pieces.append((segment, text[cut:end]))
        start = end

    pieces.append((text[start:], ""))
    if any(not piece for piece, _ in pieces):
        return None
    return pieces


def protect_document(source: str) -> ProtectedDoc:
    lines = source.split("\n")
    allowed = _allowed_lines(source)
    units: list[Unit] = []

    for line_index in sorted(allowed):
        if 0 <= line_index < len(lines):
            split = _split_line(lines[line_index], allowed[line_index])
        else:
            split = None
        if split is None:
            continue
        prefix, core, suffix = split
        if not core.strip():
            continue
        unit_id = len(units)
        skip_reason = _classify_skip(core)
        conservative = False
        segments: tuple[Segment, ...] = ()
        markers: tuple[str, ...] = ()

        if skip_reason is None:
            protected, values = _protect_inline(core)
            if protected is None:
                skip_reason, conservative = "marker_collision", True
            elif not _ASCII_LETTER_RE.search(_MARKER_RE.sub("", protected)):
                skip_reason = "inline_only"
            else:
                pieces = split_protected(protected)
                if pieces is None:
                    skip_reason, conservative = "long_unsplittable", True
                else:
                    segments = tuple(
                        Segment(
                            text=piece,
                            markers=tuple(_MARKER_RE.findall(piece)),
                            trailing=trailing,
                        )
                        for piece, trailing in pieces
                    )
                    markers = tuple(values)

        units.append(
            Unit(
                unit_id=unit_id,
                line_index=line_index,
                prefix=prefix,
                suffix=suffix,
                source_text=core,
                segments=segments,
                markers=markers,
                skip_reason=skip_reason,
                conservative=conservative,
            )
        )

    return ProtectedDoc(source=source, lines=lines, units=units)


def _protect_inline(text: str) -> tuple[str | None, list[str]]:
    if _MARKER_PREFIX in text:
        return None, []
    spans = _collect_spans(text)
    protected, values = _apply_markers(text, spans)
    return protected, values


def validate_segment_translation(text: str, markers: tuple[str, ...]) -> bool:
    """Reject empty, multiline, control-bearing or marker-mismatched output."""
    if not isinstance(text, str):
        return False
    if not text.strip():
        return False
    if "\n" in text or "\r" in text:
        return False
    if any(ord(char) < 32 for char in text):
        return False
    found = _MARKER_RE.findall(text)
    if tuple(found) != tuple(markers):
        return False
    return not _ADDED_STRUCTURE_RE.match(text.lstrip())


def restore_markers(text: str, markers: tuple[str, ...]) -> str | None:
    """Restore protected values, verifying one ordered occurrence per marker."""
    if not isinstance(text, str):
        return None
    positions: list[int] = []
    for index in range(len(markers)):
        marker = marker_for(index)
        if text.count(marker) != 1:
            return None
        positions.append(text.index(marker))
    if positions != sorted(positions):
        return None
    expected = {marker_for(index) for index in range(len(markers))}
    if any(found not in expected for found in _MARKER_RE.findall(text)):
        return None

    restored = text
    for index, value in enumerate(markers):
        restored = restored.replace(marker_for(index), value)
    if _MARKER_RE.search(restored):
        return None
    return restored
