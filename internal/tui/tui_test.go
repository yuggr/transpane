package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/monitor"
	"github.com/yuggr/transpane/internal/protection"
)

func makeView(id, text string, kind domain.MessageKind, state domain.TranslationState, translations map[int]string) monitor.View {
	doc := protection.ProtectDocument(text)
	view := monitor.View{
		Message:      domain.Message{ThreadID: "t", ID: id, Revision: "r", Timestamp: "2026-11-25T01:02:03Z", Kind: kind, Original: text},
		Doc:          doc,
		State:        state,
		Translations: map[int]string{},
		Pending:      map[int]struct{}{},
		Failed:       map[int]struct{}{},
		ErrorLabels:  map[string]struct{}{},
	}
	for key, value := range translations {
		view.Translations[key] = value
	}
	return view
}

func newTestApp() *App {
	app := New(context.Background(), config.Config{}, nil, "", nil)
	app.width = 100
	app.height = 30
	app.viewport.SetWidth(100)
	app.viewport.SetHeight(28)
	return app
}

func (a *App) handleKeyForTest(key string) tea.Cmd {
	_, cmd := a.handleKey(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return cmd
}

func TestSanitizeForDisplay(t *testing.T) {
	cases := map[string]string{
		"plain":                              "plain",
		"\x1b[31mred\x1b[0m":                 "red",
		"\x1b]8;;https://x\x07l\x1b]8;;\x07": "l",
		"\x1b]52;c;ZXZpbA==\x07text":         "text",
		"a\x1b":                              "a",
		"bell\x07 x":                         "bell x",
		"tab\tkept\nline":                    "tab\tkept\nline",
		"c1\u009b31m":                        "c131m",
	}
	for input, want := range cases {
		if got := SanitizeForDisplay(input); got != want {
			t.Fatalf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestRenderOriginalThenTranslationAndToggle(t *testing.T) {
	app := newTestApp()
	user := makeView("u1", "保存処理を調べて", domain.MessageKindUser, domain.TranslationStateSkipped, nil)
	agent := makeView("a1", "I will inspect the storage layer.", domain.MessageKindCommentary, domain.TranslationStateOriginal, nil)
	if _, cmd := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30}); cmd != nil {
		t.Fatal("unexpected cmd")
	}
	app.Update(resetMsg{views: []monitor.View{user, agent}})
	app.Update(statusMsg{status: monitor.Status{Server: "未確認", Pending: 1, LastUpdate: "01:02:03"}})
	content := app.View().Content
	for _, want := range []string{"PLaMo: 未確認", "最終更新: 01:02:03", "あなた", "進捗報告", "I will inspect the storage layer.", "s: セッション  o: 原文/翻訳"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %q", want, content)
		}
	}
	translated := makeView("a1", "I will inspect the storage layer.", domain.MessageKindCommentary, domain.TranslationStateTranslated, map[int]string{0: "保存層を確認します。"})
	app.Update(updatedMsg{view: translated})
	content = app.View().Content
	if !strings.Contains(content, "保存層を確認します。") || strings.Contains(content, "I will inspect the storage layer.") {
		t.Fatalf("translation not shown: %q", content)
	}
	app.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	content = app.View().Content
	if !strings.Contains(content, "[原文]") || !strings.Contains(content, "I will inspect the storage layer.") || strings.Contains(content, "保存層を確認します。") {
		t.Fatalf("original toggle: %q", content)
	}
}

func TestHeadersErrorsAndTimestamps(t *testing.T) {
	app := newTestApp()
	view := makeView("a1", "Body", domain.MessageKindFinal, domain.TranslationStateFailed, nil)
	view.ErrorLabels["HTTPエラー"] = struct{}{}
	app.Update(resetMsg{views: []monitor.View{view}})
	content := app.View().Content
	if !strings.Contains(content, "最終回答") || !strings.Contains(content, "[翻訳失敗]") || !strings.Contains(content, "(HTTPエラー)") {
		t.Fatalf("%q", content)
	}
	if !strings.Contains(content, ":02:03") && !strings.Contains(content, "時刻不明") {
		t.Fatalf("timestamp %q", content)
	}
}

func TestPickerShowsSameIDsAndKeepsRowIdentity(t *testing.T) {
	app := newTestApp()
	cwdA, cwdB := "/tmp/a", "/tmp/b"
	sessions := []domain.SessionInfo{
		{ThreadID: "shared", Provider: domain.ProviderCodex, Cwd: &cwdA, UpdatedAt: 1000},
		{ThreadID: "shared", Provider: domain.ProviderClaude, Cwd: &cwdB, UpdatedAt: 2000},
	}
	app.Update(sessionsMsg{sessions: sessions})
	content := app.View().Content
	if !strings.Contains(content, "セッションを選択") || !strings.Contains(content, "Codex") || !strings.Contains(content, "Claude Code") || !strings.Contains(content, "/tmp/a") || !strings.Contains(content, "/tmp/b") {
		t.Fatalf("%q", content)
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if app.picker.selected != 1 {
		t.Fatalf("selected %d", app.picker.selected)
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.picker != nil {
		t.Fatal("picker did not close")
	}
}

func TestScrollAnchorPreservedAcrossUpdate(t *testing.T) {
	app := newTestApp()
	views := []monitor.View{}
	for index := 0; index < 30; index++ {
		views = append(views, makeView(string(rune('a'+index%26)), strings.Repeat("line\n", 5)+"body", domain.MessageKindAssistant, domain.TranslationStateOriginal, nil))
	}
	app.Update(resetMsg{views: views})
	app.autoScroll = false
	app.viewport.SetYOffset(40)
	banked := app.viewport.YOffset()
	app.Update(updatedMsg{view: makeView("a", "changed", domain.MessageKindAssistant, domain.TranslationStateTranslated, map[int]string{0: "changed"})})
	if app.viewport.YOffset() != banked {
		t.Fatalf("offset moved %d -> %d", banked, app.viewport.YOffset())
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !app.autoScroll || !app.viewport.AtBottom() {
		t.Fatal("End did not resume auto scroll")
	}
}

func TestSKeyOpensPickerWithActiveSession(t *testing.T) {
	app := newTestApp()
	app.Update(resetMsg{session: &domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}, views: nil})
	app.session = &domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	cmd := app.handleKeyForTest("s")
	if cmd == nil || !app.pendingPicker {
		t.Fatal("s did not request discovery")
	}
	app.Update(sessionsMsg{sessions: []domain.SessionInfo{{ThreadID: "t", Provider: domain.ProviderCodex}}})
	if app.picker == nil {
		t.Fatal("picker did not open")
	}
}

func TestStatusNoteAndCacheNote(t *testing.T) {
	app := newTestApp()
	app.Update(statusMsg{status: monitor.Status{Server: "接続成功", Pending: 2, LastUpdate: "12:00:00", Note: "索引不一致", CacheNote: "locked", Session: "Codex / abc"}})
	content := app.View().Content
	for _, want := range []string{"Codex / abc", "接続成功", "待ち: 2", "索引不一致", "キャッシュ: locked"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %q", want, content)
		}
	}
}
