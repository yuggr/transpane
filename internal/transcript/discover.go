// Package transcript discovers sessions and reads allowlisted JSONL records.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SessionError reports a selection or access failure.
type SessionError struct{ Message string }

func (e *SessionError) Error() string { return e.Message }

func sessionErrorf(format string, args ...any) error {
	return &SessionError{Message: fmt.Sprintf(format, args...)}
}

// ReadError reports a classified reader failure. Reason is one of missing,
// denied, halted, unsupported, read or index_mismatch.
type ReadError struct{ Reason string }

func (e *ReadError) Error() string { return e.Reason }

// DecodeObject decodes one strict UTF-8 JSON object. It never lets
// encoding/json replace invalid bytes or accept trailing garbage.
func DecodeObject(raw []byte) (map[string]any, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

type lineReader struct{ reader *bufio.Reader }

func newLineReader(file *os.File) *lineReader {
	return &lineReader{reader: bufio.NewReaderSize(file, 64*1024)}
}

// readLine reads at most max bytes, stopping after a newline. complete is
// false when the limit or EOF was reached without a newline.
func (lr *lineReader) readLine(max int) (line []byte, complete bool, err error) {
	for len(line) < max {
		b, readErr := lr.reader.ReadByte()
		if readErr == io.EOF {
			return line, false, nil
		}
		if readErr != nil {
			return line, false, readErr
		}
		line = append(line, b)
		if b == '\n' {
			return line, true, nil
		}
	}
	return line, false, nil
}

// ReadCodexMeta returns the first valid session_meta payload within the
// 1 MiB head, reproducing the Python readline boundary rules.
func ReadCodexMeta(path string) (map[string]any, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	reader := newLineReader(file)
	consumed := 0
	for consumed < config.MetaReadLimit {
		line, complete, err := reader.readLine(256 * 1024)
		if err != nil || len(line) == 0 {
			return nil, false
		}
		consumed += len(line)
		if !complete && len(line) >= 256*1024 {
			// Drain the rest of this logical line, then continue with the next.
			for consumed < config.MetaReadLimit {
				rest, restComplete, readErr := reader.readLine(256 * 1024)
				if readErr != nil || len(rest) == 0 {
					return nil, false
				}
				consumed += len(rest)
				if restComplete {
					break
				}
			}
			continue
		}
		record, ok := DecodeObject(line)
		if !ok || record["type"] != "session_meta" {
			continue
		}
		payload, ok := record["payload"].(map[string]any)
		if !ok {
			continue
		}
		id, ok := payload["id"].(string)
		if !ok || id == "" {
			continue
		}
		return payload, true
	}
	return nil, false
}

func optionalString(value any) *string {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	return &text
}

func mtimeSeconds(info os.FileInfo) float64 {
	return float64(info.ModTime().UnixNano()) / 1e9
}

func withinRoot(path, rootReal string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootReal, resolved)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isRolloutName(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

// DiscoverCodex walks the sessions tree in lexical order, excluding archived
// sessions, symlinks and files outside the resolved root.
func DiscoverCodex(sessionsRoot string) []domain.SessionInfo {
	info, err := os.Stat(sessionsRoot)
	if err != nil || !info.IsDir() {
		return nil
	}
	rootReal, err := filepath.EvalSymlinks(sessionsRoot)
	if err != nil {
		return nil
	}
	type rawEntry struct {
		info   domain.SessionInfo
		parent *string
	}
	raw := []rawEntry{}
	_ = filepath.WalkDir(sessionsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != sessionsRoot && (name == "archived_sessions" || entry.Type()&os.ModeSymlink != 0) {
				return filepath.SkipDir
			}
			// WalkDir does not follow directory symlinks; skip them explicitly.
			if path != sessionsRoot {
				info, statErr := entry.Info()
				if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !isRolloutName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if !withinRoot(path, rootReal) {
			return nil
		}
		payload, ok := ReadCodexMeta(path)
		if !ok {
			return nil
		}
		threadID, ok := payload["id"].(string)
		if !ok || threadID == "" {
			return nil
		}
		stat, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		parent := optionalString(payload["parent_thread_id"])
		raw = append(raw, rawEntry{
			info: domain.SessionInfo{
				ThreadID:       threadID,
				Path:           path,
				Cwd:            optionalString(payload["cwd"]),
				UpdatedAt:      mtimeSeconds(stat),
				HistoryMode:    optionalString(payload["history_mode"]),
				CLIVersion:     optionalString(payload["cli_version"]),
				ParentThreadID: parent,
				Source:         optionalString(payload["source"]),
				Provider:       domain.ProviderCodex,
			},
			parent: parent,
		})
		return nil
	})

	seen := map[string]bool{}
	for _, item := range raw {
		seen[item.info.ThreadID] = true
	}
	found := make([]domain.SessionInfo, 0, len(raw))
	for _, item := range raw {
		if item.parent != nil && seen[*item.parent] {
			continue
		}
		found = append(found, item.info)
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].UpdatedAt != found[j].UpdatedAt {
			return found[i].UpdatedAt > found[j].UpdatedAt
		}
		return found[i].ThreadID < found[j].ThreadID
	})
	return found
}

