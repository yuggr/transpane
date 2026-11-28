package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yuggr/transpane/internal/domain"
)

func pickerSession(provider domain.Provider, id, title, cwd string, updated time.Time) domain.SessionInfo {
	directory := cwd
	mode := "legacy"
	if provider == domain.ProviderClaude {
		mode = "claude-jsonl"
	}
	return domain.SessionInfo{
		Provider:    provider,
		ThreadID:    id,
		Path:        "/logs/" + id + ".jsonl",
		Cwd:         &directory,
		Title:       title,
		HistoryMode: &mode,
		UpdatedAt:   float64(updated.UnixNano()) / 1e9,
	}
}

func pickerTestApp(t *testing.T, width, height int, sessions []domain.SessionInfo) *App {
	t.Helper()
	app := newTestApp()
	app.Update(tea.WindowSizeMsg{Width: width, Height: height})
	app.Update(sessionsMsg{sessions: sessions})
	if app.picker == nil {
		t.Fatal("picker did not open")
	}
	return app
}

func pickerContentLines(t *testing.T, app *App) []string {
	t.Helper()
	return strings.Split(app.View().Content, "\n")
}

func assertPickerLinesFit(t *testing.T, app *App, width int) {
	t.Helper()
	for _, line := range pickerContentLines(t, app) {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("width %d: line width %d: %q", width, got, line)
		}
	}
}

func typePickerText(app *App, text string) {
	for _, char := range text {
		app.Update(tea.KeyPressMsg{Code: char, Text: string(char)})
	}
}

func selectedPickerKey(t *testing.T, app *App) domain.SessionKey {
	t.Helper()
	session, ok := app.selectedPickerSession()
	if !ok {
		t.Fatal("no selected session")
	}
	return session.Key()
}

func TestPickerTwoLineRowsAcrossWidths(t *testing.T) {
	now := time.Now()
	sessions := []domain.SessionInfo{
		pickerSession(domain.ProviderCodex, "01a1e80f-23da-73d0-a34d-44d208e4e967", "PR #5 の移植検証", "/home/u/transpane", now.Add(-17*time.Minute)),
		pickerSession(domain.ProviderClaude, "7579c4bd-0000-4000-8000-000000000001", "PowerShellの配置確認", "/home/u/project-a", now.Add(-3*time.Hour)),
		pickerSession(domain.ProviderClaude, "188f4be6-0000-4000-8000-000000000002", "API認証方式の調査", "/home/u/project-b", now.Add(-26*time.Hour)),
	}
	for _, width := range []int{40, 60, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			app := pickerTestApp(t, width, 24, sessions)
			assertPickerLinesFit(t, app, width)
			content := app.View().Content
			for _, want := range []string{
				"> [Codex] PR #5 の移植検証",
				"transpane | 17分前 | 01a1e80f…",
				"[CC] PowerShellの配置確認",
				"project-a | 3時間前 | 7579c4bd…",
				"[CC] API認証方式の調査",
				"project-b | 昨日 | 188f4be6…",
			} {
				if !strings.Contains(content, want) {
					t.Fatalf("width %d: missing %q in %q", width, want, content)
				}
			}
			if strings.Contains(content, "23da-73d0") {
				t.Fatalf("width %d: full UUID leaked into the list: %q", width, content)
			}
		})
	}
}

func TestPickerHonoursWideAndCombiningCells(t *testing.T) {
	title := "家族👩‍👩‍👧‍👦 e\u0301 ＡＢＣ の確認"
	for _, width := range []int{40, 60, 80} {
		app := pickerTestApp(t, width, 24, []domain.SessionInfo{
			pickerSession(domain.ProviderCodex, "01a1e80f-23da-73d0-a34d-44d208e4e967", title, "/home/u/transpane", time.Now()),
		})
		assertPickerLinesFit(t, app, width)
		if !strings.Contains(app.View().Content, "家族👩‍👩‍👧‍👦") {
			t.Fatalf("width %d: emoji title damaged: %q", width, app.View().Content)
		}
	}
}

