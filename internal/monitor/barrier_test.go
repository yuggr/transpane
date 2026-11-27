package monitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yuggr/transpane/internal/cache"
	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/transcript"
	"github.com/yuggr/transpane/internal/translate"
)

type scriptedReader struct {
	mu     sync.Mutex
	scans  int
	reads  int
	polls  int
	scanFn func(int) (transcript.ScanResult, error)
	readFn func(int, []domain.IndexEntry) ([]domain.Message, error)
	pollFn func(int) transcript.PollResult
}

func (r *scriptedReader) ScanAll() (transcript.ScanResult, error) {
	r.mu.Lock()
	r.scans++
	call := r.scans
	fn := r.scanFn
	r.mu.Unlock()
	if fn == nil {
		return transcript.ScanResult{}, nil
	}
	return fn(call)
}

func (r *scriptedReader) ReadMessages(entries []domain.IndexEntry) ([]domain.Message, error) {
	r.mu.Lock()
	r.reads++
	call := r.reads
	fn := r.readFn
	r.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(call, entries)
}

func (r *scriptedReader) Poll() transcript.PollResult {
	r.mu.Lock()
	r.polls++
	call := r.polls
	fn := r.pollFn
	r.mu.Unlock()
	if fn == nil {
		return transcript.PollResult{}
	}
	return fn(call)
}

func (r *scriptedReader) counts() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scans, r.reads, r.polls
}

func fakeMessage(thread, id, text string) domain.Message {
	return domain.Message{ThreadID: thread, ID: id, Revision: domain.MakeRevision(text), Sequence: 0, Kind: domain.MessageKindAssistant, Original: text}
}

func fakeScan(thread string, messages ...domain.Message) transcript.ScanResult {
	entries := []domain.IndexEntry{}
	for index, message := range messages {
		entries = append(entries, domain.IndexEntry{ID: message.ID, Revision: message.Revision, Start: int64(index), End: int64(index + 1), Kind: message.Kind})
	}
	return transcript.ScanResult{ThreadID: thread, HistoryMode: stringPointer("paginated"), Entries: entries, Stats: transcript.ScanStats{Extracted: len(messages)}}
}

func stringPointer(value string) *string { return &value }

func newMonitorWithReaders(t *testing.T, cfg config.Config, transport *fakeTransport, listener *testListener, readers map[string]*scriptedReader) *Monitor {
	t.Helper()
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(transport))
	monitor := New(context.Background(), cfg, listener, Options{
		Client:       client,
		PollInterval: 10 * time.Millisecond,
		ReaderFactory: func(session domain.SessionInfo) reader {
			if instance, ok := readers[session.ThreadID]; ok {
				return instance
			}
			return &scriptedReader{}
		},
	})
	t.Cleanup(func() { _ = monitor.Close() })
	return monitor
}

func TestOldScanResultIsIgnoredAfterSwitch(t *testing.T) {
	cfg := testConfig(t)
	scanEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	readerA := &scriptedReader{scanFn: func(int) (transcript.ScanResult, error) {
		scanEntered <- struct{}{}
		<-release
		return fakeScan("a", fakeMessage("a", "a1", "A payload.")), nil
	}}
	readerB := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) {
			return fakeScan("b", fakeMessage("b", "b1", "B payload.")), nil
		},
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			return []domain.Message{fakeMessage("b", "b1", "B payload.")}, nil
		},
	}
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"a": readerA, "b": readerB})
	sessionA := domain.SessionInfo{ThreadID: "a", Provider: domain.ProviderCodex}
	sessionB := domain.SessionInfo{ThreadID: "b", Provider: domain.ProviderCodex}

	doneA := make(chan struct{})
	go func() { monitor.Start(&sessionA); close(doneA) }()
	select {
	case <-scanEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("scan A did not start")
	}
	doneB := make(chan struct{})
	go func() { monitor.Switch(sessionB); close(doneB) }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case <-doneB:
	case <-time.After(3 * time.Second):
		t.Fatal("switch B did not finish")
	}
	<-doneA
	views := monitor.Views()
	if len(views) != 1 || views[0].Message.ID != "b1" {
		t.Fatalf("stale A scan applied: %+v", views)
	}
}

