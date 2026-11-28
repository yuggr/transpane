# Transpane Go Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Python/Textual Transpane runtime with one CGO-free Go binary that preserves the externally observable contracts of the Python baseline at `f199bb6`.

**Architecture:** A one-directional Go module: `cmd -> cli -> tui/monitor -> transcript/protection/cache/translate -> domain/config`. The monitor owns all mutable session/queue state in one event loop; reader and translation workers perform I/O and return typed, generation-tagged events. The Python tree remains untouched as the oracle until Phase 6 approval.

**Tech Stack:** Go 1.27.4, `charm.land/bubbletea/v2` v2.2.0, `charm.land/bubbles/v2` v2.2.2, `charm.land/lipgloss/v2` v2.1.1, `github.com/yuin/goldmark` v1.8.7, `github.com/pelletier/go-toml/v2` v2.4.3, `modernc.org/sqlite` v1.63.0.

**Spec:** `docs/superpowers/specs/transpane-go-migration-design-v1.0.md`

## Global Constraints

- Baseline is commit `f199bb6cce011c80d28f7cdfe91929d62d30c08f` on branch `refactor/reduce-complexity`; the Python tree is read-only oracle work and must not be modified or deleted before Phase 6 approval.
- All paths, messages, env vars, TOML keys, exit codes, key bindings and state transitions follow the spec §5–§10 verbatim.
- No new features, no PTY/agent launching, no network destinations beyond HTTP IP-literal loopback/RFC1918 with path `/v1`, no proxy, no redirects, no auth, no cache schema change, no cache file deletion or chmod repair.
- Transcript files are read-only: never write, create, move, chmod, or touch their mtime. Real logs, cache DBs, configs and credentials never enter git or fixtures.
- Every request/result carries the session key and generation; stale results never mutate display, pause, note, queue, or connection state.
- Segment limit is 1000 Unicode code points. Cache key is byte-compatible with Python `json.dumps(list, ensure_ascii=False)` + SHA-256 lowercase hex. `created_at` is RFC3339 UTC.
- Release build is `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`; tests use `CGO_ENABLED=1 -race` where supported.
- No `@latest` in CI or reproduction commands. No Python/uv requirement at runtime or in normal Go CI.
- G01–G25 must be evidenced by automated tests; G22/G25 real-terminal checks and G26 real WSL/Codex/Claude/PLaMo checks are recorded as NOT RUN when no real environment is available, never as PASS.

## Review Focus

- A transcript containing a single 9 MiB line, invalid UTF-8, or a truncated final line must not allocate unbounded memory, panic, or translate partial content; the reader must resume after the next newline.
- An HTTP response that omits, duplicates or reorders a `@@CJn@@` marker must leave the original line visible and must not write the cache.
- A session switch while a cache lookup or POST is in flight must never start a new POST for the old generation nor apply the old result to the new session.
- Two files with the same ID (same provider different path, or Codex vs Claude) must stay distinct in the TUI, while `inspect`/`--session` reject the ambiguity; a third switch back after A→B→A must not resurrect old revisions.
- Terminal text containing CSI/OSC 8/OSC 52 or incomplete ESC sequences must be stripped before styling, and must never produce hyperlink/OSC output.
- A cache DB that is corrupt, locked, or group/other-readable must be disabled without modifying it, while doctor still runs the translation check.
- A rebuild that fails must keep the stop reason and must not re-enable retry/history automatically.

---

### Task 0: Toolchain, module, dependency smoke tests

**Files:**
- Create: `mise.toml`, `.golangci.yml`, `.github/workflows/check.yml`, `internal/deps/deps_test.go`
- Modify: `go.mod`

**Interfaces:**
- Produces: pinned toolchain and a compiling module exposing the dependency APIs later tasks rely on (`goldmark` parser with `WithExtensions(extension.Table)`, `tea.Model`, `viewport.Model`, `toml.Unmarshal`, `sql.Open("sqlite", path)`).

