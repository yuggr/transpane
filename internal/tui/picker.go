package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yuggr/transpane/internal/domain"
)

type pickerState struct {
	selected    int
	top         int
	selectedKey domain.SessionKey
	restoreKey  domain.SessionKey
	query       string
	searching   bool
	detail      bool
	detailTop   int
}

const (
	pickerHeading       = "セッションを選択"
	pickerDetailHeading = "セッション詳細"
	pickerUntitled      = "タイトル未取得"
	pickerNoCwd         = "cwd不明"
)

func (a *App) pickerTitleText() string {
	if a.picker == nil {
		return pickerHeading
	}
	if a.picker.detail {
		return pickerDetailHeading
	}
	query := SanitizeForDisplay(strings.TrimSpace(a.picker.query))
	if query == "" && !a.picker.searching {
		return pickerHeading
	}
	if query == "" {
		return "検索: "
	}
	return fmt.Sprintf("検索: %s（%d件）", query, len(a.pickerSessions()))
}

func (a *App) pickerFooterText() string {
	if a.picker == nil {
		return ""
	}
	if a.picker.detail {
		return "↑↓:スクロール  d:一覧  Enter:決定  Esc:閉じる"
	}
	if a.picker.searching {
		return "文字:検索  Backspace:削除  ↑↓:選択  Enter:決定  Esc:閉じる"
	}
	return "↑↓:選択  Enter:決定  /:検索  d:詳細  Esc:閉じる  s:再検索"
}

// pickerLayout budgets the screen for the footer first, then the title and
// status, always leaving room for one two-line session row when possible.
func (a *App) pickerLayout() (title, status, footer []string, listLines int, separator bool) {
	width := a.contentWidth()
	title = wrapDisplayLine(a.pickerTitleText(), width)
	if a.picker == nil || !a.picker.detail {
		status = wrapDisplayLine(a.statusText(), width)
	}
	footer = wrapDisplayLine(a.pickerFooterText(), width)
	height := a.height
	if height < 1 {
		height = 1
	}
	if len(footer) > height {
		footer = footer[:height]
	}
	remaining := height - len(footer)
	if remaining > 0 && (len(title) > 0 || len(status) > 0) {
		separator = true
		remaining--
	}
	leave := 2
	if height < 2 {
		leave = 1
	}
	aux := remaining - leave
	if aux < 0 {
		aux = 0
	}
	if len(title) > aux {
		title = title[:aux]
	}
	aux -= len(title)
	if len(status) > aux {
		status = status[:aux]
	}
	if separator && len(title) == 0 && len(status) == 0 {
		separator = false
		remaining++
	}
	listLines = remaining - len(title) - len(status)
	if listLines < 0 {
		listLines = 0
	}
	return title, status, footer, listLines, separator
}

func (a *App) pickerRows() int {
	_, _, _, rows, _ := a.pickerLayout()
	return rows
}

func (a *App) pickerSessions() []domain.SessionInfo {
	if a.picker == nil || strings.TrimSpace(a.picker.query) == "" {
		return a.sessions
	}
	return filterSessions(a.sessions, a.picker.query)
}

