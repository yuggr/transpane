# Transpane 複雑度低減リファクタリング Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `omen` の cognitive / cyclomatic 複雑度を下げる。公開 API・挙動・ファイル構成は変えない。

**Architecture:** Extract Method を主軸に、1 モジュール = 1 コミットで進める。各タスクは既存テストを回帰網として使い、不足する挙動は characterization test を先に追加して固定する。非同期コード（`ui.Monitor` / `translation.TranslationScheduler`）は await 境界・状態遷移・処理順序を変えない。

**Tech Stack:** Python 3.12, uv, pytest, ruff, omen 5.1.0, Textual, httpx, SQLite。

**Spec:** `docs/superpowers/specs/2026-11-25-complexity-refactor-design.md`

## Global Constraints

- 基点は固定コミット `507a820`。作業ブランチは `refactor/reduce-complexity`。
- `src/` のみ変更する。テストは既存を変更・削除せず、characterization test の追加のみ許可。
- 公開 API とテスト依存 API のシグネチャ・意味を変えない:
  `read_session_meta` / `split_protected` / `protect_document` /
  `sanitize_for_display` / `initial_state` / `MessageView` / `Monitor` /
  `TranspaneApp` / `SessionScreen` / `SessionReader` / `discover_sessions` /
  `discover_all_sessions` / `select_session` / `reader_for_session` /
  `claude_transcript_counts` / `discover_claude_sessions` / `Extractor` /
  `ClaudeExtractor` / `cache_key` / `TranslationCache` / `TranslationClient` /
  `TranslationScheduler` / `ErrorKind` / `TranslationFailure`。
- 依存追加、Python バージョン変更、UI フレームワーク変更、物理的なファイル分割をしない。
- 例外の種類・送出タイミング・エラーメッセージを変えない。
- `read_session_meta` の読取契約を変えない: EOF の未終端行は解析する／
  256 KiB 以上の行は除外する／上限は `consumed < META_READ_LIMIT`。
- `Monitor` / `TranslationScheduler` の await 境界、generation 確認、停止処理、
  queue/deferred/active 会計、Event の clear/set 順序を変えない。
- `run_doctor` は全診断を実行する（短絡評価禁止）。
- `cache.py` は設計書 §5.9 に従い、明確な責務分離の利益がなければ変更しない。
- 数値目標は原則 cognitive ≤ 15、改善目標 p95 ≤ 10・合計 40% 減。数値合わせだけの
  小関数抽出はしない。
- 各コミット前に `uv run pytest -q` と `uv run ruff check src tests` を通す。

## Review Focus

1. `read_session_meta` が EOF の改行なし最終行からも `session_meta` を発見できること
   （Task 1 の characterization test で固定）。
2. `read_session_meta` が 256 KiB 以上の行を除外し、後続の有効レコードを読むこと
   （Task 1 の characterization test で固定）。
3. `Monitor.switch` 内で `SessionIndexMismatch` が起きたら `_rebuild` へ進み、
   停止しないこと（Task 8 の characterization test で固定）。
4. ネストリスト・複数行ブロック・表など曖昧な Markdown が翻訳対象にならないこと
   （既存 `tests/test_markdown.py::test_ambiguous_blocks_stay_original` を Task 2 で実行）。
5. `run_doctor` が翻訳サーバー失敗時もキャッシュ診断を含む全診断を表示し、終了コード 1
   を返すこと（Task 7 の characterization test で固定）。

---

### Task 1: `session.py` の複雑度低減

**Files:**
- Modify: `src/transpane/session.py`
- Test: `tests/test_session.py`

