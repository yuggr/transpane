// Package tui is the Bubble Tea presentation layer.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/monitor"
	"github.com/yuggr/transpane/internal/transcript"
)

type resetMsg struct {
	session *domain.SessionInfo
	views   []monitor.View
}
type (
	addedMsg     struct{ view monitor.View }
	prependedMsg struct{ views []monitor.View }
	updatedMsg   struct{ view monitor.View }
	statusMsg    struct{ status monitor.Status }
	sessionsMsg  struct{ sessions []domain.SessionInfo }
	tickMsg      time.Time
)

type messageRange struct {
	start int
	end   int
	key   domain.MessageKey
}

type pickerState struct {
	selected int
	top      int
	key      domain.SessionKey
}

// App is the viewer model. Monitor events arrive as Bubble Tea messages.
type App struct {
	ctx      context.Context
	config   config.Config
	initial  *domain.SessionInfo
	cwd      string
	source   *domain.Provider
	monitor  *monitor.Monitor
	messages chan tea.Msg

	width         int
	height        int
	viewport      viewport.Model
	views         []monitor.View
	index         map[domain.MessageKey]int
	ranges        []messageRange
	status        monitor.Status
	session       *domain.SessionInfo
	originalMode  bool
	autoScroll    bool
	picker        *pickerState
	sessions      []domain.SessionInfo
	emptyNote     string
	pendingPicker bool
}

// New creates a viewer. The monitor is created in Init so tests can drive
// Update without I/O.
func New(ctx context.Context, cfg config.Config, initial *domain.SessionInfo, cwd string, source *domain.Provider) *App {
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(22))
	vp.MouseWheelEnabled = true
	app := &App{
		ctx:        ctx,
		config:     cfg,
		initial:    initial,
		cwd:        cwd,
		source:     source,
		messages:   make(chan tea.Msg, 256),
		width:      80,
		height:     24,
		viewport:   vp,
		index:      map[domain.MessageKey]int{},
		autoScroll: true,
	}
	return app
}

// Run starts the TUI and restores the terminal when it exits.
func Run(ctx context.Context, cfg config.Config, initial *domain.SessionInfo, cwd string, source *domain.Provider) error {
	viewCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	app := New(viewCtx, cfg, initial, cwd, source)
	program := tea.NewProgram(app, tea.WithContext(viewCtx))
	_, err := program.Run()
	// Stop event delivery before closing the monitor so a blocked listener
	// send can never prevent shutdown.
	cancel()
	if app.monitor != nil {
		_ = app.monitor.Close()
	}
	return err
}

func (a *App) Init() tea.Cmd {
	commands := []tea.Cmd{a.waitForEvent()}
	if a.monitor == nil {
		a.monitor = monitor.New(a.ctx, a.config, a, monitor.Options{})
	}
	if a.initial != nil {
		initial := a.initial
		commands = append(commands, func() tea.Msg {
			a.monitor.Start(initial)
			return nil
		})
	} else {
		commands = append(commands, a.discoverCmd(), a.tickCmd())
	}
	return tea.Batch(commands...)
}

func (a *App) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case nil:
		return a, nil
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.relayout()
		return a, nil
	case resetMsg:
		a.session = msg.session
		a.setViews(msg.views)
		a.rebuild(true)
		return a, a.waitForEvent()
	case addedMsg:
		a.upsert(msg.view)
		a.rebuild(true)
		return a, a.waitForEvent()
	case prependedMsg:
		a.prepend(msg.views)
		a.rebuild(true)
		return a, a.waitForEvent()
	case updatedMsg:
		a.upsert(msg.view)
		a.rebuild(true)
		return a, a.waitForEvent()
	case statusMsg:
		a.status = msg.status
		return a, a.waitForEvent()
	case sessionsMsg:
		a.sessions = msg.sessions
		a.emptyNote = ""
		openPicker := a.pendingPicker || (a.picker == nil && a.session == nil)
		a.pendingPicker = false
		if a.picker == nil && openPicker {
			if len(a.sessions) == 0 {
				a.emptyNote = "セッションが見つかりません。s で再検索できます。"
			} else {
				state := &pickerState{}
				state.key = a.sessions[0].Key()
				a.picker = state
			}
		} else if a.picker != nil {
			a.picker.restore(a.sessions)
			a.adjustPicker()
		}
		return a, nil
	case tickMsg:
		if a.session == nil || a.picker != nil {
			return a, tea.Batch(a.discoverCmd(), a.tickCmd())
		}
		return a, nil
	case tea.KeyPressMsg:
		return a.handleKey(msg)
	case tea.MouseMsg:
		updated, cmd := a.viewport.Update(msg)
		a.viewport = updated
		a.autoScroll = a.viewport.AtBottom()
		return a, cmd
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" || key == "q" {
		return a, tea.Quit
	}
	if a.picker != nil {
		return a.handlePickerKey(key)
	}
	switch key {
	case "s":
		a.pendingPicker = true
		return a, a.discoverCmd()
	case "o":
		a.originalMode = !a.originalMode
		a.rebuild(true)
	case "r":
		if a.monitor != nil {
			a.monitor.Retry()
		}
	case "h":
		if a.monitor != nil {
			a.monitor.AddHistory()
		}
	case "end":
		a.autoScroll = true
		a.viewport.GotoBottom()
	default:
		updated, cmd := a.viewport.Update(msg)
		a.viewport = updated
		a.autoScroll = a.viewport.AtBottom()
		return a, cmd
	}
	return a, nil
}