func TestOldReadResultIsIgnoredAfterSwitch(t *testing.T) {
	cfg := testConfig(t)
	readEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	readerA := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) {
			return fakeScan("a", fakeMessage("a", "a1", "A payload.")), nil
		},
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			readEntered <- struct{}{}
			<-release
			return []domain.Message{fakeMessage("a", "a1", "A payload.")}, nil
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
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"a": readerA, "b": readerB})
	sessionA := domain.SessionInfo{ThreadID: "a", Provider: domain.ProviderCodex}
	sessionB := domain.SessionInfo{ThreadID: "b", Provider: domain.ProviderCodex}

	doneA := make(chan struct{})
	go func() { monitor.Start(&sessionA); close(doneA) }()
	select {
	case <-readEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("read A did not start")
	}
	doneB := make(chan struct{})
	go func() { monitor.Switch(sessionB); close(doneB) }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case <-doneB:
	case <-time.After(3 * time.Second):
		t.Fatal("switch B did not finish")
	}
	<-doneA
	views := monitor.Views()
	if len(views) != 1 || views[0].Message.ID != "b1" {
		t.Fatalf("stale A read applied: %+v", views)
	}
}

type blockingCache struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *blockingCache) Get(string) (string, bool) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		close(c.entered)
		<-c.release
	}
	return "", false
}
func (c *blockingCache) Put(string, string, string) {}
func (c *blockingCache) Status() cache.Status       { return cache.Status{Available: true} }
func (c *blockingCache) Close() error {
	if c.closed != nil {
		c.once.Do(func() { close(c.closed) })
	}
	return nil
}

func TestCancelDuringCacheLookupDoesNotPostOldGeneration(t *testing.T) {
	cfg := testConfig(t)
	readerA := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) {
			return fakeScan("a", fakeMessage("a", "a1", "Slow A payload.")), nil
		},
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			return []domain.Message{fakeMessage("a", "a1", "Slow A payload.")}, nil
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
	blocking := &blockingCache{entered: make(chan struct{}), release: make(chan struct{})}
	listener := &testListener{}
	fake := &fakeTransport{}
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client:       client,
		Cache:        blocking,
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
	select {
	case <-blocking.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cache lookup did not start")
	}
	monitor.Switch(sessionB)
	close(blocking.release)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "b1" && allSettled(views)
	})
	fake.mu.Lock()
	texts := append([]string(nil), fake.texts...)
	fake.mu.Unlock()
	for _, text := range texts {
		if strings.Contains(text, "Slow A") {
			t.Fatalf("old generation POSTed after cancel: %v", texts)
		}
	}
}

func TestOldPollResultIsIgnoredAfterSwitch(t *testing.T) {
	cfg := testConfig(t)
	pollEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	readerA := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) { return fakeScan("a", fakeMessage("a", "a1", "one")), nil },
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			return []domain.Message{fakeMessage("a", "a1", "one")}, nil
		},
		pollFn: func(call int) transcript.PollResult {
			if call == 1 {
				pollEntered <- struct{}{}
				<-release
				message := fakeMessage("a", "a2", "stale poll")
				return transcript.PollResult{Messages: []domain.Message{message}, Entries: []domain.IndexEntry{{ID: message.ID, Revision: message.Revision}}}
			}
			return transcript.PollResult{}
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
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"a": readerA, "b": readerB})
	sessionA := domain.SessionInfo{ThreadID: "a", Provider: domain.ProviderCodex}
	sessionB := domain.SessionInfo{ThreadID: "b", Provider: domain.ProviderCodex}
	monitor.Start(&sessionA)
	select {
	case <-pollEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not start")
	}
	doneB := make(chan struct{})
	go func() { monitor.Switch(sessionB); close(doneB) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case <-doneB:
	case <-time.After(3 * time.Second):
		t.Fatal("switch B did not finish")
	}
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "b1"
	})
	if len(monitor.Views()) != 1 {
		t.Fatalf("stale poll applied: %+v", monitor.Views())
	}
}