- [ ] **Step 1: Pin tools** — `mise.toml`: `go = "1.27.4"`, `"golangci-lint" = "2.8.0"`, `"govulncheck" = "1.1.4"` (or the current v2-supported versions resolved in Phase 0); record exact versions in the report.
- [ ] **Step 2: Write dependency smoke test** in `internal/deps/deps_test.go`: parse `"| a | b |\n| - | - |"` and assert `ast.KindTable` exists; open a temp sqlite file and `CREATE TABLE`; unmarshal `[translation]\nmodel="m"` with go-toml; compile a `tea.Model`.
- [ ] **Step 3: Run** `go test ./internal/deps -v` — expected PASS.
- [ ] **Step 4: Commit** `chore: pin Go toolchain and dependency smoke tests`.

### Task 1: Domain and config

**Files:**
- Create: `internal/domain/types.go`, `internal/config/config.go`, `internal/config/url.go`
- Test: `internal/config/config_test.go`, `internal/config/url_test.go`, `internal/domain/types_test.go`

**Interfaces:**
- Produces:
  - `domain.Provider` (`ProviderCodex`, `ProviderClaude`), `domain.MessageKind`, `domain.TranslationState`, labels and `Message`, `SessionInfo`, `SessionKey`, `MessageKey`, `TaskRef`, `IndexEntry`, `MakeRevision(string) string`, `FormatTimestamp(string) string`.
  - `config.Config{CodexHome, SessionsRoot, ClaudeHome, ClaudeProjectsRoot, BaseURL, Model, Form, CachePath, ConfigPath string}`, `config.Load() (Config, error)`, `config.LoadFrom(env map[string]string, home string) (Config, error)`, `config.ValidateBaseURL(string) (string, error)`, constants mirroring Python.
- Optional metadata fields are `*string` to preserve missing vs empty.

- [ ] **Step 1: Write failing tests** porting `tests/test_config.py`: all values, empty-env fallback to `$HOME`, missing model, unknown top-level/table keys, bad form, broken TOML, 7 accepted URLs, 12 rejected URLs; and domain label/timestamp/revision tests.
- [ ] **Step 2: Run** `go test ./internal/config ./internal/domain` — expected FAIL.
- [ ] **Step 3: Implement** strict TOML decoding (`DisallowUnknownFields` equivalent by decoding to `map[string]any` then validating), XDG/env resolution (`~` expansion only), `net/netip` allowlist: loopback, 10/8, 172.16/12, 192.168/16; reject non-http, userinfo, hostname, query, fragment, path not `/v1`, port 0, IPv4-mapped, zone, public IPv6; normalize `http://host:port/v1`.
- [ ] **Step 4: Run tests** — PASS.
- [ ] **Step 5: Commit** `feat(config): port strict config and loopback URL contract`.

### Task 2: Codex and Claude discovery

**Files:**
- Create: `internal/transcript/discover.go`, `internal/transcript/codex.go`, `internal/transcript/claude.go`
- Test: `internal/transcript/discover_test.go`

**Interfaces:**
- Produces: `DiscoverCodex(root string) []domain.SessionInfo`, `DiscoverClaude(root string) []domain.SessionInfo`, `DiscoverAll(codexRoot, claudeRoot string, source *domain.Provider) []domain.SessionInfo`, `ClaudeTranscriptCounts(root string) (confirmed, unconfirmed int)`, `NormalizeCwd(string) string`, `ParseSessionRef(string) (*domain.Provider, string)`, `SelectSession([]domain.SessionInfo, string, *domain.Provider) (domain.SessionInfo, error)`, `SessionsForCwd`, `ReadCodexMeta(path string) (map[string]any, bool)`, `ReadClaudeMeta(path string) (cwd, version *string, ok bool)`.

- [ ] **Step 1: Write tests** for codex walk filtering (`archived_sessions`, symlink dirs/files, out-of-root), parent/child rules, mtime/id tie-break, 256 KiB meta boundary (skip line exactly 256 KiB, accept unterminated <256 KiB EOF, drain >=256 KiB head), Claude direct UUID files only, 1 MiB complete-line meta, individual flags, cwd absolute-only, counts, provider-prefixed selection and duplicates.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** with `os.ReadDir`/`filepath.WalkDir` no-follow checks, `EvalSymlinks` containment, sorted traversal, and a head reader that mirrors `read_session_meta`/`read_claude_meta` exactly (including the `>= 256*1024` unterminated-line drain).
- [ ] **Step 4: Run tests** — PASS; commit `feat(transcript): port codex and claude discovery`.