**Interfaces:**
- Consumes: 既存の `SessionReader`, `IndexEntry`, `ScanStats`, `Message`, `HistoryMode`。
- Produces（同一ファイル内ヘルパー）:
  - `_session_meta_payload(record: object) -> dict[str, Any] | None`
  - `_discard_rest_of_oversized_line(handle: BinaryIO, consumed: int) -> int | None`
  - `_within_root(path: Path, root_real: Path) -> bool`
  - `_is_rollout_file_name(name: str) -> bool`
  - `SessionReader._scan_file(handle: BinaryIO) -> tuple[list[IndexEntry], str | None]`
  - `SessionReader._append_scan_entry(entries: list[IndexEntry], positions: dict[str, int], entry: IndexEntry) -> None`（staticmethod）
  - `SessionReader._history_mode_label() -> str | None`
  - `SessionReader._ensure_readable() -> None`
  - `SessionReader._read_entry(handle: BinaryIO, entry: IndexEntry) -> Message`
  - `SessionReader._process_claude_line(record, offset, stats) -> tuple[Message | None, str | None]`
  - `SessionReader._process_session_meta(record, stats) -> tuple[Message | None, str | None]`
  - `SessionReader._process_codex_line(record, offset, stats) -> tuple[Message | None, str | None]`
  - `SessionReader._poll_precheck() -> tuple[PollResult | None, os.stat_result | None]`
  - `_LineSplitter._take_dropped_tail() -> tuple[list[tuple[int, bytes]], int] | None`（省略可）

- [ ] **Step 1: Add characterization tests for the metadata read contract**

`tests/test_session.py` に追加する（既存テストは変更しない）:

```python
def test_read_session_meta_accepts_unterminated_final_record(tmp_path):
    path = tmp_path / "rollout-x.jsonl"
    path.write_bytes(dump([meta("eof")]).rstrip(b"\n"))
    payload = read_session_meta(path)
    assert payload is not None
    assert payload["id"] == "eof"


def test_read_session_meta_skips_line_at_256k_boundary(tmp_path):
    path = tmp_path / "rollout-x.jsonl"
    path.write_bytes(b"x" * (256 * 1024) + b"\n" + dump([meta("after")]))
    payload = read_session_meta(path)
    assert payload is not None
    assert payload["id"] == "after"
```

- [ ] **Step 2: Run the new tests (they must PASS before refactoring)**

Run: `uv run pytest tests/test_session.py -k "unterminated_final_record or 256k_boundary" -v`
Expected: 2 passed。これは red-green ではなく現行挙動の固定（characterization）。

- [ ] **Step 3: Refactor `read_session_meta`**

`_session_meta_payload(record)` に「dict 判定 → `type == "session_meta"` → payload dict →
有効な `id`」を移す。`_discard_rest_of_oversized_line` は現行の内側 while をそのまま移し、
EOF で `None`、上限到達で `consumed` を返す。元関数の条件式・順序・戻り値の意味は変えない。

- [ ] **Step 4: Refactor discovery and scan helpers**

`iter_rollout_files` から `_within_root` と `_is_rollout_file_name` を抽出する。
`scan_all` はチャンクループを `_scan_file` に、Claude の重複置換/Codex 追記を
`_append_scan_entry` に、history_mode 算出を `_history_mode_label` に移す。

- [ ] **Step 5: Refactor read/poll/line processing**

`read_messages` の前提検査を `_ensure_readable`、1 エントリ読取を `_read_entry` に移す。
`poll` の早期リターン群（symlink / missing / 範囲外 / stat 失敗 / device・inode 変更 /
縮小）を `_poll_precheck` にまとめ、`stat` 結果も返して二重 stat を避ける。
`_process_line` は Claude / session_meta / Codex の 3 分岐を各ヘルパーへ委譲する。
`_LineSplitter.feed` は状態遷移が読める範囲で小さく抽出する（無理なら変更しない）。

- [ ] **Step 6: Run session tests and full suite**

Run: `uv run pytest tests/test_session.py -q && uv run pytest -q`
Expected: すべて PASS（合計 211 passed 以上）。

- [ ] **Step 7: Lint and measure**

Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/session.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/session.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: lint clean。`session.py` の最大 cognitive が 57 から低下し、合計も減少。

- [ ] **Step 8: Commit**

```bash
git add src/transpane/session.py tests/test_session.py
git commit -m "refactor(session): extract discovery, scan, and line helpers"
```

---

### Task 2: `markdown.py` の複雑度低減

**Files:**
- Modify: `src/transpane/markdown.py`
- Test: `tests/test_markdown.py`（既存のみ）