type statusCache struct {
	mu     sync.Mutex
	status cache.Status
}

func (c *statusCache) Get(string) (string, bool) {
	c.mu.Lock()
	c.status = cache.Status{Available: false, Path: "db", Message: "キャッシュDBがロックされています"}
	c.mu.Unlock()
	return "", false
}
func (c *statusCache) Put(string, string, string) {}
func (c *statusCache) Status() cache.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}
func (c *statusCache) Close() error { return nil }

func TestRevisionUpdateDuringCacheLookupDropsOldJob(t *testing.T) {
	cfg := testConfig(t)
	oldMessage := fakeMessage("t", "a1", "Old body.")
	pollGate := make(chan struct{})
	scripted := &scriptedReader{}
	scripted.scanFn = func(int) (transcript.ScanResult, error) { return fakeScan("t", oldMessage), nil }
	scripted.readFn = func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
		return []domain.Message{oldMessage}, nil
	}
	scripted.pollFn = func(call int) transcript.PollResult {
		if call == 1 {
			<-pollGate
			newMessage := fakeMessage("t", "a1", "New body.")
			return transcript.PollResult{Messages: []domain.Message{newMessage}, Entries: []domain.IndexEntry{{ID: newMessage.ID, Revision: newMessage.Revision}}}
		}
		return transcript.PollResult{}
	}
	blocking := &blockingCache{entered: make(chan struct{}), release: make(chan struct{})}
	listener := &testListener{}
	fake := &fakeTransport{}
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client: client, Cache: blocking, PollInterval: 10 * time.Millisecond,
		ReaderFactory: func(domain.SessionInfo) reader { return scripted },
	})
	t.Cleanup(func() { _ = monitor.Close() })
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	select {
	case <-blocking.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cache lookup did not start")
	}
	close(pollGate)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.Original == "New body."
	})
	close(blocking.release)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && views[0].State == domain.TranslationStateTranslated
	})
	fake.mu.Lock()
	texts := append([]string(nil), fake.texts...)
	fake.mu.Unlock()
	for _, text := range texts {
		if strings.Contains(text, "Old body.") {
			t.Fatalf("old revision POSTed after cache lookup: %v", texts)
		}
	}
	if pending := listener.lastStatus().Pending; pending != 0 {
		t.Fatalf("pending accounting after revision change: %d", pending)
	}
}

func TestOldRevisionFailureDoesNotPauseOrSendRemainingSegments(t *testing.T) {
	cfg := testConfig(t)
	oldMessage := fakeMessage("t", "a1", "Old first.\n\nOld second.")
	pollGate := make(chan struct{})
	scripted := &scriptedReader{}
	scripted.scanFn = func(int) (transcript.ScanResult, error) { return fakeScan("t", oldMessage), nil }
	scripted.readFn = func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
		return []domain.Message{oldMessage}, nil
	}
	scripted.pollFn = func(call int) transcript.PollResult {
		if call == 1 {
			<-pollGate
			newMessage := fakeMessage("t", "a1", "New body.")
			return transcript.PollResult{Messages: []domain.Message{newMessage}, Entries: []domain.IndexEntry{{ID: newMessage.ID, Revision: newMessage.Revision}}}
		}
		return transcript.PollResult{}
	}
	listener := &testListener{}
	fake := &fakeTransport{
		gate:     make(chan struct{}),
		gateText: "Old first.",
		started:  make(chan string, 8),
		handler: func(text string) (int, string) {
			if strings.Contains(text, "Old first.") {
				return 500, "{}"
			}
			return 200, ""
		},
	}
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client: client, PollInterval: 10 * time.Millisecond,
		ReaderFactory: func(domain.SessionInfo) reader { return scripted },
	})
	t.Cleanup(func() { _ = monitor.Close() })
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case text := <-fake.started:
			if strings.Contains(text, "Old first.") {
				goto startedOld
			}
		case <-deadline:
			t.Fatal("old revision POST did not start")
		}
	}
