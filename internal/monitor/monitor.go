// Package monitor owns session state, polling and translation scheduling.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuggr/transpane/internal/cache"
	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
	"github.com/yuggr/transpane/internal/transcript"
	"github.com/yuggr/transpane/internal/translate"
)

type state string

const (
	stateIdle       state = "idle"
	stateLoading    state = "loading"
	stateWatching   state = "watching"
	stateRebuilding state = "rebuilding"
	stateStopped    state = "stopped"
)

// Listener receives immutable snapshots from the monitor goroutine.
type Listener interface {
	Reset(*domain.SessionInfo, []View)
	Added(View)
	Prepended([]View)
	Updated(View)
	Status(Status)
}

// Options injects test doubles and tuning values.
type Options struct {
	Client        *translate.Client
	Cache         segmentCache
	SchedulerMax  int
	PollInterval  time.Duration
	ReaderFactory func(domain.SessionInfo) reader
}

// segmentCache is the subset of the SQLite cache used by the worker.
type segmentCache interface {
	Get(string) (string, bool)
	Put(string, string, string)
	Status() cache.Status
	Close() error
}

// reader is the bounded transcript reader used by the monitor. The concrete
// implementation is *transcript.Reader; tests may substitute a scripted one.
type reader interface {
	ScanAll() (transcript.ScanResult, error)
	Poll() transcript.PollResult
	ReadMessages([]domain.IndexEntry) ([]domain.Message, error)
}

type switchRequest struct {
	session *domain.SessionInfo
	done    chan struct{}
}

type retryRequest struct{}

type historyRequest struct{}

type permissionRequest struct {
	job   *Job
	reply chan bool
}

type closeRequest struct{}

type readerRequest struct {
	op         string
	generation uint64
	reader     reader
	entries    []domain.IndexEntry
	purpose    string
	count      int
}

type readerResponse struct {
	op         string
	generation uint64
	purpose    string
	scan       transcript.ScanResult
	messages   []domain.Message
	poll       transcript.PollResult
	err        error
	count      int
}

type translationTask struct {
	job        *Job
	reconnect  bool
	generation uint64
}

type translationResponse struct {
	job               *Job
	generation        uint64
	cacheStatus       *cache.Status
	ok                bool
	translated        string
	errKind           string
	status            int
	reconnect         bool
	reconnectOK       bool
	serverUnavailable bool
}

// Monitor owns all mutable session and queue state.
type Monitor struct {
	ctx      context.Context
	cancel   context.CancelFunc
	config   config.Config
	listener Listener

	events               chan any
	readerRequests       chan readerRequest
	readerResponses      chan readerResponse
	translationTasks     chan translationTask
	translationResponses chan translationResponse
	done                 chan struct{}
	workers              sync.WaitGroup

	client        *translate.Client
	cache         segmentCache
	readerFactory func(domain.SessionInfo) reader

	currentGeneration atomic.Uint64
	snapshotMu        sync.RWMutex
	snapshot          []View
	snapshotSession   *domain.SessionInfo

	// Owned by the event loop.
	generation          uint64
	state               state
	session             *domain.SessionInfo
	reader              reader
	entries             []domain.IndexEntry
	positions           map[string]int
	visible             int
	views               map[domain.MessageKey]*View
	order               []domain.MessageKey
	stopped             bool
	note                string
	lastUpdate          string
	scheduler           *Scheduler
	paused              bool
	connection          ConnectionState
	connectionError     string
	cacheStatus         cache.Status
	readerBusy          bool
	reconnecting        bool
	retryAfterReconnect bool
	pendingSwitch       chan struct{}
	genCtx              context.Context
	genCancel           context.CancelFunc
	pollInterval        time.Duration
}

