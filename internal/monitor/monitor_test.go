package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/transcript"
	"github.com/yuggr/transpane/internal/translate"
)

type testListener struct {
	mu        sync.Mutex
	resets    int
	added     []View
	prepended []View
	updated   int
	statuses  []Status
}

func (l *testListener) Reset(_ *domain.SessionInfo, _ []View) {
	l.mu.Lock()
	l.resets++
	l.mu.Unlock()
}
func (l *testListener) Added(view View) {
	l.mu.Lock()
	l.added = append(l.added, view)
	l.mu.Unlock()
}
func (l *testListener) Prepended(views []View) {
	l.mu.Lock()
	l.prepended = append(l.prepended, views...)
	l.mu.Unlock()
}
func (l *testListener) Updated(View) {
	l.mu.Lock()
	l.updated++
	l.mu.Unlock()
}
func (l *testListener) Status(status Status) {
	l.mu.Lock()
	l.statuses = append(l.statuses, status)
	l.mu.Unlock()
}
func (l *testListener) lastStatus() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.statuses) == 0 {
		return Status{}
	}
	return l.statuses[len(l.statuses)-1]
}
func (l *testListener) noteSeen(part string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, status := range l.statuses {
		if strings.Contains(status.Note, part) {
			return true
		}
	}
	return false
}

type fakeTransport struct {
	mu      sync.Mutex
	posts   int
	gets    int
	texts   []string
	handler func(text string) (status int, body string)
	gate    chan struct{}
	started chan string
}

func (f *fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		f.mu.Lock()
		f.gets++
		f.mu.Unlock()
		return response(200, `{"data":[{"id":"m"}]}`), nil
	}
	raw, _ := io.ReadAll(request.Body)
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(raw, &body)
	text := ""
	if len(body.Messages) > 1 {
		text = body.Messages[1].Content
	}
	f.mu.Lock()
	f.posts++
	f.texts = append(f.texts, text)
	gate, started := f.gate, f.started
	handler := f.handler
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- text:
		default:
		}
	}
	if gate != nil && strings.Contains(text, "Slow A") {
		<-gate
	}
	if handler != nil {
		if status, payload := handler(text); status != 200 {
			return response(status, payload), nil
		} else if payload != "" {
			return response(200, payload), nil
		}
	}
	encoded, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "訳:" + text}, "finish_reason": "stop"}},
	})
	return response(200, string(encoded)), nil
}

func (f *fakeTransport) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}
func (f *fakeTransport) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	return config.Config{
		CodexHome:          filepath.Join(root, "codex"),
		SessionsRoot:       filepath.Join(root, "codex", "sessions"),
		ClaudeHome:         filepath.Join(root, "claude"),
		ClaudeProjectsRoot: filepath.Join(root, "claude", "projects"),
		BaseURL:            "http://127.0.0.1:1234/v1",
		Model:              "m",
		Form:               "polite",
		CachePath:          filepath.Join(root, "cache", "transpane", "translations.sqlite3"),
	}
}

func codexMeta(id, mode string) map[string]any {
	return map[string]any{"timestamp": "2026-11-25T01:00:00Z", "type": "session_meta", "payload": map[string]any{"id": id, "cwd": "/tmp/demo", "history_mode": mode}}
}

func codexAgent(id, text string) map[string]any {
	return codexAgentPhase(id, text, "commentary")
}

func codexAgentPhase(id, text, phase string) map[string]any {
	return map[string]any{"timestamp": "2026-11-25T01:00:01Z", "type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "id": id, "phase": phase, "content": []any{map[string]any{"type": "Text", "text": text}}},
	}}
}

func codexUser(id, text string) map[string]any {
	return map[string]any{"timestamp": "2026-11-25T01:00:00Z", "type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "UserMessage", "id": id, "content": []any{map[string]any{"type": "text", "text": text}}},
	}}
}

func writeCodex(t *testing.T, cfg config.Config, id string, records []map[string]any) string {
	t.Helper()
	path := filepath.Join(cfg.SessionsRoot, "2026", "11", "25", "rollout-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	file.Close()
	return path
}

func appendCodex(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		_ = encoder.Encode(record)
	}
	file.Close()
}

