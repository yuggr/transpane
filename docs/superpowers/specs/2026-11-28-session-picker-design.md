# Session picker identifiability design (Issue #6)

## Intent

An operator should be able to choose a session by what the session was
about, which project it belongs to, and when it last changed. UUIDs and
full paths remain available, but they are no longer the primary row
content.

## Goals

- Show a deterministic title for Codex and Claude Code sessions.
- Render two lines per candidate: work content first, then project,
  relative update time and the existing `DisplayID`.
- Search incrementally over title, project, provider and ID.
- Refresh candidates every two seconds while the picker is open.
- Keep the selected session stable by `SessionKey` across refresh,
  search and resize.
- Keep title acquisition local, bounded and non-blocking.

## Non-goals

- LLM summaries, translation of titles, full-history search, unread
  state, persistent title storage, or a redesign of the viewer.
- Changing the Monitor, Scheduler, translation or SQLite cache
  contracts.
- Issue #7 viewer styling or Issue #8 README work.

## Architecture

| Concern | Location |
| --- | --- |
| Discovery | `internal/transcript/discover.go` (unchanged contracts) |
| Bounded title resolution and per-file caches | `internal/transcript/catalog.go` |
| Picker state, layout, search, detail | `internal/tui/picker.go` |
| Bubble Tea wiring and timer | `internal/tui/app.go` |
| Secret redaction reuse | `internal/protection/spans.go` |

`transcript.Catalog` is the interactive discovery facade. It wraps the
existing discovery functions with injected metadata readers, then adds
titles. The one-shot `DiscoverAll`/CLI paths keep using the original
readers.

## Title resolution

- Claude Code: prefer the latest `ai-title` record found in a bounded
  head read, then a bounded tail read. Fall back to the first
  allowlisted user message.
- Codex: parse `session_meta.history_mode`, then use the existing
  `Extractor` to obtain the first allowlisted `user` message.
- Only allowlisted user content is considered. Tool results, thinking,
  internal events and synthetic messages are excluded by the existing
  extractors.
- Known credential spans are redacted with the existing protection
  patterns before the title is cached.
- The first meaningful line is collapsed to one line and capped at 80
  runes. Missing titles render as `タイトル未取得`.
- Limits: 256 KiB head, 64 KiB tail, 1024 lines, 80 runes.

## Caching and refresh

`Catalog` caches metadata and titles by path plus file identity
(size and nanosecond mtime). Unchanged files are not reopened on the
two-second refresh. `Lstat` rejects symlinks before a cached read.

The picker keeps one tick chain alive for the whole app. Each tick
schedules exactly one successor and discovers only when there is no
active session or the picker is open. A `CompareAndSwap` guard prevents
overlapping discovery commands. After the user dismisses the picker, a
background refresh updates the candidate slice but does not reopen the
picker; `s` explicitly reopens it.

## Layout

Each candidate occupies two lines:

```text
> [Codex] PR #5 の移植検証
  transpane | 17分前 | 01a1e80f…

  [CC] PowerShellの配置確認
  project-a | 3時間前 | 7579c4bd…
```

- Provider short labels are `Codex` and `CC`.
- Project labels use the cwd basename. When basenames collide, the
  shortest unique path suffix is used, up to the full path.
- `DisplayID` is reused. When short IDs collide, the shortest unique
  prefix is used. When the raw ID and provider also collide, the log
  file basename is appended.
- Relative times are `たった今`, `N分前`, `N時間前`, `昨日`, `N日前`,
  `N週間前`, `Nか月前` or `N年前`.
- At narrow widths the second line drops the ID before the project and
  keeps the update time whenever possible. The title line is always
  preserved first.
- `d` opens a detail view with the full UUID, cwd, log path, mode and
  absolute update time.

## Search and selection

`/` enters incremental, case-insensitive partial matching over title,
provider labels, project (basename and full cwd), `DisplayID` and raw
ID. `Enter` selects, `Esc` closes, `s` refreshes, and `d` toggles
detail. `Backspace` and `Ctrl+U` edit the query; `/` toggles search
capture without discarding the query.

`SessionKey{Provider, ID, Path}` is the only selection identity. A key
that is temporarily filtered out is remembered, so clearing the query
restores the same session. Indexes are used only for navigation within
the current view.

## Safety

- No network access, cache writes or transcript writes occur during
  title resolution.
- Discovery containment, allowlists and existing sanitization remain in
  force.
- All picker, title and detail text passes the existing
  `SanitizeForDisplay` before rendering.
- Tests use synthetic transcripts only.