startedOld:
	close(pollGate)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.Original == "New body."
	})
	close(fake.gate)
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && allSettled(views) && views[0].State == domain.TranslationStateTranslated
	})
	fake.mu.Lock()
	texts := append([]string(nil), fake.texts...)
	fake.mu.Unlock()
	for _, text := range texts {
		if strings.Contains(text, "Old second.") {
			t.Fatalf("old revision remaining segment sent: %v", texts)
		}
	}
	if server := listener.lastStatus().Server; server == "通信失敗" {
		t.Fatalf("old revision failure paused current generation: %+v", listener.lastStatus())
	}
	if pending := listener.lastStatus().Pending; pending != 0 {
		t.Fatalf("pending accounting after old failure: %d", pending)
	}
}

func TestInitialAndRebuildReadFailureCombinations(t *testing.T) {
	cfg := testConfig(t)
	t.Run("initial-index-mismatch-rebuilds", func(t *testing.T) {
		scripted := &scriptedReader{}
		scripted.scanFn = func(call int) (transcript.ScanResult, error) {
			return fakeScan("t", fakeMessage("t", "a1", "one")), nil
		}
		scripted.readFn = func(call int, _ []domain.IndexEntry) ([]domain.Message, error) {
			if call == 1 {
				return nil, &transcript.ReadError{Reason: "index_mismatch"}
			}
			return []domain.Message{fakeMessage("t", "a1", "one")}, nil
		}
		listener := &testListener{}
		monitor := newMonitorWithReaders(t, cfg, &fakeTransport{}, listener, map[string]*scriptedReader{"t": scripted})
		session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
		monitor.Start(&session)
		waitUntil(t, 3*time.Second, func() bool {
			views := monitor.Views()
			return len(views) == 1 && views[0].Message.ID == "a1"
		})
		if scans, reads, _ := scripted.counts(); scans != 2 || reads != 2 {
			t.Fatalf("scans=%d reads=%d", scans, reads)
		}
		if !listener.noteSeen("索引不一致") {
			t.Fatal("rebuild note missing")
		}
	})

	for _, item := range []struct {
		name  string
		build func(*scriptedReader)
		note  string
	}{
		{
			name: "initial-read-error",
			build: func(scripted *scriptedReader) {
				scripted.scanFn = func(int) (transcript.ScanResult, error) { return fakeScan("t", fakeMessage("t", "a1", "one")), nil }
				scripted.readFn = func(int, []domain.IndexEntry) ([]domain.Message, error) {
					return nil, &transcript.ReadError{Reason: "read"}
				}
			},
			note: "ログを読み取れません",
		},
		{
			name: "rebuild-scan-error",
			build: func(scripted *scriptedReader) {
				scripted.scanFn = func(call int) (transcript.ScanResult, error) {
					if call == 1 {
						return fakeScan("t", fakeMessage("t", "a1", "one")), nil
					}
					return transcript.ScanResult{}, &transcript.SessionError{Message: "scan"}
				}
				scripted.readFn = func(int, []domain.IndexEntry) ([]domain.Message, error) {
					return []domain.Message{fakeMessage("t", "a1", "one")}, nil
				}
				scripted.pollFn = func(call int) transcript.PollResult {
					if call == 1 {
						return transcript.PollResult{Rebuilt: true}
					}
					return transcript.PollResult{}
				}
			},
			note: "ログを読み取れません（許可範囲外の可能性）",
		},
		{
			name: "rebuild-read-error",
			build: func(scripted *scriptedReader) {
				scripted.scanFn = func(int) (transcript.ScanResult, error) { return fakeScan("t", fakeMessage("t", "a1", "one")), nil }
				scripted.readFn = func(call int, _ []domain.IndexEntry) ([]domain.Message, error) {
					if call == 1 {
						return []domain.Message{fakeMessage("t", "a1", "one")}, nil
					}
					return nil, &transcript.ReadError{Reason: "read"}
				}
				scripted.pollFn = func(call int) transcript.PollResult {
					if call == 1 {
						return transcript.PollResult{Rebuilt: true}
					}
					return transcript.PollResult{}
				}
			},
			note: "ログを読み取れません",
		},
	} {
		t.Run(item.name, func(t *testing.T) {
			scripted := &scriptedReader{}
			item.build(scripted)
			listener := &testListener{}
			monitor := newMonitorWithReaders(t, cfg, &fakeTransport{}, listener, map[string]*scriptedReader{"t": scripted})
			session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
			monitor.Start(&session)
			waitUntil(t, 3*time.Second, func() bool { return listener.noteSeen(item.note) })
			if len(monitor.Views()) != 0 {
				t.Fatalf("failure did not reset views: %+v", monitor.Views())
			}
			scans, reads, _ := scripted.counts()
			monitor.Retry()
			monitor.AddHistory()
			time.Sleep(50 * time.Millisecond)
			afterScans, afterReads, _ := scripted.counts()
			if afterScans != scans || afterReads != reads {
				t.Fatalf("stopped monitor restarted: scans %d->%d reads %d->%d", scans, afterScans, reads, afterReads)
			}
		})
	}
}

