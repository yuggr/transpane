package protection

import (
	"strings"
	"testing"
)

const sample = "## Results\n\nI updated `store.py` and ran the targeted tests.\n\n- All tests passed.\n- Review `tests/test_store.py` before merging.\n\n```bash\nuv run pytest tests/test_store.py\n```\n"

func TestSampleStructureAndProtection(t *testing.T) {
	doc := ProtectDocument(sample)
	units := doc.TranslatableUnits()
	if len(units) != 4 {
		t.Fatalf("units %d: %+v", len(units), units)
	}
	if units[0].Prefix != "## " || units[0].SourceText != "Results" {
		t.Fatalf("heading %+v", units[0])
	}
	if units[1].Segments[0].Text != "I updated @@CJ0@@ and ran the targeted tests." || len(units[1].Markers) != 1 || units[1].Markers[0] != "`store.py`" {
		t.Fatalf("paragraph %+v", units[1])
	}
	if units[2].Prefix != "- " || units[2].SourceText != "All tests passed." {
		t.Fatalf("bullet %+v", units[2])
	}
	for _, unit := range doc.Units {
		if unit.LineIndex == 7 || unit.LineIndex == 8 || unit.LineIndex == 9 {
			t.Fatalf("fence line became unit: %+v", unit)
		}
	}
}

func TestRenderExact(t *testing.T) {
	doc := ProtectDocument(sample)
	translations := map[int]string{}
	for _, unit := range doc.TranslatableUnits() {
		translations[unit.ID] = "訳 " + unit.Segments[0].Text
	}
	rendered := doc.Render(translations)
	for _, want := range []string{"## 訳 Results", "訳 I updated `store.py` and ran the targeted tests.", "- 訳 Review `tests/test_store.py` before merging.", "```bash\nuv run pytest tests/test_store.py\n```"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q in %q", want, rendered)
		}
	}
}

func TestRenderInvalidKeepsOriginal(t *testing.T) {
	source := "Please read `a.py` now.\n"
	doc := ProtectDocument(source)
	unit := doc.Units[0]
	if got := doc.Render(map[int]string{unit.ID: "壊れた @@CJ1@@"}); got != source {
		t.Fatalf("%q", got)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		text    string
		markers []string
		want    bool
	}{
		{"訳 @@CJ0@@ です", []string{"@@CJ0@@"}, true},
		{"訳 です", []string{"@@CJ0@@"}, false},
		{"@@CJ0@@ @@CJ0@@", []string{"@@CJ0@@"}, false},
		{"@@CJ1@@ @@CJ0@@", []string{"@@CJ0@@", "@@CJ1@@"}, false},
		{"@@CJ9@@", []string{"@@CJ0@@"}, false},
		{"", nil, false},
		{"line\nbreak", nil, false},
		{"\x07bell", nil, false},
		{"- injected list", nil, false},
		{"```code", nil, false},
	}
	for _, item := range cases {
		if got := ValidateSegmentTranslation(item.text, item.markers); got != item.want {
			t.Fatalf("%q: got %v", item.text, got)
		}
	}
}

