package protection

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	markerPattern      = regexp.MustCompile(`@@CJ\d+@@`)
	markerPrefix       = "@@CJ"
	asciiLetterPattern = regexp.MustCompile(`[A-Za-z]`)
	japanesePattern    = regexp.MustCompile(`[\x{3000}-\x{303f}\x{3040}-\x{30ff}\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{ff66}-\x{ff9f}]`)
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{10,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`https?://[^\s/@:]+:[^\s/@]+@`),
}

type inlinePattern struct {
	regex   *regexp.Regexp
	context func(rune) bool
}

var inlinePatterns = []inlinePattern{
	{regex: regexp.MustCompile(`!?\[[^\]\n]*\]\([^)\n]*\)`)},
	{regex: regexp.MustCompile(`<https?://[^>\s]+>`)},
	{regex: regexp.MustCompile(`</?[A-Za-z][^>\n]*>`)},
	{regex: regexp.MustCompile(`\$\$?[^$\n]+\$\$?`)},
	{regex: regexp.MustCompile(`https?://[^\s<>()\[\]]+`)},
	{regex: regexp.MustCompile(`\.{0,2}/[\w./%~+\-]+`), context: func(r rune) bool {
		return isWordRune(r) || r == '.'
	}},
	{regex: regexp.MustCompile(`\b[\w.\-]+(?:/[\w.\-]+)+\b`)},
	{regex: regexp.MustCompile(`\b[\w\-]+\.(?:py|pyi|ts|tsx|js|jsx|mjs|cjs|json|jsonl|toml|yaml|yml|md|txt|rs|go|java|kt|c|h|cc|cpp|hpp|sh|bash|zsh|fish|sql|html|css|scss|ini|cfg|conf|lock|xml|gradle|mod|sum|env)\b`)},
	{regex: regexp.MustCompile(`\b[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\(\)`)},
	{regex: regexp.MustCompile(`\b[A-Za-z]\w*_[A-Za-z0-9_]+\b`)},
	{regex: regexp.MustCompile(`\b[a-z]+[A-Z][A-Za-z0-9]*\b`)},
	{regex: regexp.MustCompile(`\b[a-z_]\w*(?:\.[a-z_]\w*)+\b`)},
	{regex: regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)},
	{regex: regexp.MustCompile(`--?[A-Za-z][\w\-]*`), context: func(r rune) bool {
		return isWordRune(r) || r == '-'
	}},
	{regex: regexp.MustCompile(`v?\d+\.\d+(?:\.\d+)?\b`), context: func(r rune) bool {
		return isWordRune(r) || r == '.'
	}},
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

var (
	headingPrefix    = regexp.MustCompile(`^( {0,3}#{1,6}[ \t]+)`)
	listPrefix       = regexp.MustCompile(`^([ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+(?:\[[ xX]\][ \t]+)?)`)
	blockquotePrefix = regexp.MustCompile(`^([ \t]*>[ \t]?)`)
	paragraphPrefix  = regexp.MustCompile(`^([ \t]*)`)
	headingClose     = regexp.MustCompile(`([ \t]+#+[ \t]*)$`)
	addedStructure   = regexp.MustCompile(`^(?:#{1,6}\s|[-*+]\s|\d{1,9}[.)]\s|` + "```" + `|~~~|>|\|)`)
)

// MarkerFor is the ASCII marker substituted for the span at index.
func MarkerFor(index int) string {
	return markerPrefix + itoa(index) + "@@"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

type span struct{ start, end int }

func collectSpans(text string) []span {
	spans := []span{}
	spans = append(spans, backtickSpans(text)...)
	for _, pattern := range inlinePatterns {
		for _, location := range pattern.regex.FindAllStringIndex(text, -1) {
			if pattern.context != nil && location[0] > 0 {
				previous, _ := utf8.DecodeLastRuneInString(text[:location[0]])
				if pattern.context(previous) {
					continue
				}
			}
			if location[1] > location[0] {
				spans = append(spans, span{start: location[0], end: location[1]})
			}
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	merged := []span{}
	lastEnd := -1
	for _, item := range spans {
		if item.start >= lastEnd {
			merged = append(merged, item)
			lastEnd = item.end
		}
	}
	return merged
}

// backtickSpans implements the Python same-length delimiter backreference
// without a backtracking engine.
func backtickSpans(text string) []span {
	spans := []span{}
	for index := 0; index < len(text); {
		if text[index] != '`' {
			index++
			continue
		}
		end := index
		for end < len(text) && text[end] == '`' {
			end++
		}
		run := text[index:end]
		searchFrom := end
		found := -1
		for {
			offset := strings.Index(text[searchFrom:], run)
			if offset < 0 {
				break
			}
			found = searchFrom + offset
			break
		}
		if found < 0 {
			index = end
			continue
		}
		spans = append(spans, span{start: index, end: found + len(run)})
		index = found + len(run)
	}
	return spans
}

func applyMarkers(text string, spans []span) (string, []string) {
	var builder strings.Builder
	values := make([]string, 0, len(spans))
	last := 0
	for index, item := range spans {
		builder.WriteString(text[last:item.start])
		builder.WriteString(MarkerFor(index))
		values = append(values, text[item.start:item.end])
		last = item.end
	}
	builder.WriteString(text[last:])
	return builder.String(), values
}

func protectInline(text string) (string, []string, bool) {
	if strings.Contains(text, markerPrefix) {
		return "", nil, false
	}
	protected, values := applyMarkers(text, collectSpans(text))
	return protected, values, true
}

func splitLine(line, kind string) (prefix, core, suffix string, ok bool) {
	if kind == "heading" {
		match := headingPrefix.FindStringSubmatchIndex(line)
		if match == nil {
			return "", "", "", false
		}
		rest := line[match[1]:]
		closing := ""
		if close := headingClose.FindStringSubmatchIndex(rest); close != nil {
			closing = rest[close[2]:close[3]]
			rest = rest[:close[2]]
		}
		core = strings.TrimRightFunc(rest, unicode.IsSpace)
		return line[match[2]:match[3]], core, rest[len(core):] + closing, true
	}
	pattern := paragraphPrefix
	switch kind {
	case "list_item":
		pattern = listPrefix
	case "blockquote":
		pattern = blockquotePrefix
	}
	match := pattern.FindStringSubmatchIndex(line)
	if match == nil {
		return "", "", "", false
	}
	rest := line[match[1]:]
	core = strings.TrimRightFunc(rest, unicode.IsSpace)
	return line[match[2]:match[3]], core, rest[len(core):], true
}

func classifySkip(core string) string {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(core) {
			return "secret"
		}
	}
	if japanesePattern.MatchString(core) {
		return "japanese"
	}
	if !asciiLetterPattern.MatchString(core) {
		return "no_letters"
	}
	stripped := strings.TrimSpace(core)
	if strings.HasPrefix(stripped, "$ ") {
		return "command"
	}
	if strings.HasPrefix(stripped, "|") {
		return "structured"
	}
	if (strings.HasPrefix(stripped, "{") && strings.HasSuffix(stripped, "}")) ||
		(strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]")) {
		return "structured"
	}
	if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
		return "code"
	}
	return ""
}