func TestHistoryDeniedKeepsViewsAndBlocksRetry(t *testing.T) {
	cfg := testConfig(t)
	messages := []domain.Message{}
	for index := 0; index < 60; index++ {
		messages = append(messages, fakeMessage("t", fmt.Sprintf("a%02d", index), fmt.Sprintf("Message %d.", index)))
	}
	scripted := &scriptedReader{}
	scripted.scanFn = func(int) (transcript.ScanResult, error) { return fakeScan("t", messages...), nil }
	scripted.readFn = func(call int, entries []domain.IndexEntry) ([]domain.Message, error) {
		if call == 1 {
			result := []domain.Message{}
			for _, entry := range entries {
				for _, message := range messages {
					if message.ID == entry.ID {
						result = append(result, message)
					}
				}
			}
			return result, nil
		}
		return nil, &transcript.ReadError{Reason: "denied"}
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
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"t": scripted, "b": readerB})
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 50 && allSettled(views)
	})
	monitor.AddHistory()
	waitUntil(t, 3*time.Second, func() bool { return listener.noteSeen("監視停止") })
	if len(monitor.Views()) != 50 {
		t.Fatalf("history denied reset views: %d", len(monitor.Views()))
	}
	scans, reads, _ := scripted.counts()
	monitor.Retry()
	monitor.AddHistory()
	time.Sleep(50 * time.Millisecond)
	afterScans, afterReads, _ := scripted.counts()
	if afterScans != scans || afterReads != reads {
		t.Fatalf("stopped history session restarted: scans %d->%d reads %d->%d", scans, afterScans, reads, afterReads)
	}
	sessionB := domain.SessionInfo{ThreadID: "b", Provider: domain.ProviderCodex}
	monitor.Switch(sessionB)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "b1"
	})
}

func TestRuntimeCacheFailureReachesStatus(t *testing.T) {
	cfg := testConfig(t)
	message := fakeMessage("t", "a1", "Body.")
	scripted := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) { return fakeScan("t", message), nil },
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) { return []domain.Message{message}, nil },
	}
	instance := &statusCache{status: cache.Status{Available: true, Path: "db"}}
	listener := &testListener{}
	fake := &fakeTransport{}
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client: client, Cache: instance, PollInterval: 10 * time.Millisecond,
		ReaderFactory: func(domain.SessionInfo) reader { return scripted },
	})
	t.Cleanup(func() { _ = monitor.Close() })
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool {
		return strings.Contains(listener.lastStatus().CacheNote, "ロック")
	})
}

func TestCloseWaitsForShutdown(t *testing.T) {
	cfg := testConfig(t)
	instance := &blockingCache{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	listener := &testListener{}
	monitor := New(context.Background(), cfg, listener, Options{
		Client:       translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(&fakeTransport{})),
		Cache:        instance,
		PollInterval: 10 * time.Millisecond,
	})
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-instance.closed:
	default:
		t.Fatal("Close returned before cache close")
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchKeepsServerPauseUntilExplicitReconnect(t *testing.T) {
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
			return 503, "{}"
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
		return len(views) == 1 && views[0].State == domain.TranslationStateFailed && listener.lastStatus().Server == "通信失敗"
	})
	monitor.Switch(sessionB)
	time.Sleep(100 * time.Millisecond)
	views := monitor.Views()
	if len(views) != 1 || len(views[0].Translations) != 0 {
		t.Fatalf("switch resumed paused translation: %+v", views)
	}
	fake.mu.Lock()
	texts := append([]string(nil), fake.texts...)
	fake.mu.Unlock()
	for _, text := range texts {
		if strings.Contains(text, "B payload.") {
			t.Fatalf("new session posted while paused: %v", texts)
		}
	}
	monitor.Retry()
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].State == domain.TranslationStateTranslated
	})
	if fake.getCount() == 0 {
		t.Fatal("retry did not reconnect")
	}
}

