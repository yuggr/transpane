package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/monitor"
)

func appAtSize(t *testing.T, width, height int, views ...monitor.View) *App {
	t.Helper()
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: width, Height: height})
	app.Update(resetMsg{views: views})
	app.Update(statusMsg{status: monitor.Status{
		Server: "接続成功", Pending: 1, LastUpdate: "12:34:56",
		Note:    "status note with a long English description and 日本語 status text",
		Session: "Codex / 12345678…",
	}})
	return app
}

func TestViewLinesNeverExceedTerminalWidth(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"english", "This is a long English sentence that must wrap at word boundaries without losing any visible words or characters at the right edge."},
		{"japanese", "これは非常に長い日本語の文章で、端末の表示幅に応じて正しく折り返され、右端で欠落してはいけません。"},
		{"mixed", "Mixed 日本語 and English 1234567890 plus a verylongidentifierwithoutspaces and https://example.com/very/long/path/check.yml"},
		{"emoji", "Emoji 👩‍👩‍👧‍👦 family, combining e\u0301 and fullwidth ＡＢＣ must remain intact."},
	}
	for _, width := range []int{40, 60, 80, 120} {
		for _, item := range cases {
			t.Run(item.name, func(t *testing.T) {
				view := makeView("a1", item.body, domain.MessageKindAssistant, domain.TranslationStateOriginal, nil)
				app := appAtSize(t, width, 24, view)
				content := app.View().Content
				for _, line := range strings.Split(content, "\n") {
					if w := ansi.StringWidth(line); w > width {
						t.Fatalf("width %d: rendered width %d for %q", width, w, line)
					}
				}
				normalized := strings.Join(strings.Fields(content), "")
				original := strings.Join(strings.Fields(item.body), "")
				if !strings.Contains(normalized, original) {
					t.Fatalf("width %d: body characters lost: %q", width, item.body)
				}
			})
		}
	}
}

func assertLinesFit(t *testing.T, lines []string, width int) {
	t.Helper()
	for _, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line width %d > %d: %q", got, width, line)
		}
	}
}

func TestWrapEnglishAtWordBoundaries(t *testing.T) {
	text := "This is a long English sentence that should wrap at word boundaries without breaking ordinary words."
	for _, width := range []int{40, 60, 80} {
		lines := wrapDisplayLine(text, width)
		assertLinesFit(t, lines, width)
		if strings.Join(strings.Fields(strings.Join(lines, " ")), " ") != text {
			t.Fatalf("width %d: words changed: %#v", width, lines)
		}

	}
}

func TestWrapJapaneseUsesCellWidth(t *testing.T) {
	text := "これは非常に長い日本語の文章で、端末の表示セル幅に応じて折り返される必要があります。"
	for _, width := range []int{40, 60} {
		lines := wrapDisplayLine(text, width)
		assertLinesFit(t, lines, width)
		if strings.Join(strings.Fields(strings.Join(lines, "")), "") != text {
			t.Fatalf("width %d: Japanese text lost", width)
		}
	}
}

func TestWrapMixedAndLongTokens(t *testing.T) {
	text := "Mixed 日本語 and English with averylongidentifierwithoutspaces and https://example.com/very/long/path/check.yml"
	lines := wrapDisplayLine(text, 40)
	assertLinesFit(t, lines, 40)
	joined := strings.Join(strings.Fields(strings.Join(lines, " ")), "")
	original := strings.Join(strings.Fields(text), "")
	if joined != original {
		t.Fatalf("mixed text changed:\n%q\n%q", joined, original)
	}
}

func TestWrapPreservesExplicitLinesAndIndent(t *testing.T) {
	lines := wrapDisplayText("first\n\n  indented line\n\tTabbed line", 80)
	want := []string{"first", "", "  indented line", "    Tabbed line"}
	if len(lines) != len(want) {
		t.Fatalf("lines %#v", lines)
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Fatalf("line %d: got %q want %q", index, lines[index], want[index])
		}
	}
}