func TestRestoreMarkers(t *testing.T) {
	values := []string{"`a.py`", "**b**"}
	if got, ok := RestoreMarkers("@@CJ0@@ then @@CJ1@@", values); !ok || got != "`a.py` then **b**" {
		t.Fatalf("%q %v", got, ok)
	}
	for _, text := range []string{"@@CJ1@@ then @@CJ0@@", "@@CJ0@@ and @@CJ0@@", "@@CJ0@@ and @@CJ9@@"} {
		if _, ok := RestoreMarkers(text, values); ok {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestSkipReasons(t *testing.T) {
	doc := ProtectDocument("これは日本語です\n\n12345 !!!\n\n保存処理を調べて and English\n")
	for _, unit := range doc.Units {
		if unit.SkipReason == "" {
			t.Fatalf("not skipped: %+v", unit)
		}
	}
	secret := ProtectDocument("Use this token: sk-abcdefghijklmnopqrstuvwx and keep it secret.\n")
	if len(secret.Units) != 1 || secret.Units[0].SkipReason != "secret" || len(secret.TranslatableUnits()) != 0 {
		t.Fatalf("%+v", secret.Units)
	}
}

func TestProtectedSpans(t *testing.T) {
	text := "See https://example.com/a and [the docs](https://example.com/b), edit src/transpane/main_file.py, run --verbose with SOME_TOKEN_VAR, then call do_work() and check v1.2.3.\n"
	doc := ProtectDocument(text)
	unit := doc.Units[0]
	if unit.SkipReason != "" {
		t.Fatalf("skipped: %+v", unit)
	}
	protected := unit.Segments[0].Text
	for _, value := range []string{"https://example.com/a", "[the docs](https://example.com/b)", "src/transpane/main_file.py", "--verbose", "SOME_TOKEN_VAR", "do_work()", "v1.2.3"} {
		if strings.Contains(protected, value) {
			t.Fatalf("protected text still contains %q: %q", value, protected)
		}
	}
	if !strings.Contains(protected, "@@CJ0@@") || len(unit.Markers) != 7 {
		t.Fatalf("markers %d %+v", len(unit.Markers), unit)
	}
}

func TestAmbiguousBlocksStayOriginal(t *testing.T) {
	source := "A wrapped paragraph\nthat spans two lines.\n\n- first item\n  continued item\n\n- parent\n  - nested child\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n![image](https://example.com/a.png)\n\n    indented code\n"
	doc := ProtectDocument(source)
	if len(doc.TranslatableUnits()) != 0 {
		t.Fatalf("units: %+v", doc.TranslatableUnits())
	}
	if got := doc.Render(nil); got != source {
		t.Fatalf("render changed source")
	}
}

func TestPrefixesAndClosingMarkers(t *testing.T) {
	source := "### Title ###\n\n> Quoted sentence.\n\n1. Ordered item\n"
	doc := ProtectDocument(source)
	if len(doc.Units) != 3 || doc.Units[0].Prefix != "### " || doc.Units[1].Prefix != "> " || doc.Units[2].Prefix != "1. " {
		t.Fatalf("%+v", doc.Units)
	}
	if !strings.HasPrefix(doc.Units[0].Suffix, " ###") {
		t.Fatalf("suffix %q", doc.Units[0].Suffix)
	}
	rendered := doc.Render(map[int]string{0: "訳", 1: "訳", 2: "訳"})
	if !strings.HasPrefix(rendered, "### 訳 ###") || !strings.Contains(rendered, "> 訳") || !strings.Contains(rendered, "1. 訳") {
		t.Fatalf("%q", rendered)
	}
}

func TestSplitLongText(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("Alpha beta gamma delta. ", 200))
	pieces, ok := SplitProtected(text, 1000)
	if !ok {
		t.Fatal("split failed")
	}
	var rebuilt strings.Builder
	for _, piece := range pieces {
		if len([]rune(piece[0])) > 1000 {
			t.Fatalf("piece too long: %d", len([]rune(piece[0])))
		}
		rebuilt.WriteString(piece[0])
		rebuilt.WriteString(piece[1])
	}
	if rebuilt.String() != text {
		t.Fatal("separators lost")
	}
}

func TestSplitDoesNotCutMarkers(t *testing.T) {
	marker := "@@CJ0@@"
	text := strings.Repeat("word ", 100) + marker + strings.Repeat(" tail", 100)
	pieces, ok := SplitProtected(text, 500)
	if !ok {
		t.Fatal("split failed")
	}
	count := 0
	for _, piece := range pieces {
		if strings.Contains(piece[0], marker) {
			if strings.Count(piece[0], marker) != 1 {
				t.Fatal("marker cut")
			}
		}
		count += strings.Count(piece[0], marker)
	}
	if count != 1 {
		t.Fatalf("marker count %d", count)
	}
}

func TestConservativeFailures(t *testing.T) {
	doc := ProtectDocument(strings.Repeat("x", 1500) + "\n")
	if doc.Units[0].SkipReason != "long_unsplittable" || !doc.Units[0].Conservative {
		t.Fatalf("%+v", doc.Units[0])
	}
	collision := ProtectDocument("Existing @@CJ0@@ marker in text.\n")
	if collision.Units[0].SkipReason != "marker_collision" || !collision.Units[0].Conservative {
		t.Fatalf("%+v", collision.Units[0])
	}
}

func TestImagesProtected(t *testing.T) {
	doc := ProtectDocument("Look at ![chart](https://example.com/c.png) for details.\n")
	unit := doc.Units[0]
	if unit.Segments[0].Text != "Look at @@CJ0@@ for details." || unit.Markers[0] != "![chart](https://example.com/c.png)" {
		t.Fatalf("%+v", unit)
	}
}

func TestEmptyDocument(t *testing.T) {
	doc := ProtectDocument("")
	if len(doc.Units) != 0 || doc.Render(nil) != "" {
		t.Fatal("empty doc")
	}
}

func TestUnicodeAndCRLF(t *testing.T) {
	source := "A café with 🚀 and `x`.\r\n\r\n- 日本語 not translated.\r\n"
	doc := ProtectDocument(source)
	if doc.Render(nil) != source {
		t.Fatal("source changed")
	}
	for _, unit := range doc.Units {
		if strings.Contains(unit.SourceText, "日本語") && unit.SkipReason != "japanese" {
			t.Fatalf("%+v", unit)
		}
	}
}