**Interfaces:**
- Consumes: 既存の `Unit`, `Segment`, `ProtectedDoc`, `_MARKER_RE`。
- Produces:
  - `_container_depth(stack: list[str]) -> int`
  - `_is_single_line_block(block_map: tuple[int, int] | None) -> bool`
  - `_record_allowed(allowed: dict[int, str], token, stack: list[str]) -> None`
  - `_nested_list_ranges(tokens) -> list[tuple[int, int]]`
  - `_drop_allowed_ranges(allowed: dict[int, str], ranges: list[tuple[int, int]]) -> None`
  - `_find_cut(text: str, start: int, window_end: int, marker_spans: list[tuple[int, int]]) -> int | None`
  - `_protect_core(core: str) -> tuple[str | None, bool, tuple[Segment, ...], tuple[str, ...]]`

- [ ] **Step 1: Run the existing markdown tests as the baseline**

Run: `uv run pytest tests/test_markdown.py -q`
Expected: all pass（特に `test_ambiguous_blocks_stay_original` が通ること）。

- [ ] **Step 2: Refactor `_allowed_lines`**

トークン走査は残し、許可判定を `_record_allowed`、コンテナ深さを `_container_depth`、
単一行判定を `_is_single_line_block`、ネストリスト範囲を `_nested_list_ranges`、
範囲削除を `_drop_allowed_ranges` に移す。`heading` は深さ 0、`paragraph` は深さ ≤1 の
条件と、`list_item` / `blockquote` / `paragraph` の優先順位を変えない。

- [ ] **Step 3: Refactor `split_protected`**

句読点優先→空白の 2 段カット探索を `_find_cut` にまとめる。マーカー内部を切らない判定、
`cut <= start` で `None` を返す条件、区切り空白の取り込みを変えない。

- [ ] **Step 4: Refactor `protect_document`**

`skip_reason` 判定・インライン保護・`split_protected`・`Segment` 生成を `_protect_core` に
まとめる。`marker_collision` / `long_unsplittable` の conservative 設定と
`inline_only` の扱いを変えない。

- [ ] **Step 5: Run tests, lint, and measure**

Run: `uv run pytest tests/test_markdown.py -q && uv run pytest -q`
Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/markdown.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/markdown.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: tests pass、lint clean、`markdown.py` の最大 cognitive が 45 から低下。

- [ ] **Step 6: Commit**

```bash
git add src/transpane/markdown.py
git commit -m "refactor(markdown): split block classification and segment protection"
```

---

### Task 3: `claude.py` の複雑度低減

**Files:**
- Modify: `src/transpane/claude.py`
- Test: `tests/test_session.py`（既存のみ）

**Interfaces:**
- Consumes: 既存の `AgentProvider`, `SessionInfo`。
- Produces:
  - `_is_contained(path: Path, root_real: Path) -> bool`
  - `_iter_project_transcripts(project: Path, root_real: Path) -> Iterator[Path]`
  - `_is_eligible_record(record: dict, stem: str) -> bool`
  - `_meta_values(record: dict) -> tuple[str | None, str | None]`
  - `_read_head_chunk(handle: BinaryIO, limit: int, consumed: int) -> tuple[bytes | None, int, int]`

- [ ] **Step 1: Run the Claude discovery tests as the baseline**

Run: `uv run pytest tests/test_session.py -k claude -q`
Expected: all pass。

- [ ] **Step 2: Refactor `iter_claude_files`**

symlink / 包含検査を `_is_contained`、プロジェクト配下の列挙を
`_iter_project_transcripts` に移す。`<project>/<uuid>.jsonl` の直接ファイル限定、
ソート順、`root_real` 配下の検査を変えない。

- [ ] **Step 3: Refactor `read_claude_meta`**

type / sessionId / sidechain の適格判定を `_is_eligible_record`、cwd / version の
正規化を `_meta_values` に移す。`isSidechain` が bool でない場合も除外する現行規則を
変えない。

- [ ] **Step 4: Refactor `_iter_head_lines`**

チャンク読取を `_read_head_chunk` に移す。`buffer.endswith(b"\n")` で yield、
`len(chunk) < chunk_size` で終了、`consumed < limit` の判定を変えない。

- [ ] **Step 5: Run tests, lint, and measure**

Run: `uv run pytest tests/test_session.py -q && uv run pytest -q`
Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/claude.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/claude.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: tests pass、lint clean、`claude.py` の最大 cognitive が 35 から低下。

- [ ] **Step 6: Commit**

