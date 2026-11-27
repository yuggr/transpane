# Python baseline and contract mapping

- Base: `f199bb6cce011c80d28f7cdfe91929d62d30c08f`
- Command: `uv sync --locked && uv run pytest -q`
- Result: **219 passed** (2026-11-26, this worktree)
- Node IDs: `2026-11-26-python-baseline-nodes.txt`
- Lint baseline: `uv run ruff check .` (to be recorded in Phase 5)
- Synthetic oracle: `scripts/compat/oracle.py` → `testdata/compat/golden.json`
- Go differential test: `internal/compat` (`TestSyntheticGolden`)

## Mapping policy

Counts are not required to match. Python-internal assertions were replaced by an
external contract assertion where the implementation detail is not observable.
Every module below has a Go test that exercises the same externally visible
behavior; barrier-heavy UI regression cases are marked partial when the Go test
suite does not yet reproduce the exact interleaving.

| Python test file | Node count | Go test location | Status |
|---|---:|---|---|
| `test_config.py` | 8 collected | `internal/config` | Complete: env/XDG, strict TOML, URL allowlist |
| `test_cache.py` | 7 | `internal/cache` | Complete: roundtrip, perms, corruption, lock, unusual keys |
| `test_events.py` | 8 | `internal/transcript/extract_test.go` | Complete: legacy/paginated/plan/phase/revision |
| `test_claude_events.py` | 12 | `internal/transcript/extract_test.go` | Complete: flags, wrappers, tool blocks, synthetic, uuid |
| `test_session.py` | 34 | `internal/transcript/{discover,reader}_test.go` | Complete for discovery, boundaries, index, symlink, rebuild |
| `test_markdown.py` | 16 | `internal/protection` | Complete for samples, spans, skip reasons, split, marker validation |
| `test_translation.py` | 17 | `internal/translate` + `internal/monitor` scheduler tests | Client complete; scheduler accounting complete; invalid marker/no-cache, cancel during cache lookup/POST, 4xx/5xx state, partial unit failure and bounded queue covered deterministically |
| `test_cli.py` | 20 | `internal/cli` | Complete for grammar, diagnostics, source rules, no-body/no-cache |
| `test_ui.py` | 21 | `internal/monitor` + `internal/tui` | Monitor translation/history/retry/rebuild/stop complete; TUI rendering/sanitize/picker/scroll complete; Textual pixel/ANSI comparison and exhaustive terminal interleavings not run |
| `test_claude_ui.py` | 26 | `internal/monitor` + `internal/tui` | Same-ID/provider switch and stale scan/read/poll/reconnect results covered; exact historical barrier combinations are replaced by deterministic equivalents |
| `test_fixtures.py` | 3 | `internal/compat` | Synthetic differential golden |

## Differential results

- Codex paginated fixture: messages, IDs, revisions, byte offsets, kinds and stats
  match the Python oracle byte-for-byte.
- Claude direct fixture: messages, revision, offsets, kinds and stats match the
  Python oracle; thinking/internal-wrapper records are excluded identically.
- Protection fixture: unit IDs, line indexes, prefixes/suffixes, markers,
  segment texts/trailing and empty-map render match the Python oracle.
- Cache keys: Python-produced SHA-256 values match for ASCII, Japanese,
  U+2028/U+2029, quotes/backslashes, control characters, `<>&/` and emoji.
- Cache DB: a committed synthetic DB created by Python is read by Go in
  `internal/cache`; a Go-created DB was read by Python in manual verification.
- Observed differences (approval pending): port 0 is rejected (Python normalized it to 80),
  IPv4-mapped IPv6 and zoned IPv6 are rejected, non-regular files are refused,
  and an unsupported mode discovered by poll stops translation immediately
  (fail closed).

## Python baseline lint

`uv run ruff check .` and `uv run ruff format --check .` are recorded in the
Phase 5 verification log (not used as a Go quality gate).

## Observed differences requiring approval and constraints

- Non-canonical IPv6 loopback (`[0:0:0:0:0:0:0:1]`) is canonicalized to `[::1]`;
  Python preserves the input text, so cache keys can differ for this configuration.
- Empty query (`/v1?`) is rejected by Go but accepted by Python. Fail-closed.
- Go uses Python-compatible Unicode `\s`, `\w` and `\b` semantics for secret and
  protected-span detection. Unicode whitespace Bearer tokens and Unicode paths
  are covered by the shared oracle fixture.
- Port 0 is rejected by Go; Python normalized it to 80.
- IPv4-mapped IPv6, zoned IPv6, non-regular files and symlinked session files are
  rejected by Go.
- `~user` is not expanded; only `~` and `~/` use the current home directory.
- A pathological backtick sequence such as two single backticks plus a later
  single backtick can choose a different protected span than Python's
  backtracking regex. Go follows the spec's same-length procedural rule.
- CLI long-option prefix matching is supported (`--sou`, `--cw`, `--ses`);
  subcommand `--help` prints the shared help text rather than argparse's
  per-subcommand text, while preserving exit 0.
- Malformed ISO-8601 timestamp variants outside the accepted layouts render
  `時刻不明`; the baseline accepted a slightly wider set.
- None of these differences is approved yet. They remain proposed compatibility
  changes for the spec owner to accept or reject.