// New starts the monitor event loop and its single reader and translation
// workers.
func New(ctx context.Context, cfg config.Config, listener Listener, options Options) *Monitor {
	child, cancel := context.WithCancel(ctx)
	client := options.Client
	if client == nil {
		client = translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form)
	}
	cacheInstance := options.Cache
	if cacheInstance == nil {
		cacheInstance = cache.Open(cfg.CachePath)
	}
	maxJobs := options.SchedulerMax
	if maxJobs <= 0 {
		maxJobs = config.MaxPendingJobs
	}
	interval := options.PollInterval
	if interval <= 0 {
		interval = time.Duration(config.PollIntervalSeconds * float64(time.Second))
	}
	monitor := &Monitor{
		ctx:                  child,
		cancel:               cancel,
		config:               cfg,
		listener:             listener,
		events:               make(chan any, 64),
		readerRequests:       make(chan readerRequest, 16),
		readerResponses:      make(chan readerResponse, 16),
		translationTasks:     make(chan translationTask, 2),
		translationResponses: make(chan translationResponse, 16),
		done:                 make(chan struct{}),
		client:               client,
		cache:                cacheInstance,
		state:                stateIdle,
		positions:            map[string]int{},
		views:                map[domain.MessageKey]*View{},
		scheduler:            NewScheduler(maxJobs),
		connection:           ConnectionUnknown,
		cacheStatus:          cacheInstance.Status(),
		pollInterval:         interval,
		readerFactory:        options.ReaderFactory,
	}
	monitor.genCtx, monitor.genCancel = context.WithCancel(child)
	monitor.lastUpdate = time.Now().Format("15:04:05")
	monitor.workers.Add(2)
	go monitor.readerLoop()
	go monitor.translationLoop() //nolint:contextcheck // worker derives from monitor context
	go monitor.loop()
	return monitor
}

// Start selects a session and waits for its initial window to load.
func (m *Monitor) Start(session *domain.SessionInfo) {
	if session != nil {
		m.Switch(*session)
	}
}

// Switch selects a new session and waits until the initial load settles.
func (m *Monitor) Switch(session domain.SessionInfo) {
	done := make(chan struct{})
	request := switchRequest{session: &session, done: done}
	select {
	case m.events <- request:
	case <-m.done:
		return
	case <-m.ctx.Done():
		return
	}
	select {
	case <-done:
	case <-m.done:
	case <-m.ctx.Done():
	}
}

// Retry reconnects when paused and resends only failed units.
func (m *Monitor) Retry() {
	select {
	case m.events <- retryRequest{}:
	case <-m.done:
	case <-m.ctx.Done():
	}
}

// AddHistory adds the previous 50 messages.
func (m *Monitor) AddHistory() {
	select {
	case m.events <- historyRequest{}:
	case <-m.done:
	case <-m.ctx.Done():
	}
}

// Views returns a deep copy of the published snapshot.
func (m *Monitor) Views() []View {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return cloneViews(m.snapshot)
}

// Session returns a copy of the current session.
func (m *Monitor) Session() *domain.SessionInfo {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	if m.snapshotSession == nil {
		return nil
	}
	copy := *m.snapshotSession
	return &copy
}

// Close stops workers and releases the cache.
func (m *Monitor) Close() error {
	select {
	case m.events <- closeRequest{}:
	case <-m.ctx.Done():
	case <-m.done:
	}
	// m.done closes only after workers have joined and the client/cache have
	// been closed by shutdown. Do not treat request receipt as completion.
	<-m.done
	return nil
}

func (m *Monitor) publish() {
	views := make([]View, 0, len(m.order))
	for _, key := range m.order {
		if view, ok := m.views[key]; ok {
			views = append(views, cloneView(*view))
		}
	}
	m.snapshotMu.Lock()
	m.snapshot = views
	if m.session != nil {
		copy := *m.session
		m.snapshotSession = &copy
	} else {
		m.snapshotSession = nil
	}
	m.snapshotMu.Unlock()
}

func (m *Monitor) emitStatus() {
	m.publish()
	status := Status{
		Server:     m.connection.Label(),
		Pending:    m.scheduler.Pending(),
		LastUpdate: m.lastUpdate,
		Note:       m.note,
		Error:      m.connectionError,
		CacheNote:  "",
		Session:    "",
	}
	if !m.cacheStatus.Available {
		status.CacheNote = m.cacheStatus.Message + "（再起動後は再翻訳が必要です）"
	}
	if m.session != nil {
		status.Session = m.session.Provider.Label() + " / " + m.session.DisplayID()
	}
	m.listener.Status(status)
}

func (m *Monitor) loop() {
	defer close(m.done)
	defer m.shutdown()
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	m.emitStatus()
	for {
		select {
		case <-m.ctx.Done():
			return
		case event := <-m.events:
			switch request := event.(type) {
			case switchRequest:
				m.handleSwitch(request)
			case retryRequest:
				m.handleRetry()
			case historyRequest:
				m.handleHistory()
			case permissionRequest:
				m.handlePermission(request)
			case closeRequest:
				return
			}
		case response := <-m.readerResponses:
			m.handleReaderResponse(response)
		case response := <-m.translationResponses:
			m.handleTranslationResponse(response)
		case <-ticker.C:
			m.pollTick()
		}
	}
}

