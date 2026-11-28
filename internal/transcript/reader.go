package transcript

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
)

// ScanStats counts one scan or poll pass.
type ScanStats struct {
	Records        int
	Events         int
	Extracted      int
	Ignored        int
	InvalidLines   int
	OversizedLines int
}

// ScanResult is the result of indexing a whole transcript.
type ScanResult struct {
	ThreadID        string
	HistoryMode     *string
	UnsupportedMode *string
	ThreadChanged   *string
	Entries         []domain.IndexEntry
	Stats           ScanStats
}

// PollResult is the result of one incremental read.
type PollResult struct {
	Messages        []domain.Message
	Entries         []domain.IndexEntry
	Stats           ScanStats
	Missing         bool
	Rebuilt         bool
	Denied          bool
	ThreadChanged   *string
	UnsupportedMode *string
}

// ReaderOptions selects provider, containment root and test limits.
type ReaderOptions struct {
	Provider     domain.Provider
	AllowedRoot  string
	MaxPollBytes int
	MaxLineBytes int
}

// Reader reads one transcript from the start, then only appended complete
// lines. It never writes to the file.
type Reader struct {
	path         string
	threadID     string
	provider     domain.Provider
	allowedRoot  string
	maxPollBytes int
	maxLineBytes int
	splitter     *lineSplitter
	mode         HistoryMode
	extractor    *Extractor
	claude       *ClaudeExtractor
	unsupported  *string
	halted       bool
	scanInfo     os.FileInfo
	stats        ScanStats
}

func NewReader(path, threadID string, opts ReaderOptions) *Reader {
	provider := opts.Provider
	if provider == "" {
		provider = domain.ProviderCodex
	}
	maxPoll := opts.MaxPollBytes
	if maxPoll <= 0 {
		maxPoll = config.MaxPollBytes
	}
	reader := &Reader{
		path:         path,
		threadID:     threadID,
		provider:     provider,
		allowedRoot:  opts.AllowedRoot,
		maxPollBytes: maxPoll,
		maxLineBytes: defaultMaxLine(opts.MaxLineBytes),
	}
	reader.resetExtractor()
	reader.splitter = newLineSplitter(0, reader.maxLineBytes)
	return reader
}

// ReaderForSession builds a reader whose allowed root matches the provider.
func ReaderForSession(session domain.SessionInfo, codexRoot, claudeRoot string) *Reader {
	root := codexRoot
	if session.Provider == domain.ProviderClaude {
		root = claudeRoot
	}
	return NewReader(session.Path, session.ThreadID, ReaderOptions{Provider: session.Provider, AllowedRoot: root})
}

func (r *Reader) resetExtractor() {
	if r.provider == domain.ProviderClaude {
		r.mode = ""
		r.claude = NewClaudeExtractor(r.threadID)
		r.extractor = nil
		return
	}
	r.mode = HistoryModeLegacy
	r.extractor = NewExtractor(r.threadID, HistoryModeLegacy)
	r.claude = nil
}

