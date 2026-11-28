package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yuggr/transpane/internal/domain"
)

func TestCatalogResolvesTitlesForBothProviders(t *testing.T) {
	codexRoot := t.TempDir()
	writeRollout(t, codexRoot, "2026/11/25/rollout-legacy.jsonl", []map[string]any{
		codexMeta("legacy-thread", "legacy"),
		{"timestamp": "2026-11-25T01:00:01Z", "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "PR #5 の移植検証\n二行目は使わない",
		}},
	})
	writeRollout(t, codexRoot, "2026/11/25/rollout-paginated.jsonl", []map[string]any{
		codexMeta("paginated-thread", "paginated"),
		{"timestamp": "2026-11-25T01:00:01Z", "type": "event_msg", "payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{"type": "UserMessage", "id": "u1", "content": []any{
				map[string]any{"type": "text", "text": "PowerShellの配置確認"},
			}},
		}},
	})
	claudeRoot := t.TempDir()
	claudeSession := "11111111-1111-4111-8111-111111111111"
	writeClaude(t, claudeRoot, "demo", claudeSession, []map[string]any{
		claudeRecord(claudeSession, "API認証方式の調査", nil),
	})

	catalog := NewCatalog(codexRoot, claudeRoot, nil)
	sessions := catalog.Discover("")
	titles := map[string]string{}
	for _, session := range sessions {
		titles[session.ThreadID] = session.Title
	}
	want := map[string]string{
		"legacy-thread":    "PR #5 の移植検証",
		"paginated-thread": "PowerShellの配置確認",
		claudeSession:      "API認証方式の調査",
	}
	for id, title := range want {
		if titles[id] != title {
			t.Fatalf("%s: title %q want %q", id, titles[id], title)
		}
	}
}

func TestCatalogPrefersExistingClaudeTitle(t *testing.T) {
	root := t.TempDir()
	session := "22222222-2222-4222-8222-222222222222"
	writeClaude(t, root, "demo", session, []map[string]any{
		claudeRecord(session, "first user message", nil),
		{"type": "ai-title", "sessionId": session, "aiTitle": "Phase 0 calibration harness"},
	})
	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "Phase 0 calibration harness" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogFindsClaudeTitleInBoundedTail(t *testing.T) {
	root := t.TempDir()
	session := "33333333-3333-4333-8333-333333333333"
	records := []map[string]any{claudeRecord(session, "head user message", nil)}
	for index := 0; index < 300; index++ {
		records = append(records, map[string]any{
			"type":      "attachment",
			"sessionId": session,
			"uuid":      "attachment-" + time.Unix(int64(index), 0).Format("150405"),
			"blob":      strings.Repeat("x", 1024),
		})
	}
	records = append(records, map[string]any{
		"type": "ai-title", "sessionId": session, "aiTitle": "tail title",
	})
	writeClaude(t, root, "demo", session, records)

	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "tail title" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogNeverUsesInternalEventsOrToolResults(t *testing.T) {
	root := t.TempDir()
	session := "44444444-4444-4444-8444-444444444444"
	records := []map[string]any{
		claudeRecord(session, "SECRET SIDECHAIN", map[string]any{"isSidechain": true}),
		{
			"type": "assistant", "sessionId": session, "uuid": "assistant-1",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "SECRET THINKING"},
				map[string]any{"type": "tool_use", "id": "t1", "name": "Read"},
				map[string]any{"type": "text", "text": "SECRET ASSISTANT TEXT"},
			}},
		},
		{
			"type": "user", "sessionId": session, "uuid": "user-tool",
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "SECRET TOOL RESULT"},
			}},
		},
		claudeRecord(session, "<system-reminder>SECRET INTERNAL</system-reminder>", nil),
	}
	writeClaude(t, root, "demo", session, records)

	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 {
		t.Fatalf("%+v", sessions)
	}
	if sessions[0].Title != "" {
		t.Fatalf("unexpected title %q", sessions[0].Title)
	}
}

