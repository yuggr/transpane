# Session picker identifiability (Issue #6)

## Goal

Make the session picker identify sessions by work content, project, and
relative update time while keeping `SessionKey` as the only selection
identity.

## Plan

- [x] Add bounded, deterministic title resolution with per-file caching.
- [x] Cache discovery metadata by file identity to keep the 2-second
      picker refresh cheap.
- [x] Render two-line picker rows with short provider, project, relative
      time, and `DisplayID`.
- [x] Add disambiguation for equal project names, equal short IDs, and
      equal raw IDs.
- [x] Add `/` incremental search over title, project, provider, and ID.
- [x] Add a detail view with full UUID, cwd, log path, mode, and absolute
      update time.
- [x] Keep the picker timer alive and single-instance while the picker is
      open; never update a closed picker.
- [x] Preserve the selected `SessionKey` across search, refresh, and
      resize.
- [x] Add regression tests at 40/60/80/120 cells and run the full quality
      suite plus a PTY check.