func TestWrapGraphemeClusters(t *testing.T) {
	text := "emoji 👩‍👩‍👧‍👦 family, combining e\u0301 and fullwidth ＡＢＣ."
	lines := wrapDisplayLine(text, 20)
	assertLinesFit(t, lines, 20)
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "👩‍👩‍👧‍👦") || !strings.Contains(joined, "e\u0301") || !strings.Contains(joined, "ＡＢＣ") {
		t.Fatalf("grapheme cluster damaged: %q", joined)
	}
}

func TestResizeRewrapsAtNewWidth(t *testing.T) {
	body := "This is a long English sentence that must rewrap after a terminal resize without losing characters."
	view := makeView("a1", body, domain.MessageKindAssistant, domain.TranslationStateOriginal, nil)
	app := appAtSize(t, 40, 24, view)
	before := strings.Split(app.buildContent(), "\n")
	assertLinesFit(t, before, 40)
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	after := strings.Split(app.buildContent(), "\n")
	assertLinesFit(t, after, 80)
	normalized := strings.Join(strings.Fields(strings.Join(after, " ")), "")
	original := strings.Join(strings.Fields(body), "")
	if !strings.Contains(normalized, original) {
		t.Fatalf("resize lost body text: %#v", after)
	}
}

func TestResizeKeepsMessageAnchor(t *testing.T) {
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	views := []monitor.View{
		makeView("a1", strings.Repeat("first long sentence ", 20), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("first long sentence ", 20)}),
		makeView("a2", strings.Repeat("second long sentence ", 20), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("second long sentence ", 20)}),
		makeView("a3", "third body", domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "third"}),
	}
	app.Update(resetMsg{views: views})
	app.autoScroll = false
	for _, item := range app.ranges {
		if item.key.ID == "a2" {
			app.viewport.SetYOffset(item.start + 2)
			break
		}
	}
	beforeKey, _, ok := app.topAnchor()
	if !ok || beforeKey.ID != "a2" {
		t.Fatalf("anchor setup failed: %+v", beforeKey)
	}
	app.Update(tea.WindowSizeMsg{Width: 70, Height: 10})
	afterKey, _, ok := app.topAnchor()
	if !ok || afterKey.ID != beforeKey.ID {
		t.Fatalf("resize moved off message: %+v -> %+v", beforeKey, afterKey)
	}
}

func TestTranslationUpdateKeepsAnchor(t *testing.T) {
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	views := []monitor.View{
		makeView("a1", strings.Repeat("first sentence with words. ", 8), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("first sentence with words. ", 8)}),
		makeView("a2", strings.Repeat("second sentence with words. ", 8), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("second sentence with words. ", 8)}),
		makeView("a3", "third body", domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "third"}),
	}
	app.Update(resetMsg{views: views})
	app.autoScroll = false
	// Put the top of the viewport inside a2.
	for _, item := range app.ranges {
		if item.key.ID == "a2" {
			app.viewport.SetYOffset(item.start + 1)
			break
		}
	}
	beforeKey, beforeInner, ok := app.topAnchor()
	if !ok || beforeKey.ID != "a2" {
		t.Fatalf("anchor setup failed: %+v %d", beforeKey, beforeInner)
	}
	updated := makeView("a2", strings.Repeat("second sentence with words. ", 8), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("second sentence with words. ", 16)})
	app.Update(updatedMsg{view: updated})
	afterKey, afterInner, ok := app.topAnchor()
	if !ok || afterKey != beforeKey || afterInner != beforeInner {
		t.Fatalf("anchor moved %+v/%d -> %+v/%d", beforeKey, beforeInner, afterKey, afterInner)
	}
}

