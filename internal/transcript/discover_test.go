package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuggr/transpane/internal/domain"
)

const claudeSession = "11111111-1111-4111-8111-111111111111"

func dump(records []map[string]any) []byte {
	var b strings.Builder
	for _, record := range records {
		raw, _ := json.Marshal(record)
		b.Write(raw)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func codexMeta(id, mode string) map[string]any {
	payload := map[string]any{"id": id, "cwd": "/tmp/demo", "cli_version": "0.169.0"}
	if mode != "" {
		payload["history_mode"] = mode
	}
	return map[string]any{"timestamp": "2026-11-25T01:00:00Z", "type": "session_meta", "payload": payload}
}

func writeRollout(t *testing.T, root, relative string, records []map[string]any) string {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, dump(records), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func claudeRecord(session, text string, extra map[string]any) map[string]any {
	record := map[string]any{
		"type": "user", "sessionId": session, "uuid": "00000000-0000-4000-8000-000000000001",
		"timestamp": "2026-11-25T03:00:00Z", "cwd": "/tmp/demo", "version": "2.1.285",
		"message": map[string]any{"role": "user", "content": text},
	}
	for key, value := range extra {
		record[key] = value
	}
	return record
}

func writeClaude(t *testing.T, root, project, session string, records []map[string]any) string {
	t.Helper()
	path := filepath.Join(root, project, session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, dump(records), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexDiscoveryFiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	parent := writeRollout(t, root, "2026/11/25/rollout-parent.jsonl", []map[string]any{codexMeta("parent", "legacy")})
	writeRollout(t, root, "2026/11/25/rollout-child.jsonl", []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "child", "parent_thread_id": "parent"}},
	})
	writeRollout(t, root, "2026/11/25/rollout-orphan.jsonl", []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "orphan", "parent_thread_id": "missing"}},
	})
	writeRollout(t, root, "2026/11/25/rollout-nometa.jsonl", []map[string]any{{"type": "event_msg"}})
	writeRollout(t, root, "archived_sessions/2026/11/25/rollout-old.jsonl", []map[string]any{codexMeta("archived", "legacy")})

	if err := os.Chtimes(parent, timeAt(1000), timeAt(1000)); err != nil {
		t.Fatal(err)
	}
	found := DiscoverCodex(root)
	ids := []string{}
	for _, item := range found {
		ids = append(ids, item.ThreadID)
	}
	if strings.Join(ids, ",") != "orphan,parent" {
		t.Fatalf("ids %v", ids)
	}
	if found[1].HistoryMode == nil || *found[1].HistoryMode != "legacy" {
		t.Fatal("history mode")
	}
}

func TestCodexDiscoveryTieBreaksByID(t *testing.T) {
	root := t.TempDir()
	b := writeRollout(t, root, "2026/11/25/rollout-b.jsonl", []map[string]any{codexMeta("b", "legacy")})
	a := writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{codexMeta("a", "legacy")})
	for _, p := range []string{a, b} {
		_ = os.Chtimes(p, timeAt(500), timeAt(500))
	}
	found := DiscoverCodex(root)
	if len(found) != 2 || found[0].ThreadID != "a" || found[1].ThreadID != "b" {
		t.Fatalf("%+v", found)
	}
}

func TestReadCodexMetaBoundaries(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"invalid-head": append([]byte("not json\n"), dump([]map[string]any{codexMeta("real", "legacy")})...),
		"eof":          []byte(strings.TrimSuffix(string(dump([]map[string]any{codexMeta("eof", "legacy")})), "\n")),
		"boundary":     append(bytesOf('x', 256*1024), append([]byte("\n"), dump([]map[string]any{codexMeta("after", "legacy")})...)...),
		"oversized":    append([]byte("junk"), append(bytesOf('x', 256*1024-4), dump([]map[string]any{codexMeta("split", "legacy")})...)...),
	}
	for name, body := range cases {
		path := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		payload, ok := ReadCodexMeta(path)
		switch name {
		case "oversized":
			if ok {
				t.Fatalf("%s unexpectedly parsed", name)
			}
		case "boundary":
			if !ok || payload["id"] != "after" {
				t.Fatalf("%s: %v %v", name, payload, ok)
			}
		default:
			wanted := name
			if name == "invalid-head" {
				wanted = "real"
			}
			if !ok || payload["id"] != wanted {
				t.Fatalf("%s: %v %v", name, payload, ok)
			}
		}
	}
}

func bytesOf(b byte, n int) []byte { return []byte(strings.Repeat(string(b), n)) }

func timeAt(sec int64) time.Time { return time.Unix(sec, 0) }

