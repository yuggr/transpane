package tui

import "regexp"

var (
	oscPattern     = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	csiPattern     = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	controlPattern = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]`)
)

// SanitizeForDisplay removes OSC/CSI sequences, a bare ESC and C0/C1 control
// characters. Newlines and tabs are preserved.
func SanitizeForDisplay(text string) string {
	text = oscPattern.ReplaceAllString(text, "")
	text = csiPattern.ReplaceAllString(text, "")
	text = regexp.MustCompile("\x1b").ReplaceAllString(text, "")
	return controlPattern.ReplaceAllString(text, "")
}