func (m *Monitor) shutdown() {
	m.genCancel()
	m.cancel()
	if m.pendingSwitch != nil {
		close(m.pendingSwitch)
		m.pendingSwitch = nil
	}
	m.workers.Wait()
	_ = m.client.Close()
	_ = m.cache.Close()
}

func (m *Monitor) handleSwitch(request switchRequest) {
	m.finishPendingSwitch()
	oldGeneration := m.generation
	m.generation++
	m.currentGeneration.Store(m.generation)
	m.scheduler.CancelGeneration(oldGeneration)
	m.genCancel()
	m.genCtx, m.genCancel = context.WithCancel(m.ctx)
	if request.session == nil {
		close(request.done)
		return
	}
	m.session = request.session
	m.entries = nil
	m.positions = map[string]int{}
	m.views = map[domain.MessageKey]*View{}
	m.order = nil
	m.visible = 0
	m.stopped = false
	m.note = ""
	m.state = stateLoading
	m.resetGenerationTransient()
	m.lastUpdate = time.Now().Format("15:04:05")
	if m.readerFactory != nil {
		m.reader = m.readerFactory(*request.session)
	} else {
		m.reader = transcript.ReaderForSession(*request.session, m.config.SessionsRoot, m.config.ClaudeProjectsRoot)
	}
	m.pendingSwitch = request.done
	m.readerBusy = true
	m.sendReader(readerRequest{
		op:         "scan",
		generation: m.generation,
		reader:     m.reader,
		purpose:    "initial",
	})
	m.emitStatus()
}

// resetGenerationTransient clears reconnect state that belongs to the old
// generation. The paused state is intentionally kept: a new generation still
// needs a successful GET before translation resumes.
func (m *Monitor) resetGenerationTransient() {
	m.reconnecting = false
	m.retryAfterReconnect = false
}

func (m *Monitor) finishPendingSwitch() {
	if m.pendingSwitch != nil {
		close(m.pendingSwitch)
		m.pendingSwitch = nil
	}
}

func (m *Monitor) sendReader(request readerRequest) {
	select {
	case m.readerRequests <- request:
	case <-m.ctx.Done():
	}
}

func (m *Monitor) handleReaderResponse(response readerResponse) {
	if response.generation != m.generation {
		return
	}
	m.readerBusy = false
	switch response.op {
	case "scan":
		m.handleScan(response)
	case "read":
		m.handleRead(response)
	case "poll":
		m.handlePoll(response.poll)
	}
}

func (m *Monitor) handleScan(response readerResponse) {
	if response.err != nil {
		m.stop("ログを読み取れません（許可範囲外の可能性）", true)
		return
	}
	scan := response.scan
	if scan.UnsupportedMode != nil {
		m.stop(fmt.Sprintf("未対応の履歴形式: %s（本文は送信しません）", *scan.UnsupportedMode), true)
		return
	}
	if scan.ThreadChanged != nil {
		m.stop("ログのセッションIDが変更されました（自動切替しません）", true)
		return
	}
	if response.purpose == "initial" {
		if scan.Stats.Extracted == 0 {
			m.note = "対象イベント未検出（ログ形式または内部イベントのみ）"
		}
	} else if scan.Stats.Extracted == 0 {
		m.note += " / 対象イベント未検出"
	}
	selected := m.setScanEntries(scan)
	m.readerBusy = true
	m.sendReader(readerRequest{
		op:         "read",
		generation: m.generation,
		reader:     m.reader,
		entries:    selected,
		purpose:    response.purpose,
		count:      len(selected),
	})
}

func (m *Monitor) setScanEntries(scan transcript.ScanResult) []domain.IndexEntry {
	m.entries = append([]domain.IndexEntry(nil), scan.Entries...)
	m.positions = make(map[string]int, len(m.entries))
	for index, entry := range m.entries {
		m.positions[entry.ID] = index
	}
	m.visible = config.InitialMessageCount
	if m.visible > len(m.entries) {
		m.visible = len(m.entries)
	}
	return m.entries[len(m.entries)-m.visible:]
}