func codexSession(t *testing.T, cfg config.Config, id string) domain.SessionInfo {
	t.Helper()
	found := transcript.DiscoverCodex(cfg.SessionsRoot)
	session, err := transcript.SelectSession(found, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func newMonitor(t *testing.T, cfg config.Config, transport http.RoundTripper, listener *testListener) *Monitor {
	t.Helper()
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(transport))
	monitor := New(context.Background(), cfg, listener, Options{Client: client, PollInterval: 20 * time.Millisecond})
	t.Cleanup(func() { _ = monitor.Close() })
	return monitor
}

func waitUntil(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func allSettled(views []View) bool {
	for _, view := range views {
		if len(view.Pending) > 0 {
			return false
		}
	}
	return true
}

func TestMonitorTranslatesPreservesOrderAndFile(t *testing.T) {
	cfg := testConfig(t)
	path := writeCodex(t, cfg, "t", []map[string]any{
		codexMeta("t", "paginated"), codexUser("u-t", "保存処理を調べて"), codexAgent("a1", "I will inspect the storage layer."), codexAgentPhase("a2", "The tests passed.", "final_answer"),
	})
	before, _ := os.ReadFile(path)
	beforeStat, _ := os.Stat(path)
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 3 && allSettled(views)
	})
	views := monitor.Views()
	if views[0].Message.ID != "u-t" || views[0].State != domain.TranslationStateSkipped || views[0].DisplayText() != "保存処理を調べて" {
		t.Fatalf("%+v", views[0])
	}
	if views[1].Message.ID != "a1" || views[1].State != domain.TranslationStateTranslated || !strings.Contains(views[1].DisplayText(), "訳:I will inspect") {
		t.Fatalf("%+v", views[1])
	}
	if views[2].Message.Kind != domain.MessageKindFinal {
		t.Fatalf("%+v", views[2])
	}
	after, _ := os.ReadFile(path)
	afterStat, _ := os.Stat(path)
	if string(before) != string(after) || !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Fatal("transcript changed")
	}
}

func TestMonitorInitialWindowAndHistory(t *testing.T) {
	cfg := testConfig(t)
	records := []map[string]any{codexMeta("t", "paginated")}
	for index := 0; index < 100; index++ {
		records = append(records, codexAgent(fmt.Sprintf("a%d", index), fmt.Sprintf("Message number %d.", index)))
	}
	path := writeCodex(t, cfg, "t", records)
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 50 && allSettled(views)
	})
	views := monitor.Views()
	if views[0].Message.ID != "a50" || views[49].Message.ID != "a99" {
		t.Fatalf("initial window %s %s", views[0].Message.ID, views[49].Message.ID)
	}
	more := []map[string]any{}
	for index := 0; index < 55; index++ {
		more = append(more, codexAgent(fmt.Sprintf("b%d", index), fmt.Sprintf("New message number %d.", index)))
	}
	appendCodex(t, path, more)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 105 && allSettled(views)
	})
	monitor.AddHistory()
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 155 && allSettled(views)
	})
	views = monitor.Views()
	if views[0].Message.ID != "a0" || views[49].Message.ID != "a49" || views[50].Message.ID != "a50" {
		t.Fatalf("history window %s %s %s", views[0].Message.ID, views[49].Message.ID, views[50].Message.ID)
	}
	seen := map[string]bool{}
	for _, view := range views {
		if seen[view.Message.ID] {
			t.Fatalf("duplicate %s", view.Message.ID)
		}
		seen[view.Message.ID] = true
	}
}

func TestMonitorSwitchDropsStaleResults(t *testing.T) {
	cfg := testConfig(t)
	writeCodex(t, cfg, "a", []map[string]any{codexMeta("a", "paginated"), codexAgent("a1", "Slow A payload.")})
	writeCodex(t, cfg, "b", []map[string]any{codexMeta("b", "paginated"), codexAgent("b1", "B payload.")})
	listener := &testListener{}
	fake := &fakeTransport{gate: make(chan struct{}), started: make(chan string, 4)}
	monitor := newMonitor(t, cfg, fake, listener)
	sessionA := codexSession(t, cfg, "a")
	sessionB := codexSession(t, cfg, "b")
	monitor.Start(&sessionA)
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow request did not start")
	}
	monitor.Switch(sessionB)
	close(fake.gate)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && strings.Contains(views[0].DisplayText(), "訳:B payload.")
	})
	views := monitor.Views()
	if views[0].Message.ID != "b1" || strings.Contains(views[0].DisplayText(), "Slow A") {
		t.Fatalf("%+v", views[0])
	}
}