```bash
git add src/transpane/claude.py
git commit -m "refactor(claude): extract transcript discovery and meta helpers"
```

---

### Task 4: `events.py` の複雑度低減

**Files:**
- Modify: `src/transpane/events.py`
- Test: `tests/test_events.py`, `tests/test_claude_events.py`（既存のみ）

**Interfaces:**
- Consumes: 既存の `ExtractResult`, `Message`, `MessageKind`, `make_revision`。
- Produces:
  - `_texts_from_blocks(blocks: list, ignored_blocks: frozenset[str]) -> list[str] | None`
  - `Extractor._extract_event_msg(payload, record, offset) -> ExtractResult | None`
  - `Extractor._extract_plan_item(item, record, offset) -> ExtractResult | None`
  - `ClaudeExtractor._validate_claude_record(record) -> tuple[str | None, dict[str, Any] | None]`
  - `ClaudeExtractor._claude_text(record_type: str, content: Any) -> str | None`

- [ ] **Step 1: Run the extractor tests as the baseline**

Run: `uv run pytest tests/test_events.py tests/test_claude_events.py -q`
Expected: all pass。

- [ ] **Step 2: Refactor `_content_text`**

list 側のブロック解釈を `_texts_from_blocks` に移す。dict 以外・未知 type は `None`、
`text` が str でない場合は `None`、空白のみは捨てる、という現行規則を変えない。

- [ ] **Step 3: Refactor Codex `Extractor`**

`extract` は `event_msg` 以外を `not_allowed` とし、`event_msg` の分岐を
`_extract_event_msg` に委譲する。`item_completed` / legacy の `user_message` /
`agent_message` の優先順位と、`_extract_item` が `None` を返した場合の
`not_allowed` を変えない。`Plan` 分岐は `_extract_plan_item` に移す。

- [ ] **Step 4: Refactor Claude `ClaudeExtractor`**

`_validate_claude_record` は次の順序で理由を返す:
`type` → `_excluded_by_flag` → `sessionId` 型/一致 → `uuid` → `message` dict →
`role` → synthetic → user の `toolUseResult`。理由がなければ
`(None, message)` を返す。`extract` は「検証 → `_claude_text` → Message 構築」の 3 段に
する。`not_allowed` / `excluded` / `thread_mismatch` / `missing_id` / `bad_payload` /
`role_mismatch` / `synthetic` / `empty` の出し分けを変えない。

- [ ] **Step 5: Run tests, lint, and measure**

Run: `uv run pytest tests/test_events.py tests/test_claude_events.py -q && uv run pytest -q`
Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/events.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/events.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: tests pass、lint clean、`events.py` の最大 cognitive が 25 から低下。

- [ ] **Step 6: Commit**

```bash
git add src/transpane/events.py
git commit -m "refactor(events): extract content and record validation helpers"
```

---

### Task 5: `translation.py` の複雑度低減

**Files:**
- Modify: `src/transpane/translation.py`
- Test: `tests/test_translation.py`（既存のみ）

**Interfaces:**
- Consumes: 既存の `_UnitTask`, `TaskRef`, `SchedulerStatus`, `ErrorKind`。
- Produces:
  - `TranslationScheduler._next_task_or_wait() -> _UnitTask | None`（async）
  - `TranslationScheduler._process_task(task: _UnitTask) -> None`（async）
  - `TranslationClient._post_completion(payload: dict) -> httpx.Response`（async）
  - `TranslationClient._parse_completion(body: Any) -> str`

- [ ] **Step 1: Run the translation tests as the baseline**

Run: `uv run pytest tests/test_translation.py -q`
Expected: all pass（特に cancel 系 3 テスト）。

- [ ] **Step 2: Refactor `TranslationScheduler.run`**

次の順序を保ったまま、ループ先頭を `_next_task_or_wait` に、1 タスク処理を
`_process_task` に移す:
pause → `_wake.clear()` → `await _wake.wait()` /
`_pop_task()` が `None` → `_wake.clear()` → queue/deferred があれば continue、なければ
`await _wake.wait()`。`_closed` のときのみ `None` を返す。

