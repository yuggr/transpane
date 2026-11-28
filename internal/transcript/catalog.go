package transcript

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
)

// Catalog discovers sessions for an interactive picker. Unlike the one-shot
// discovery functions, it remembers per-file metadata and titles so a periodic
// refresh does not reread unchanged transcripts.
type Catalog struct {
	codexRoot  string
	claudeRoot string
	source     *domain.Provider

	mu         sync.Mutex
	codexMeta  map[string]codexMetaEntry
	claudeMeta map[string]claudeMetaEntry
	titles     map[string]titleEntry
}

type fileStamp struct {
	size    int64
	modTime int64
}

type codexMetaEntry struct {
	stamp   fileStamp
	payload map[string]any
	ok      bool
}

type claudeMetaEntry struct {
	stamp   fileStamp
	cwd     *string
	version *string
	ok      bool
}

type titleEntry struct {
	stamp fileStamp
	title string
}

// NewCatalog creates an empty cache for one provider configuration.
func NewCatalog(codexRoot, claudeRoot string, source *domain.Provider) *Catalog {
	return &Catalog{
		codexRoot:  codexRoot,
		claudeRoot: claudeRoot,
		source:     source,
		codexMeta:  map[string]codexMetaEntry{},
		claudeMeta: map[string]claudeMetaEntry{},
		titles:     map[string]titleEntry{},
	}
}

// Discover returns the current sessions, optionally filtered by exact cwd,
// with deterministic titles attached. Calls are serialized so overlapping
// refresh ticks cannot multiply transcript I/O.
func (c *Catalog) Discover(cwd string) []domain.SessionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	sessions := discoverAll(c.codexRoot, c.claudeRoot, c.source, c.readCodexMeta, c.readClaudeMeta)
	c.prune(sessions)
	if cwd != "" {
		sessions = SessionsForCwd(sessions, cwd)
	}
	for index := range sessions {
		sessions[index].Title = c.titleFor(sessions[index])
	}
	return sessions
}

func (c *Catalog) prune(sessions []domain.SessionInfo) {
	keep := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		keep[session.Path] = true
	}
	for path := range c.codexMeta {
		if !keep[path] {
			delete(c.codexMeta, path)
		}
	}
	for path := range c.claudeMeta {
		if !keep[path] {
			delete(c.claudeMeta, path)
		}
	}
	for path := range c.titles {
		if !keep[path] {
			delete(c.titles, path)
		}
	}
}

func stampFor(path string) (fileStamp, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileStamp{}, false
	}
	return fileStamp{size: info.Size(), modTime: info.ModTime().UnixNano()}, true
}

func (c *Catalog) readCodexMeta(path string) (map[string]any, bool) {
	stamp, ok := stampFor(path)
	if !ok {
		delete(c.codexMeta, path)
		return nil, false
	}
	if cached, found := c.codexMeta[path]; found && cached.stamp == stamp {
		return cached.payload, cached.ok
	}
	payload, ok := ReadCodexMeta(path)
	c.codexMeta[path] = codexMetaEntry{stamp: stamp, payload: payload, ok: ok}
	return payload, ok
}

func (c *Catalog) readClaudeMeta(path string) (*string, *string, bool) {
	stamp, ok := stampFor(path)
	if !ok {
		delete(c.claudeMeta, path)
		return nil, nil, false
	}
	if cached, found := c.claudeMeta[path]; found && cached.stamp == stamp {
		return cached.cwd, cached.version, cached.ok
	}
	cwd, version, ok := ReadClaudeMeta(path)
	c.claudeMeta[path] = claudeMetaEntry{stamp: stamp, cwd: cwd, version: version, ok: ok}
	return cwd, version, ok
}

func (c *Catalog) titleFor(session domain.SessionInfo) string {
	stamp, ok := stampFor(session.Path)
	if !ok {
		delete(c.titles, session.Path)
		return ""
	}
	if cached, found := c.titles[session.Path]; found && cached.stamp == stamp {
		return cached.title
	}
	title := extractTitle(session)
	c.titles[session.Path] = titleEntry{stamp: stamp, title: title}
	return title
}