func (m *Monitor) handleRead(response readerResponse) {
	if response.err != nil {
		var readErr *transcript.ReadError
		if errors.As(response.err, &readErr) {
			if readErr.Reason == "index_mismatch" {
				if response.purpose == "rebuild" {
					m.stop("索引とログの内容が一致しません（監視停止）", true)
					return
				}
				m.rebuild(m.generation, "索引不一致のため再構築しました")
				return
			}
			if response.purpose == "history" {
				if readErr.Reason == "missing" {
					m.note = "ログ取得停止（ファイルが見つかりません）"
					m.emitStatus()
					return
				}
				if readErr.Reason == "read" {
					m.note = "ログを読み取れません"
					m.emitStatus()
					return
				}
				// A denied/halted/unsupported history read keeps the current
				// display; only explicit switch may recover.
				m.stop(m.readErrorNote(readErr), false)
				return
			}
			m.stop(m.readErrorNote(readErr), true)
			return
		}
		m.stop("ログを読み取れません", true)
		return
	}
	switch response.purpose {
	case "initial", "rebuild":
		m.state = stateWatching
		m.stopped = false
		m.replaceViews(response.messages)
		m.finishPendingSwitch()
		m.emitStatus()
	case "history":
		m.visible += response.count
		m.prependViews(response.messages)
		m.emitStatus()
	}
}

func (m *Monitor) replaceViews(messages []domain.Message) {
	m.views = map[domain.MessageKey]*View{}
	m.order = nil
	ordered := make([]*View, 0, len(messages))
	for _, message := range messages {
		view := newView(message)
		key := domain.MessageKey{ThreadID: message.ThreadID, ID: message.ID}
		m.views[key] = &view
		m.order = append(m.order, key)
		ordered = append(ordered, &view)
	}
	m.listener.Reset(m.session, cloneViews(m.collect()))
	for index := len(ordered) - 1; index >= 0; index-- {
		m.schedule(ordered[index], 1)
	}
}

func (m *Monitor) collect() []View {
	views := make([]View, 0, len(m.order))
	for _, key := range m.order {
		if view, ok := m.views[key]; ok {
			views = append(views, *view)
		}
	}
	return views
}

func (m *Monitor) schedule(view *View, priority int) {
	if m.stopped || view.Message.Kind == domain.MessageKindUser {
		return
	}
	units := view.Doc.TranslatableUnits()
	if len(units) == 0 {
		return
	}
	view.State = domain.TranslationStateQueued
	for _, unit := range units {
		view.Pending[unit.ID] = struct{}{}
		ctx, cancel := context.WithCancel(m.genCtx)
		job := &Job{
			Ref: domain.TaskRef{
				Session:    m.session.Key(),
				Generation: m.generation,
				Message:    domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID},
				Revision:   view.Message.Revision,
				UnitID:     unit.ID,
			},
			Segments: unit.Segments,
			Priority: priority,
			Ctx:      ctx,
			Cancel:   cancel,
		}
		m.scheduler.Submit(job)
	}
	m.updated(view)
	m.dispatch()
}

func (m *Monitor) dispatch() {
	for {
		if m.paused || m.reconnecting || m.scheduler.Inflight() != nil {
			return
		}
		job := m.scheduler.Next()
		if job == nil {
			return
		}
		if !m.refCurrent(job.Ref) {
			job.cancel()
			m.scheduler.Complete(job, false)
			continue
		}
		select {
		case m.translationTasks <- translationTask{job: job}:
			return
		case <-m.ctx.Done():
			job.cancel()
			m.scheduler.Complete(job, false)
			return
		}
	}
}

func (m *Monitor) handlePermission(request permissionRequest) {
	allowed := !m.paused && !m.reconnecting && request.job == m.scheduler.Inflight() && m.refCurrent(request.job.Ref)
	select {
	case request.reply <- allowed:
	case <-m.ctx.Done():
	}
}

func (m *Monitor) requestPermission(ctx context.Context, job *Job) bool {
	reply := make(chan bool, 1)
	select {
	case m.events <- permissionRequest{job: job, reply: reply}:
	case <-ctx.Done():
		return false
	}
	select {
	case allowed := <-reply:
		return allowed
	case <-ctx.Done():
		return false
	}
}