### Task 3: Extractors

**Files:**
- Create: `internal/transcript/extract.go`
- Test: `internal/transcript/extract_test.go`

**Interfaces:**
- Produces: `ParseHistoryMode(*string) (historyMode, error)` with reason text `未対応の history_mode: 'x'`; `Extractor.Extract(record map[string]any, offset int64) (domain.Message, string, bool)`; `ClaudeExtractor.Extract(...)`; `ExtractReason` constants `not_allowed`, `missing_id`, `empty`, `bad_payload`, `excluded`, `thread_mismatch`, `role_mismatch`, `synthetic`.

- [ ] **Step 1: Write tests** porting `test_events.py` and `test_claude_events.py` in full (legacy/paginated/plan/phase, missing id, content joining, all 5 flags with non-bool, flags-before-session-mismatch, 10 internal tags across blocks, tool_result/toolUseResult, synthetic, uuid vs message.id).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** strict `map[string]any` decoding (do not decode into typed structs where type errors could be silently coerced), ordinal UTF-8 byte offsets, SHA-256 revision.
- [ ] **Step 4: Run tests** — PASS; commit `feat(transcript): port allowlisted codex and claude extraction`.

### Task 4: Reader, line splitter, index

**Files:**
- Create: `internal/transcript/reader.go`, `internal/transcript/splitter.go`
- Test: `internal/transcript/reader_test.go`, `internal/transcript/splitter_test.go`

**Interfaces:**
- Produces: `NewReader(path, threadID string, opts ReaderOptions) *Reader` with `Provider`, `AllowedRoot`, `MaxPollBytes`, `MaxLineBytes`; `ScanAll() (ScanResult, error)`; `Poll() PollResult`; `ReadMessages([]domain.IndexEntry) ([]domain.Message, error)`; `ScanResult{ThreadID, HistoryMode *string, UnsupportedMode *string, ThreadChanged *string, Entries []domain.IndexEntry, Stats ScanStats}`; `PollResult{Messages, Entries, Stats, Missing, Rebuilt, Denied, ThreadChanged, UnsupportedMode}`; `type ReadError struct{Reason string}` (`missing`, `denied`, `halted`, `unsupported`, `read`, `index_mismatch`).

- [ ] **Step 1: Write tests** porting `test_session.py`: scan/read, partial lines and partial UTF-8, 300-byte oversized/invalid recovery, unsupported, thread change, missing/rebuilt/shrink, Claude dedupe/latest revision by offset, poll entries, same-size rewrite index mismatch, denied/missing/read distinction, symlink and out-of-root rejection, 8 MiB line splitter.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** 1 MiB chunked byte-offset splitter preserving incomplete tails; >8 MiB lines drained and counted; UTF-8 validation before JSON; inode/device + size checks; no writes; `index_mismatch` when re-extracted ID/revision differs. Tests may inject `MaxLineBytes`; keep production value 8 MiB.
- [ ] **Step 4: Run tests + `-race`** — PASS; commit `feat(transcript): add bounded reader and index`.

### Task 5: Markdown protection

**Files:**
- Create: `internal/protection/protect.go`, `internal/protection/spans.go`, `internal/protection/split.go`, `internal/protection/validate.go`
- Test: `internal/protection/protect_test.go`, `internal/protection/validate_test.go`, `internal/protection/fuzz_test.go`

**Interfaces:**
- Produces: `Segment{Text string; Markers []string; Trailing string}`, `Unit{ID, LineIndex int; Prefix, Suffix, SourceText string; Segments []Segment; Markers []string; SkipReason string; Conservative bool}`, `ProtectedDoc{Source string; Lines []string; Units []Unit}`, `ProtectDocument(string) *ProtectedDoc`, `(*ProtectedDoc).TranslatableUnits() []Unit`, `(*ProtectedDoc).HasConservativeExclusion() bool`, `(*ProtectedDoc).Render(map[int]string) string`, `ValidateSegmentTranslation(string, []string) bool`, `RestoreMarkers(string, []string) (string, bool)`, `SplitProtected(string, int) ([][2]string, bool)`, `MarkerFor(int) string`.