func isTranscriptChild(path string, entry os.DirEntry, rootReal string) bool {
	name := entry.Name()
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	stem := strings.TrimSuffix(name, ".jsonl")
	if !uuidPattern.MatchString(stem) {
		return false
	}
	if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
		return false
	}
	return withinRoot(path, rootReal)
}

// iterClaudeFiles yields direct <project>/<uuid>.jsonl regular files.
func iterClaudeFiles(projectsRoot string) []string {
	info, err := os.Stat(projectsRoot)
	if err != nil || !info.IsDir() {
		return nil
	}
	rootReal, err := filepath.EvalSymlinks(projectsRoot)
	if err != nil {
		return nil
	}
	projects, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name() < projects[j].Name() })
	found := []string{}
	for _, project := range projects {
		projectPath := filepath.Join(projectsRoot, project.Name())
		if project.Type()&os.ModeSymlink != 0 || !project.IsDir() || !withinRoot(projectPath, rootReal) {
			continue
		}
		children, readErr := os.ReadDir(projectPath)
		if readErr != nil {
			continue
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			path := filepath.Join(projectPath, child.Name())
			if isTranscriptChild(path, child, rootReal) {
				found = append(found, path)
			}
		}
	}
	return found
}

// ReadClaudeMeta confirms the session ID from the first eligible complete
// head line within 1 MiB.
func ReadClaudeMeta(path string) (cwd, version *string, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer file.Close()
	reader := newLineReader(file)
	consumed := 0
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	for consumed < config.MetaReadLimit {
		line, complete, readErr := reader.readLine(config.MetaReadLimit - consumed)
		if !complete || len(line) == 0 || readErr != nil {
			return nil, nil, false
		}
		consumed += len(line)
		record, ok := DecodeObject(line)
		if !ok || !eligibleClaudeRecord(record, stem) {
			continue
		}
		return claudeMetaValues(record)
	}
	return nil, nil, false
}

func eligibleClaudeRecord(record map[string]any, stem string) bool {
	recordType, _ := record["type"].(string)
	if recordType != "user" && recordType != "assistant" {
		return false
	}
	if sessionID, _ := record["sessionId"].(string); sessionID != stem {
		return false
	}
	sidechain, present := record["isSidechain"]
	if !present {
		return true
	}
	flag, isBool := sidechain.(bool)
	return isBool && !flag
}

func claudeMetaValues(record map[string]any) (cwd, version *string, ok bool) {
	if raw, present := record["cwd"]; present {
		if text, isString := raw.(string); isString && filepath.IsAbs(text) {
			cwd = &text
		}
	}
	if raw, present := record["version"]; present {
		if text, isString := raw.(string); isString {
			version = &text
		}
	}
	return cwd, version, true
}

// DiscoverClaude discovers direct <project>/<uuid>.jsonl transcripts.
func DiscoverClaude(projectsRoot string) []domain.SessionInfo {
	found := []domain.SessionInfo{}
	for _, path := range iterClaudeFiles(projectsRoot) {
		cwd, version, ok := ReadClaudeMeta(path)
		if !ok {
			continue
		}
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		mode := "claude-jsonl"
		found = append(found, domain.SessionInfo{
			ThreadID:    strings.TrimSuffix(filepath.Base(path), ".jsonl"),
			Path:        path,
			Cwd:         cwd,
			UpdatedAt:   mtimeSeconds(stat),
			HistoryMode: &mode,
			CLIVersion:  version,
			Provider:    domain.ProviderClaude,
		})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].UpdatedAt != found[j].UpdatedAt {
			return found[i].UpdatedAt > found[j].UpdatedAt
		}
		if found[i].Provider != found[j].Provider {
			return found[i].Provider < found[j].Provider
		}
		if found[i].ThreadID != found[j].ThreadID {
			return found[i].ThreadID < found[j].ThreadID
		}
		return found[i].Path < found[j].Path
	})
	return found
}