func TestCatalogDoesNotUseCodexToolOrAgentEvents(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "paginated"),
		{"type": "event_msg", "payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{"type": "CommandExecution", "id": "c1", "output": "SECRET TOOL OUTPUT"},
		}},
		{"type": "event_msg", "payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{"type": "AgentMessage", "id": "a1", "content": []any{
				map[string]any{"type": "Text", "text": "SECRET ASSISTANT TEXT"},
			}},
		}},
	})
	catalog := NewCatalog(root, "", nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogRedactsKnownSecretsInTitles(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "Use token sk-abcdefghijklmnopqrstuvwx now",
		}},
	})
	catalog := NewCatalog(root, "", nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 {
		t.Fatalf("%+v", sessions)
	}
	if strings.Contains(sessions[0].Title, "abcdefghijklmnopqrstuvwx") || !strings.Contains(sessions[0].Title, "***") {
		t.Fatalf("secret not redacted: %q", sessions[0].Title)
	}
}

func TestTitlePreviewIsSingleLineAndBounded(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "\n\n  " + strings.Repeat("あ", 100) + "  \nsecond",
		}},
	})
	catalog := NewCatalog(root, "", nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 {
		t.Fatalf("%+v", sessions)
	}
	title := sessions[0].Title
	if strings.Contains(title, "\n") {
		t.Fatalf("multi-line title %q", title)
	}
	if utf8.RuneCountInString(title) != 81 || !strings.HasSuffix(title, "…") {
		t.Fatalf("title length %d: %q", utf8.RuneCountInString(title), title)
	}
}

func TestCatalogCachesUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "first"}},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(root, "", nil)
	if got := catalog.Discover(""); len(got) != 1 || got[0].Title != "first" {
		t.Fatalf("first discover %+v", got)
	}

	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "other"}},
	})
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := catalog.Discover(""); len(got) != 1 || got[0].Title != "first" {
		t.Fatalf("cached discover reread the file: %+v", got)
	}

	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if got := catalog.Discover(""); len(got) != 1 || got[0].Title != "other" {
		t.Fatalf("changed file was not reread: %+v", got)
	}
}

func TestCatalogCachesUnchangedMeta(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(root, "", nil)
	first := catalog.Discover("")
	if len(first) != 1 || first[0].Cwd == nil || *first[0].Cwd != "/tmp/demo" {
		t.Fatalf("%+v", first)
	}

	meta := codexMeta("a", "legacy")
	meta["payload"].(map[string]any)["cwd"] = "/tmp/abcd"
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{meta})
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	cached := catalog.Discover("")
	if len(cached) != 1 || cached[0].Cwd == nil {
		t.Fatalf("cached meta missing: %+v", cached)
	}
	if *cached[0].Cwd != "/tmp/demo" {
		t.Fatalf("cached meta reread: cwd=%q", *cached[0].Cwd)
	}
}

func TestCatalogDoesNotModifyTranscript(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "unchanged"}},
	})
	before, err := os.ReadFile(path) //nolint:gosec // path is a test temp file
	if err != nil {
		t.Fatal(err)
	}
	_ = NewCatalog(root, "", nil).Discover("")
	after, err := os.ReadFile(path) //nolint:gosec // path is a test temp file
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("catalog modified the transcript")
	}
}

func TestCatalogFiltersByCwdAfterDiscovery(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "a", "cwd": "/tmp/a"}},
	})
	writeRollout(t, root, "2026/11/25/rollout-b.jsonl", []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "b", "cwd": "/tmp/b"}},
	})
	catalog := NewCatalog(root, "", nil)
	got := catalog.Discover("/tmp/a")
	if len(got) != 1 || got[0].ThreadID != "a" {
		t.Fatalf("%+v", got)
	}
	if got[0].Provider != domain.ProviderCodex {
		t.Fatalf("%+v", got[0])
	}
}