func (m *Monitor) handleTranslationResponse(response translationResponse) {
	if response.reconnect {
		m.handleReconnect(response)
		return
	}
	job := response.job
	if job == nil || job != m.scheduler.Inflight() {
		return
	}
	if response.cacheStatus != nil {
		if m.cacheStatus != *response.cacheStatus {
			m.cacheStatus = *response.cacheStatus
			defer m.emitStatus()
		}
	}
	if !m.refCurrent(job.Ref) {
		m.scheduler.Complete(job, false)
		job.cancel()
		m.dispatch()
		m.emitStatus()
		return
	}
	if !response.ok {
		m.scheduler.Complete(job, false)
		job.cancel()
		view := m.resultView(job.Ref)
		if view != nil {
			delete(view.Pending, job.Ref.UnitID)
			view.Failed[job.Ref.UnitID] = struct{}{}
			if response.errKind != "" && response.errKind != "cancelled" {
				view.ErrorLabels[translate.ErrorKind(response.errKind).Label()] = struct{}{}
			}
			view.RefreshState()
			m.updated(view)
		}
		if response.errKind != "cancelled" {
			m.applyFailure(response.errKind, response.status)
		}
		m.lastUpdate = time.Now().Format("15:04:05")
		m.emitStatus()
		m.dispatch()
		return
	}

	job.Results = append(job.Results, SegmentResult{OK: true, Text: response.translated})
	job.NextIndex++
	if !m.refCurrent(job.Ref) {
		m.scheduler.Complete(job, false)
		job.cancel()
		m.emitStatus()
		m.dispatch()
		return
	}
	if job.NextIndex < len(job.Segments) {
		m.scheduler.Complete(job, true)
		m.emitStatus()
		m.dispatch()
		return
	}
	m.scheduler.Complete(job, false)
	job.cancel()
	combined := ""
	for index, result := range job.Results {
		combined += result.Text + job.Segments[index].Trailing
	}
	if view := m.resultView(job.Ref); view != nil {
		delete(view.Pending, job.Ref.UnitID)
		view.Translations[job.Ref.UnitID] = combined
		view.RefreshState()
		m.updated(view)
	}
	m.connection = ConnectionConnected
	m.connectionError = ""
	m.lastUpdate = time.Now().Format("15:04:05")
	m.emitStatus()
	m.dispatch()
}

func (m *Monitor) applyFailure(kind string, status int) {
	errorKind := translate.ErrorKind(kind)
	unavailable := errorKind == translate.ErrorKindConnect || errorKind == translate.ErrorKindTimeout ||
		(errorKind == translate.ErrorKindHTTP && status >= 500)
	if unavailable {
		m.paused = true
		m.connection = ConnectionFailed
		m.connectionError = errorKind.Label()
		return
	}
	m.connection = ConnectionConnected
}

func (m *Monitor) refCurrent(ref domain.TaskRef) bool {
	if m.stopped || m.session == nil || ref.Generation != m.generation || ref.Session != m.session.Key() {
		return false
	}
	view, ok := m.views[ref.Message]
	if !ok || view.Message.Revision != ref.Revision {
		return false
	}
	for _, unit := range view.Doc.TranslatableUnits() {
		if unit.ID == ref.UnitID {
			return true
		}
	}
	return false
}

func (m *Monitor) resultView(ref domain.TaskRef) *View {
	if !m.refCurrent(ref) {
		return nil
	}
	return m.views[ref.Message]
}

func (m *Monitor) cancelRevisionJobs(threadID, messageID, revision string) {
	match := func(job *Job) bool {
		return job.Ref.Message.ThreadID == threadID && job.Ref.Message.ID == messageID && job.Ref.Revision == revision
	}
	if inflight := m.scheduler.Inflight(); inflight != nil && match(inflight) {
		inflight.cancel()
	}
	m.scheduler.DropWhere(match)
}

func (m *Monitor) handleReconnect(response translationResponse) {
	if response.generation != m.generation {
		return
	}
	m.reconnecting = false
	if response.reconnectOK {
		m.paused = false
		m.connection = ConnectionConnected
		m.connectionError = ""
		m.note = ""
		if m.retryAfterReconnect {
			m.requeueFailed()
		}
	} else {
		m.note = "翻訳サーバーへの再接続に失敗しました"
		if response.serverUnavailable {
			m.connection = ConnectionFailed
		} else {
			m.connection = ConnectionConnected
		}
		m.connectionError = translate.ErrorKind(response.errKind).Label()
	}
	m.retryAfterReconnect = false
	m.emitStatus()
	m.dispatch()
}

