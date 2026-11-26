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

	"github.com/yuggr/transpane/internal/cache"
	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
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
	mu         sync.Mutex
	posts      int
	gets       int
	texts      []string
	handler    func(text string) (status int, body string)
	gate       chan struct{}
	gateText   string
	started    chan string
	getGate    chan struct{}
	getStarted chan struct{}
	getDone    chan struct{}
	getFail    bool
}

func (f *fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		f.mu.Lock()
		f.gets++
		started, gate, done, fail := f.getStarted, f.getGate, f.getDone, f.getFail
		f.mu.Unlock()
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		if gate != nil {
			<-gate
		}
		if done != nil {
			close(done)
		}
		if fail {
			return response(500, "{}"), nil
		}
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
	block := "Slow A"
	if f.gateText != "" {
		block = f.gateText
	}
	if gate != nil && strings.Contains(text, block) {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	_ = file.Close()
	return path
}

func appendCodex(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		_ = encoder.Encode(record)
	}
	_ = file.Close()
}

func writeClaude(t *testing.T, cfg config.Config, id, text string) string {
	t.Helper()
	path := filepath.Join(cfg.ClaudeProjectsRoot, "demo", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"type": "assistant", "sessionId": id, "uuid": "u-1",
		"timestamp": "2026-11-25T03:00:00Z", "cwd": "/tmp/demo", "version": "2.1.285",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}},
	}
	file, err := os.Create(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(file).Encode(record)
	_ = file.Close()
	return path
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
	before, _ := os.ReadFile(path) //nolint:gosec // test-controlled temp path
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
	after, _ := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	afterStat, _ := os.Stat(path)
	if string(before) != string(after) || !beforeStat.ModTime().Equal(afterStat.ModTime()) || beforeStat.Mode() != afterStat.Mode() {
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
	file, _ := os.Create(replacement) //nolint:gosec // test-controlled temp path
	encoder := json.NewEncoder(file)
	for _, record := range records {
		_ = encoder.Encode(record)
	}
	_ = file.Close()
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
	original, _ := os.ReadFile(path)           //nolint:gosec // test-controlled temp path
	_ = os.WriteFile(outside, original, 0o600) //nolint:gosec // test-controlled temp path
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

func TestMonitorSameIDAcrossProvidersAndBack(t *testing.T) {
	cfg := testConfig(t)
	shared := "88888888-8888-4888-8888-888888888888"
	writeCodex(t, cfg, shared, []map[string]any{codexMeta(shared, "paginated"), codexAgent("a1", "A payload.")})
	writeClaude(t, cfg, shared, "B payload.")
	sessions := transcript.DiscoverAll(cfg.SessionsRoot, cfg.ClaudeProjectsRoot, nil)
	codexProvider, claudeProvider := domain.ProviderCodex, domain.ProviderClaude
	codexSessionInfo, err := transcript.SelectSession(sessions, "codex:"+shared, &codexProvider)
	if err != nil {
		t.Fatal(err)
	}
	claudeSessionInfo, err := transcript.SelectSession(sessions, "claude:"+shared, &claudeProvider)
	if err != nil {
		t.Fatal(err)
	}
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitor(t, cfg, fake, listener)
	monitor.Start(&codexSessionInfo)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && strings.Contains(views[0].DisplayText(), "A payload.")
	})
	monitor.Switch(claudeSessionInfo)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && views[0].Message.ThreadID == shared && strings.Contains(views[0].DisplayText(), "B payload.")
	})
	monitor.Switch(codexSessionInfo)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && strings.Contains(views[0].DisplayText(), "A payload.")
	})
	views := monitor.Views()
	if strings.Contains(views[0].DisplayText(), "B payload.") {
		t.Fatal("stale B applied after A")
	}
}

func TestMonitorSwitchBackDropsInflightB(t *testing.T) {
	cfg := testConfig(t)
	writeCodex(t, cfg, "a", []map[string]any{codexMeta("a", "paginated"), codexAgent("a1", "A payload.")})
	writeCodex(t, cfg, "b", []map[string]any{codexMeta("b", "paginated"), codexAgent("b1", "Slow B payload.")})
	listener := &testListener{}
	fake := &fakeTransport{gate: make(chan struct{}), gateText: "Slow B", started: make(chan string, 8)}
	monitor := newMonitor(t, cfg, fake, listener)
	sessionA := codexSession(t, cfg, "a")
	sessionB := codexSession(t, cfg, "b")
	monitor.Start(&sessionA)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views)
	})
	monitor.Switch(sessionB)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case text := <-fake.started:
			if strings.Contains(text, "Slow B") {
				goto startedB
			}
		case <-deadline:
			t.Fatal("B request did not start")
		}
	}
startedB:
	monitor.Switch(sessionA)
	close(fake.gate)
	time.Sleep(150 * time.Millisecond)
	views := monitor.Views()
	if len(views) != 1 || strings.Contains(views[0].DisplayText(), "Slow B") || !strings.Contains(views[0].DisplayText(), "A payload.") {
		t.Fatalf("%+v", views)
	}
}