func (r *Reader) sourceAllowed() bool {
	if r.allowedRoot == "" {
		return true
	}
	info, err := os.Lstat(r.path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	rootReal, err := filepath.EvalSymlinks(r.allowedRoot)
	if err != nil {
		return false
	}
	if !withinRoot(r.path, rootReal) {
		return false
	}
	if r.provider == domain.ProviderClaude {
		stem := strings.TrimSuffix(filepath.Base(r.path), ".jsonl")
		return stem == r.threadID
	}
	return true
}

// ScanAll indexes the whole transcript.
func (r *Reader) ScanAll() (ScanResult, error) {
	if !r.sourceAllowed() {
		return ScanResult{}, sessionErrorf("セッションのパスが許可範囲外です")
	}
	r.splitter = newLineSplitter(0, r.maxLineBytes)
	r.resetExtractor()
	r.unsupported = nil
	r.halted = false
	r.stats = ScanStats{}
	if r.provider == domain.ProviderClaude {
		r.claude = NewClaudeExtractor(r.threadID)
	}

	file, err := os.Open(r.path)
	if err != nil {
		return ScanResult{}, err
	}
	entries, threadChanged, scanErr := r.scanFile(file)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if scanErr != nil {
		return ScanResult{}, scanErr
	}
	if statErr != nil {
		return ScanResult{}, statErr
	}
	if closeErr != nil {
		return ScanResult{}, closeErr
	}
	r.scanInfo = info
	return ScanResult{
		ThreadID:        r.threadID,
		HistoryMode:     r.historyModeLabel(),
		UnsupportedMode: r.unsupported,
		ThreadChanged:   threadChanged,
		Entries:         entries,
		Stats:           r.stats,
	}, nil
}

func (r *Reader) scanFile(file *os.File) ([]domain.IndexEntry, *string, error) {
	entries := []domain.IndexEntry{}
	positions := map[string]int{}
	var threadChanged *string
	buffer := make([]byte, config.InitialScanChunkBytes)
	for {
		count, err := file.Read(buffer)
		if count > 0 {
			lines, oversized := r.splitter.feed(buffer[:count])
			r.stats.OversizedLines += oversized
			for _, line := range lines {
				message, changed := r.processLine(line.offset, line.raw, &r.stats)
				if changed != nil && threadChanged == nil {
					threadChanged = changed
				}
				if message != nil {
					entry := makeEntry(message, int64(len(line.raw)))
					if r.provider == domain.ProviderClaude {
						appendScanEntry(&entries, positions, entry)
					} else {
						entries = append(entries, entry)
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return entries, threadChanged, nil
		}
		if err != nil {
			return nil, threadChanged, err
		}
	}
}

func appendScanEntry(entries *[]domain.IndexEntry, positions map[string]int, entry domain.IndexEntry) {
	position, found := positions[entry.ID]
	if !found {
		positions[entry.ID] = len(*entries)
		*entries = append(*entries, entry)
		return
	}
	if (*entries)[position].Revision != entry.Revision {
		(*entries)[position] = entry
	}
}

func makeEntry(message *domain.Message, rawLength int64) domain.IndexEntry {
	return domain.IndexEntry{
		ID:        message.ID,
		Revision:  message.Revision,
		Start:     message.Sequence,
		End:       message.Sequence + rawLength,
		Timestamp: message.Timestamp,
		Kind:      message.Kind,
	}
}

func (r *Reader) historyModeLabel() *string {
	if r.provider == domain.ProviderClaude {
		mode := "claude-jsonl"
		return &mode
	}
	if r.unsupported != nil || r.mode == "" {
		return nil
	}
	mode := string(r.mode)
	return &mode
}

func (r *Reader) pollPrecheck() (*PollResult, os.FileInfo) {
	info, err := os.Lstat(r.path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return &PollResult{Denied: true}, nil
	}
	if err != nil {
		return &PollResult{Missing: true}, nil
	}
	if !r.sourceAllowed() {
		return &PollResult{Denied: true}, nil
	}
	stat, err := os.Stat(r.path)
	if err != nil {
		return &PollResult{Missing: true}, nil
	}
	if r.scanInfo != nil && !os.SameFile(r.scanInfo, stat) {
		return &PollResult{Rebuilt: true}, nil
	}
	if stat.Size() < r.splitter.consumed() {
		return &PollResult{Rebuilt: true}, nil
	}
	return nil, stat
}

// Poll reads at most one poll chunk of appended complete lines.
func (r *Reader) Poll() PollResult {
	precheck, stat := r.pollPrecheck()
	if precheck != nil {
		return *precheck
	}
	consumed := r.splitter.consumed()
	remaining := stat.Size() - consumed
	if remaining <= 0 {
		return PollResult{}
	}
	file, err := os.Open(r.path)
	if err != nil {
		return PollResult{Missing: true}
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(consumed, io.SeekStart); err != nil {
		return PollResult{}
	}
	limit := int64(r.maxPollBytes)
	if remaining < limit {
		limit = remaining
	}
	chunk := make([]byte, limit)
	count, err := file.Read(chunk)
	if err != nil && !errors.Is(err, io.EOF) {
		return PollResult{}
	}
	stats := ScanStats{}
	lines, oversized := r.splitter.feed(chunk[:count])
	stats.OversizedLines += oversized
	messages := []domain.Message{}
	entries := []domain.IndexEntry{}
	var threadChanged *string
	for _, line := range lines {
		message, changed := r.processLine(line.offset, line.raw, &stats)
		if changed != nil && threadChanged == nil {
			threadChanged = changed
		}
		if message != nil {
			messages = append(messages, *message)
			entries = append(entries, makeEntry(message, int64(len(line.raw))))
		}
	}
	return PollResult{
		Messages:        messages,
		Entries:         entries,
		Stats:           stats,
		ThreadChanged:   threadChanged,
		UnsupportedMode: r.unsupported,
	}
}

func (r *Reader) ensureReadable() error {
	if r.unsupported != nil {
		return &ReadError{Reason: "unsupported"}
	}
	if r.halted {
		return &ReadError{Reason: "halted"}
	}
	info, err := os.Lstat(r.path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return &ReadError{Reason: "denied"}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return &ReadError{Reason: "missing"}
		}
		return &ReadError{Reason: "missing"}
	}
	if !r.sourceAllowed() {
		return &ReadError{Reason: "denied"}
	}
	return nil
}

// ReadMessages seeks to each indexed byte range, re-extracts it and verifies
// the ID and revision.
func (r *Reader) ReadMessages(entries []domain.IndexEntry) ([]domain.Message, error) {
	if len(entries) == 0 {
		return []domain.Message{}, nil
	}
	if err := r.ensureReadable(); err != nil {
		return nil, err
	}
	file, err := os.Open(r.path)
	if err != nil {
		return nil, &ReadError{Reason: "read"}
	}
	defer func() { _ = file.Close() }()
	messages := make([]domain.Message, 0, len(entries))
	for _, entry := range entries {
		if _, err := file.Seek(entry.Start, io.SeekStart); err != nil {
			return nil, &ReadError{Reason: "read"}
		}
		raw := make([]byte, entry.End-entry.Start)
		if _, err := io.ReadFull(file, raw); err != nil {
			return nil, &ReadError{Reason: "read"}
		}
		record, ok := DecodeObject(raw)
		if !ok {
			return nil, &ReadError{Reason: "index_mismatch"}
		}
		message := r.extractRecord(record, entry.Start)
		if message == nil || message.ID != entry.ID || message.Revision != entry.Revision {
			return nil, &ReadError{Reason: "index_mismatch"}
		}
		messages = append(messages, *message)
	}
	return messages, nil
}

func (r *Reader) extractRecord(record map[string]any, offset int64) *domain.Message {
	if r.unsupported != nil || r.halted {
		return nil
	}
	if r.provider == domain.ProviderCodex && record["type"] == "session_meta" {
		return nil
	}
	if r.provider == domain.ProviderClaude {
		return r.claude.Extract(record, offset).Message
	}
	if r.extractor == nil {
		return nil
	}
	return r.extractor.Extract(record, offset).Message
}

func (r *Reader) processLine(offset int64, raw []byte, stats *ScanStats) (*domain.Message, *string) {
	stats.Records++
	record, ok := DecodeObject(raw)
	if !ok {
		stats.InvalidLines++
		return nil, nil
	}
	if r.provider == domain.ProviderClaude {
		return r.processClaudeLine(record, offset, stats)
	}
	if record["type"] == "session_meta" {
		return r.processSessionMeta(record)
	}
	return r.processCodexLine(record, offset, stats)
}

func (r *Reader) processClaudeLine(record map[string]any, offset int64, stats *ScanStats) (*domain.Message, *string) {
	stats.Events++
	if r.halted {
		stats.Ignored++
		return nil, nil
	}
	result := r.claude.Extract(record, offset)
	if result.Reason == "thread_mismatch" {
		r.halted = true
		sessionID, _ := record["sessionId"].(string)
		return nil, &sessionID
	}
	if result.Message == nil {
		stats.Ignored++
		return nil, nil
	}
	stats.Extracted++
	return result.Message, nil
}

func (r *Reader) processSessionMeta(record map[string]any) (*domain.Message, *string) {
	payload, ok := record["payload"].(map[string]any)
	if !ok {
		return nil, nil
	}
	if seen, ok := payload["id"].(string); ok && seen != "" && seen != r.threadID {
		r.halted = true
		return nil, &seen
	}
	mode, err := ParseHistoryMode(payload["history_mode"])
	if err != nil {
		value := reprValue(payload["history_mode"])
		r.unsupported = &value
		r.halted = true
		return nil, nil
	}
	r.mode = mode
	r.extractor = NewExtractor(r.threadID, mode)
	return nil, nil
}

func (r *Reader) processCodexLine(record map[string]any, offset int64, stats *ScanStats) (*domain.Message, *string) {
	stats.Events++
	if r.halted {
		stats.Ignored++
		return nil, nil
	}
	message := r.extractRecord(record, offset)
	if message == nil {
		stats.Ignored++
		return nil, nil
	}
	stats.Extracted++
	return message, nil
}

func reprValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return "'" + typed + "'"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprintf("%v", value)
	}
}