func (a *App) handlePickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		a.picker = nil
	case "s":
		return a, a.discoverCmd()
	case "up", "k":
		if a.picker.selected > 0 {
			a.picker.selected--
			a.picker.key = a.sessions[a.picker.selected].Key()
		}
		a.adjustPicker()
	case "down", "j":
		if a.picker.selected+1 < len(a.sessions) {
			a.picker.selected++
			a.picker.key = a.sessions[a.picker.selected].Key()
		}
		a.adjustPicker()
	case "enter":
		if a.picker.selected < len(a.sessions) {
			selected := a.sessions[a.picker.selected]
			a.picker = nil
			a.autoScroll = true
			if a.monitor != nil {
				return a, func() tea.Msg {
					a.monitor.Switch(selected)
					return nil
				}
			}
		}
	}
	return a, nil
}

// restore keeps the selected row's SessionKey when the candidate list refreshes.
func (p *pickerState) restore(sessions []domain.SessionInfo) {
	if len(sessions) == 0 {
		p.selected = 0
		p.top = 0
		return
	}
	for index, session := range sessions {
		if session.Key() == p.key {
			p.selected = index
			return
		}
	}
	if p.selected >= len(sessions) {
		p.selected = len(sessions) - 1
	}
	if p.selected < 0 {
		p.selected = 0
	}
	p.key = sessions[p.selected].Key()
	if p.top > p.selected {
		p.top = p.selected
	}
}

func (a *App) adjustPicker() {
	height := a.height - 4
	if height < 1 {
		height = 1
	}
	if a.picker.selected < a.picker.top {
		a.picker.top = a.picker.selected
	}
	if a.picker.selected >= a.picker.top+height {
		a.picker.top = a.picker.selected - height + 1
	}
}

func (a *App) discoverCmd() tea.Cmd {
	cfg := a.config
	source := a.source
	cwd := a.cwd
	return func() tea.Msg {
		sessions := transcript.DiscoverAll(cfg.SessionsRoot, cfg.ClaudeProjectsRoot, source)
		if cwd != "" {
			sessions = transcript.SessionsForCwd(sessions, cwd)
		}
		return sessionsMsg{sessions: sessions}
	}
}

func (a *App) tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func (a *App) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-a.messages:
			return msg
		case <-a.ctx.Done():
			return nil
		}
	}
}

// Monitor listener implementation.

func (a *App) Reset(session *domain.SessionInfo, views []monitor.View) {
	a.send(resetMsg{session: session, views: views})
}
func (a *App) Added(view monitor.View)        { a.send(addedMsg{view: view}) }
func (a *App) Prepended(views []monitor.View) { a.send(prependedMsg{views: views}) }
func (a *App) Updated(view monitor.View)      { a.send(updatedMsg{view: view}) }
func (a *App) Status(status monitor.Status)   { a.send(statusMsg{status: status}) }

func (a *App) send(message tea.Msg) {
	select {
	case a.messages <- message:
	case <-a.ctx.Done():
	}
}

func (a *App) setViews(views []monitor.View) {
	a.views = append([]monitor.View(nil), views...)
	a.reindex()
}

func (a *App) upsert(view monitor.View) {
	key := domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID}
	if index, ok := a.index[key]; ok {
		a.views[index] = view
		return
	}
	a.views = append(a.views, view)
	a.index[key] = len(a.views) - 1
}

func (a *App) prepend(views []monitor.View) {
	existing := map[domain.MessageKey]bool{}
	for _, view := range views {
		existing[domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID}] = true
	}
	kept := make([]monitor.View, 0, len(a.views))
	for _, view := range a.views {
		if existing[domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID}] {
			continue
		}
		kept = append(kept, view)
	}
	a.views = append(append([]monitor.View(nil), views...), kept...)
	a.reindex()
}

func (a *App) reindex() {
	a.index = make(map[domain.MessageKey]int, len(a.views))
	for index, view := range a.views {
		a.index[domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID}] = index
	}
}

func (a *App) relayout() {
	height := a.height - 2
	if height < 1 {
		height = 1
	}
	a.viewport.SetWidth(a.width)
	a.viewport.SetHeight(height)
	a.rebuild(true)
}