func TestClaudeDiscoveryDirectFilesOnly(t *testing.T) {
	root := t.TempDir()
	valid := writeClaude(t, root, "demo", claudeSession, []map[string]any{claudeRecord(claudeSession, "ok", nil)})
	writeClaude(t, root, "demo/subagents", "22222222-2222-4222-8222-222222222222", []map[string]any{claudeRecord("22222222-2222-4222-8222-222222222222", "sub", nil)})
	writeClaude(t, root, "demo", "not-a-uuid", []map[string]any{claudeRecord("not-a-uuid", "bad", nil)})
	_ = os.WriteFile(filepath.Join(root, "demo", "sessions-index.json"), []byte("{}"), 0o600)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	_ = os.WriteFile(outside, dump([]map[string]any{claudeRecord(claudeSession, "outside", nil)}), 0o600)
	_ = os.Symlink(outside, filepath.Join(root, "demo", "44444444-4444-4444-8444-444444444444.jsonl"))
	_ = os.Symlink(filepath.Join(root, "demo"), filepath.Join(root, "linked-project"))

	found := DiscoverClaude(root)
	if len(found) != 1 || found[0].ThreadID != claudeSession || found[0].Path != valid {
		t.Fatalf("%+v", found)
	}
	if found[0].Provider != domain.ProviderClaude || found[0].ModeLabel() != "claude-jsonl" || found[0].CLIVersion == nil || *found[0].CLIVersion != "2.1.285" {
		t.Fatalf("%+v", found[0])
	}
}

func TestClaudeMetaAbove256KAndCounts(t *testing.T) {
	root := t.TempDir()
	_ = writeClaude(t, root, "demo", claudeSession, []map[string]any{claudeRecord(claudeSession, strings.Repeat("x", 300000), nil)})
	confirmed, unconfirmed := ClaudeTranscriptCounts(root)
	if confirmed != 1 || unconfirmed != 0 {
		t.Fatalf("counts %d %d", confirmed, unconfirmed)
	}
	_ = writeClaude(t, root, "demo", "22222222-2222-4222-8222-222222222222", []map[string]any{claudeRecord(claudeSession, "wrong", nil)})
	_ = writeClaude(t, root, "demo", "33333333-3333-4333-8333-333333333333", []map[string]any{claudeRecord("33333333-3333-4333-8333-333333333333", "side", map[string]any{"isSidechain": true})})
	confirmed, unconfirmed = ClaudeTranscriptCounts(root)
	if confirmed != 1 || unconfirmed != 2 {
		t.Fatalf("counts %d %d", confirmed, unconfirmed)
	}
}

func TestClaudeRelativeCwdBecomesNil(t *testing.T) {
	root := t.TempDir()
	session := "55555555-5555-4555-8555-555555555555"
	record := claudeRecord(session, "ok", nil)
	record["cwd"] = "relative/path"
	_ = writeClaude(t, root, "demo", session, []map[string]any{record})
	found := DiscoverClaude(root)
	if len(found) != 1 || found[0].Cwd != nil {
		t.Fatalf("%+v", found)
	}
}

func TestSessionSelectionAndCwd(t *testing.T) {
	sessions := []domain.SessionInfo{
		{ThreadID: "dup", Provider: domain.ProviderCodex, Cwd: strptr("/tmp/project")},
		{ThreadID: "dup", Provider: domain.ProviderClaude, Cwd: strptr("/tmp/project/sub")},
		{ThreadID: "other", Provider: domain.ProviderCodex, Cwd: strptr("/tmp//project/../project")},
	}
	if _, err := SelectSession(sessions, "dup", nil); err == nil || !strings.Contains(err.Error(), "重複") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := SelectSession(sessions, "missing", nil); err == nil || !strings.Contains(err.Error(), "見つかりません") {
		t.Fatalf("missing: %v", err)
	}
	codex := domain.ProviderCodex
	got, err := SelectSession(sessions, "claude:dup", &codex)
	if err == nil || !strings.Contains(err.Error(), "矛盾") {
		t.Fatalf("conflict: %v", err)
	}
	got, err = SelectSession(sessions, "dup", &codex)
	if err != nil || got.Provider != domain.ProviderCodex {
		t.Fatalf("codex: %v %+v", err, got)
	}
	if got := SessionsForCwd(sessions, "/tmp/project"); len(got) != 2 {
		t.Fatalf("cwd filter %+v", got)
	}
	if NormalizeCwd("/tmp//project/../project") != "/tmp/project" {
		t.Fatal("normalize")
	}
}

func strptr(v string) *string { return &v }