`_process_task` は「取消済みなら `_finish_task(cancelled=True)`」「
`await _translate_segment` → results 追加 → next_index 加算」「await 後の取消確認 →
`CANCELLED` 化して `_deliver`」「残セグメントがあれば再 push」「完了なら `_deliver`」
の順序を変えない。

- [ ] **Step 3: Refactor `TranslationClient.translate`**

HTTP POST と例外変換を `_post_completion`、レスポンス検証を `_parse_completion` に移す。
`JSONDecodeError`（`ValueError`）→ `INVALID`、`choices`/`finish_reason`/`message`/
`content` の検証順と `EMPTY` の条件を変えない。

- [ ] **Step 4: Run tests, lint, and measure**

Run: `uv run pytest tests/test_translation.py -q && uv run pytest -q`
Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/translation.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/translation.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: tests pass、lint clean、`translation.py` の最大 cognitive が 21 から低下。

- [ ] **Step 5: Commit**

```bash
git add src/transpane/translation.py
git commit -m "refactor(translation): extract scheduler loop and response parsing"
```

---

### Task 6: `config.py` の複雑度低減

**Files:**
- Modify: `src/transpane/config.py`
- Test: `tests/test_config.py`（既存のみ）

**Interfaces:**
- Consumes: 既存の `ConfigError`, `_RFC1918`。
- Produces:
  - `_split_base_url(raw: str) -> SplitResult`
  - `_validate_address(host: str) -> ipaddress.IPv4Address | ipaddress.IPv6Address`
  - `_normalized_url(parts: SplitResult, host: str, address: ipaddress.IPv4Address | ipaddress.IPv6Address) -> str`

- [ ] **Step 1: Run the config tests as the baseline**

Run: `uv run pytest tests/test_config.py -q`
Expected: all pass。

- [ ] **Step 2: Refactor `validate_base_url`**

検査順を変えずに 3 ヘルパーへ分割する:
`_split_base_url`（空・urlsplit・scheme・userinfo・query/fragment・path）、
`_validate_address`（hostname なし・IP リテラル・loopback/RFC1918）、
`_normalized_url`（port 解析・IPv6 ブラケット・正規化文字列）。
各 `ConfigError` の文言と送出順を変えない。

- [ ] **Step 3: Run tests, lint, and measure**

Run: `uv run pytest tests/test_config.py -q && uv run pytest -q`
Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/config.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/config.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: tests pass、lint clean、`validate_base_url` の cognitive が 17 から低下。

- [ ] **Step 4: Commit**

```bash
git add src/transpane/config.py
git commit -m "refactor(config): split base URL validation stages"
```

---

### Task 7: `cli.py` の `run_doctor` 低減

**Files:**
- Modify: `src/transpane/cli.py`
- Test: `tests/test_cli.py`

**Interfaces:**
- Consumes: 既存の `_root_state`, `_print_root`, `_ROOT_MESSAGES`, `claude_transcript_counts`,
  `TranslationCache`, `_list_models`, `TranslationFailure`。
- Produces:
  - `_doctor_roots(config: Config, source: AgentProvider | None) -> bool`
  - `_doctor_claude_counts(config: Config, source: AgentProvider | None) -> None`
  - `_doctor_cache(config: Config) -> bool`
  - `_doctor_translation(config: Config) -> bool`

- [ ] **Step 1: Add a characterization test for "all diagnostics run"**

`tests/test_cli.py` に追加する（既存テストは変更しない）:

```python
def test_doctor_runs_cache_diagnostics_when_translation_fails(env, capsys, monkeypatch):
    async def fake_models(config):
        raise TranslationFailure(ErrorKind.CONNECT)

    monkeypatch.setattr(cli, "_list_models", fake_models)
    code = cli.main(["doctor"])
    out = capsys.readouterr().out
    assert code == 1
    assert "接続失敗" in out
    assert "キャッシュ:" in out
```

- [ ] **Step 2: Run the new test (it must PASS before refactoring)**

Run: `uv run pytest tests/test_cli.py -k runs_cache_diagnostics -v`
Expected: 1 passed（characterization）。

- [ ] **Step 3: Refactor `run_doctor`**

ヘッダ表示の後、`_doctor_roots`・`_doctor_claude_counts`・`_doctor_cache`・
`_doctor_translation` を**すべて**呼ぶ。`ok` は各戻り値の AND で集約し、早期 return や
`and` による呼び出し省略をしない。`_doctor_translation` はサーバー行の表示、モデル一覧
取得、`[OK]`/`[NG]` 表示、終了コード寄与を現行どおり行う。