func (a *App) rebuild(preserve bool) {
	key, inner, anchored := a.topAnchor()
	a.viewport.SetContent(a.buildContent())
	if a.autoScroll {
		a.viewport.GotoBottom()
		return
	}
	if preserve && anchored {
		a.restoreAnchor(key, inner)
	}
}

func (a *App) topAnchor() (domain.MessageKey, int, bool) {
	if len(a.ranges) == 0 {
		return domain.MessageKey{}, 0, false
	}
	offset := a.viewport.YOffset()
	for index := len(a.ranges) - 1; index >= 0; index-- {
		item := a.ranges[index]
		if item.start <= offset {
			return item.key, offset - item.start, true
		}
	}
	return a.ranges[0].key, 0, true
}

func (a *App) restoreAnchor(key domain.MessageKey, inner int) {
	for _, item := range a.ranges {
		if item.key == key {
			offset := item.start + inner
			if offset > item.end {
				offset = item.end
			}
			a.viewport.SetYOffset(offset)
			return
		}
	}
}

func (a *App) buildContent() string {
	lines := []string{}
	a.ranges = nil
	for index, view := range a.views {
		if index > 0 {
			lines = append(lines, "")
		}
		start := len(lines)
		lines = append(lines, a.header(view))
		body := view.DisplayText()
		if a.originalMode {
			body = view.Message.Original
		}
		body = SanitizeForDisplay(body)
		if body == "" {
			lines = append(lines, "")
		} else {
			lines = append(lines, strings.Split(body, "\n")...)
		}
		a.ranges = append(a.ranges, messageRange{
			start: start,
			end:   len(lines) - 1,
			key:   domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID},
		})
	}
	return strings.Join(lines, "\n")
}

func (a *App) header(view monitor.View) string {
	parts := []string{domain.FormatTimestamp(view.Message.Timestamp), view.Message.Kind.Label()}
	if a.originalMode {
		parts = append(parts, "[原文]")
	} else {
		if label := view.State.Label(); label != "" {
			parts = append(parts, "["+label+"]")
		}
	}
	if len(view.ErrorLabels) > 0 {
		labels := make([]string, 0, len(view.ErrorLabels))
		for label := range view.ErrorLabels {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		parts = append(parts, "("+strings.Join(labels, "/")+")")
	}
	return strings.Join(parts, "  ")
}

func (a *App) statusText() string {
	text := ""
	if a.status.Session != "" {
		text = a.status.Session + " | "
	}
	text += "PLaMo: " + a.status.Server
	if a.status.Error != "" {
		text += " (" + a.status.Error + ")"
	}
	text += fmt.Sprintf(" | 待ち: %d | 最終更新: %s", a.status.Pending, a.status.LastUpdate)
	if a.status.Note != "" {
		text += " | " + a.status.Note
	}
	if a.status.CacheNote != "" {
		text += " | キャッシュ: " + a.status.CacheNote
	}
	if a.session == nil && a.emptyNote != "" {
		text += " | " + a.emptyNote
	}
	return SanitizeForDisplay(text)
}

func (a *App) View() tea.View {
	var content string
	if a.picker != nil {
		content = a.pickerView()
	} else {
		content = a.statusText() + "\n" + a.viewport.View() + "\n" + a.footer()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (a *App) footerView() string {
	return "s: セッション  o: 原文/翻訳  r: 再試行  h: 履歴追加  End: 最新  q: 終了"
}

func (a *App) footer() string { return a.footerView() }

func (a *App) pickerView() string {
	lines := []string{
		"セッションを選択（取得元 / ID / cwd / 最終更新 / 形式）",
		a.statusText(),
	}
	height := a.height - 4
	if height < 1 {
		height = 1
	}
	if len(a.sessions) == 0 {
		lines = append(lines, "セッションが見つかりません。s で再検索できます。")
	}
	end := a.picker.top + height
	if end > len(a.sessions) {
		end = len(a.sessions)
	}
	for index := a.picker.top; index < end; index++ {
		session := a.sessions[index]
		cwd := "cwd不明"
		if session.Cwd != nil {
			cwd = *session.Cwd
		}
		updated := time.Unix(0, int64(session.UpdatedAt*1e9)).Format("2006-01-02 15:04:05")
		row := fmt.Sprintf("%s  %s  %s  %s  %s", session.Provider.Label(), session.ThreadID, cwd, updated, session.ModeLabel())
		if session.OriginLabel() != "" {
			row += "  " + session.OriginLabel()
		}
		row = SanitizeForDisplay(row)
		if index == a.picker.selected {
			row = "> " + row
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
	}
	lines = append(lines, "↑↓: 選択  Enter: 決定  Esc: 閉じる  s: 再検索")
	return strings.Join(lines, "\n")
}