func TestPickerIncrementalSearchOverTitleProjectProviderAndID(t *testing.T) {
	now := time.Now()
	sessions := []domain.SessionInfo{
		pickerSession(domain.ProviderCodex, "01a1e80f-23da-73d0-a34d-44d208e4e967", "PR #5 の移植検証", "/home/u/transpane", now.Add(-17*time.Minute)),
		pickerSession(domain.ProviderClaude, "7579c4bd-0000-4000-8000-000000000001", "PowerShellの配置確認", "/home/u/project-a", now.Add(-3*time.Hour)),
		pickerSession(domain.ProviderClaude, "188f4be6-0000-4000-8000-000000000002", "API認証方式の調査", "/home/u/project-b", now.Add(-26*time.Hour)),
	}
	app := pickerTestApp(t, 100, 30, sessions)
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	selected := selectedPickerKey(t, app)
	if selected.ID != "7579c4bd-0000-4000-8000-000000000001" {
		t.Fatalf("setup selected %+v", selected)
	}

	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !app.picker.searching {
		t.Fatal("search did not start")
	}
	typePickerText(app, "claude")
	if got := selectedPickerKey(t, app); got != selected {
		t.Fatalf("selection changed while still matching: %+v -> %+v", selected, got)
	}
	if visible := app.pickerSessions(); len(visible) != 2 {
		t.Fatalf("claude search matched %d", len(visible))
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typePickerText(app, "01a1e80f")
	if got := selectedPickerKey(t, app); got.ID != "01a1e80f-23da-73d0-a34d-44d208e4e967" {
		t.Fatalf("ID search selected %+v", got)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typePickerText(app, "project-a")
	if visible := app.pickerSessions(); len(visible) != 1 || visible[0].ThreadID != selected.ID {
		t.Fatalf("project search %+v", visible)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typePickerText(app, "Codex")
	if visible := app.pickerSessions(); len(visible) != 1 || visible[0].Provider != domain.ProviderCodex {
		t.Fatalf("provider search %+v", visible)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typePickerText(app, "PowerShell")
	if visible := app.pickerSessions(); len(visible) != 1 || visible[0].ThreadID != selected.ID {
		t.Fatalf("title search %+v", visible)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if visible := app.pickerSessions(); len(visible) != 3 {
		t.Fatalf("clear did not restore candidates: %d", len(visible))
	}
}

func TestPickerDetailShowsFullMetadata(t *testing.T) {
	id := "7579c4bd-0000-4000-8000-000000000001"
	cwd := "/home/u/project-a"
	updated := time.Date(2026, 11, 28, 9, 30, 0, 0, time.Local)
	session := pickerSession(domain.ProviderClaude, id, "PowerShellの配置確認", cwd, updated)
	for _, width := range []int{40, 80} {
		app := pickerTestApp(t, width, 24, []domain.SessionInfo{session})
		app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
		if !app.picker.detail {
			t.Fatal("detail did not open")
		}
		assertPickerLinesFit(t, app, width)
		content := app.View().Content
		compact := strings.Join(strings.Fields(content), "")
		for _, want := range []string{
			"セッション詳細",
			id,
			cwd,
			session.Path,
			"形式:claude-jsonl",
			"最終更新:" + strings.ReplaceAll(updated.Format("2006-01-02 15:04:05"), " ", ""),
		} {
			if !strings.Contains(compact, want) {
				t.Fatalf("width %d: missing %q in %q", width, want, content)
			}
		}
	}
}

func TestPickerSelectionSurvivesRefreshSearchAndResize(t *testing.T) {
	now := time.Now()
	a := pickerSession(domain.ProviderCodex, "aaaaaaaa-1111-4000-8000-000000000001", "alpha", "/home/u/alpha", now.Add(-time.Minute))
	b := pickerSession(domain.ProviderClaude, "bbbbbbbb-2222-4000-8000-000000000002", "bravo", "/home/u/bravo", now.Add(-2*time.Minute))
	c := pickerSession(domain.ProviderClaude, "cccccccc-3333-4000-8000-000000000003", "charlie", "/home/u/charlie", now.Add(-3*time.Minute))
	app := pickerTestApp(t, 100, 30, []domain.SessionInfo{a, b, c})
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	wanted := selectedPickerKey(t, app)
	if wanted.ID != b.ThreadID {
		t.Fatalf("setup selected %+v", wanted)
	}

	app.Update(sessionsMsg{sessions: []domain.SessionInfo{c, a, b}})
	if got := selectedPickerKey(t, app); got != wanted {
		t.Fatalf("refresh changed selection %+v -> %+v", wanted, got)
	}

	app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if got := selectedPickerKey(t, app); got != wanted {
		t.Fatalf("resize changed selection %+v -> %+v", wanted, got)
	}

	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	typePickerText(app, "bravo")
	if got := selectedPickerKey(t, app); got != wanted {
		t.Fatalf("search changed selection %+v -> %+v", wanted, got)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typePickerText(app, "alpha")
	if got := selectedPickerKey(t, app); got.ID != a.ThreadID {
		t.Fatalf("narrowed search selected %+v", got)
	}
	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := selectedPickerKey(t, app); got != wanted {
		t.Fatalf("clearing search did not restore selection %+v -> %+v", wanted, got)
	}

	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if got := selectedPickerKey(t, app); got != wanted {
		t.Fatalf("second resize changed selection %+v -> %+v", wanted, got)
	}
}

func TestPickerDisambiguatesProjectsIDsAndPaths(t *testing.T) {
	sessions := []domain.SessionInfo{
		pickerSession(domain.ProviderCodex, "aaaaaaaa-1111-4000-8000-000000000001", "same", "/team-a/api", time.Now()),
		pickerSession(domain.ProviderCodex, "aaaaaaaa-2222-4000-8000-000000000002", "same", "/team-b/api", time.Now()),
	}
	projects := projectLabels(sessions)
	if projects[0] != "/team-a/api" || projects[1] != "/team-b/api" {
		t.Fatalf("projects %#v", projects)
	}
	ids := idLabels(sessions)
	if ids[0] == ids[1] || !strings.HasPrefix(ids[0], "aaaaaaaa-1") || !strings.HasPrefix(ids[1], "aaaaaaaa-2") {
		t.Fatalf("ids %#v", ids)
	}

	rows := buildPickerRows([]domain.SessionInfo{
		{Provider: domain.ProviderCodex, ThreadID: "shared-id", Path: "/logs/one.jsonl", Title: "same"},
		{Provider: domain.ProviderCodex, ThreadID: "shared-id", Path: "/logs/two.jsonl", Title: "same"},
	}, time.Now())
	if rows[0].extra != "one.jsonl" || rows[1].extra != "two.jsonl" {
		t.Fatalf("path disambiguation %#v", rows)
	}
}

func TestPickerRelativeTimeLabels(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	cases := []struct {
		updated time.Time
		want    string
	}{
		{now.Add(-30 * time.Second), "たった今"},
		{now.Add(-5 * time.Minute), "5分前"},
		{now.Add(-3 * time.Hour), "3時間前"},
		{now.Add(-26 * time.Hour), "昨日"},
		{now.Add(-3 * 24 * time.Hour), "3日前"},
		{now.Add(-10 * 24 * time.Hour), "1週間前"},
		{now.Add(-40 * 24 * time.Hour), "1か月前"},
		{now.Add(-400 * 24 * time.Hour), "1年前"},
		{now.Add(time.Minute), "たった今"},
	}
	for _, item := range cases {
		got := formatRelativeTime(float64(item.updated.UnixNano())/1e9, now)
		if got != item.want {
			t.Fatalf("%s: got %q want %q", item.updated, got, item.want)
		}
	}
	if got := formatRelativeTime(0, now); got != "更新不明" {
		t.Fatalf("zero: %q", got)
	}
}

func TestPickerTickAndClosedPickerBehaviour(t *testing.T) {
	app := newTestApp()
	app.session = &domain.SessionInfo{ThreadID: "active", Provider: domain.ProviderCodex}
	if app.shouldDiscoverOnTick() {
		t.Fatal("active session without picker should not refresh")
	}
	app.picker = &pickerState{}
	if !app.shouldDiscoverOnTick() {
		t.Fatal("open picker should refresh")
	}
	app.session = nil
	app.picker = nil
	if !app.shouldDiscoverOnTick() {
		t.Fatal("missing session should refresh")
	}

	app = pickerTestApp(t, 80, 24, []domain.SessionInfo{{ThreadID: "one", Provider: domain.ProviderCodex}})
	app.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.picker != nil {
		t.Fatal("esc did not close picker")
	}
	app.Update(sessionsMsg{sessions: []domain.SessionInfo{{ThreadID: "two", Provider: domain.ProviderClaude}}})
	if app.picker != nil {
		t.Fatal("closed picker reopened on a background refresh")
	}
	if len(app.sessions) != 1 || app.sessions[0].ThreadID != "two" {
		t.Fatalf("refresh not applied: %+v", app.sessions)
	}
}

func TestPickerFallsBackAndSanitizesTitle(t *testing.T) {
	app := pickerTestApp(t, 80, 24, []domain.SessionInfo{
		{Provider: domain.ProviderCodex, ThreadID: "missing-title", Title: ""},
		{Provider: domain.ProviderClaude, ThreadID: "unsafe-title", Title: "safe\x1b]8;;https://evil\x07title"},
	})
	content := app.View().Content
	if !strings.Contains(content, "タイトル未取得") {
		t.Fatalf("missing fallback: %q", content)
	}
	if !strings.Contains(content, "safetitle") {
		t.Fatalf("sanitized title missing: %q", content)
	}
	if strings.Contains(content, "\x1b]8") {
		t.Fatalf("unsanitized title: %q", content)
	}
}

func TestPickerSanitizesSearchQuery(t *testing.T) {
	app := pickerTestApp(t, 80, 24, []domain.SessionInfo{
		{Provider: domain.ProviderCodex, ThreadID: "one", Title: "one"},
	})
	app.picker.query = "x\x1b]8;;https://evil\x07y"
	content := app.View().Content
	if strings.Contains(content, "\x1b]8") {
		t.Fatalf("unsanitized search query: %q", content)
	}
}

func TestPickerScrollsSelectedRowIntoView(t *testing.T) {
	now := time.Now()
	sessions := make([]domain.SessionInfo, 30)
	for index := range sessions {
		sessions[index] = pickerSession(domain.ProviderCodex, "id-"+strings.Repeat("x", index%5)+"-"+string(rune('a'+index%26)), "title-"+string(rune('a'+index%26)), "/tmp/project-"+string(rune('a'+index%26)), now.Add(-time.Duration(index)*time.Minute))
	}
	app := pickerTestApp(t, 80, 24, sessions)
	for index := 0; index < 25; index++ {
		app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	content := app.View().Content
	selected := app.picker.selected
	if !strings.Contains(content, "> [Codex] "+sessions[selected].Title) {
		t.Fatalf("selected row not visible: %q", content)
	}
	if !strings.Contains(content, "再検索") {
		t.Fatalf("footer not visible: %q", content)
	}
	assertPickerLinesFit(t, app, 80)
}

func TestProjectLabelsGroupByUniqueCwd(t *testing.T) {
	now := time.Now()
	sessions := []domain.SessionInfo{
		pickerSession(domain.ProviderCodex, "same-a", "one", "/home/u/shared", now),
		pickerSession(domain.ProviderClaude, "same-b", "two", "/home/u/shared", now),
		pickerSession(domain.ProviderCodex, "other-c", "three", "/home/u/other/shared", now),
		pickerSession(domain.ProviderClaude, "unique-d", "four", "/home/u/only", now),
	}
	labels := projectLabels(sessions)
	if labels[0] != labels[1] {
		t.Fatalf("same cwd labels differ: %#v", labels)
	}
	if labels[0] != "…/u/shared" || labels[2] != "…/other/shared" {
		t.Fatalf("colliding cwds not expanded: %#v", labels)
	}
	if labels[3] != "only" {
		t.Fatalf("unique cwd should keep its basename: %#v", labels)
	}
	if labels[0] == "/home/u/shared" {
		t.Fatalf("same-cwd sessions were treated as a collision: %#v", labels)
	}
}

func TestPickerSearchSelectionUsesCurrentAndRestoreKeys(t *testing.T) {
	now := time.Now()
	a := pickerSession(domain.ProviderCodex, "aaaaaaaa-1111-4000-8000-000000000001", "alpha", "/home/u/alpha", now.Add(-time.Minute))
	b := pickerSession(domain.ProviderClaude, "bbbbbbbb-2222-4000-8000-000000000002", "bravo", "/home/u/bravo", now.Add(-2*time.Minute))
	c := pickerSession(domain.ProviderClaude, "cccccccc-3333-4000-8000-000000000003", "charlie", "/home/u/charlie", now.Add(-3*time.Minute))
	app := pickerTestApp(t, 100, 30, []domain.SessionInfo{a, b, c})
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	restore := selectedPickerKey(t, app)
	if restore.ID != b.ThreadID {
		t.Fatalf("setup selected %+v", restore)
	}

	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	typePickerText(app, "alpha")
	current := selectedPickerKey(t, app)
	if current.ID != a.ThreadID {
		t.Fatalf("filtered selection %+v", current)
	}
	if app.picker.restoreKey != restore {
		t.Fatalf("restore key %+v want %+v", app.picker.restoreKey, restore)
	}

	app.Update(sessionsMsg{sessions: []domain.SessionInfo{c, b, a}})
	if got := selectedPickerKey(t, app); got != current {
		t.Fatalf("reorder changed the filtered selection %+v -> %+v", current, got)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := selectedPickerKey(t, app); got != restore {
		t.Fatalf("clearing search did not restore %+v -> %+v", restore, got)
	}

	typePickerText(app, "alpha")
	if got := selectedPickerKey(t, app); got.ID != a.ThreadID {
		t.Fatalf("second search selected %+v", got)
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.picker != nil {
		t.Fatal("enter did not close the picker")
	}
}

func TestPickerSearchSelectionHandlesCandidateDisappearance(t *testing.T) {
	now := time.Now()
	a := pickerSession(domain.ProviderCodex, "aaaaaaaa-1111-4000-8000-000000000001", "alpha", "/home/u/alpha", now.Add(-time.Minute))
	b := pickerSession(domain.ProviderClaude, "bbbbbbbb-2222-4000-8000-000000000002", "bravo", "/home/u/bravo", now.Add(-2*time.Minute))
	c := pickerSession(domain.ProviderClaude, "cccccccc-3333-4000-8000-000000000003", "charlie", "/home/u/charlie", now.Add(-3*time.Minute))
	app := pickerTestApp(t, 100, 30, []domain.SessionInfo{a, b, c})
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	typePickerText(app, "a")
	if got := selectedPickerKey(t, app); got.ID != b.ThreadID {
		t.Fatalf("setup selected %+v", got)
	}

	app.Update(sessionsMsg{sessions: []domain.SessionInfo{a, c}})
	if got := selectedPickerKey(t, app); got.ID != c.ThreadID {
		t.Fatalf("candidate disappearance did not fall back: %+v", got)
	}

	app.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := selectedPickerKey(t, app); got.ID != c.ThreadID {
		t.Fatalf("clearing search changed the fallback: %+v", got)
	}
}

func TestPickerDistinguishesSameRawIDAcrossClaudeProjects(t *testing.T) {
	id := "99999999-0000-4000-8000-000000000009"
	cwd := "/home/u/shared"
	updated := time.Now().Add(-time.Minute)
	mode := "claude-jsonl"
	first := domain.SessionInfo{
		Provider: domain.ProviderClaude, ThreadID: id,
		Path: "/root/.claude/projects/-home-u-project-a/" + id + ".jsonl",
		Cwd:  &cwd, Title: "same work", HistoryMode: &mode,
		UpdatedAt: float64(updated.UnixNano()) / 1e9,
	}
	second := first
	second.Path = "/root/.claude/projects/-home-u-project-b/" + id + ".jsonl"
	sessions := []domain.SessionInfo{first, second}
	rows := buildPickerRows(sessions, time.Now())
	if rows[0].extra == "" || rows[0].extra == rows[1].extra {
		t.Fatalf("path discriminators do not differ: %#v", rows)
	}

	app := pickerTestApp(t, 40, 24, sessions)
	assertPickerLinesFit(t, app, 40)
	content := app.View().Content
	if !strings.Contains(content, "project-a") || !strings.Contains(content, "project-b") {
		t.Fatalf("same raw ID rows are not distinguishable: %q", content)
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	selected, ok := app.selectedPickerSession()
	if !ok || selected.Path != second.Path {
		t.Fatalf("selection did not follow SessionKey.Path: %+v %v", selected, ok)
	}
}

func TestPickerDetailScrollReachesAllFields(t *testing.T) {
	id := "7579c4bd-0000-4000-8000-000000000001"
	cwd := "/home/u/project-a"
	updated := time.Date(2026, 11, 28, 9, 30, 0, 0, time.Local)
	session := pickerSession(domain.ProviderClaude, id, "PowerShellの配置確認", cwd, updated)
	app := pickerTestApp(t, 40, 8, []domain.SessionInfo{session})
	app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})

	targets := []string{
		id,
		cwd,
		session.Path,
		"形式:claude-jsonl",
		"最終更新:" + strings.ReplaceAll(updated.Format("2006-01-02 15:04:05"), " ", ""),
	}
	seen := map[string]bool{}
	for step := 0; step < 30; step++ {
		compact := strings.Join(strings.Fields(app.View().Content), "")
		for _, target := range targets {
			if strings.Contains(compact, target) {
				seen[target] = true
			}
		}
		app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	for _, target := range targets {
		if !seen[target] {
			t.Fatalf("detail scroll never showed %q (top=%d)", target, app.picker.detailTop)
		}
	}
	if app.picker.detailTop == 0 {
		t.Fatal("detail did not scroll")
	}
	assertPickerLinesFit(t, app, 40)
}