func (m *Monitor) requeueFailed() {
	for _, key := range m.order {
		view, ok := m.views[key]
		if !ok || view.Message.Kind == domain.MessageKindUser {
			continue
		}
		failedUnits := []protection.Unit{}
		for _, unit := range view.Doc.TranslatableUnits() {
			if _, failed := view.Failed[unit.ID]; failed {
				failedUnits = append(failedUnits, unit)
			}
		}
		if len(failedUnits) == 0 {
			continue
		}
		for _, unit := range failedUnits {
			delete(view.Failed, unit.ID)
			view.Pending[unit.ID] = struct{}{}
			ctx, cancel := context.WithCancel(m.genCtx)
			job := &Job{
				Ref: domain.TaskRef{
					Session:    m.session.Key(),
					Generation: m.generation,
					Message:    domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID},
					Revision:   view.Message.Revision,
					UnitID:     unit.ID,
				},
				Segments: unit.Segments,
				Priority: 0,
				Ctx:      ctx,
				Cancel:   cancel,
			}
			m.scheduler.Submit(job)
		}
		view.ErrorLabels = map[string]struct{}{}
		view.RefreshState()
		m.updated(view)
	}
	m.dispatch()
}

func (m *Monitor) handleRetry() {
	if m.stopped || m.state != stateWatching {
		return
	}
	if m.paused {
		if m.reconnecting {
			return
		}
		m.reconnecting = true
		m.retryAfterReconnect = true
		select {
		case m.translationTasks <- translationTask{reconnect: true, generation: m.generation}:
		case <-m.ctx.Done():
		}
		return
	}
	m.requeueFailed()
	m.emitStatus()
}

func (m *Monitor) handleHistory() {
	if m.stopped || m.state != stateWatching || m.reader == nil || m.readerBusy {
		return
	}
	if m.visible >= len(m.entries) {
		return
	}
	end := len(m.entries) - m.visible
	start := end - config.HistoryPageSize
	if start < 0 {
		start = 0
	}
	selected := m.entries[start:end]
	if len(selected) == 0 {
		return
	}
	m.readerBusy = true
	m.sendReader(readerRequest{
		op:         "read",
		generation: m.generation,
		reader:     m.reader,
		entries:    selected,
		purpose:    "history",
		count:      len(selected),
	})
}

func (m *Monitor) pollTick() {
	if m.state != stateWatching || m.stopped || m.reader == nil || m.readerBusy {
		return
	}
	m.readerBusy = true
	m.sendReader(readerRequest{
		op:         "poll",
		generation: m.generation,
		reader:     m.reader,
	})
}

func (m *Monitor) handlePoll(result transcript.PollResult) {
	if result.Denied {
		m.stop("監視停止（許可範囲外のパスです）", false)
		return
	}
	if result.Missing {
		m.note = "ログ取得停止（ファイルが見つかりません）"
		m.emitStatus()
		return
	}
	if result.Rebuilt {
		m.rebuild(m.generation, "ログが置換/縮小されたため再構築しました")
		return
	}
	if result.ThreadChanged != nil {
		m.stop("ログのセッションIDが変更されました（自動切替しません）", false)
		return
	}
	if result.UnsupportedMode != nil {
		m.stop(fmt.Sprintf("未対応の履歴形式: %s（本文は送信しません）", *result.UnsupportedMode), false)
		return
	}
	if len(result.Messages) > 0 {
		m.note = ""
		m.ingest(result.Messages, result.Entries)
	} else if m.note != "" {
		m.note = ""
	}
	m.emitStatus()
}

func (m *Monitor) ingest(messages []domain.Message, entries []domain.IndexEntry) {
	if m.stopped {
		return
	}
	count := len(messages)
	if len(entries) < count {
		count = len(entries)
	}
	for index := 0; index < count; index++ {
		message := messages[index]
		entry := entries[index]
		key := domain.MessageKey{ThreadID: message.ThreadID, ID: message.ID}
		if view, ok := m.views[key]; ok {
			if view.Message.Revision == message.Revision {
				continue
			}
			m.updateView(view, message, entry)
			continue
		}
		if _, known := m.positions[message.ID]; known {
			m.storeEntry(entry)
			continue
		}
		m.appendView(message, entry)
	}
}

func (m *Monitor) updateView(view *View, message domain.Message, entry domain.IndexEntry) {
	m.cancelRevisionJobs(view.Message.ThreadID, view.Message.ID, view.Message.Revision)
	view.Message = message
	view.Doc = protection.ProtectDocument(message.Original)
	view.Translations = map[int]string{}
	view.Failed = map[int]struct{}{}
	view.Pending = map[int]struct{}{}
	view.ErrorLabels = map[string]struct{}{}
	if message.Kind == domain.MessageKindUser {
		view.State = domain.TranslationStateSkipped
	} else {
		view.State = initialState(view.Doc)
	}
	view.RefreshState()
	m.storeEntry(entry)
	m.updated(view)
	m.schedule(view, 0)
}

