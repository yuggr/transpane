package protection

import (
	"strings"
	"unicode/utf8"
)

// SplitProtected splits protected text into chunks of at most limit runes,
// never cutting a marker. It returns (segment, trailing separator) pairs, or
// false when no safe boundary exists.
func SplitProtected(text string, limit int) ([][2]string, bool) {
	runes := []rune(text)
	if len(runes) <= limit {
		return [][2]string{{text, ""}}, true
	}
	markerRanges := [][2]int{}
	for _, location := range markerPattern.FindAllStringIndex(text, -1) {
		start := utf8.RuneCountInString(text[:location[0]])
		end := start + utf8.RuneCountInString(text[location[0]:location[1]])
		markerRanges = append(markerRanges, [2]int{start, end})
	}
	insideMarker := func(index int) bool {
		for _, item := range markerRanges {
			if item[0] < index && index < item[1] {
				return true
			}
		}
		return false
	}
	findCut := func(start, windowEnd int) int {
		for index := windowEnd; index > start; index-- {
			if insideMarker(index) {
				continue
			}
			if (runes[index-1] == '.' || runes[index-1] == '!' || runes[index-1] == '?' ||
				runes[index-1] == ';' || runes[index-1] == ':' || runes[index-1] == ',') &&
				index < len(runes) && (runes[index] == ' ' || runes[index] == '\t') {
				return index
			}
		}
		for index := windowEnd; index > start; index-- {
			if insideMarker(index) {
				continue
			}
			if runes[index] == ' ' || runes[index] == '\t' {
				return index
			}
		}
		return -1
	}

	pieces := [][2]string{}
	start := 0
	for len(runes)-start > limit {
		windowEnd := start + limit
		cut := findCut(start, windowEnd)
		if cut <= start {
			return nil, false
		}
		end := cut
		for end < len(runes) && (runes[end] == ' ' || runes[end] == '\t') {
			end++
		}
		pieces = append(pieces, [2]string{string(runes[start:cut]), string(runes[cut:end])})
		start = end
	}
	pieces = append(pieces, [2]string{string(runes[start:]), ""})
	for _, piece := range pieces {
		if piece[0] == "" {
			return nil, false
		}
	}
	return pieces, true
}

// TrimSpace is a small helper for tests and callers that need the same
// Unicode whitespace rule as the Python baseline.
func TrimSpace(value string) string { return strings.TrimSpace(value) }