func TestRebuildDuringReconnectAllowsNewRetry(t *testing.T) {
	cfg := testConfig(t)
	allowRebuild := make(chan struct{})
	scripted := &scriptedReader{}
	scripted.scanFn = func(call int) (transcript.ScanResult, error) {
		if call == 1 {
			return fakeScan("t", fakeMessage("t", "a1", "one")), nil
		}
		return fakeScan("t", fakeMessage("t", "a2", "replaced")), nil
	}
	scripted.readFn = func(call int, _ []domain.IndexEntry) ([]domain.Message, error) {
		if call == 1 {
			return []domain.Message{fakeMessage("t", "a1", "one")}, nil
		}
		return []domain.Message{fakeMessage("t", "a2", "replaced")}, nil
	}
	scripted.pollFn = func(call int) transcript.PollResult {
		if call == 1 {
			<-allowRebuild
			return transcript.PollResult{Rebuilt: true}
		}
		return transcript.PollResult{}
	}
	listener := &testListener{}
	fake := &fakeTransport{handler: func(text string) (int, string) {
		if strings.Contains(text, "one") {
			return 500, "{}"
		}
		return 200, ""
	}}
	fake.getGate = make(chan struct{})
	fake.getStarted = make(chan struct{}, 2)
	fake.getDone = make(chan struct{})
	fake.getFail = true
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form, translate.WithTransport(fake))
	monitor := New(context.Background(), cfg, listener, Options{
		Client:        client,
		PollInterval:  10 * time.Millisecond,
		ReaderFactory: func(domain.SessionInfo) reader { return scripted },
	})
	t.Cleanup(func() { _ = monitor.Close() })
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].State == domain.TranslationStateFailed
	})
	monitor.Retry()
	select {
	case <-fake.getStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("reconnect did not start")
	}
	close(allowRebuild)
	waitUntil(t, 3*time.Second, func() bool {
		return listener.noteSeen("再構築")
	})
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "a2"
	})
	close(fake.getGate)
	select {
	case <-fake.getDone:
	case <-time.After(3 * time.Second):
		t.Fatal("stale reconnect did not finish")
	}
	fake.mu.Lock()
	fake.getGate = nil
	fake.getDone = nil
	fake.getStarted = make(chan struct{}, 2)
	fake.getFail = false
	fake.mu.Unlock()
	monitor.Retry()
	waitUntil(t, 5*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "a2" && allSettled(views) && views[0].State == domain.TranslationStateTranslated
	})
}

func TestThreadChangeStopsAndSwitchRecovers(t *testing.T) {
	cfg := testConfig(t)
	changed := "other"
	readerA := &scriptedReader{scanFn: func(int) (transcript.ScanResult, error) {
		result := fakeScan("a")
		result.ThreadChanged = &changed
		return result, nil
	}}
	readerB := &scriptedReader{
		scanFn: func(int) (transcript.ScanResult, error) {
			return fakeScan("b", fakeMessage("b", "b1", "B payload.")), nil
		},
		readFn: func(_ int, _ []domain.IndexEntry) ([]domain.Message, error) {
			return []domain.Message{fakeMessage("b", "b1", "B payload.")}, nil
		},
	}
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"a": readerA, "b": readerB})
	sessionA := domain.SessionInfo{ThreadID: "a", Provider: domain.ProviderCodex}
	sessionB := domain.SessionInfo{ThreadID: "b", Provider: domain.ProviderCodex}
	monitor.Start(&sessionA)
	waitUntil(t, 3*time.Second, func() bool { return listener.noteSeen("セッションIDが変更されました") })
	if len(monitor.Views()) != 0 {
		t.Fatal("thread change retained views")
	}
	monitor.Retry()
	monitor.AddHistory()
	time.Sleep(50 * time.Millisecond)
	scans, reads, _ := readerA.counts()
	if scans != 1 || reads != 0 {
		t.Fatalf("stopped session restarted: %d %d", scans, reads)
	}
	monitor.Switch(sessionB)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "b1" && allSettled(views)
	})
}