func (m *Monitor) appendView(message domain.Message, entry domain.IndexEntry) {
	view := newView(message)
	key := domain.MessageKey{ThreadID: message.ThreadID, ID: message.ID}
	m.views[key] = &view
	m.order = append(m.order, key)
	m.storeEntry(entry)
	m.visible++
	m.updated(&view)
	m.schedule(&view, 0)
}

func (m *Monitor) prependViews(messages []domain.Message) {
	added := []*View{}
	for _, message := range messages {
		key := domain.MessageKey{ThreadID: message.ThreadID, ID: message.ID}
		if _, ok := m.views[key]; ok {
			continue
		}
		view := newView(message)
		m.views[key] = &view
		added = append(added, &view)
	}
	if len(added) == 0 {
		return
	}
	newOrder := make([]domain.MessageKey, 0, len(added)+len(m.order))
	views := make([]View, 0, len(added))
	for _, view := range added {
		newOrder = append(newOrder, domain.MessageKey{ThreadID: view.Message.ThreadID, ID: view.Message.ID})
		views = append(views, *view)
	}
	m.order = append(newOrder, m.order...)
	m.listener.Prepended(cloneViews(views))
	m.publish()
	for _, view := range added {
		m.schedule(view, 2)
	}
}

func (m *Monitor) storeEntry(entry domain.IndexEntry) {
	if position, ok := m.positions[entry.ID]; ok {
		m.entries[position] = entry
		return
	}
	m.positions[entry.ID] = len(m.entries)
	m.entries = append(m.entries, entry)
}

func (m *Monitor) updated(view *View) {
	view.RefreshState()
	m.listener.Updated(cloneView(*view))
	m.publish()
}

func (m *Monitor) clearPending() {
	for _, view := range m.views {
		if len(view.Pending) == 0 {
			continue
		}
		view.Pending = map[int]struct{}{}
		view.RefreshState()
		m.listener.Updated(cloneView(*view))
	}
	m.publish()
}

func (m *Monitor) rebuild(generation uint64, note string) {
	if generation != m.generation || m.session == nil || m.reader == nil {
		return
	}
	oldGeneration := m.generation
	m.generation++
	m.currentGeneration.Store(m.generation)
	m.scheduler.CancelGeneration(oldGeneration)
	m.genCancel()
	m.genCtx, m.genCancel = context.WithCancel(m.ctx)
	m.resetGenerationTransient()
	m.clearPending()
	m.stopped = true
	m.state = stateRebuilding
	m.note = note
	m.readerBusy = true
	m.emitStatus()
	m.sendReader(readerRequest{
		op:         "scan",
		generation: m.generation,
		reader:     m.reader,
		purpose:    "rebuild",
	})
}

func (m *Monitor) stop(note string, reset bool) {
	oldGeneration := m.generation
	m.generation++
	m.currentGeneration.Store(m.generation)
	m.scheduler.CancelGeneration(oldGeneration)
	m.genCancel()
	m.genCtx, m.genCancel = context.WithCancel(m.ctx)
	m.resetGenerationTransient()
	m.clearPending()
	m.stopped = true
	m.state = stateStopped
	m.note = note
	if reset {
		m.views = map[domain.MessageKey]*View{}
		m.order = nil
		m.entries = nil
		m.positions = map[string]int{}
		m.visible = 0
		m.listener.Reset(m.session, []View{})
	}
	m.finishPendingSwitch()
	m.emitStatus()
}

func (m *Monitor) readErrorNote(err *transcript.ReadError) string {
	switch err.Reason {
	case "denied":
		return "監視停止（許可範囲外のパスです）"
	case "halted":
		return "ログのセッションIDが変更されました（自動切替しません）"
	case "missing":
		return "ログを読み取れません（ファイルが見つかりません）"
	default:
		return "ログを読み取れません"
	}
}

