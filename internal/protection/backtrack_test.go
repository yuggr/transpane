package protection

import (
	"strings"
	"testing"
)

func TestSecretBoundaryBacktracksTrailingHyphen(t *testing.T) {
	lines := []string{
		"Use sk-abcdefghijklmnopqrstuvwx- now.",
		"Use sk-abcdefghijklmnopqrstuvwx--- now.",
		"Token ghp_abcdefghijklmnopqrstuvwx- is here.",
	}
	for _, line := range lines {
		doc := ProtectDocument(line + "\n")
		if len(doc.Units) != 1 || doc.Units[0].SkipReason != "secret" {
			t.Fatalf("%q not detected: %+v", line, doc.Units)
		}
	}
}

func TestInlineBoundaryBacktracksTrailingHyphen(t *testing.T) {
	doc := ProtectDocument("Edit src/foo- before merging.\n")
	unit := doc.Units[0]
	if len(unit.Markers) != 1 || unit.Markers[0] != "src/foo" {
		t.Fatalf("markers %+v", unit.Markers)
	}
	if strings.Contains(unit.Segments[0].Text, "foo") {
		t.Fatalf("segment %q", unit.Segments[0].Text)
	}
}

func TestInlineAlternationUsesLongestBoundaryMatch(t *testing.T) {
	doc := ProtectDocument("Open data.pyi now.\n")
	unit := doc.Units[0]
	if len(unit.Markers) != 1 || unit.Markers[0] != "data.pyi" {
		t.Fatalf("markers %+v", unit.Markers)
	}
}

func TestUnicodeBearerUsesNonOverlappingToken(t *testing.T) {
	// This token is not an AKIA/GitHub/Slack/sk- key; only Bearer can match.
	line := "Use Bearer\u00a0ZYXWVUTSRQPONMLKJIHGFEDCBA now.\n"
	doc := ProtectDocument(line)
	if len(doc.Units) != 1 || doc.Units[0].SkipReason != "secret" {
		t.Fatalf("%+v", doc.Units)
	}
}

func TestDotComponentsInPathAreProtected(t *testing.T) {
	cases := map[string]string{
		"Edit src/.github/workflows/check.yml now.\n": "src/.github/workflows/check.yml",
		"Open .github/workflows/check.yml now.\n":     "github/workflows/check.yml",
		"Edit ./src/.config/app.yaml now.\n":          "./src/.config/app.yaml",
	}
	for source, marker := range cases {
		doc := ProtectDocument(source)
		unit := doc.Units[0]
		if len(unit.Markers) != 1 || unit.Markers[0] != marker {
			t.Fatalf("%q: markers %+v want %q", source, unit.Markers, marker)
		}
	}
}