func filterSessions(sessions []domain.SessionInfo, query string) []domain.SessionInfo {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return sessions
	}
	filtered := make([]domain.SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if sessionMatches(session, needle) {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

func sessionMatches(session domain.SessionInfo, needle string) bool {
	fields := []string{
		session.Title,
		session.Provider.Label(),
		session.Provider.ShortLabel(),
		session.ThreadID,
		session.DisplayID(),
		session.ModeLabel(),
		projectBase(session.Cwd),
	}
	if session.Cwd != nil {
		fields = append(fields, *session.Cwd)
	}
	for _, field := range fields {
		if field != "" && strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func (a *App) adjustPicker() {
	if a.picker == nil {
		return
	}
	rows := a.pickerRows()
	visible := rows / 2
	if visible < 1 {
		visible = 1
	}
	sessions := a.pickerSessions()
	if len(sessions) == 0 {
		a.picker.selected = 0
		a.picker.top = 0
		return
	}
	if a.picker.selected >= len(sessions) {
		a.picker.selected = len(sessions) - 1
	}
	if a.picker.selected < 0 {
		a.picker.selected = 0
	}
	if a.picker.selected < a.picker.top {
		a.picker.top = a.picker.selected
	}
	if a.picker.selected >= a.picker.top+visible {
		a.picker.top = a.picker.selected - visible + 1
	}
	maxTop := len(sessions) - visible
	if maxTop < 0 {
		maxTop = 0
	}
	if a.picker.top > maxTop {
		a.picker.top = maxTop
	}
	if a.picker.top < 0 {
		a.picker.top = 0
	}
}

// syncPickerSelection keeps the currently displayed SessionKey across
// refresh, search and resize. The key is the source of truth; the index is
// only a position in the current filtered view.
func (a *App) syncPickerSelection() {
	if a.picker == nil {
		return
	}
	sessions := a.pickerSessions()
	if len(sessions) == 0 {
		a.picker.selected = 0
		a.picker.top = 0
		return
	}
	if a.picker.selectedKey == (domain.SessionKey{}) {
		a.picker.selectedKey = sessions[0].Key()
	}
	for index, session := range sessions {
		if session.Key() == a.picker.selectedKey {
			a.picker.selected = index
			a.adjustPicker()
			return
		}
	}
	if a.picker.selected >= len(sessions) {
		a.picker.selected = len(sessions) - 1
	}
	if a.picker.selected < 0 {
		a.picker.selected = 0
	}
	a.picker.selectedKey = sessions[a.picker.selected].Key()
	a.adjustPicker()
}

// restoreSearchSelection returns to the session that was selected before the
// search began. It only runs when the query becomes empty.
func (a *App) restoreSearchSelection() {
	p := a.picker
	if p == nil {
		return
	}
	if p.restoreKey != (domain.SessionKey{}) {
		for index, session := range a.sessions {
			if session.Key() == p.restoreKey {
				p.selectedKey = p.restoreKey
				p.selected = index
				p.restoreKey = domain.SessionKey{}
				p.top = 0
				a.adjustPicker()
				return
			}
		}
		p.restoreKey = domain.SessionKey{}
	}
	a.syncPickerSelection()
}

func (a *App) setPickerQuery(query string) {
	if a.picker == nil {
		return
	}
	wasEmpty := strings.TrimSpace(a.picker.query) == ""
	if wasEmpty && strings.TrimSpace(query) != "" && a.picker.searching && a.picker.restoreKey == (domain.SessionKey{}) {
		a.picker.restoreKey = a.picker.selectedKey
	}
	a.picker.query = query
	if strings.TrimSpace(query) == "" {
		a.restoreSearchSelection()
		return
	}
	a.syncPickerSelection()
}

func (a *App) startPickerSearch() {
	if a.picker == nil {
		return
	}
	if a.picker.query == "" {
		a.picker.restoreKey = a.picker.selectedKey
	}
	a.picker.searching = true
}

func (a *App) movePicker(delta int) {
	sessions := a.pickerSessions()
	if len(sessions) == 0 {
		return
	}
	next := a.picker.selected + delta
	if next < 0 || next >= len(sessions) {
		return
	}
	a.picker.selected = next
	a.picker.selectedKey = sessions[next].Key()
	a.adjustPicker()
}

func (a *App) moveDetail(delta int) {
	if a.picker == nil {
		return
	}
	rows := a.pickerRows()
	if rows < 1 {
		return
	}
	detail := a.pickerDetailLines(a.contentWidth())
	maxTop := len(detail) - rows
	if maxTop < 0 {
		maxTop = 0
	}
	a.picker.detailTop += delta
	if a.picker.detailTop > maxTop {
		a.picker.detailTop = maxTop
	}
	if a.picker.detailTop < 0 {
		a.picker.detailTop = 0
	}
}

func (a *App) selectedPickerSession() (domain.SessionInfo, bool) {
	if a.picker == nil {
		return domain.SessionInfo{}, false
	}
	sessions := a.pickerSessions()
	if a.picker.selected < 0 || a.picker.selected >= len(sessions) {
		return domain.SessionInfo{}, false
	}
	return sessions[a.picker.selected], true
}

func (a *App) closePicker() {
	a.picker = nil
	a.pickerDismissed = true
}

func (a *App) selectPickerSession() (tea.Model, tea.Cmd) {
	selected, ok := a.selectedPickerSession()
	if !ok {
		return a, nil
	}
	a.closePicker()
	a.autoScroll = true
	if a.monitor != nil {
		return a, func() tea.Msg {
			a.monitor.Switch(selected)
			return nil
		}
	}
	return a, nil
}

func (a *App) handlePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := a.picker
	if p == nil {
		return a, nil
	}
	key := msg.String()
	if p.searching {
		switch key {
		case "esc":
			a.closePicker()
		case "enter":
			return a.selectPickerSession()
		case "up":
			a.movePicker(-1)
		case "down":
			a.movePicker(1)
		case "/":
			p.searching = false
		case "backspace":
			if p.query != "" {
				a.setPickerQuery(removeLastRune(p.query))
			}
		case "ctrl+u":
			a.setPickerQuery("")
		default:
			if text, ok := printableKeyText(msg); ok {
				a.setPickerQuery(p.query + text)
			}
		}
		a.adjustPicker()
		return a, nil
	}
	if p.detail {
		switch key {
		case "esc":
			a.closePicker()
		case "up", "k":
			a.moveDetail(-1)
		case "down", "j":
			a.moveDetail(1)
		case "d":
			p.detail = false
			p.detailTop = 0
			a.adjustPicker()
		case "enter":
			return a.selectPickerSession()
		case "s":
			return a, a.discoverCmd()
		}
		return a, nil
	}
	switch key {
	case "esc":
		a.closePicker()
	case "s":
		return a, a.discoverCmd()
	case "/":
		a.startPickerSearch()
	case "d":
		p.detail = true
		p.detailTop = 0
		a.adjustPicker()
	case "up", "k":
		a.movePicker(-1)
	case "down", "j":
		a.movePicker(1)
	case "enter":
		return a.selectPickerSession()
	}
	return a, nil
}

func printableKeyText(msg tea.KeyPressMsg) (string, bool) {
	if msg.Mod.Contains(tea.ModCtrl) || msg.Mod.Contains(tea.ModAlt) {
		return "", false
	}
	if msg.Text != "" {
		return msg.Text, true
	}
	if msg.Code >= 32 && msg.Code != 127 {
		return string(msg.Code), true
	}
	return "", false
}

func removeLastRune(text string) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

func (a *App) pickerView() string {
	a.adjustPicker()
	width := a.contentWidth()
	title, status, footer, rows, separator := a.pickerLayout()
	lines := append(append([]string{}, title...), status...)
	if separator {
		lines = append(lines, "")
	}
	if a.picker.detail {
		detail := a.pickerDetailLines(width)
		maxTop := len(detail) - rows
		if maxTop < 0 {
			maxTop = 0
		}
		if a.picker.detailTop > maxTop {
			a.picker.detailTop = maxTop
		}
		if a.picker.detailTop < 0 {
			a.picker.detailTop = 0
		}
		end := a.picker.detailTop + rows
		if end > len(detail) {
			end = len(detail)
		}
		lines = append(lines, detail[a.picker.detailTop:end]...)
	} else {
		sessions := a.pickerSessions()
		if len(sessions) == 0 {
			message := "セッションが見つかりません。s で再検索できます。"
			if strings.TrimSpace(a.picker.query) != "" {
				message = "該当するセッションがありません。"
			}
			lines = append(lines, wrapDisplayLine(message, width)...)
		} else {
			rowData := buildPickerRows(sessions, time.Now())
			count := rows / 2
			if count < 1 {
				count = 1
			}
			start := a.picker.top
			if start > len(rowData) {
				start = len(rowData)
			}
			end := start + count
			if end > len(rowData) {
				end = len(rowData)
			}
			lineBudget := rows
			for index := start; index < end && lineBudget > 0; index++ {
				rowLines := renderPickerRow(rowData[index], index == a.picker.selected, width, lineBudget)
				lines = append(lines, rowLines...)
				lineBudget -= len(rowLines)
			}
		}
	}
	lines = append(lines, footer...)
	if a.height > 0 && len(lines) > a.height {
		lines = lines[:a.height]
	}
	return strings.Join(lines, "\n")
}

type pickerRow struct {
	session domain.SessionInfo
	title   string
	project string
	updated string
	id      string
	extra   string
}

func buildPickerRows(sessions []domain.SessionInfo, now time.Time) []pickerRow {
	projects := projectLabels(sessions)
	ids := idLabels(sessions)
	paths := pathDiscriminators(sessions)
	rows := make([]pickerRow, len(sessions))
	for index, session := range sessions {
		rows[index] = pickerRow{
			session: session,
			title:   strings.TrimSpace(session.Title),
			project: projects[index],
			updated: formatRelativeTime(session.UpdatedAt, now),
			id:      ids[index],
			extra:   paths[index],
		}
	}
	return rows
}

func renderPickerRow(row pickerRow, selected bool, width, lineBudget int) []string {
	marker := "  "
	if selected {
		marker = "> "
	}
	title := row.title
	if title == "" {
		title = pickerUntitled
	}
	title = strings.ReplaceAll(strings.ReplaceAll(title, "\r", " "), "\n", " ")
	line1 := marker + "[" + row.session.Provider.ShortLabel() + "] " + title
	line1 = ansi.Truncate(SanitizeForDisplay(line1), width, "…")
	if lineBudget < 2 {
		return []string{line1}
	}
	line2 := "  " + fitSecondLine(row.project, row.updated, row.id, row.extra, maxInt(width-2, 1))
	line2 = ansi.Truncate(SanitizeForDisplay(line2), width, "…")
	return []string{line1, line2}
}

// fitSecondLine keeps the update time whenever it can, then the project, then
// the ID. A path discriminator for a duplicated raw ID is promoted so a narrow
// view can still show why two otherwise identical rows differ.
func fitSecondLine(project, updated, id, extra string, width int) string {
	combined := id
	if extra != "" {
		if combined == "" {
			combined = extra
		} else {
			combined += "·" + extra
		}
	}
	candidates := [][]string{
		{project, updated, id},
		{project, updated},
		{updated, id},
		{updated},
	}
	if extra != "" {
		candidates = [][]string{
			{project, updated, id, extra},
			{project, updated, combined},
			{updated, id, extra},
			{updated, combined},
			{project, updated, extra},
			{updated, extra},
			{project, updated},
			{updated, id},
			{combined},
			{updated},
			{extra},
			{id},
		}
	}
	for _, parts := range candidates {
		cleaned := parts[:0]
		for _, part := range parts {
			if part != "" {
				cleaned = append(cleaned, part)
			}
		}
		if len(cleaned) == 0 {
			continue
		}
		joined := strings.Join(cleaned, " | ")
		if ansi.StringWidth(joined) <= width {
			return joined
		}
	}
	for _, part := range []string{combined, updated, project, id, extra} {
		if part != "" {
			return ansi.Truncate(part, width, "…")
		}
	}
	return ""
}

func (a *App) pickerDetailLines(width int) []string {
	session, ok := a.selectedPickerSession()
	if !ok {
		return wrapDisplayLine("セッションがありません", width)
	}
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = pickerUntitled
	}
	cwd := "不明"
	if session.Cwd != nil {
		cwd = *session.Cwd
	}
	fields := []string{
		"タイトル: " + title,
		"取得元: " + session.Provider.Label(),
		"ID: " + session.ThreadID,
		"cwd: " + cwd,
		"ログ: " + session.Path,
		"形式: " + session.ModeLabel(),
		"最終更新: " + formatAbsoluteTime(session.UpdatedAt) + " (" + formatRelativeTime(session.UpdatedAt, time.Now()) + ")",
	}
	if origin := session.OriginLabel(); origin != "" {
		fields = append(fields, "区分: "+origin)
	}
	lines := []string{}
	for _, field := range fields {
		lines = append(lines, wrapDisplayLine(SanitizeForDisplay(field), width)...)
	}
	return lines
}

// projectLabels builds labels per unique cwd, not per session. Sessions that
// share a cwd always share a label; only distinct cwds with the same basename
// grow their parent suffix.
func projectLabels(sessions []domain.SessionInfo) []string {
	type cwdLabel struct {
		cwd   string
		label string
	}
	byCwd := map[string]*cwdLabel{}
	order := []string{}
	for _, session := range sessions {
		if session.Cwd == nil || strings.TrimSpace(*session.Cwd) == "" {
			continue
		}
		key := filepath.Clean(*session.Cwd)
		if _, found := byCwd[key]; found {
			continue
		}
		byCwd[key] = &cwdLabel{cwd: key, label: projectBase(&key)}
		order = append(order, key)
	}
	maxDepth := 0
	for _, key := range order {
		if depth := len(pathComponents(byCwd[key].cwd)); depth > maxDepth {
			maxDepth = depth
		}
	}
	for depth := 2; depth <= maxDepth; depth++ {
		counts := map[string]int{}
		for _, key := range order {
			counts[byCwd[key].label]++
		}
		changed := false
		for _, key := range order {
			item := byCwd[key]
			if counts[item.label] <= 1 {
				continue
			}
			if candidate := projectSuffix(&item.cwd, depth); candidate != item.label {
				item.label = candidate
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	labels := make([]string, len(sessions))
	for index, session := range sessions {
		if session.Cwd == nil || strings.TrimSpace(*session.Cwd) == "" {
			labels[index] = pickerNoCwd
			continue
		}
		labels[index] = byCwd[filepath.Clean(*session.Cwd)].label
	}
	return labels
}

// pathDiscriminators returns a compact path label only for sessions whose
// provider and raw ID collide. The DisplayID is identical for those rows, so
// the path label is what lets the user tell them apart.
func pathDiscriminators(sessions []domain.SessionInfo) []string {
	labels := make([]string, len(sessions))
	groups := map[string][]int{}
	for index, session := range sessions {
		key := string(session.Provider) + "|" + session.ThreadID
		groups[key] = append(groups[key], index)
	}
	for _, indices := range groups {
		if len(indices) <= 1 {
			continue
		}
		maxDepth := 0
		for _, index := range indices {
			directories := pathDirectories(sessions[index].Path)
			if len(directories) > maxDepth {
				maxDepth = len(directories)
			}
			labels[index] = directorySuffix(directories, 1)
		}
		for depth := 2; depth <= maxDepth; depth++ {
			counts := map[string]int{}
			for _, index := range indices {
				counts[labels[index]]++
			}
			changed := false
			for _, index := range indices {
				if counts[labels[index]] <= 1 {
					continue
				}
				directories := pathDirectories(sessions[index].Path)
				if candidate := directorySuffix(directories, depth); candidate != labels[index] {
					labels[index] = candidate
					changed = true
				}
			}
			if !changed {
				break
			}
		}
		counts := map[string]int{}
		for _, index := range indices {
			counts[labels[index]]++
		}
		for _, index := range indices {
			if labels[index] == "" || counts[labels[index]] > 1 {
				labels[index] = shortPathBase(sessions[index].Path)
			}
		}
	}
	return labels
}

func pathDirectories(path string) []string {
	parts := pathComponents(path)
	if len(parts) <= 1 {
		return nil
	}
	return parts[:len(parts)-1]
}

func directorySuffix(directories []string, depth int) string {
	if len(directories) == 0 {
		return ""
	}
	if depth >= len(directories) {
		return strings.Join(directories, "/")
	}
	suffix := strings.Join(directories[len(directories)-depth:], "/")
	if depth == 1 {
		return suffix
	}
	return "…/" + suffix
}

func projectBase(cwd *string) string {
	if cwd == nil || strings.TrimSpace(*cwd) == "" {
		return pickerNoCwd
	}
	parts := pathComponents(*cwd)
	if len(parts) == 0 {
		return pickerNoCwd
	}
	return parts[len(parts)-1]
}

func projectSuffix(cwd *string, depth int) string {
	if cwd == nil || strings.TrimSpace(*cwd) == "" {
		return pickerNoCwd
	}
	parts := pathComponents(*cwd)
	if len(parts) == 0 {
		return pickerNoCwd
	}
	if depth >= len(parts) {
		return *cwd
	}
	return "…/" + strings.Join(parts[len(parts)-depth:], "/")
}

func pathComponents(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
}

func idLabels(sessions []domain.SessionInfo) []string {
	labels := make([]string, len(sessions))
	counts := map[string]int{}
	for index, session := range sessions {
		labels[index] = session.DisplayID()
		counts[labels[index]]++
	}
	for index, session := range sessions {
		if counts[labels[index]] > 1 {
			labels[index] = uniqueIDPrefix(session.ThreadID, sessions)
		}
	}
	return labels
}

func uniqueIDPrefix(id string, sessions []domain.SessionInfo) string {
	runes := []rune(id)
	if len(runes) <= 8 {
		return id
	}
	for length := 8; length < len(runes); length++ {
		prefix := string(runes[:length])
		unique := true
		for _, other := range sessions {
			if other.ThreadID == id {
				continue
			}
			if strings.HasPrefix(other.ThreadID, prefix) {
				unique = false
				break
			}
		}
		if unique {
			return prefix + "…"
		}
	}
	return id
}

func shortPathBase(path string) string {
	parts := pathComponents(path)
	if len(parts) == 0 {
		return ""
	}
	return truncateRunes(parts[len(parts)-1], 18)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if limit < 1 || len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func formatRelativeTime(updated float64, now time.Time) string {
	if updated <= 0 {
		return "更新不明"
	}
	then := time.Unix(0, int64(updated*1e9)).Local()
	if now.IsZero() {
		now = time.Now()
	}
	if then.After(now) {
		return "たった今"
	}
	delta := now.Sub(then)
	if delta < time.Minute {
		return "たった今"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%d分前", int(delta/time.Minute))
	}
	nowDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	thenDate := time.Date(then.Year(), then.Month(), then.Day(), 0, 0, 0, 0, then.Location())
	days := int(nowDate.Sub(thenDate) / (24 * time.Hour))
	switch {
	case days <= 0:
		return fmt.Sprintf("%d時間前", int(delta/time.Hour))
	case days == 1:
		return "昨日"
	case days < 7:
		return fmt.Sprintf("%d日前", days)
	case days < 30:
		return fmt.Sprintf("%d週間前", days/7)
	case days < 365:
		return fmt.Sprintf("%dか月前", days/30)
	default:
		return fmt.Sprintf("%d年前", days/365)
	}
}

func formatAbsoluteTime(updated float64) string {
	if updated <= 0 {
		return "不明"
	}
	return time.Unix(0, int64(updated*1e9)).Local().Format("2006-01-02 15:04:05")
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