func extractTitle(session domain.SessionInfo) string {
	file, err := os.Open(session.Path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	if session.Provider == domain.ProviderClaude {
		return extractClaudeTitle(file, session.ThreadID)
	}
	return extractCodexTitle(file, session.ThreadID)
}

func extractCodexTitle(file *os.File, threadID string) string {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	reader := newLineReader(file)
	mode := HistoryModeLegacy
	extractor := NewExtractor(threadID, mode)
	consumed := 0
	lines := 0
	for consumed < config.TitleReadLimit && lines < config.TitleMaxLines {
		line, complete, err := reader.readLine(config.TitleReadLimit - consumed)
		if err != nil || len(line) == 0 {
			break
		}
		consumed += len(line)
		lines++
		if !complete {
			break
		}
		record, ok := DecodeObject(line)
		if !ok {
			continue
		}
		if record["type"] == "session_meta" {
			payload, _ := record["payload"].(map[string]any)
			if payload != nil {
				if parsed, parseErr := ParseHistoryMode(payload["history_mode"]); parseErr == nil {
					mode = parsed
					extractor = NewExtractor(threadID, mode)
				}
			}
			continue
		}
		result := extractor.Extract(record, 0)
		if result.Message != nil && result.Message.Kind == domain.MessageKindUser {
			if title := titleFromText(result.Message.Original); title != "" {
				return title
			}
		}
	}
	return ""
}

func extractClaudeTitle(file *os.File, threadID string) string {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	reader := newLineReader(file)
	extractor := NewClaudeExtractor(threadID)
	consumed := 0
	lines := 0
	metatitle := ""
	firstUser := ""
	for consumed < config.TitleReadLimit && lines < config.TitleMaxLines {
		line, complete, err := reader.readLine(config.TitleReadLimit - consumed)
		if err != nil || len(line) == 0 {
			break
		}
		consumed += len(line)
		lines++
		if !complete {
			break
		}
		record, ok := DecodeObject(line)
		if !ok {
			continue
		}
		if record["type"] == "ai-title" {
			if text, ok := record["aiTitle"].(string); ok {
				if title := titleFromText(text); title != "" {
					metatitle = title
				}
			}
			continue
		}
		if firstUser == "" {
			result := extractor.Extract(record, 0)
			if result.Message != nil && result.Message.Kind == domain.MessageKindUser {
				firstUser = titleFromText(result.Message.Original)
			}
		}
	}
	if title := claudeTailTitle(file); title != "" {
		return title
	}
	if metatitle != "" {
		return metatitle
	}
	return firstUser
}

func claudeTailTitle(file *os.File) string {
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size <= config.TitleReadLimit {
		// The bounded head pass already covered the whole file.
		return ""
	}
	offset := size - config.TitleTailLimit
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	data := make([]byte, size-offset)
	count, err := io.ReadFull(file, data)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return ""
	}
	data = data[:count]
	if offset > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			return ""
		}
		data = data[newline+1:]
	}
	title := ""
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		record, ok := DecodeObject(line)
		if !ok {
			continue
		}
		if record["type"] != "ai-title" {
			continue
		}
		if text, ok := record["aiTitle"].(string); ok {
			if cleaned := titleFromText(text); cleaned != "" {
				title = cleaned
			}
		}
	}
	return title
}

// titleFromText returns the first meaningful, single display line of a
// metadata title or allowlisted user message. It never follows markup or
// internal tags and caps the preview length.
func titleFromText(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.Trim(line, "`#>-*_= \t") == "" {
			continue
		}
		if strings.HasPrefix(line, "<") && strings.Contains(line, ">") {
			continue
		}
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		return truncateTitle(protection.RedactSecrets(line))
	}
	return ""
}

func truncateTitle(text string) string {
	runes := []rune(text)
	limit := config.TitlePreviewRunes
	if limit < 1 {
		limit = 1
	}
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