- [ ] **Step 1: Write tests** porting `test_markdown.py` exactly, plus Unicode boundary/emoji/CRLF/tab/marker-collision cases and `FuzzProtect`/`FuzzValidate` (no panic, render of empty map equals source, markers restored exactly).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** goldmark AST walk tracking open containers; allow only single-physical-line top-level paragraphs, simple list items, simple blockquotes, ATX headings; drop nested-list ranges; implement inline protection procedurally for backticks and byte/rune-correct boundaries, using RE2 patterns for the rest. Split at rune boundaries by 1000 runes, never inside markers. Preserve original `\n` join and trailing separators. Conservative exclusion on uncertainty.
- [ ] **Step 4: Run tests + 30s fuzz** — PASS; commit `feat(protection): port markdown protection and segmentation`.

### Task 6: Cache

**Files:**
- Create: `internal/cache/key.go`, `internal/cache/cache.go`
- Test: `internal/cache/cache_test.go`

**Interfaces:**
- Produces: `Key(baseURL, model, form, segment string) string`; `Open(path string) *Cache`; `(*Cache).Status() Status{Available bool; Path, Message string}`; `Get(string) (string, bool)`; `Put(string, string, string)`; `Close() error`. Internal `Disable` permanently on any error.
- `Open` checks parent perms (no `0o077`), creates dir `0700`, file `0600` with `O_CREATE|O_EXCL` when absent, never chmods existing, `busy_timeout=500ms`, single connection, no migration/VACUUM/journal changes.

- [ ] **Step 1: Write tests** porting `test_cache.py`, plus Python-compatible key goldens (ASCII, Japanese, U+2028, backslash, quote, control, `<>&`), and bidirectional read/write of a fixture DB created with the Python key.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** Python-compatible 8-element serializer: `[1, 1, base, model, "en", "ja", form, segment]`, comma+space, `json`-style escapes, non-ASCII raw. Disable (never delete/repair) on permission/corruption/lock/write errors.
- [ ] **Step 4: Run tests** — PASS; commit `feat(cache): add sqlite cache with python-compatible keys`.

### Task 7: Translation HTTP client

**Files:**
- Create: `internal/translate/client.go`, `internal/translate/errors.go`
- Test: `internal/translate/client_test.go`

**Interfaces:**
- Produces: `ErrorKind` constants `connect`, `timeout`, `http`, `invalid`, `empty`, `incomplete`, `cancelled` with Japanese labels; `Failure{Kind, Status}` and `ServerUnavailable() bool`; `NewClient(baseURL, model, form string, opts ...Option) *Client`, `ListModels(ctx) ([]string, error)`, `Translate(ctx, text string) (string, error)`, `Close() error`.
- `Option`: `WithTransport(http.RoundTripper)`, `WithTimeouts(connect, read, total time.Duration)`.

- [ ] **Step 1: Write tests** with `httptest.Server`/custom RoundTripper for exact URL/body, system prompt, 4xx/5xx, invalid JSON, empty content, finish_reason length, connect/timeout classification, redirect not followed, proxy env ignored, `GET /models` filtering, response body cap 4 MiB, and context cancellation.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** `http.Transport{Proxy: nil, DialContext timeout 3s, TLSHandshakeTimeout 3s, ResponseHeaderTimeout 60s}` + total 90s `context.WithTimeout` + `http.Client{CheckRedirect: ErrUseLastResponse}`; read `io.LimitReader`; close body; no auth.
- [ ] **Step 4: Run tests** — PASS; commit `feat(translate): add constrained plamo http client`.

### Task 8: Scheduler and monitor

**Files:**
- Create: `internal/monitor/scheduler.go`, `internal/monitor/monitor.go`, `internal/monitor/view.go`, `internal/monitor/status.go`
- Test: `internal/monitor/scheduler_test.go`, `internal/monitor/monitor_test.go`, `internal/monitor/race_test.go`