- [ ] **Step 4: Run tests, lint, and measure**

Run: `uv run pytest tests/test_cli.py -q && uv run pytest -q`
Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/cli.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/cli.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: tests pass、lint clean、`run_doctor` の cognitive が 24 から低下。

- [ ] **Step 5: Commit**

```bash
git add src/transpane/cli.py tests/test_cli.py
git commit -m "refactor(cli): split doctor diagnostics without short-circuiting"
```

---

### Task 8: `ui.py` の複雑度低減（非同期不変条件を維持）

**Files:**
- Modify: `src/transpane/ui.py`
- Test: `tests/test_ui.py`, `tests/test_claude_ui.py`

**Interfaces:**
- Consumes: 既存の `SessionReader`, `ScanResult`, `PollResult`, `SessionReadError`,
  `SessionIndexMismatch`, `TranslationState`。
- Produces:
  - `SnapshotOutcome`（dataclass, slots）:
    `messages: list[Message] | None` / `failure: str | None` /
    `unsupported_mode: str | None` / `read_error: SessionReadError | None` /
    `extracted: int`
  - `Monitor._read_snapshot(generation: int) -> SnapshotOutcome | None`（async）
  - `Monitor._handle_poll_result(result: PollResult, generation: int) -> bool`（async）
  - `Monitor._read_history_page(selected: list[IndexEntry], generation: int) -> list[Message] | None`（async）
  - `Monitor._prepend_views(messages: list[Message]) -> list[MessageView]`
  - `Monitor._retry_view(view: MessageView) -> None`
  - `Monitor._update_view(view: MessageView, message: Message, entry: IndexEntry) -> None`
  - `Monitor._append_view(message: Message, entry: IndexEntry) -> None`
  - `Monitor._result_view(ref: TaskRef) -> MessageView | None`

- [ ] **Step 1: Add a characterization test for switch-side index mismatch**

`tests/test_claude_ui.py` に追加する（既存テストは変更しない）。あわせて
`transpane.session` の import に `SessionIndexMismatch` を加える:

```python
async def test_switch_index_mismatch_rebuilds_instead_of_stopping(tmp_path, monkeypatch):
    config = make_config(tmp_path)
    write_claude_session(
        config.claude_projects_root, CLAUDE_ID, [claude_user(CLAUDE_ID, "hello")]
    )
    session = select_session(
        discover_all_sessions(
            config.sessions_root, config.claude_projects_root, AgentProvider.CLAUDE
        ),
        CLAUDE_ID,
    )
    original = SessionReader.read_messages
    calls = {"count": 0}

    def mismatch_once(self, entries):
        calls["count"] += 1
        if calls["count"] == 1:
            raise SessionIndexMismatch()
        return original(self, entries)

    monkeypatch.setattr(SessionReader, "read_messages", mismatch_once)
    monitor = Monitor(config, FakeListener(), transport=httpx.MockTransport(translating_handler))
    await monitor.start(session)
    assert monitor._note is not None and "索引不一致" in monitor._note
    assert not monitor._stopped
    assert calls["count"] >= 2
    await monitor.close()
```

- [ ] **Step 2: Run the new test (it must PASS before refactoring)**

Run: `uv run pytest tests/test_claude_ui.py -k switch_index_mismatch -v`
Expected: 1 passed（characterization。`switch` → `_rebuild` の経路を固定）。

- [ ] **Step 3: Extract `_read_snapshot` and rewiring only the common part**

`_read_snapshot(generation)` は次だけを行う:
scan（`OSError`/`SessionError` を `failure="scan_error"`）→ generation 不一致なら `None` →
unsupported / thread_changed 検査 → `_set_scan_entries` → `read_messages`
（`SessionIndexMismatch` を `failure="index_mismatch"`、`SessionReadError` を
`failure="read_error"` と `read_error` に保持）→ 成功なら `messages` と `extracted` を返す。
**`_stop` も `_rebuild` も呼ばない。**