func TestMonitorUnsupportedModeSendsNothing(t *testing.T) {
	cfg := testConfig(t)
	writeCodex(t, cfg, "t", []map[string]any{codexMeta("t", "chunked"), codexAgent("a1", "Secret.")})
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool { return listener.noteSeen("未対応") })
	if len(monitor.Views()) != 0 || fake.postCount() != 0 {
		t.Fatalf("views=%d posts=%d", len(monitor.Views()), fake.postCount())
	}
}

func TestMonitorRetryOnlyFailedUnits(t *testing.T) {
	cfg := testConfig(t)
	writeCodex(t, cfg, "t", []map[string]any{codexMeta("t", "paginated"), codexAgent("a1", "Retry me.")})
	listener := &testListener{}
	fail := true
	fake := &fakeTransport{handler: func(text string) (int, string) {
		if fail {
			return 500, "{}"
		}
		return 200, ""
	}}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].State == domain.TranslationStateFailed
	})
	if fake.getCount() != 0 {
		t.Fatal("reconnect happened before retry")
	}
	fail = false
	monitor.Retry()
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].State == domain.TranslationStateTranslated
	})
	if fake.postCount() != 2 || fake.getCount() == 0 {
		t.Fatalf("posts=%d gets=%d", fake.postCount(), fake.getCount())
	}
}

func TestMonitorRebuildsAfterReplacement(t *testing.T) {
	cfg := testConfig(t)
	path := writeCodex(t, cfg, "t", []map[string]any{codexMeta("t", "paginated"), codexAgent("a1", "one.")})
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views)
	})
	replacement := path + ".new"
	records := []map[string]any{codexMeta("t", "paginated"), codexAgent("a2", "replaced.")}
	file, _ := os.Create(replacement)
	encoder := json.NewEncoder(file)
	for _, record := range records {
		_ = encoder.Encode(record)
	}
	file.Close()
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "a2" && allSettled(views) && listener.noteSeen("再構築")
	})
}

func TestMonitorDeniedStopsUntilSwitch(t *testing.T) {
	cfg := testConfig(t)
	path := writeCodex(t, cfg, "t", []map[string]any{codexMeta("t", "paginated"), codexAgent("a1", "one.")})
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 5*time.Second, func() bool { return len(monitor.Views()) == 1 && allSettled(monitor.Views()) })
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	original, _ := os.ReadFile(path)
	_ = os.WriteFile(outside, original, 0o644)
	_ = os.Remove(path)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 3*time.Second, func() bool { return listener.noteSeen("監視停止") })
	posts := fake.postCount()
	if len(monitor.Views()) != 1 {
		t.Fatal("views cleared on denied")
	}
	monitor.Retry()
	time.Sleep(150 * time.Millisecond)
	if fake.postCount() != posts || len(monitor.Views()) != 1 {
		t.Fatal("stopped monitor restarted")
	}
}

func TestMonitorSameTextUsesCache(t *testing.T) {
	cfg := testConfig(t)
	writeCodex(t, cfg, "t", []map[string]any{codexMeta("t", "paginated"), codexAgent("a1", "Same."), codexAgent("a2", "Same.")})
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 2 && allSettled(views)
	})
	if fake.postCount() != 1 {
		t.Fatalf("posts=%d", fake.postCount())
	}
}

func TestMonitorSecretNotSent(t *testing.T) {
	cfg := testConfig(t)
	writeCodex(t, cfg, "t", []map[string]any{codexMeta("t", "paginated"), codexAgent("a1", "Use sk-abcdefghijklmnopqrstuvwx now.")})
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	session := codexSession(t, cfg, "t")
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool { return len(monitor.Views()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if fake.postCount() != 0 {
		t.Fatalf("secret sent: %d", fake.postCount())
	}
}