**Interfaces:**
- Produces:
  - `View{Message domain.Message; Doc *protection.ProtectedDoc; State domain.TranslationState; Translations map[int]string; Pending, Failed map[int]struct{}; ErrorLabels map[string]struct{}}` with `DisplayText() string` and `RefreshState()`.
  - `Listener` interface `Reset(*domain.SessionInfo, []View)`, `Added(View)`, `Prepended([]View)`, `Updated(View)`, `Status(Status)`.
  - `Status{Server string; Pending int; LastUpdate string; Note, Error, CacheNote, Session *string}` (labels match Python).
  - `Scheduler` with `Submit(TaskRef, []protection.Segment, priority int)`, `CancelGeneration(uint64)`, `Next() (*Job, bool)`, `Finish(*Job, Result)`, `Requeue(*Job)`, `Reconnect(ctx) bool`, `Snapshot() SchedulerStatus`; active = queued+inflight, deferred excluded, max 32.
  - `Monitor` with `Start(context.Context, *domain.SessionInfo)`, `Switch(domain.SessionInfo)`, `Retry()`, `AddHistory()`, `Views() []View`, `Session() *domain.SessionInfo`, `Close() error`. All mutations happen on the monitor goroutine; public methods send typed requests.
  - `New(cfg config.Config, listener Listener, client *translate.Client, cache *cache.Cache, fakeTranslate func(context.Context,string)(string,error)) *Monitor` (fake only for tests).

- [ ] **Step 1: Write deterministic scheduler tests** (no sleeps where a barrier works): 1 worker, ≤32 active, deferred FIFO, priority order, segment re-queue, cache hit, memo dedupe, invalid marker no cache, 5xx pause + reconnect rules (404/invalid = connected, 500/connect = failed), cancel during cache lookup no POST, cancel during POST suppresses remaining segments, partial failure.
- [ ] **Step 2: Write monitor tests** porting `test_ui.py` and the regression cases in `test_claude_ui.py`: replacement/shrink rebuild, denied/thread-change stop, missing keeps views, history cursor non-advance and recovery, retry blocked in Loading/Rebuilding/Stopped, note→index→read order, old-generation scan/read/poll/reconnect results ignored, A→B→A, same ID different provider/path, readback mismatch rebuild.
- [ ] **Step 3: Run** — FAIL.
- [ ] **Step 4: Implement** event loop, one reader operation at a time, one translation worker, heap `(priority, sequence)`, deferred deque, generation checks at every result path, single `stop(reason, reset)` path, explicit `Loading/Watching/Rebuilding/Stopped/Closed` handling.
- [ ] **Step 5: Run** `go test ./internal/monitor -race` — PASS; commit `feat(monitor): port monitor state machine and scheduler`.

### Task 9: CLI

**Files:**
- Create: `internal/cli/args.go`, `internal/cli/cli.go`, `internal/cli/doctor.go`, `internal/cli/list.go`, `cmd/transpane/main.go`
- Test: `internal/cli/cli_test.go`, `internal/cli/args_test.go`

**Interfaces:**
- Produces: `cli.Run(ctx context.Context, args []string, env []string, out, err io.Writer) int` and `cli.Main() int`; `--version` prints `transpane <version> (<commit>, go <goversion>)`; `version`/`commit` package vars settable with `-ldflags`.
- Arg grammar exactly `--source` before subcommand; `--cwd`/`--session` mutually exclusive; `inspect --session`; argparse-compatible `--help` exit 0, usage errors 2.

- [ ] **Step 1: Write tests** porting `test_cli.py` with temp dirs: sessions/inspect output and no body, no cache creation, unknown/ambiguous session 2, doctor success/missing model/failure/cache+translation order/root reads once/per-source rules/Claude counts, config error 2, exclusive flags 2, help 0, source-precedes-subcommand, `--version` no config access.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** manual arg parser (support `-h/--help`, `--version` before config load), doctor diagnostics that continue after failures, sessions/inspect without HTTP or cache creation.
- [ ] **Step 4: Run tests** — PASS; commit `feat(cli): port command grammar and diagnostics`.

### Task 10: TUI

