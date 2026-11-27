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

// Python's re \s, \w and \b are Unicode-aware; Go's RE2 versions are ASCII
// only. These classes reproduce Python's \s and \w. \b is implemented by
// pythonBoundary so a secret or protected span is not missed at a Unicode
// word edge.
const (
	pythonSpaceBody = `\t\n\v\f\r \x{001c}-\x{001f}\x{0085}\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	pythonSpace     = `[` + pythonSpaceBody + `]`
	notPythonSpace  = `[^` + pythonSpaceBody + `]`
	pythonWordBody  = `\p{L}\p{N}_`
	pythonWord      = `[` + pythonWordBody + `]`
)

// textPattern is a regex plus Python \b / lookbehind boundary checks.
type textPattern struct {
	regex         *regexp.Regexp
	startBoundary bool
	endBoundary   bool
	context       func(rune) bool
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

// pythonBoundary reports Python's \b at a byte position.
func pythonBoundary(text string, position int) bool {
	beforeWord := false
	afterWord := false
	if position > 0 {
		before, _ := utf8.DecodeLastRuneInString(text[:position])
		beforeWord = isWordRune(before)
	}
	if position < len(text) {
		after, _ := utf8.DecodeRuneInString(text[position:])
		afterWord = isWordRune(after)
	}
	return beforeWord != afterWord
}

func (p textPattern) find(text string) []span {
	found := []span{}
	search := 0
	for search <= len(text) {
		location := p.regex.FindStringIndex(text[search:])
		if location == nil {
			break
		}
		start := search + location[0]
		end := search + location[1]
		valid := end > start
		if valid && p.startBoundary && !pythonBoundary(text, start) {
			valid = false
		}
		if valid && p.endBoundary && !pythonBoundary(text, end) {
			valid = false
		}
		if valid && p.context != nil && start > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:start])
			if p.context(previous) {
				valid = false
			}
		}
		if valid {
			found = append(found, span{start: start, end: end})
			search = end
			continue
		}
		if start >= len(text) {
			break
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		search = start + size
	}
	return found
}

var secretPatterns = []textPattern{
	{regex: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{regex: regexp.MustCompile(`(?i)bearer` + pythonSpace + `+[A-Za-z0-9._~+/=-]{10,}`), startBoundary: true},
	{regex: regexp.MustCompile(`AKIA[0-9A-Z]{16}`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`https?://[^` + pythonSpaceBody + `/@:]+:[^` + pythonSpaceBody + `/@]+@`)},
}

var inlinePatterns = []textPattern{
	{regex: regexp.MustCompile(`!?\[[^\]\n]*\]\([^)\n]*\)`)},
	{regex: regexp.MustCompile(`<https?://[^>` + pythonSpaceBody + `]+>`)},
	{regex: regexp.MustCompile(`</?[A-Za-z][^>\n]*>`)},
	{regex: regexp.MustCompile(`\$\$?[^$\n]+\$\$?`)},
	{regex: regexp.MustCompile(`https?://[^` + pythonSpaceBody + `<>()\[\]]+`)},
	{regex: regexp.MustCompile(`\.{0,2}/[` + pythonWordBody + `./%~+\-]+`), context: func(r rune) bool {
		return isWordRune(r) || r == '.'
	}},
	{regex: regexp.MustCompile(pythonWord + `[\p{L}\p{N}_.\-]*(?:/` + pythonWord + `[\p{L}\p{N}_.\-]*)+`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(pythonWord + `[\p{L}\p{N}_\-]*\.(?:py|pyi|ts|tsx|js|jsx|mjs|cjs|json|jsonl|toml|yaml|yml|md|txt|rs|go|java|kt|c|h|cc|cpp|hpp|sh|bash|zsh|fish|sql|html|css|scss|ini|cfg|conf|lock|xml|gradle|mod|sum|env)`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`[A-Za-z_]` + pythonWord + `*(?:\.` + `[A-Za-z_]` + pythonWord + `*)*\(\)`), startBoundary: true},
	{regex: regexp.MustCompile(`[A-Za-z]` + pythonWord + `*_` + pythonWord + `+`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`[a-z]+[A-Z][A-Za-z0-9]*`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`[a-z_]` + pythonWord + `*(?:\.` + `[a-z_]` + pythonWord + `*)+`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`[A-Z][A-Z0-9_]{2,}`), startBoundary: true, endBoundary: true},
	{regex: regexp.MustCompile(`--?[A-Za-z][` + pythonWordBody + `\-]*`), context: func(r rune) bool {
		return isWordRune(r) || r == '-'
	}},
	{regex: regexp.MustCompile(`v?\d+\.\d+(?:\.\d+)?`), endBoundary: true, context: func(r rune) bool {
		return isWordRune(r) || r == '.'
	}},
}

var (
	headingPrefix    = regexp.MustCompile(`^( {0,3}#{1,6}[ \t]+)`)
	listPrefix       = regexp.MustCompile(`^([ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+(?:\[[ xX]\][ \t]+)?)`)
	blockquotePrefix = regexp.MustCompile(`^([ \t]*>[ \t]?)`)
	paragraphPrefix  = regexp.MustCompile(`^([ \t]*)`)
	headingClose     = regexp.MustCompile(`([ \t]+#+[ \t]*)$`)
	addedStructure   = regexp.MustCompile(`^(?:#{1,6}` + pythonSpace + `|[-*+]` + pythonSpace + `|\d{1,9}[.)]` + pythonSpace + `|` + "```" + `|~~~|>|\|)`)
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
		spans = append(spans, pattern.find(text)...)
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
		if len(pattern.find(core)) > 0 {
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
