from __future__ import annotations

from transpane.markdown import (
    protect_document,
    restore_markers,
    split_protected,
    validate_segment_translation,
)

SAMPLE = """## Results

I updated `store.py` and ran the targeted tests.

- All tests passed.
- Review `tests/test_store.py` before merging.

```bash
uv run pytest tests/test_store.py
```
"""


def test_sample_structure_and_protection():
    doc = protect_document(SAMPLE)
    assert len(doc.translatable_units) == 4

    heading = doc.units[0]
    assert heading.prefix == "## "
    assert heading.source_text == "Results"

    paragraph = doc.units[1]
    assert paragraph.segments[0].text == "I updated @@CJ0@@ and ran the targeted tests."
    assert paragraph.markers == ("`store.py`",)

    bullet = doc.units[2]
    assert bullet.prefix == "- "
    assert bullet.source_text == "All tests passed."

    # The fence is not a translation unit at all.
    assert all(line_index not in (7, 8, 9) for line_index in [u.line_index for u in doc.units])


def test_restore_is_exact():
    doc = protect_document(SAMPLE)
    translations = {}
    for unit in doc.translatable_units:
        translations[unit.unit_id] = "訳 " + unit.segments[0].text
    rendered = doc.render(translations)
    assert "## 訳 Results" in rendered
    assert "訳 I updated `store.py` and ran the targeted tests." in rendered
    assert "- 訳 Review `tests/test_store.py` before merging." in rendered
    assert "```bash\nuv run pytest tests/test_store.py\n```" in rendered


def test_render_keeps_original_when_translation_is_invalid():
    doc = protect_document("Please read `a.py` now.\n")
    unit = doc.units[0]
    rendered = doc.render({unit.unit_id: "壊れた @@CJ1@@"})
    assert rendered == "Please read `a.py` now.\n"


def test_marker_validation_rejects_loss_duplicate_and_reorder():
    assert validate_segment_translation("訳 @@CJ0@@ です", ("@@CJ0@@",))
    assert not validate_segment_translation("訳 です", ("@@CJ0@@",))
    assert not validate_segment_translation("@@CJ0@@ @@CJ0@@", ("@@CJ0@@",))
    assert not validate_segment_translation("@@CJ1@@ @@CJ0@@", ("@@CJ0@@", "@@CJ1@@"))
    assert not validate_segment_translation("@@CJ9@@", ("@@CJ0@@",))
    assert not validate_segment_translation("", ())
    assert not validate_segment_translation("line\nbreak", ())
    assert not validate_segment_translation("\x07bell", ())
    assert not validate_segment_translation("- injected list", ())
    assert not validate_segment_translation("```code", ())


def test_restore_markers_checks_order():
    values = ("`a.py`", "**b**")
    assert restore_markers("@@CJ0@@ then @@CJ1@@", values) == "`a.py` then **b**"
    assert restore_markers("@@CJ1@@ then @@CJ0@@", values) is None
    assert restore_markers("@@CJ0@@ and @@CJ0@@", values) is None
    assert restore_markers("@@CJ0@@ and @@CJ9@@", values) is None


def test_skip_japanese_and_non_letters():
    doc = protect_document("これは日本語です\n\n12345 !!!\n\n保存処理を調べて and English\n")
    assert all(unit.skip_reason is not None for unit in doc.units)
    reasons = {unit.skip_reason for unit in doc.units}
    assert reasons <= {"japanese", "no_letters"}


def test_skip_secrets():
    doc = protect_document("Use this token: sk-abcdefghijklmnopqrstuvwx and keep it secret.\n")
    assert doc.units[0].skip_reason == "secret"
    assert doc.translatable_units == []


def test_protects_urls_links_paths_options_and_identifiers():
    text = (
        "See https://example.com/a and [the docs](https://example.com/b), "
        "edit src/transpane/main_file.py, run --verbose with SOME_TOKEN_VAR, "
        "then call do_work() and check v1.2.3.\n"
    )
    doc = protect_document(text)
    unit = doc.units[0]
    assert unit.skip_reason is None
    protected = unit.segments[0].text
    for value in (
        "https://example.com/a",
        "[the docs](https://example.com/b)",
        "src/transpane/main_file.py",
        "--verbose",
        "SOME_TOKEN_VAR",
        "do_work()",
        "v1.2.3",
    ):
        assert value not in protected
    assert "@@CJ0@@" in protected
    assert len(unit.markers) == 7


def test_ambiguous_blocks_stay_original():
    source = (
        "A wrapped paragraph\nthat spans two lines.\n\n"
        "- first item\n  continued item\n\n"
        "- parent\n  - nested child\n\n"
        "| a | b |\n| - | - |\n| 1 | 2 |\n\n"
        "![image](https://example.com/a.png)\n\n"
        "    indented code\n"
    )
    doc = protect_document(source)
    assert doc.translatable_units == []
    assert doc.render({}) == source


def test_heading_and_quote_prefixes_are_kept():
    source = "### Title ###\n\n> Quoted sentence.\n\n1. Ordered item\n"
    doc = protect_document(source)
    assert [unit.prefix for unit in doc.units] == ["### ", "> ", "1. "]
    assert doc.units[0].suffix.startswith(" ###")
    rendered = doc.render({unit.unit_id: "訳" for unit in doc.units})
    assert rendered.startswith("### 訳 ###")
    assert "> 訳" in rendered
    assert "1. 訳" in rendered


def test_long_text_is_split_without_losing_separators():
    text = ("Alpha beta gamma delta. " * 200).strip()
    pieces = split_protected(text, limit=1000)
    assert pieces is not None
    assert all(len(piece) <= 1000 for piece, _ in pieces)
    assert "".join(piece + trailing for piece, trailing in pieces) == text


def test_split_does_not_cut_markers():
    marker = "@@CJ0@@"
    text = ("word " * 100) + marker + (" tail" * 100)
    pieces = split_protected(text, limit=500)
    assert pieces is not None
    for piece, _ in pieces:
        if marker in piece:
            assert piece.count(marker) == 1
    assert sum(piece.count(marker) for piece, _ in pieces) == 1


def test_unsplittable_long_text_is_conservative():
    doc = protect_document("x" * 1500 + "\n")
    unit = doc.units[0]
    assert unit.skip_reason == "long_unsplittable"
    assert unit.conservative is True


def test_marker_collision_keeps_original():
    doc = protect_document("Existing @@CJ0@@ marker in text.\n")
    assert doc.units[0].skip_reason == "marker_collision"
    assert doc.units[0].conservative is True


def test_images_are_protected_inline():
    doc = protect_document("Look at ![chart](https://example.com/c.png) for details.\n")
    unit = doc.units[0]
    assert unit.segments[0].text == "Look at @@CJ0@@ for details."
    assert unit.markers == ("![chart](https://example.com/c.png)",)


def test_empty_document_has_no_units():
    doc = protect_document("")
    assert doc.units == []
    assert doc.render({}) == ""
