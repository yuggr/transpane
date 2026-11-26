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
| `test_translation.py` | 17 | `internal/translate` + `internal/monitor` scheduler tests | Client complete; scheduler priority/cancel/deferred complete; live async interleavings replaced by deterministic monitor tests |
| `test_cli.py` | 20 | `internal/cli` | Complete for grammar, diagnostics, source rules, no-body/no-cache |
| `test_ui.py` | 21 | `internal/monitor` + `internal/tui` | Monitor translation/history/retry/rebuild/stop complete; TUI rendering/sanitize/picker/scroll complete; PTY screen comparison not run |
| `test_claude_ui.py` | 26 | `internal/monitor` + `internal/tui` | Same-ID/provider switch and stale in-flight result covered; every barrier combination from PR #4 is **PARTIAL** |
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
- Approved differences: port 0 is rejected (Python normalized it to 80),
  IPv4-mapped IPv6 and zoned IPv6 are rejected, non-regular files are refused,
  and an unsupported mode discovered by poll stops translation immediately
  (fail closed).

## Python baseline lint

`uv run ruff check .` and `uv run ruff format --check .` are recorded in the
Phase 5 verification log (not used as a Go quality gate).
