// Package protection protects Markdown structure and inline spans before
// translation, then restores the original bytes for display.
package protection

import (
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"

	"github.com/yuggr/transpane/internal/config"
)

// Segment is one request-sized piece of a protected line.
type Segment struct {
	Text     string
	Markers  []string
	Trailing string
}

// Unit is one physical line that may be translated.
type Unit struct {
	ID           int
	LineIndex    int
	Prefix       string
	Suffix       string
	SourceText   string
	Segments     []Segment
	Markers      []string
	SkipReason   string
	Conservative bool
}

// ProtectedDoc keeps the original source and the translatable units.
type ProtectedDoc struct {
	Source string
	Lines  []string
	Units  []Unit
}

func (d *ProtectedDoc) TranslatableUnits() []Unit {
	units := []Unit{}
	for _, unit := range d.Units {
		if unit.SkipReason == "" {
			units = append(units, unit)
		}
	}
	return units
}

func (d *ProtectedDoc) HasConservativeExclusion() bool {
	for _, unit := range d.Units {
		if unit.Conservative {
			return true
		}
	}
	return false
}

// Render replaces successfully translated units, leaving every other byte as
// the original. Invalid translations are ignored.
func (d *ProtectedDoc) Render(translations map[int]string) string {
	lines := make([]string, len(d.Lines))
	copy(lines, d.Lines)
	for _, unit := range d.Units {
		translated, present := translations[unit.ID]
		if !present || translated == "" {
			continue
		}
		restored, ok := RestoreMarkers(translated, unit.Markers)
		if !ok {
			continue
		}
		lines[unit.LineIndex] = unit.Prefix + restored + unit.Suffix
	}
	return strings.Join(lines, "\n")
}

var markdownParser = goldmark.New(goldmark.WithExtensions(extension.Table))

func lineStarts(source string) []int {
	starts := []int{0}
	for index := 0; index < len(source); index++ {
		if source[index] == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func lineIndexAt(starts []int, offset int) int {
	index := sort.SearchInts(starts, offset+1) - 1
	if index < 0 {
		return 0
	}
	return index
}

func containerDepth(stack []ast.Node) int {
	depth := 0
	for _, node := range stack {
		switch node.(type) {
		case *ast.ListItem, *ast.Blockquote:
			depth++
		}
	}
	return depth
}

func hasAncestor[Node ast.Node](stack []ast.Node) bool {
	for _, node := range stack {
		if _, ok := node.(Node); ok {
			return true
		}
	}
	return false
}

func recordBlock(allowed map[int]string, node ast.Node, stack []ast.Node, source string, starts []int) {
	if node.Type() != ast.TypeBlock {
		return
	}
	lines := node.Lines()
	if lines == nil || lines.Len() != 1 {
		return
	}
	segment := lines.At(0)
	if strings.Contains(strings.TrimSuffix(string(segment.Value([]byte(source))), "\n"), "\n") {
		return
	}
	line := lineIndexAt(starts, segment.Start)

	switch typed := node.(type) {
	case *ast.Heading:
		_ = typed
		if containerDepth(stack) != 0 {
			return
		}
		lineText := source[starts[line]:]
		if newline := strings.IndexByte(lineText, '\n'); newline >= 0 {
			lineText = lineText[:newline]
		}
		if !headingPrefix.MatchString(lineText) {
			return
		}
		allowed[line] = "heading"
	case *ast.Paragraph, *ast.TextBlock:
		if containerDepth(stack) > 1 {
			return
		}
		switch {
		case hasAncestor[*ast.ListItem](stack):
			allowed[line] = "list_item"
		case hasAncestor[*ast.Blockquote](stack):
			allowed[line] = "blockquote"
		default:
			allowed[line] = "paragraph"
		}
	}
}

func nodeLineRange(node ast.Node, source string, starts []int) (minLine, maxLine int, ok bool) {
	minLine = int(^uint(0) >> 1)
	maxLine = -1
	_ = ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || current.Type() != ast.TypeBlock {
			return ast.WalkContinue, nil
		}
		lines := current.Lines()
		if lines == nil || lines.Len() == 0 {
			return ast.WalkContinue, nil
		}
		first := lineIndexAt(starts, lines.At(0).Start)
		lastSegment := lines.At(lines.Len() - 1)
		lastOffset := lastSegment.Stop
		if lastOffset > lastSegment.Start {
			lastOffset--
		}
		last := lineIndexAt(starts, lastOffset)
		if first < minLine {
			minLine = first
		}
		if last > maxLine {
			maxLine = last
		}
		return ast.WalkContinue, nil
	})
	return minLine, maxLine, maxLine >= 0
}