`switch` と `_rebuild` の呼び出し元は、§5.5 の差分 6 点をそのまま保つ:
Reader の生成/再利用、`_stopped` の遷移、表示の初期化/保持、索引不一致時の
`_rebuild`/`_stop`、0 件 note、generation 更新・取消・polling 開始。フラグで挙動を
切り替えない。

- [ ] **Step 4: Extract poll/history/retry/ingest helpers**

`_poll_loop` の分岐を `_handle_poll_result`（`bool` は継続可否）へ移す。OSError 時の
`_note` 設定と `continue` は `_poll_loop` に残す。`add_history` は
`_read_history_page` と `_prepend_views` を使い、成功時のみ `_visible += len(selected)`。
`retry` は `_retry_view`、`_ingest` は `_update_view` / `_append_view`、
`_on_unit_done` は `_result_view` を使う。`_stop` は一元化したまま変更しない。

- [ ] **Step 5: Run the UI suites and full suite**

Run: `uv run pytest tests/test_ui.py tests/test_claude_ui.py -q && uv run pytest -q`
Expected: すべて PASS。

- [ ] **Step 6: Lint and measure**

Run: `uv run ruff check src tests && uv run ruff format --check src/transpane/ui.py`
Run: `omen complexity -p src -f json | jq -r '.files[] | select(.file|endswith("transpane/ui.py")) | [.total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv'`
Expected: lint clean、`ui.py` の最大 cognitive が 25 から低下。

- [ ] **Step 7: Commit**

```bash
git add src/transpane/ui.py tests/test_claude_ui.py
git commit -m "refactor(ui): extract monitor scan, poll, and history helpers"
```

---

### Task 9: 最終検証と複雑度レポート

**Files:**
- Create: `docs/superpowers/plans/2026-11-25-complexity-refactor-report.md`
- Verify: `src/` 全体

**Interfaces:**
- Consumes: 全タスクの変更。
- Produces: before/after の複雑度レポート。

- [ ] **Step 1: Run the full verification suite**

```bash
uv run pytest -q
uv run ruff check src tests
uv run ruff format --check src tests
git diff --check
git diff 507a820 --stat
```

Expected: 214 passed、lint/format clean、whitespace error なし。`git diff 507a820 --stat`
に `src/`・追加テスト・`docs/superpowers/specs/2026-11-25-complexity-refactor-design.md`・
本レポート以外が出ない。

- [ ] **Step 2: Collect after metrics and compare**

```bash
omen complexity -p src -f json > /tmp/omen-src-after.json
jq -r '.summary' /tmp/omen-src-after.json
jq -r '[.files[].functions[]] | length as $n | ([.[].metrics.cognitive]|add) as $c | ([.[].metrics.cyclomatic]|add) as $x | "functions=\($n) cognitive=\($c) cyclomatic=\($x)"' /tmp/omen-src-after.json
jq -r '.files[] | [.file, .total_cognitive, ([.functions[].metrics.cognitive]|max)] | @tsv' /tmp/omen-src-after.json | sort -t$'\t' -k2,2 -rn
```

Expected（改善目標）: `max_cognitive <= 15`、`p95_cognitive <= 10`、cognitive 合計 `<= 600`。
未達の場合は、関数名・値・理由をレポートに記す。

- [ ] **Step 3: Write the report**

`docs/superpowers/plans/2026-11-25-complexity-refactor-report.md` に次を書く:
- before/after の関数数、cognitive 合計、cyclomatic 合計、p95、最大値
- ファイル別 before/after 表
- cognitive > 15 が残った場合の例外一覧と理由
- 追加した characterization test の一覧
- コミット一覧

- [ ] **Step 4: Commit the report**

```bash
git add docs/superpowers/plans/2026-11-25-complexity-refactor-report.md
git commit -m "docs: report complexity refactor results"
```

---

## Execution Notes

- 各タスクは独立してテスト green でコミットする。失敗した場合は次のタスクへ進まない。
- characterization test は「現行挙動の固定」であり、red-green ではなく PASS を確認してから
  リファクタリングする。
- 数値目標のために可読性を犠牲にしない。例外は設計書 §2.1 に従い記録する。
- `ui.py` は最もリスクが高い。Step 3〜4 で await 境界・generation 確認・停止処理・
  queue 会計を変更していないことを diff で確認する。
