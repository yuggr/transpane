# Transpane Go migration report

STATUS: PARTIAL

BASE / HEAD: `f199bb6cce011c80d28f7cdfe91929d62d30c08f` / `7b7201d` (PR #5 review fixes)

PHASES:
- Phase 0: complete — baseline pytest 219 passed, toolchain pinned (Go 1.27.4, golangci-lint 2.15.0, govulncheck v1.10.0), dependency smoke tests, synthetic oracle.
- Phase 1: complete — config/domain, Codex/Claude discovery, extractors, bounded reader/index.
- Phase 2: complete — protection, cache, constrained HTTP client.
- Phase 3: complete — monitor state machine, scheduler, history, cancellation, generation barriers.
- Phase 4: complete — CLI and Bubble Tea TUI; PTY smoke test.
- Phase 5: partial — synthetic differential goldens, clean-environment build/run, README; real WSL Codex/Claude/PLaMo manual run not executed.
- Phase 6: NOT STARTED — Python code/CI removal, tagging, publishing and merge require separate approval.

DEPENDENCIES / TOOLCHAIN:
- Go 1.27.4; `charm.land/bubbletea/v2` v2.2.0; `charm.land/bubbles/v2` v2.2.2; goldmark v1.8.7; go-toml/v2 v2.4.3; modernc.org/sqlite v1.63.0.
- golangci-lint 2.15.0 (built with Go 1.27.0); govulncheck v1.10.0; mise 2026.11.9.
- Release build CGO_ENABLED=0; race tests CGO_ENABLED=1.

CHANGED FILES / ARCHITECTURE:
- Added `cmd/transpane` and `internal/{cli,config,domain,transcript,protection,cache,translate,monitor,tui,compat,deps}`.
- Added `testdata/compat` synthetic fixtures + Python oracle golden, `scripts/compat/oracle.py`.
- Added `mise.toml`, `.golangci.yml`, `.github/workflows/check.yml`, baseline mapping and this report.
- Python source/tests remain untouched at the baseline as the oracle. No Python file was changed.

PYTHON BASELINE RESULTS + CONTRACT MAPPING:
- `uv sync --locked && uv run pytest -q` → **219 passed** (one timing-sensitive Claude UI test failed once and passed on rerun; Python tests were not modified).
- Node IDs: `docs/superpowers/plans/2026-11-26-python-baseline-nodes.txt`.
- Module mapping and replacement rationale: `docs/superpowers/plans/2026-11-26-go-migration-baseline.md`.
- Python ruff baseline: `oracle.py` passes; three pre-existing Markdown documents are reported by `ruff format --check` (pre-existing, not part of Go CI).
- Go differential: `internal/compat` passes against Python-generated golden for Codex/Claude messages, offsets, revisions, kinds, stats, protection units/segments/render and cache keys.

G01-G26:

| ID | Status | Evidence |
|---|---|---|
| G01 | PASS | `internal/config`, `internal/cli` tests: TOML/env/XDG/URL normalization, unknown keys, grammar, exits |
| G02 | PASS | `internal/transcript`: Codex walk/parent/symlink, legacy/paginated/Plan, unknown mode |
| G03 | PASS | `internal/transcript`: Claude direct UUID, 1 MiB meta, flags, wrappers, tool/synthetic, uuid revisions |
| G04 | PASS | Boundary tests: Codex 256 KiB/1 MiB/EOF vs Claude complete-line 1 MiB |
| G05 | PASS | Reader unit tests + `FuzzProtect`/`FuzzValidate`; no panic, recovery after invalid/oversized |
| G06 | PASS | Index ID/revision, same-size rewrite, replacement, shrink, missing, outside-root/symlink |
| G07 | PASS | Monitor tests: initial 50, `h`, new unique, revision in place, duplicate ID, failed cursor |
| G08 | PASS | Protection tests + fuzz: spans, Unicode, 1000-rune split, restore |
| G09 | PASS | Extractors/monitor: secret line skipped, only agent text reaches fake HTTP; tool/thinking ignored |
| G10 | PASS | `internal/translate`: POST/GET shape, 4xx/5xx, invalid/empty/incomplete, timeout/connect, context cancel |
| G11 | PASS | Proxy env ignored, redirect not followed, IP allowlist; TUI/CLI control-sequence stripping tests |
| G12 | PASS* | Cache key Python goldens, perms/corrupt/lock/non-destructive, reserved-character URI paths; Python-created synthetic DB read by Go test; Go-created DB read by Python manually. A mid-life read-only transition is not separately automated. |
| G13 | PASS | Scheduler tests: cache reuse, invalid marker not cached, memo, policy/model/form keys |
| G14 | PASS | Scheduler tests: one worker, active≤32, deferred FIFO, priority/sequence, requeue, accounting |
| G15 | PASS | Deterministic cache-lookup barrier (switch cancels before POST), cancel during HTTP, remaining-segment suppression |
| G16 | PASS | Same ID across Codex/Claude, A→B→A, same-ID picker rows, stale in-flight B discarded |
| G17 | PASS* | Old scan/read/poll/reconnect results ignored; switch/rebuild × index_mismatch/read_error four combinations covered deterministically |
| G18 | PASS | Denied and thread-change stop retained; retry/history blocked; explicit switch recovers |
| G19 | PASS | Rebuild retry/history blocked, note→scan→read ordering, readback mismatch stop, stop reason retained |
| G20 | PASS | Pause/reconnect rules, retained unsubmitted work, retry only failed units, 4xx vs 5xx classification |
| G21 | PASS | CLI tests: doctor all diagnostics, root read once, no body/HTTP in sessions/inspect |
| G22 | PASS* | TUI unit tests + PTY run: original first, translation, keys, picker snapshot, scroll anchor, headers/status |
| G23 | PASS | Monitor test asserts transcript bytes, mtime and mode unchanged; no agent process launch by design |
| G24 | PASS* | Race suite, 30 s fuzz each, Close/quit/terminal restore in PTY; no goroutine-leak detector |
| G25 | PASS | Static 13.8 MB binary, clean `env -i` `--version`/`doctor` without Go/Python/uv, SHA-256 |
| G26 | NOT RUN | No real WSL Codex/Claude/PLaMo session was exercised; synthetic logs + mock HTTP only |

DIFFERENTIAL RESULTS / observed differences (approval pending):
- Messages/offsets/revisions/kinds/stats/protection/cache keys match the Python oracle.
- Deliberate fail-closed differences: port 0 rejected (Python normalizes to 80); IPv4-mapped IPv6, zoned IPv6 and non-regular files rejected; poll-discovered unsupported mode stops translation immediately; CLI/TUI strip control characters from metadata.
- Markdown rendering is sanitized source text, not Textual pixel parity (allowed by spec).

TEST / RACE / LINT / FUZZ / VULN / BUILD:
- `go mod verify` → all modules verified.
- `go test ./...` → 188 passed.
- `CGO_ENABLED=1 go test -race ./...` → 188 passed.
- `go vet ./...` → clean.
- `golangci-lint run ./...` → 0 issues; `golangci-lint fmt --diff` → clean.
- `mise run fuzz` → all 5 targets 30 s each → PASS.
- `govulncheck ./...` → No vulnerabilities found.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w"` → static ELF.

BINARY SIZE / CHECKSUM / RUNTIME DEPENDENCIES:
- Size: 13,881,504 bytes (stripped, final build at `7b7201d`).
- SHA-256: `09c36572811958e88fca6858f9d88f133e2d26966c67fcdcf947a8ff775fcf76`.
- `file` → ELF 64-bit LSB executable, x86-64, statically linked; `ldd` → not a dynamic executable.
- Runtime: Linux amd64 only; no Go/Python/uv/system library requirement.

WSL / CODEX / CLAUDE / PLaMo manual evidence:
- Not run against real Codex/Claude or a real PLaMo model.
- Synthetic end-to-end: `sessions`, `inspect`, `doctor` against a mock `/v1` server and a generated Codex session.
- PTY TUI: original text appears first, translation appears after the mock response, `q` exits 0 with terminal restore.
- Cache: Go-created DB read by Python `sqlite3`; Python-created DB read by Go TUI offline (`PYTHON-CACHE` displayed).
- This is not a substitute for G26.

PERFORMANCE (cold/warm; original vs translation):
- Not measured for Go. Python baseline (reference only): original median 100 ms, translation median 507 ms.
- Translation latency is dominated by the external model; no speed target was set.

CACHE COMPATIBILITY / ROLLBACK:
- Key algorithm and SQLite schema are Python-compatible (goldens + bidirectional synthetic DB check).
- Go never deletes, replaces or chmod-repairs an existing DB; a wide-permission/corrupt/locked DB is disabled for the process.
- Rollback to Python: fixed-commit worktree + `uv sync --locked` + `uv run transpane` (README). Existing DB remains usable by both.

PR #5 REVIEW FOLLOW-UP (HEAD `7b7201d`):
- P1-01 PASS: Python/RE2 `\s`, `\w`, `\b` differences are replaced by Unicode-aware classes and Python boundary checks. Trailing-`\b` backtracking (for example a synthetic key ending in `-`) and alternation/longest-boundary cases are handled via anchored longest matching plus rune-wise end backtracking. Unicode whitespace Bearer (non-overlapping synthetic token), Unicode path/function spans, oracle golden and HTTP non-send tests added.
- P2-01 PASS: dispatch, post-cache permission and result handling validate TaskRef revision/unit; cancelled job contexts are also checked so A→B→A cannot apply an old A result. Old revision jobs are cancelled/dropped; remaining segments suppressed; old failures cannot change pause/connection; accounting barrier tests added.
- P2-02 PASS: session switch keeps the server pause; only a successful explicit reconnect GET resumes; stale reconnect results are discarded.
- P2-03 PASS: SQLite DSN is percent-encoded and the same absolute path is checked and opened; `?`, `#`, `%` path tests added.
- P2-04 PASS: the translation worker sends cache status snapshots with results; Monitor/TUI show runtime cache failures; no cross-goroutine cache field access.
- P2-05 PASS: history denied/halted keeps existing views; initial/rebuild failures still reset; stop/retry/switch tests added.
- P2-06 PASS: `Monitor.Close` waits for worker join plus client/cache close; no request-receipt shortcut; shutdown deadlock barriers.
- P2-07 PASS: `h` requests received while a poll/read is in progress are queued and executed after the reader completes; Loading/Rebuilding/Stopped still suppress requests. Deterministic poll-blocked AddHistory barrier test added; public `AddHistory` remains non-blocking to avoid listener-backpressure deadlock.
- G17 combinations: all four switch/rebuild × index_mismatch/read_error combinations are covered. TUI code was not changed in this round; its existing fold/picker tests remain, and real-terminal CJK confirmation remains part of G26.

FRESH REVIEW / FIXES:
- A fresh-context reviewer examined the branch and returned "With fixes".
- Critical C1 (rebuild during in-flight reconnect left `reconnecting`/`retryAfterReconnect` set and stalled translation) is fixed; a deterministic 500→Retry→rebuild→stale GET→Retry→translated regression test was added.
- Critical C2/C3 (unsanitized metadata, stale reconnect crossing generations) were already fixed before the review and re-verified; C1 was the residual.
- Important I1 (empty `form`/`base_url` silently defaulted) is fixed with table tests.
- Important I2/I3/I5 (picker key stability, TUI shutdown ordering, Python-created cache DB fixture) are fixed/tested.
- Important I4 (missing invalid-marker/no-cache, 4xx vs 5xx state, partial-unit tests) is closed.
- Minor items are documented as observed differences requiring approval (IPv6 canonicalization, empty query, `~user`, backtick artifact, subcommand help text, timestamp variants); `created_at` now uses six digits when microseconds are non-zero.

UNRESOLVED RISKS / NOT VERIFIED:
- Real Codex/Claude transcript variants and real PLaMo model timing (G26) are unverified; PARTIAL by definition.
- Barrier coverage is not exhaustive for every poll/reconnect interleaving (G15/G17 PARTIAL).
- Reader I/O cannot be interrupted mid-file-read; stale results are discarded by generation.
- No Linux arm64 or Windows-native support is claimed.
- Phase 6 cleanup/tag/publish/merge is not approved.
