package protection

import (
	"strings"
	"testing"
)

func TestUnicodeWhitespaceBearerIsSecret(t *testing.T) {
	spaces := []string{"\u00a0", "\u1680", "\u2000", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000", "\u0085", "\t", "\v"}
	for _, space := range spaces {
		line := "Use token Bearer" + space + "sk-abcdefghijklmnopqrstuvwx and keep it secret.\n"
		doc := ProtectDocument(line)
		if len(doc.Units) != 1 || doc.Units[0].SkipReason != "secret" {
			t.Fatalf("U+%04X not detected as secret: %+v", []rune(space)[0], doc.Units)
		}
	}
}

func TestUnicodeWordBoundaryProtection(t *testing.T) {
	cases := map[string]string{
		"Edit src/é/foo.py before merging.\n": "src/é/foo.py",
		"Call café_work() before shipping.\n": "café_work()",
		"Open résumé.json now.\n":             "résumé.json",
	}
	for source, marker := range cases {
		doc := ProtectDocument(source)
		if len(doc.Units) != 1 || doc.Units[0].SkipReason != "" {
			t.Fatalf("%q: %+v", source, doc.Units)
		}
		unit := doc.Units[0]
		if len(unit.Markers) != 1 || unit.Markers[0] != marker {
			t.Fatalf("%q: markers %+v want %q", source, unit.Markers, marker)
		}
		if strings.Contains(unit.Segments[0].Text, marker) {
			t.Fatalf("%q: protected value still in segment %q", source, unit.Segments[0].Text)
		}
	}
}

func TestUnicodeBoundaryDoesNotDetectWherePythonDoesNot(t *testing.T) {
	// Python's \b sees é as a word character before AKIA, so this is not a
	// secret line in the baseline. Matching that avoids over-blocking.
	doc := ProtectDocument("éAKIAIOSFODNN7EXAMPLE ok\n")
	if len(doc.Units) != 1 || doc.Units[0].SkipReason == "secret" {
		t.Fatalf("unexpected secret classification: %+v", doc.Units)
	}
}

func TestUnicodeWhitespaceAddedStructureRejected(t *testing.T) {
	for _, text := range []string{"#\u00a0injected", "-\u00a0injected", ">\u3000injected", "1.\u00a0injected"} {
		if ValidateSegmentTranslation(text, nil) {
			t.Fatalf("accepted added structure with Unicode space: %q", text)
		}
	}
}