// ClaudeTranscriptCounts returns confirmed and unconfirmed direct transcript
// counts, counting files even when they have no eligible meta line.
func ClaudeTranscriptCounts(projectsRoot string) (confirmed, unconfirmed int) {
	for _, path := range iterClaudeFiles(projectsRoot) {
		if _, _, ok := ReadClaudeMeta(path); ok {
			confirmed++
		} else {
			unconfirmed++
		}
	}
	return confirmed, unconfirmed
}

// DiscoverAll merges providers in the same order as the Python baseline.
func DiscoverAll(codexRoot, claudeRoot string, source *domain.Provider) []domain.SessionInfo {
	found := []domain.SessionInfo{}
	if source == nil || *source == domain.ProviderCodex {
		found = append(found, DiscoverCodex(codexRoot)...)
	}
	if source == nil || *source == domain.ProviderClaude {
		found = append(found, DiscoverClaude(claudeRoot)...)
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].UpdatedAt != found[j].UpdatedAt {
			return found[i].UpdatedAt > found[j].UpdatedAt
		}
		if found[i].Provider != found[j].Provider {
			return found[i].Provider < found[j].Provider
		}
		if found[i].ThreadID != found[j].ThreadID {
			return found[i].ThreadID < found[j].ThreadID
		}
		return found[i].Path < found[j].Path
	})
	return found
}

// NormalizeCwd expands ~ and returns a clean absolute path.
func NormalizeCwd(cwd string) string {
	if cwd == "~" || strings.HasPrefix(cwd, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if cwd == "~" {
				cwd = home
			} else {
				cwd = filepath.Join(home, cwd[2:])
			}
		}
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return filepath.Clean(cwd)
	}
	return filepath.Clean(absolute)
}

// ParseSessionRef splits an optional provider prefix. Unknown prefixes are
// treated as part of the ID.
func ParseSessionRef(ref string) (*domain.Provider, string) {
	prefix, rest, found := strings.Cut(ref, ":")
	if !found || rest == "" {
		return nil, ref
	}
	switch prefix {
	case string(domain.ProviderCodex):
		provider := domain.ProviderCodex
		return &provider, rest
	case string(domain.ProviderClaude):
		provider := domain.ProviderClaude
		return &provider, rest
	default:
		return nil, ref
	}
}

// SelectSession resolves one reference, rejecting ambiguity.
func SelectSession(sessions []domain.SessionInfo, ref string, source *domain.Provider) (domain.SessionInfo, error) {
	wantedProvider, threadID := ParseSessionRef(ref)
	if wantedProvider != nil && source != nil && *wantedProvider != *source {
		return domain.SessionInfo{}, sessionErrorf("--source とセッションIDの接頭辞が矛盾しています")
	}
	matches := []domain.SessionInfo{}
	for _, session := range sessions {
		if session.ThreadID != threadID {
			continue
		}
		if source != nil && session.Provider != *source {
			continue
		}
		if wantedProvider != nil && session.Provider != *wantedProvider {
			continue
		}
		matches = append(matches, session)
	}
	if len(matches) == 0 {
		return domain.SessionInfo{}, sessionErrorf("セッションが見つかりません: %s", ref)
	}
	if len(matches) > 1 {
		return domain.SessionInfo{}, sessionErrorf("セッションIDが重複しています: %s", ref)
	}
	return matches[0], nil
}

// SessionsForCwd filters sessions by exact normalized cwd.
func SessionsForCwd(sessions []domain.SessionInfo, cwd string) []domain.SessionInfo {
	wanted := NormalizeCwd(cwd)
	found := []domain.SessionInfo{}
	for _, session := range sessions {
		if session.Cwd != nil && NormalizeCwd(*session.Cwd) == wanted {
			found = append(found, session)
		}
	}
	return found
}
