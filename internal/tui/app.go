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
	"github.com/charmbracelet/x/ansi"

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
	vp.SoftWrap = false
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
		a.applyLayout()
		if a.autoScroll {
			a.viewport.GotoBottom()
		}
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

const (
	pickerTitleText  = "セッションを選択（取得元 / ID / cwd / 最終更新 / 形式）"
	pickerFooterText = "↑↓: 選択  Enter: 決定  Esc: 閉じる  s: 再検索"
)

// pickerLayout budgets the screen for one selected row and the key guide first,
// then title/status. This keeps the selected row visible on very short screens.
func (a *App) pickerLayout() (title, status, footer []string, rows int, separator bool) {
	width := a.contentWidth()
	title = wrapDisplayLine(pickerTitleText, width)
	status = wrapDisplayLine(a.statusText(), width)
	footer = wrapDisplayLine(pickerFooterText, width)
	height := a.height
	if height < 1 {
		height = 1
	}
	budget := height - 1 // reserve one list row
	if budget < 0 {
		budget = 0
	}
	if len(footer) > budget {
		footer = footer[:budget]
	}
	budget -= len(footer)
	if (len(title) > 0 || len(status) > 0) && budget > 0 {
		separator = true
		budget--
	}
	if len(title) > budget {
		title = title[:budget]
	}
	budget -= len(title)
	if len(status) > budget {
		status = status[:budget]
	}
	budget -= len(status)
	rows = 1 + budget
	if separator && len(title) == 0 && len(status) == 0 {
		separator = false
		rows++
	}
	return title, status, footer, rows, separator
}

func (a *App) pickerRows() int {
	_, _, _, rows, _ := a.pickerLayout()
	return rows
}

func (a *App) adjustPicker() {
	height := a.pickerRows()
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
	a.applyLayout()
	a.rebuild(true)
}

func (a *App) contentWidth() int {
	width := a.width
	if width <= 0 {
		width = a.viewport.Width()
	}
	if width <= 0 {
		width = 80
	}
	return width
}

const displayTabWidth = 4

func expandTabsForDisplay(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var builder strings.Builder
	column := 0
	for _, r := range line {
		if r == '\t' {
			spaces := displayTabWidth - column%displayTabWidth
			builder.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		builder.WriteRune(r)
		column += ansi.StringWidth(string(r))
	}
	return builder.String()
}

func wrapDisplayLine(line string, width int) []string {
	if width < 1 {
		width = 1
	}
	line = expandTabsForDisplay(line)
	wrapped := ansi.Wrap(line, width, " ")
	if wrapped == "" {
		return []string{""}
	}
	return strings.Split(wrapped, "\n")
}

func wrapDisplayText(text string, width int) []string {
	physical := strings.Split(text, "\n")
	lines := []string{}
	for _, line := range physical {
		lines = append(lines, wrapDisplayLine(line, width)...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (a *App) layoutLines() (status, footer []string) {
	width := a.contentWidth()
	status = wrapDisplayLine(a.statusText(), width)
	footer = wrapDisplayLine(a.footerView(), width)
	maxAux := a.height - 1
	if maxAux < 0 {
		maxAux = 0
	}
	if len(status)+len(footer) > maxAux {
		if len(status) >= maxAux {
			status = status[:maxAux]
			footer = nil
		} else {
			footer = footer[:maxAux-len(status)]
		}
	}
	return status, footer
}

func (a *App) applyLayout() {
	status, footer := a.layoutLines()
	height := a.height - len(status) - len(footer)
	if height < 1 {
		height = 1
	}
	a.viewport.SetWidth(a.contentWidth())
	a.viewport.SetHeight(height)
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
	width := a.contentWidth()
	lines := []string{}
	a.ranges = nil
	for index, view := range a.views {
		if index > 0 {
			lines = append(lines, "")
		}
		start := len(lines)
		lines = append(lines, wrapDisplayLine(a.header(view), width)...)
		body := view.DisplayText()
		if a.originalMode {
			body = view.Message.Original
		}
		body = SanitizeForDisplay(body)
		lines = append(lines, wrapDisplayText(body, width)...)
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
		a.applyLayout()
		status, footer := a.layoutLines()
		parts := []string{}
		if len(status) > 0 {
			parts = append(parts, strings.Join(status, "\n"))
		}
		parts = append(parts, a.viewport.View())
		if len(footer) > 0 {
			parts = append(parts, strings.Join(footer, "\n"))
		}
		content = strings.Join(parts, "\n")
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (a *App) footerView() string {
	return "s: セッション  o: 原文/翻訳  r: 再試行  h: 履歴追加  End: 最新  q: 終了"
}

func (a *App) pickerView() string {
	a.adjustPicker()
	width := a.contentWidth()
	title, status, footer, rows, separator := a.pickerLayout()
	lines := append(append([]string{}, title...), status...)
	if separator {
		lines = append(lines, "")
	}
	if len(a.sessions) == 0 {
		message := wrapDisplayLine("セッションが見つかりません。s で再検索できます。", width)
		if len(message) > rows {
			message = message[:rows]
		}
		lines = append(lines, message...)
	}
	end := a.picker.top + rows
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
		row = ansi.Truncate(SanitizeForDisplay(row), width, "…")
		if index == a.picker.selected {
			row = "> " + row
		} else {
			row = "  " + row
		}
		lines = append(lines, ansi.Truncate(row, width, "…"))
	}
	lines = append(lines, footer...)
	if a.height > 0 && len(lines) > a.height {
		lines = lines[:a.height]
	}
	return strings.Join(lines, "\n")
}