func (m *Monitor) readerLoop() {
	defer m.workers.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case request := <-m.readerRequests:
			if request.generation != m.currentGeneration.Load() {
				m.respondReader(readerResponse{op: request.op, generation: request.generation, purpose: request.purpose, count: request.count})
				continue
			}
			response := readerResponse{op: request.op, generation: request.generation, purpose: request.purpose, count: request.count}
			switch request.op {
			case "scan":
				scan, err := request.reader.ScanAll()
				response.scan = scan
				response.err = err
			case "read":
				messages, err := request.reader.ReadMessages(request.entries)
				response.messages = messages
				response.err = err
			case "poll":
				response.poll = request.reader.Poll()
			}
			m.respondReader(response)
		}
	}
}

func (m *Monitor) respondReader(response readerResponse) {
	select {
	case m.readerResponses <- response:
	case <-m.ctx.Done():
	}
}

func (m *Monitor) translationLoop() {
	defer m.workers.Done()
	memo := map[string]string{}
	for {
		select {
		case <-m.ctx.Done():
			return
		case task := <-m.translationTasks:
			if task.reconnect {
				ctx, cancel := context.WithTimeout(m.ctx, time.Duration(config.TotalTimeoutSeconds*float64(time.Second)))
				_, err := m.client.ListModels(ctx)
				cancel()
				response := translationResponse{reconnect: true, generation: task.generation, reconnectOK: err == nil}
				if err != nil {
					var failure *translate.Failure
					if errors.As(err, &failure) {
						response.errKind = string(failure.Kind)
						response.status = failure.Status
						response.serverUnavailable = failure.ServerUnavailable()
					} else {
						response.errKind = string(translate.ErrorKindConnect)
						response.serverUnavailable = true
					}
				}
				m.respondTranslation(response)
				continue
			}
			response := m.translateSegment(m.ctx, task.job, memo)
			m.respondTranslation(response)
		}
	}
}

func (m *Monitor) translateSegment(fallback context.Context, job *Job, memo map[string]string) (response translationResponse) {
	response = translationResponse{job: job}
	defer func() {
		status := m.cache.Status()
		response.cacheStatus = &status
	}()
	if job.NextIndex >= len(job.Segments) {
		response.errKind = string(translate.ErrorKindCancelled)
		return response
	}
	segment := job.Segments[job.NextIndex]
	key := cache.Key(m.config.BaseURL, m.config.Model, m.config.Form, segment.Text)
	ctx := job.Ctx //nolint:contextcheck // per-generation context from schedule()
	if ctx == nil {
		ctx = fallback
	}
	if err := ctx.Err(); err != nil {
		response.errKind = string(translate.ErrorKindCancelled)
		return response
	}
	if translated, ok := memo[key]; ok && protection.ValidateSegmentTranslation(translated, segment.Markers) {
		response.ok = true
		response.translated = translated
		return response
	}
	if cached, ok := m.cache.Get(key); ok && protection.ValidateSegmentTranslation(cached, segment.Markers) {
		memo[key] = cached
		response.ok = true
		response.translated = cached
		return response
	}
	if err := ctx.Err(); err != nil {
		response.errKind = string(translate.ErrorKindCancelled)
		return response
	}
	if !m.requestPermission(ctx, job) {
		response.errKind = string(translate.ErrorKindCancelled)
		return response
	}
	if err := ctx.Err(); err != nil {
		response.errKind = string(translate.ErrorKindCancelled)
		return response
	}
	translated, err := m.client.Translate(ctx, segment.Text)
	if err != nil {
		var failure *translate.Failure
		if errors.As(err, &failure) {
			response.errKind = string(failure.Kind)
			response.status = failure.Status
		} else {
			response.errKind = string(translate.ErrorKindConnect)
		}
		return response
	}
	if !protection.ValidateSegmentTranslation(translated, segment.Markers) {
		response.errKind = string(translate.ErrorKindInvalid)
		return response
	}
	memo[key] = translated
	if ctx.Err() == nil {
		m.cache.Put(key, translated, timestampNow())
	}
	response.ok = true
	response.translated = translated
	return response
}

// timestampNow matches Python datetime.now(UTC).isoformat(): six fractional
// digits when microseconds are non-zero, otherwise no fraction.
func timestampNow() string {
	now := time.Now().UTC()
	if now.Nanosecond()/1000 == 0 {
		return now.Format("2006-01-02T15:04:05-07:00")
	}
	return now.Format("2006-01-02T15:04:05.000000-07:00")
}

func (m *Monitor) respondTranslation(response translationResponse) {
	select {
	case m.translationResponses <- response:
	case <-m.ctx.Done():
	}
}