**Files:**
- Create: `internal/tui/app.go`, `internal/tui/view.go`, `internal/tui/picker.go`, `internal/tui/sanitize.go`, `internal/tui/markdown.go`
- Test: `internal/tui/app_test.go`, `internal/tui/sanitize_test.go`, `internal/tui/render_test.go`

**Interfaces:**
- `New(cfg config.Config, initial *domain.SessionInfo, cwd string, source *domain.Provider, opts ...Option) *App`, `Run(ctx) error`, `App` implements `tea.Model`.
- Listener events enter `Update` as `monitorMsg`; monitor methods are invoked through `tea.Cmd`s; `s` opens a list with provider/ID/cwd/mtime/mode and keeps the selected row's `SessionKey`; `o` toggles original; `r` retries; `h` history; `End` latest+auto-scroll; `q`/Ctrl+C quit; arrows/PageUp/PageDown/mouse scroll the viewport.
- Rendering: sanitize OSC/CSI/C0/C1 from original and translation, append app styles only, use viewport wrapped by cell width, anchor by message ID + inner line, no link activation, no images.

- [ ] **Step 1: Write tests** for sanitize (OSC 8/52, incomplete ESC, C0/C1), initial-state logic, renderer preserving code/link label/URL, header labels/timestamps, original-before-translation, revision in-place update, history prepend, scroll anchor, status line and footer text, picker filter and same-ID rows.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** `View() tea.View` with `AltScreen`, `MouseModeCellMotion`, viewport, status/footer; run monitor and forward events with `p.Send`; ensure shutdown cancels context, joins workers, closes cache, and restores the terminal.
- [ ] **Step 4: Run tests** — PASS; commit `feat(tui): add bubble tea viewer`.

### Task 11: Fixtures, compatibility oracle, CI, release build

**Files:**
- Create: `testdata/compat/*.jsonl`, `testdata/compat/golden/*.json`, `scripts/compat/oracle.py`, `.github/workflows/release.yml`, `README.md` updates
- Test: `internal/compat/compat_test.go`, `internal/translate/cache_compat_test.go`

**Interfaces:**
- `scripts/compat/oracle.py` runs against the fixed Python tree and emits JSON: messages/id/revision/offset/kind/stats, protected segments, restore results, cache keys, CLI stdout/stderr/exit, HTTP request/state transitions.
- Go tests read committed goldens only; Python is not required by normal Go CI.

- [ ] **Step 1: Generate synthetic fixtures** (no real logs): legacy, paginated, plan, boundary/oversize, Claude direct/flags/wrappers, long/protected Markdown, cache DB.
- [ ] **Step 2: Run the Python oracle** from a branch worktree (`uv sync --locked`, pytest, ruff) and commit normalized goldens; list the actual 219 node IDs and map each to a Go test or a recorded external-contract replacement.
- [ ] **Step 3: Compare Go output** for messages, spans, markers, cache keys, CLI output; record every difference and classify approved vs must-fix.
- [ ] **Step 4: CI** jobs: test, vet, lint, race, vuln, `CGO_ENABLED=0` build, artifact + `SHA256SUMS`; no publishing.
- [ ] **Step 5: Build** `dist/transpane`, verify `file`/`ldd` (no dynamic deps), run `--help`/`--version`/`doctor` in a clean env without Python/uv/Go.
- [ ] **Step 6: Commit** `test: add compat fixtures and release pipeline`.

### Task 12: Final verification and report

- [ ] **Step 1:** `go mod verify && go test ./... && go vet ./... && golangci-lint run ./... && golangci-lint fmt --diff`.
- [ ] **Step 2:** `CGO_ENABLED=1 go test -race ./...`, run each fuzz target 30s, `govulncheck ./...`.
- [ ] **Step 3:** `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/transpane ./cmd/transpane`.
- [ ] **Step 4:** Evaluate G01–G26, record PASS/FAIL/NOT RUN with evidence; PARTIAL if G26 or Phase 6 is unverified; do not claim completion from self-assessment.
- [ ] **Step 5:** Update README (install, keys, config, cache migration, rollback, constraints) and write the final report in the spec §15 format.