func TestHistoryPrependKeepsAnchor(t *testing.T) {
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	views := []monitor.View{}
	for index := 0; index < 10; index++ {
		views = append(views, makeView("a"+string(rune('0'+index)), strings.Repeat("line words ", 30), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("line words ", 30)}))
	}
	app.Update(resetMsg{views: views})
	app.autoScroll = false
	for _, item := range app.ranges {
		if item.key.ID == "a5" {
			app.viewport.SetYOffset(item.start + 2)
			break
		}
	}
	beforeKey, beforeInner, ok := app.topAnchor()
	if !ok || beforeKey.ID != "a5" {
		t.Fatalf("anchor setup failed: %+v", beforeKey)
	}
	older := []monitor.View{
		makeView("a9", strings.Repeat("older words ", 20), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("older words ", 20)}),
		makeView("a8", strings.Repeat("older words ", 20), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("older words ", 20)}),
	}
	app.Update(prependedMsg{views: older})
	afterKey, afterInner, ok := app.topAnchor()
	if !ok || afterKey.ID != beforeKey.ID || afterInner != beforeInner {
		t.Fatalf("prepend moved anchor %+v/%d -> %+v/%d", beforeKey, beforeInner, afterKey, afterInner)
	}
}

func TestOriginalToggleKeepsAnchor(t *testing.T) {
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	view := makeView("a1", strings.Repeat("original English words ", 20), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("translated Japanese words ", 20)})
	app.Update(resetMsg{views: []monitor.View{view}})
	app.autoScroll = false
	app.viewport.SetYOffset(2)
	beforeKey, beforeInner, _ := app.topAnchor()
	app.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	afterKey, afterInner, _ := app.topAnchor()
	if afterKey != beforeKey || afterInner != beforeInner {
		t.Fatalf("original toggle moved anchor %+v/%d -> %+v/%d", beforeKey, beforeInner, afterKey, afterInner)
	}
}

func TestWrapDoesNotMutateData(t *testing.T) {
	original := strings.Repeat("A very long line with 日本語 and https://example.com/path. ", 10)
	view := makeView("a1", original, domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + original})
	app := appAtSize(t, 40, 24, view)
	beforeOriginal := app.views[0].Message.Original
	beforeTranslation := app.views[0].Translations[0]
	_ = app.buildContent()
	_ = app.View()
	if app.views[0].Message.Original != beforeOriginal || app.views[0].Translations[0] != beforeTranslation {
		t.Fatal("display wrapping mutated message data")
	}
}

func TestNarrowViewerAndPickerStayInsideScreen(t *testing.T) {
	body := "A long English sentence that needs multiple wrapped display lines on a narrow terminal."
	view := makeView("a1", body, domain.MessageKindAssistant, domain.TranslationStateOriginal, nil)
	app := appAtSize(t, 40, 6, view)
	content := app.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) > 6 {
		t.Fatalf("viewer height exceeded: %d lines", len(lines))
	}
	assertLinesFit(t, lines, 40)

	cwd := "/tmp/a/very/long/project/path/that/should/not/overflow/the/picker"
	app.Update(sessionsMsg{sessions: []domain.SessionInfo{{
		ThreadID: "88888888-8888-4888-8888-888888888888",
		Provider: domain.ProviderCodex,
		Cwd:      &cwd,
	}}})
	content = app.View().Content
	lines = strings.Split(content, "\n")
	if len(lines) > 6 {
		t.Fatalf("picker height exceeded: %d lines", len(lines))
	}
	assertLinesFit(t, lines, 40)
}

func TestEndReturnsToLatestWithWrappedContent(t *testing.T) {
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	views := []monitor.View{}
	for index := 0; index < 5; index++ {
		views = append(views, makeView("a"+string(rune('0'+index)), strings.Repeat("long wrapped words ", 40), domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "訳 " + strings.Repeat("long wrapped words ", 40)}))
	}
	app.Update(resetMsg{views: views})
	app.autoScroll = false
	app.viewport.GotoTop()
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !app.autoScroll || !app.viewport.AtBottom() {
		t.Fatal("End did not restore bottom auto-scroll")
	}
}
