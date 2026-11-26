package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuggr/transpane/internal/domain"
)

func legacyUser(text string) map[string]any {
	return map[string]any{"timestamp": "2026-11-25T01:00:01Z", "type": "event_msg", "payload": map[string]any{"type": "user_message", "message": text}}
}

func paginatedAgent(id, text string) map[string]any {
	return map[string]any{"timestamp": "2026-11-25T01:00:02Z", "type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "id": id, "phase": "commentary", "content": []any{map[string]any{"type": "Text", "text": text}}},
	}}
}

func appendLines(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(dump(records)); err != nil {
		t.Fatal(err)
	}
}

func TestScanAndReadMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-a.jsonl")
	records := []map[string]any{
		codexMeta("thread-1", "legacy"),
		legacyUser("保存処理を調べて"),
		{"type": "response_item", "payload": map[string]any{"type": "message"}},
		{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": "I will look."}},
	}
	if err := os.WriteFile(path, dump(records), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(path, "thread-1", ReaderOptions{})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	if result.HistoryMode == nil || *result.HistoryMode != "legacy" || result.Stats.Records != 4 || result.Stats.Extracted != 2 || result.Stats.Ignored != 1 {
		t.Fatalf("%+v", result)
	}
	if len(result.Entries) != 2 || result.Entries[0].Kind != domain.MessageKindUser || result.Entries[1].Kind != domain.MessageKindAssistant {
		t.Fatalf("%+v", result.Entries)
	}
	messages, err := reader.ReadMessages(result.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Original != "保存処理を調べて" || messages[1].Original != "I will look." || messages[0].Sequence != result.Entries[0].Start {
		t.Fatalf("%+v", messages)
	}
}

func TestIncrementalWaitsForCompleteLineAndUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-a.jsonl")
	if err := os.WriteFile(path, dump([]map[string]any{codexMeta("t", "legacy"), legacyUser("first")}), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(path, "t", ReaderOptions{})
	if _, err := reader.ScanAll(); err != nil {
		t.Fatal(err)
	}
	payload := dump([]map[string]any{legacyUser("second")})
	half := len(payload) / 2
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = file.Write(payload[:half])
	file.Close()
	if polled := reader.Poll(); len(polled.Messages) != 0 {
		t.Fatal("partial line parsed")
	}
	file, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = file.Write(payload[half:])
	file.Close()
	polled := reader.Poll()
	if len(polled.Messages) != 1 || polled.Messages[0].Original != "second" {
		t.Fatalf("%+v", polled)
	}
	if len(reader.Poll().Messages) != 0 {
		t.Fatal("duplicate poll")
	}

	payload = dump([]map[string]any{legacyUser("日本語の途中")})
	cut := len(payload)/2 - 1
	file, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = file.Write(payload[:cut])
	file.Close()
	_ = reader.Poll()
	file, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = file.Write(payload[cut:])
	file.Close()
	polled = reader.Poll()
	if len(polled.Messages) != 1 || polled.Messages[0].Original != "日本語の途中" {
		t.Fatalf("%+v", polled)
	}
}

func TestInvalidAndOversizedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-a.jsonl")
	body := dump([]map[string]any{codexMeta("t", "legacy")})
	body = append(body, []byte(`{"type": "event_msg", "payload": {"type": "agent_message", "message": "`+strings.Repeat("x", 400)+`"}}`+"\n")...)
	body = append(body, []byte("{invalid json\n")...)
	body = append(body, dump([]map[string]any{legacyUser("after")})...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(path, "t", ReaderOptions{MaxLineBytes: 300})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.OversizedLines != 1 || result.Stats.InvalidLines != 1 || result.Stats.Extracted != 1 {
		t.Fatalf("%+v", result.Stats)
	}
	messages, err := reader.ReadMessages(result.Entries)
	if err != nil || len(messages) != 1 || messages[0].Original != "after" {
		t.Fatalf("%+v %v", messages, err)
	}
}

func TestUnsupportedModeAndThreadChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-a.jsonl")
	if err := os.WriteFile(path, dump([]map[string]any{codexMeta("t", "chunked"), legacyUser("secret")}), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(path, "t", ReaderOptions{})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	if result.UnsupportedMode == nil || *result.UnsupportedMode != "'chunked'" || result.HistoryMode != nil || len(result.Entries) != 0 {
		t.Fatalf("%+v", result)
	}

	path2 := filepath.Join(t.TempDir(), "rollout-b.jsonl")
	if err := os.WriteFile(path2, dump([]map[string]any{codexMeta("t", "legacy")}), 0o644); err != nil {
		t.Fatal(err)
	}
	reader2 := NewReader(path2, "t", ReaderOptions{})
	if _, err := reader2.ScanAll(); err != nil {
		t.Fatal(err)
	}
	appendLines(t, path2, []map[string]any{codexMeta("other", "legacy"), legacyUser("should not appear")})
	polled := reader2.Poll()
	if polled.ThreadChanged == nil || *polled.ThreadChanged != "other" || len(polled.Messages) != 0 {
		t.Fatalf("%+v", polled)
	}
}

func TestMissingRebuiltAndShrink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-a.jsonl")
	if err := os.WriteFile(path, dump([]map[string]any{codexMeta("t", "legacy"), legacyUser("one")}), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(path, "t", ReaderOptions{})
	if _, err := reader.ScanAll(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	if !reader.Poll().Missing {
		t.Fatal("missing not detected")
	}
	if err := os.WriteFile(path, dump([]map[string]any{codexMeta("t", "legacy"), legacyUser("one")}), 0o644); err != nil {
		t.Fatal(err)
	}
	reader = NewReader(path, "t", ReaderOptions{})
	if _, err := reader.ScanAll(); err != nil {
		t.Fatal(err)
	}
	appendLines(t, path, []map[string]any{legacyUser("two")})
	replacement := filepath.Join(dir, "rollout-b.jsonl")
	if err := os.WriteFile(replacement, dump([]map[string]any{codexMeta("t", "legacy"), legacyUser("replaced")}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if !reader.Poll().Rebuilt {
		t.Fatal("replacement not detected")
	}
	path3 := filepath.Join(dir, "rollout-c.jsonl")
	if err := os.WriteFile(path3, dump([]map[string]any{codexMeta("t", "legacy"), legacyUser("one"), legacyUser("two")}), 0o644); err != nil {
		t.Fatal(err)
	}
	reader3 := NewReader(path3, "t", ReaderOptions{})
	if _, err := reader3.ScanAll(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path3, dump([]map[string]any{codexMeta("t", "legacy")}), 0o644); err != nil {
		t.Fatal(err)
	}
	if !reader3.Poll().Rebuilt {
		t.Fatal("shrink not detected")
	}
}

func claudeAssistant(text, uuid string) map[string]any {
	return map[string]any{
		"type": "assistant", "sessionId": claudeSession, "uuid": uuid,
		"timestamp": "2026-11-25T03:00:00Z",
		"message":   map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}},
	}
}

func TestClaudeReaderDedupesAndVerifies(t *testing.T) {
	root := t.TempDir()
	path := writeClaude(t, root, "demo", claudeSession, []map[string]any{
		claudeAssistant("first", "u-1"),
		claudeAssistant("duplicate", "u-1"),
		claudeAssistant("updated", "u-1"),
		claudeAssistant("other", "u-2"),
	})
	offsets := []int64{}
	var position int64
	raw, _ := os.ReadFile(path)
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if line == "" {
			continue
		}
		offsets = append(offsets, position)
		position += int64(len(line))
	}
	reader := NewReader(path, claudeSession, ReaderOptions{Provider: domain.ProviderClaude})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || result.Entries[0].ID != "u-1" || result.Entries[0].Start != offsets[2] || result.Entries[1].ID != "u-2" {
		t.Fatalf("%+v", result.Entries)
	}
	messages, err := reader.ReadMessages(result.Entries)
	if err != nil || messages[0].Original != "updated" || messages[1].Original != "other" {
		t.Fatalf("%+v %v", messages, err)
	}

}

func TestClaudeSameSizeRewriteDetectedOnReadback(t *testing.T) {
	root := t.TempDir()
	path := writeClaude(t, root, "demo", claudeSession, []map[string]any{claudeAssistant("one", "u-1")})
	reader := NewReader(path, claudeSession, ReaderOptions{Provider: domain.ProviderClaude})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	_ = writeClaude(t, root, "demo", claudeSession, []map[string]any{claudeAssistant("two", "u-1")})
	after, _ := os.Stat(path)
	if before.Size() != after.Size() {
		t.Fatalf("test fixture sizes differ: %d %d", before.Size(), after.Size())
	}
	if len(reader.Poll().Messages) != 0 {
		t.Fatal("poll returned rewritten content")
	}
	if _, err := reader.ReadMessages(result.Entries); err == nil || err.(*ReadError).Reason != "index_mismatch" {
		t.Fatalf("readback error %v", err)
	}
}

func TestClaudeMismatchAndSymlinks(t *testing.T) {
	root := t.TempDir()
	path := writeClaude(t, root, "demo", claudeSession, []map[string]any{
		claudeRecord(claudeSession, "ok", nil),
		claudeRecord("other", "other", nil),
	})
	reader := NewReader(path, claudeSession, ReaderOptions{Provider: domain.ProviderClaude})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadChanged == nil || *result.ThreadChanged != "other" {
		t.Fatalf("%+v", result)
	}
	if _, err := reader.ReadMessages(result.Entries); err == nil || err.(*ReadError).Reason != "halted" {
		t.Fatalf("read error %v", err)
	}

	valid := writeClaude(t, root, "demo", "66666666-6666-4666-8666-666666666666", []map[string]any{claudeRecord("66666666-6666-4666-8666-666666666666", "ok", nil)})
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "demo", "77777777-7777-4777-8777-777777777777.jsonl")
	_ = os.Symlink(outside, link)
	if _, err := NewReader(link, "77777777-7777-4777-8777-777777777777", ReaderOptions{Provider: domain.ProviderClaude, AllowedRoot: root}).ScanAll(); err == nil {
		t.Fatal("symlink accepted")
	}
	_ = valid
	_ = json.Marshal
}
