package protection

import (
	"strings"
	"unicode"
)

// ValidateSegmentTranslation rejects empty, multiline, control-bearing,
// marker-mismatched or structure-adding output.
func ValidateSegmentTranslation(text string, markers []string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if strings.ContainsAny(text, "\r\n") {
		return false
	}
	for _, r := range text {
		if r < 32 {
			return false
		}
	}
	found := markerPattern.FindAllString(text, -1)
	if len(found) != len(markers) {
		return false
	}
	for index := range markers {
		if found[index] != markers[index] {
			return false
		}
	}
	return !addedStructure.MatchString(strings.TrimLeftFunc(text, unicode.IsSpace))
}

// RestoreMarkers replaces every marker exactly once, in original order.
func RestoreMarkers(text string, markers []string) (string, bool) {
	positions := make([]int, 0, len(markers))
	for index := range markers {
		marker := MarkerFor(index)
		if strings.Count(text, marker) != 1 {
			return "", false
		}
		positions = append(positions, strings.Index(text, marker))
	}
	for index := 1; index < len(positions); index++ {
		if positions[index] <= positions[index-1] {
			return "", false
		}
	}
	expected := map[string]bool{}
	for index := range markers {
		expected[MarkerFor(index)] = true
	}
	for _, found := range markerPattern.FindAllString(text, -1) {
		if !expected[found] {
			return "", false
		}
	}
	restored := text
	for index, value := range markers {
		restored = strings.Replace(restored, MarkerFor(index), value, 1)
	}
	if markerPattern.MatchString(restored) {
		return "", false
	}
	return restored, true
}