func TestRebuildReadFailureStopsAndBlocksRetry(t *testing.T) {
	cfg := testConfig(t)
	reader := &scriptedReader{}
	reader.scanFn = func(call int) (transcript.ScanResult, error) {
		if call == 1 {
			return fakeScan("t", fakeMessage("t", "a1", "one")), nil
		}
		return fakeScan("t", fakeMessage("t", "a2", "replaced")), nil
	}
	reader.readFn = func(call int, _ []domain.IndexEntry) ([]domain.Message, error) {
		if call == 1 {
			return []domain.Message{fakeMessage("t", "a1", "one")}, nil
		}
		return nil, &transcript.ReadError{Reason: "index_mismatch"}
	}
	reader.pollFn = func(call int) transcript.PollResult {
		if call == 1 {
			return transcript.PollResult{Rebuilt: true}
		}
		return transcript.PollResult{}
	}
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"t": reader})
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	waitUntil(t, 3*time.Second, func() bool { return listener.noteSeen("索引とログの内容が一致しません") })
	scans, reads, _ := reader.counts()
	monitor.Retry()
	monitor.AddHistory()
	time.Sleep(100 * time.Millisecond)
	afterScans, afterReads, _ := reader.counts()
	if afterScans != scans || afterReads != reads {
		t.Fatalf("stopped monitor restarted: scans %d->%d reads %d->%d", scans, afterScans, reads, afterReads)
	}
	if len(monitor.Views()) != 0 {
		t.Fatal("rebuild failure did not reset views")
	}
	if !listener.noteSeen("索引とログの内容が一致しません") {
		t.Fatal("stop reason lost")
	}
}

func TestRetryAndHistoryBlockedDuringRebuild(t *testing.T) {
	cfg := testConfig(t)
	rebuildReadEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	reader := &scriptedReader{}
	reader.scanFn = func(call int) (transcript.ScanResult, error) {
		if call == 1 {
			return fakeScan("t", fakeMessage("t", "a1", "one")), nil
		}
		return fakeScan("t", fakeMessage("t", "a2", "replaced")), nil
	}
	reader.readFn = func(call int, _ []domain.IndexEntry) ([]domain.Message, error) {
		if call == 1 {
			return []domain.Message{fakeMessage("t", "a1", "one")}, nil
		}
		rebuildReadEntered <- struct{}{}
		<-release
		return []domain.Message{fakeMessage("t", "a2", "replaced")}, nil
	}
	reader.pollFn = func(call int) transcript.PollResult {
		if call == 1 {
			return transcript.PollResult{Rebuilt: true}
		}
		return transcript.PollResult{}
	}
	listener := &testListener{}
	fake := &fakeTransport{}
	monitor := newMonitorWithReaders(t, cfg, fake, listener, map[string]*scriptedReader{"t": reader})
	session := domain.SessionInfo{ThreadID: "t", Provider: domain.ProviderCodex}
	monitor.Start(&session)
	select {
	case <-rebuildReadEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("rebuild read did not start")
	}
	scans, reads, _ := reader.counts()
	monitor.Retry()
	monitor.AddHistory()
	time.Sleep(50 * time.Millisecond)
	afterScans, afterReads, _ := reader.counts()
	if afterScans != scans || afterReads != reads {
		t.Fatalf("rebuild allowed retry/history: scans %d->%d reads %d->%d", scans, afterScans, reads, afterReads)
	}
	close(release)
	waitUntil(t, 3*time.Second, func() bool {
		views := monitor.Views()
		return len(views) == 1 && views[0].Message.ID == "a2"
	})
}