func TestStaleReconnectResultIgnoredAfterSwitch(t *testing.T) {
	cfg := testConfig(t)
	readerA := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) { return fakeScan("a", fakeMessage("a", "a1", "Fail.")), nil },
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			return []domain.Message{fakeMessage("a", "a1", "Fail.")}, nil
		},
	}
	readerB := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) {
			return fakeScan("b", fakeMessage("b", "b1", "B payload.")), nil
		},
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			return []domain.Message{fakeMessage("b", "b1", "B payload.")}, nil
		},
	}
	listener := &testListener{}
	fake := &fakeTransport{handler: func(text string) (int, string) {
		if strings.Contains(text, "Fail.") {
			return 500, "{}"
		}
		return 200, ""
	}}
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client:       client,
		PollInterval: 10 * time.Millisecond,
		ReaderFactory: func(session domain.SessionInfo) reader {
			if session.ThreadID == "a" {
				return readerA
			}
			return readerB
		},
	})
	t.Cleanup(func() { _ = monitor.Close() })
	sessionA := domain.SessionInfo{ThreadID: "a", Provider: domain.ProviderCodex}
	sessionB := domain.SessionInfo{ThreadID: "b", Provider: domain.ProviderCodex}
	monitor.Start(&sessionA)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].State == domain.TranslationStateFailed
	})
	fake.mu.Lock()
	fake.getGate = make(chan struct{})
	fake.getStarted = make(chan struct{}, 1)
	fake.getDone = make(chan struct{})
	fake.getFail = true
	fake.mu.Unlock()
	monitor.Retry()
	select {
	case <-fake.getStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("reconnect did not start")
	}
	monitor.Switch(sessionB)
	close(fake.getGate)
	select {
	case <-fake.getDone:
	case <-time.After(3 * time.Second):
		t.Fatal("reconnect did not finish")
	}
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views)
	})
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(listener.lastStatus().Note, "再接続") {
		t.Fatalf("stale reconnect changed new session note: %+v", listener.lastStatus())
	}
}

func TestMonitorInvalidMarkerIsNotCached(t *testing.T) {
	cfg := testConfig(t)
	message := fakeMessage("t", "a1", "Read `a.py` now.")
	scripted := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) { return fakeScan("t", message), nil },
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) { return []domain.Message{message}, nil },
	}
	listener := &testListener{}
	fake := &fakeTransport{handler: func(string) (int, string) {
		return 200, `{"choices":[{"message":{"content":"マーカーなし"},"finish_reason":"stop"}]}`
	}}
	instance := cache.Open(cfg.CachePath)
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client: client, Cache: instance, PollInterval: 10 * time.Millisecond,
		ReaderFactory: func(domain.SessionInfo) reader { return scripted },
	})
	t.Cleanup(func() { _ = monitor.Close() })
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].State == domain.TranslationStateFailed
	})
	doc := protection.ProtectDocument(message.Original)
	segment := doc.TranslatableUnits()[0].Segments[0].Text
	if _, ok := instance.Get(cache.Key(cfg.BaseURL, cfg.Model, cfg.Form, segment)); ok {
		t.Fatal("invalid marker output was cached")
	}
}

func TestMonitorFourHundredAndFiveHundredConnectionState(t *testing.T) {
	for _, item := range []struct {
		status int
		server string
	}{
		{status: 400, server: "接続成功"},
		{status: 503, server: "通信失敗"},
	} {
		t.Run(fmt.Sprintf("status-%d", item.status), func(t *testing.T) {
			cfg := testConfig(t)
			message := fakeMessage("t", "a1", "Body.")
			scripted := &scriptedReader{
				scanFn: func(int) (transcript.ScanResult, error) { return fakeScan("t", message), nil },
				readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) { return []domain.Message{message}, nil },
			}
			listener := &testListener{}
			fake := &fakeTransport{handler: func(string) (int, string) { return item.status, "{}" }}
			monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"t": scripted})
			session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
			monitor.Start(&session)
			waitUntil(t, 3*time.Second, func() bool {
				views := monitor.Views()
				return len(views) == 1 && views[0].State == domain.TranslationStateFailed
			})
			if got := listener.lastStatus().Server; got != item.server {
				t.Fatalf("server state %q want %q", got, item.server)
			}
		})
	}
}

func TestMonitorPartialUnitFailure(t *testing.T) {
	cfg := testConfig(t)
	message := fakeMessage("t", "a1", "First line.\n\nSecond line.")
	scripted := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) { return fakeScan("t", message), nil },
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) { return []domain.Message{message}, nil },
	}
	listener := &testListener{}
	fake := &fakeTransport{handler: func(text string) (int, string) {
		if strings.Contains(text, "Second") {
			return 503, "{}"
		}
		return 200, ""
	}}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"t": scripted})
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && len(views[0].Failed) == 1
	})
	view := monitor.Views()[0]
	if view.State != domain.TranslationStatePartial || len(view.Translations) != 1 || len(view.Failed) != 1 {
		t.Fatalf("partial state %+v", view)
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