func TestCatalogStopsAtUnsupportedCodexHistoryMode(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "future"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "must not be used"}},
	})
	catalog := NewCatalog(root, "", nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogStopsAtCodexSessionIDMismatch(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "2026/11/25/rollout-a.jsonl", []map[string]any{
		codexMeta("a", "legacy"),
		codexMeta("other", "legacy"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "must not be used"}},
	})
	catalog := NewCatalog(root, "", nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].ThreadID != "a" || sessions[0].Title != "" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogStopsAtClaudeThreadMismatch(t *testing.T) {
	root := t.TempDir()
	session := "55555555-5555-4555-8555-555555555555"
	writeClaude(t, root, "demo", session, []map[string]any{
		claudeRecord(session, "valid first user", nil),
		claudeRecord("66666666-6666-4666-8666-666666666666", "wrong session", nil),
		claudeRecord(session, "must not be used", nil),
	})
	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogValidatesClaudeAITitleRecords(t *testing.T) {
	root := t.TempDir()
	session := "77777777-7777-4777-8777-777777777777"
	writeClaude(t, root, "demo", session, []map[string]any{
		claudeRecord(session, "fallback title", nil),
		{"type": "ai-title", "sessionId": "88888888-8888-4888-8888-888888888888", "aiTitle": "other session"},
		{"type": "ai-title", "sessionId": session, "isSidechain": true, "aiTitle": "sidechain"},
		{"type": "ai-title", "sessionId": session, "isMeta": true, "aiTitle": "meta"},
		{"type": "ai-title", "sessionId": session, "aiTitle": 123},
		{"type": "ai-title", "sessionId": session},
		{"type": "ai-title", "sessionId": session, "aiTitle": "valid title"},
	})
	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "valid title" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogRequiresNewlineTerminatedHeadAITitle(t *testing.T) {
	root := t.TempDir()
	session := "99999999-9999-4999-8999-999999999999"
	path := filepath.Join(root, "demo", session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := string(dump([]map[string]any{claudeRecord(session, "fallback title", nil)})) +
		`{"type":"ai-title","sessionId":"` + session + `","aiTitle":"unterminated"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "fallback title" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogRequiresNewlineTerminatedTailAITitle(t *testing.T) {
	root := t.TempDir()
	session := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	records := []map[string]any{claudeRecord(session, "fallback title", nil)}
	for index := 0; index < 300; index++ {
		records = append(records, map[string]any{
			"type":      "attachment",
			"sessionId": session,
			"uuid":      "attachment-" + time.Unix(int64(index), 0).Format("150405"),
			"blob":      strings.Repeat("x", 1024),
		})
	}
	path := filepath.Join(root, "demo", session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := string(dump(records)) + `{"type":"ai-title","sessionId":"` + session + `","aiTitle":"unterminated"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog("", root, nil)
	sessions := catalog.Discover("")
	if len(sessions) != 1 || sessions[0].Title != "fallback title" {
		t.Fatalf("%+v", sessions)
	}
}

func TestCatalogCachesIneligibleLogs(t *testing.T) {
	root := t.TempDir()
	relative := "2026/11/25/rollout-a.jsonl"
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	invalidInvalid := `{"type":"session_meta","payload":{}}`
	valid := `{"type":"session_meta","payload":{"id":"a"}}`
	if len(valid) <= len(invalidInvalid) {
		t.Fatal("test fixture assumption broken")
	}
	invalid := invalidInvalid + strings.Repeat(" ", len(valid)-len(invalidInvalid))
	if len(invalid) != len(valid) {
		t.Fatal("fixtures must have equal length")
	}
	if err := os.WriteFile(path, []byte(invalid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(root, "", nil)
	if got := catalog.Discover(""); len(got) != 0 {
		t.Fatalf("invalid log discovered: %+v", got)
	}

	if err := os.WriteFile(path, []byte(valid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := catalog.Discover(""); len(got) != 0 {
		t.Fatalf("negative result was not cached: %+v", got)
	}

	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if got := catalog.Discover(""); len(got) != 1 || got[0].ThreadID != "a" {
		t.Fatalf("changed ineligible log was not reread: %+v", got)
	}
}

func TestCatalogInvalidatesOnFileReplacement(t *testing.T) {
	root := t.TempDir()
	relative := "2026/11/25/rollout-a.jsonl"
	path := writeRollout(t, root, relative, []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "first"}},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(root, "", nil)
	if got := catalog.Discover(""); len(got) != 1 || got[0].Title != "first" {
		t.Fatalf("first discover %+v", got)
	}

	replacement := filepath.Join(filepath.Dir(path), "replacement.jsonl")
	replaced := []map[string]any{
		codexMeta("a", "legacy"),
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "other"}},
	}
	if err := os.WriteFile(replacement, dump(replaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := catalog.Discover(""); len(got) != 1 || got[0].Title != "other" {
		t.Fatalf("replacement was not detected: %+v", got)
	}
}