func hasNestedListItem(item *ast.ListItem) bool {
	found := false
	_ = ast.Walk(item, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node == item {
			return ast.WalkContinue, nil
		}
		if _, ok := node.(*ast.ListItem); ok {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

func allowedLines(source string) map[int]string {
	allowed := map[int]string{}
	starts := lineStarts(source)
	doc := markdownParser.Parser().Parse(text.NewReader([]byte(source)))
	stack := []ast.Node{}
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return ast.WalkContinue, nil
		}
		recordBlock(allowed, node, stack, source, starts)
		stack = append(stack, node)
		return ast.WalkContinue, nil
	})

	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		item, ok := node.(*ast.ListItem)
		if !ok || !hasNestedListItem(item) {
			return ast.WalkContinue, nil
		}
		minLine, maxLine, ok := nodeLineRange(item, source, starts)
		if !ok {
			return ast.WalkContinue, nil
		}
		for line := minLine; line <= maxLine; line++ {
			delete(allowed, line)
		}
		return ast.WalkContinue, nil
	})
	return allowed
}

// ProtectDocument parses the source and records every eligible line. It never
// rewrites the source.
func ProtectDocument(source string) *ProtectedDoc {
	lines := strings.Split(source, "\n")
	allowed := allowedLines(source)
	units := []Unit{}

	lineIndexes := make([]int, 0, len(allowed))
	for line := range allowed {
		lineIndexes = append(lineIndexes, line)
	}
	sort.Ints(lineIndexes)

	for _, lineIndex := range lineIndexes {
		if lineIndex < 0 || lineIndex >= len(lines) {
			continue
		}
		prefix, core, suffix, ok := splitLine(lines[lineIndex], allowed[lineIndex])
		if !ok || strings.TrimSpace(core) == "" {
			continue
		}
		unitID := len(units)
		skipReason, conservative, segments, markers := protectCore(core)
		units = append(units, Unit{
			ID:           unitID,
			LineIndex:    lineIndex,
			Prefix:       prefix,
			Suffix:       suffix,
			SourceText:   core,
			Segments:     segments,
			Markers:      markers,
			SkipReason:   skipReason,
			Conservative: conservative,
		})
	}
	return &ProtectedDoc{Source: source, Lines: lines, Units: units}
}

func protectCore(core string) (string, bool, []Segment, []string) {
	if reason := classifySkip(core); reason != "" {
		return reason, false, nil, nil
	}
	protected, values, ok := protectInline(core)
	if !ok {
		return "marker_collision", true, nil, nil
	}
	if !asciiLetterPattern.MatchString(markerPattern.ReplaceAllString(protected, "")) {
		return "inline_only", false, nil, nil
	}
	pieces, ok := SplitProtected(protected, config.MaxSegmentChars)
	if !ok {
		return "long_unsplittable", true, nil, nil
	}
	segments := make([]Segment, 0, len(pieces))
	allMarkers := make([]string, 0)
	for _, piece := range pieces {
		markers := markerPattern.FindAllString(piece[0], -1)
		if markers == nil {
			markers = []string{}
		}
		segments = append(segments, Segment{Text: piece[0], Markers: markers, Trailing: piece[1]})
		allMarkers = append(allMarkers, markers...)
	}
	return "", false, segments, values
}
