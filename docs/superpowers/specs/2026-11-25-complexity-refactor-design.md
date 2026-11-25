# Transpane 複雑度低減リファクタリング設計書

- 版: 1.1
- 作成日: 2026-11-25
- 状態: レビュー対応済み（承認待ち）
- 基点: 固定コミット 507a820（`main`）
- 作業ブランチ: `refactor/reduce-complexity`
- 計測: `omen 5.1.0`

### 変更概要（v1.0 → v1.1）

- P2-01: 数値目標を「原則目標」と「改善目標」に分離し、挙動維持・責務分離・
  可読性を優先する旨と、数値未達でも理由を報告して終了できる条件を明記。
- P2-02: `switch` と `_rebuild` の差分 6 点を明記し、共通化は同一契約の処理に
  限定。復旧・停止の判断は呼び出し元に残す。
- P2-03: `read_session_meta` の現行読取契約（未終端 JSON、256 KiB 以上の行の
  除外、上限判定）を維持することを明記。読取ループの仕様変更をスコープ外化。
- P2-04: Monitor / TranslationScheduler の非同期不変条件を新設（§6）。
- 任意改善: `ruff format --check` と `git diff --check` を追加、差分基準を
  507a820 に固定、関数数・cyclomatic・行数の増減報告を追加、
  `run_doctor` の全診断実行契約と `cache._ensure` の原則非変更を明記。

## 1. 目的

`omen` が報告する循環的複雑度（cyclomatic）と認知的複雑度（cognitive）を下げ、
読み手が一関数を一度に把握できる状態にする。振る舞いと公開 API は変えない。

## 2. 目標・非目標・反目標

### 2.1 数値目標の位置付け

計測はすべて `omen complexity -p src -f json`（omen 5.1.0、基点 507a820）で行う。
`omen -f json all` の summary は tests を含むため、合否判定には使わない。

ベースライン: 関数 186、合計 cognitive 1000、合計 cyclomatic 748、最大 cognitive
57、p95 cognitive 24、p90 cognitive 17。

**原則目標（達成に努める）**

- `src/transpane/` の全関数で cognitive ≤ 15。現状 24 関数が超過している。
- 合理的な例外がある場合は、関数名・値・理由を最終報告に記録する。

**改善目標（達成が望ましい）**

- `src/transpane/` の cognitive p95 ≤ 10（cognitive > 10 の関数を 9 個以下）。
- `src/transpane/` の合計 cognitive ≤ 600（1000 から 40% 以上削減）。

**優先順位**

- 挙動維持、責務分離、可読性を数値目標より優先する。
- 数値達成のみを目的とした追加の小関数抽出は禁止する。
- 合理的な改善余地がなくなった場合は、数値未達でも理由を報告して終了できる。
- 新しい合格閾値は設けない。関数数・cyclomatic 合計・コード行数の増減は
  参考値として報告する。

### 2.2 非目標

- 機能追加、挙動変更、出力文言の変更。
- 依存追加、Python バージョン変更、UI フレームワーク変更。
- テストの複雑度低減（対象は `src/` のみ）。
- ベンチマーク最適化や性能改善。
- 既存不具合の修正。発見しても今回のスコープでは直さず、報告に留める。
- モジュールの物理分割（新規ファイルへの移動）。

### 2.3 反目標（これをやったら失敗）

- テストを書き換えて通す、またはテストをスキップ/削除する。
- 公開関数のシグネチャや意味を変える。
- 例外の種類・タイミング・エラーメッセージを変える。
- 非同期処理の await 境界、状態遷移、処理順序を変える（§6）。
- ヘルパー分割そのものが目的化し、1〜2 行の関数を大量に作る。
- 複数の挙動を切り替えるためだけの過剰なフラグや抽象化を追加する。

### 2.4 テストの扱い

既存テストは 1 件も変更・削除しない。挙動等価性を固定するための
characterization test の**追加は許可**する。追加した場合は最終報告に明記する。

## 3. 制約

- 公開・テスト依存 API は維持する:
  `read_session_meta` / `split_protected` / `protect_document` /
  `sanitize_for_display` / `initial_state` / `MessageView` / `Monitor` /
  `TranspaneApp` / `SessionScreen` / `SessionReader` / `discover_sessions` /
  `discover_all_sessions` / `select_session` / `reader_for_session` /
  `claude_transcript_counts` / `discover_claude_sessions` / `Extractor` /
  `ClaudeExtractor` / `cache_key` / `TranslationCache` / `TranslationClient` /
  `TranslationScheduler` / `ErrorKind` / `TranslationFailure`。
- ファイル構成は維持する。分割は同一ファイル内の関数抽出に限る。
- `ruff` の設定（py312、line-length 100、E/F/I/UP/B/SIM）に従う。
- 1 コミット = 1 モジュール（または密接な 2 モジュール）とし、各コミットで
  テストと lint が通る状態を保つ。

## 4. 検討した方式

| 方式 | 内容 | 評価 |
|---|---|---|
| A. 抽出中心＋最小の整理（採用） | Extract Method を主軸にし、責務が混在する関数だけ内部で分割 | リスクと効果のバランスが良い。テストがそのまま回帰網になる |
| B. 純粋な抽出のみ | 関数抽出だけ。構造整理はしない | 安全だが `scan_all` や `_poll_loop` の見通しは悪いまま |
| C. モジュール分割まで | session.py を discovery/reader 等へ物理分割 | 効果は最大だが import 変更と diff が大きく、今回の目的に対して過剰 |

採用理由: 複雑度の大半は「一関数に複数の段階が直列に並ぶ」ことに起因する。
関数抽出で計測値と可読性の両方を下げられ、公開 API とファイル構成を保てる A が
最も費用対効果が高い。C は将来の課題として残す。

## 5. 具体的な設計

各項目は「抽出するヘルパー」と「元関数に残す責務」を示す。ヘルパー名は実装時に
調整してよいが、責務の分割線は維持する。

### 5.1 `session.py`（合計 241、最大 57）

- `read_session_meta` (57)
  - `_session_meta_payload(record)` : `session_meta` かつ有効な `id` を持つ
    payload を返す。不正なら None。これは純粋な判定であり、IO に触れない。
  - `_discard_rest_of_oversized_line(handle, consumed)` : 現行の内側 while
    （改行まで読み捨て、EOF で None、上限到達で consumed を返す）をそのまま
    移す。**意味を変えない。**
  - 元関数は「上限まで行を読み、巨大断片だけ読み捨て、JSON を解析し、
    payload を判定する」流れを保つ。

**P2-03: メタデータ読取契約の維持（変更禁止）**

現行 `read_session_meta` の挙動を次のとおり維持する。これらは今回のスコープで
変更しない。Claude Code 側（`claude._iter_head_lines`）の読取規則とは異なるため、
混同・共通化しない。

- `handle.readline(256 * 1024)` で読む断片が改行で終わらず、長さが 256 KiB
  以上の場合、その論理行は 256 KiB 以上の行として扱い、改行まで（または
  `META_READ_LIMIT` まで）読み捨てて解析しない。
- 改行で終わらない断片が 256 KiB 未満の場合は EOF の未終端行であり、現行どおり
  `json.loads` に渡す。したがって「完全行のみを採用する」仕様には変更しない。
- 読み取り上限は `consumed < META_READ_LIMIT`（1 MiB）で判定する。1 回の
  `readline` で上限を超えることは許容し、厳密な上限へ変更しない。
- 上記のため、行読取ループの抽出は「内側の読み捨てループを関数へ移す」に留め、
  条件式・順序・戻り値の意味を変えない。リスクが高いと判断した場合は、
  この関数の IO ループは抽出せず、payload 判定の抽出だけで打ち切ってよい。
- `iter_rollout_files` (21)
  - `_within_root(path, root_real)` : symlink 検査と resolve 包含検査。
  - `_is_rollout_name(name)` : `rollout-*.jsonl` 判定。
- `scan_all` (34)
  - `_scan_file(handle)` : チャンク読みと行処理のループ。
  - `_add_scan_entry(entries, positions, entry)` : Claude の重複・改訂の
    置換規則と Codex の追記規則。
  - `_history_mode_label()` : 戻り値の history_mode 算出。
- `read_messages` (28)
  - `_ensure_readable()` : unsupported/halted/symlink/missing/範囲外の検査。
  - `_read_entry(handle, entry)` : 1 エントリの seek/read/parse/照合。
- `_process_line` (24)
  - `_process_claude_line(record, offset, stats)`、
    `_process_session_meta(record, stats)`、
    `_process_codex_line(record, offset, stats)` に分岐ごとに抽出。
- `poll` (17)
  - `_poll_precheck()` : symlink/missing/範囲外/stat 不変・縮小の早期結果。
- `_LineSplitter.feed` (15)
  - `_take_dropped_tail()` / `_take_oversized_line()` の小さな抽出。
    状態遷移が読み取れる粒度に留める。

### 5.2 `markdown.py`（合計 155、最大 45）

- `_allowed_lines` (45)
  - `_container_depth(stack)`、`_is_translatable_block(token, stack)`、
    `_record_allowed(allowed, token, stack)`、
    `_nested_list_ranges(tokens)`、`_drop_ranges(allowed, ranges)`。
  - 元関数は「token を走査し、許可行を集め、複雑リスト範囲を落とす」流れにする。
- `split_protected` (36)
  - `_find_cut(text, start, window_end, marker_spans)` : 句読点優先→空白の
    2 段探索を 1 関数にまとめる。
  - `_skip_spaces(text, index)`。
- `protect_document` (22)
  - `_protect_core(core)` : skip 判定・インライン保護・分割・Segment 生成を
    まとめ、`(skip_reason, conservative, segments, markers)` を返す。

### 5.3 `claude.py`（合計 90、最大 35）

- `iter_claude_files` (35)
  - `_is_contained(path, root_real)`、
    `_iter_project_transcripts(project, root_real)`。
- `read_claude_meta` (23)
  - `_is_eligible_record(record, stem)` : type/sessionId/sidechain の判定。
  - `_meta_values(record)` : cwd/version の正規化。
- `_iter_head_lines` (20)
  - 行の切り出しと上限判定を `_read_head_chunk` 等に抽出。

### 5.4 `events.py`（合計 98、最大 25）

- `_content_text` (25)
  - `_texts_from_blocks(blocks, ignored_blocks)` : list 側の解釈。
  - 元関数は str/list の入口分岐と内部タグ検査・join に絞る。
- `Extractor.extract` (18)
  - `_extract_event_msg(payload, record, offset)` に event_msg 分岐を抽出。
- `Extractor._extract_item` (14)
  - `_extract_plan_item(item, record, offset)` を抽出。
- `ClaudeExtractor.extract` (16)
  - `_validate_claude_record(record)` : type/flag/session/uuid/role/synthetic の
    事前条件を 1 関数にまとめ、失敗理由を返す。
  - `_claude_text(record, content)` : user/assistant の text 抽出。
  - 元関数は「検証 → text → Message 構築」の 3 段にする。

### 5.5 `ui.py`（合計 207、最大 25）

#### P2-02: `switch` と `_rebuild` の共通化境界

両者は「scan → 検証 → 読取 → 反映」が共通である一方、次の 6 点が異なる。
**これらは統合せず、現行挙動を維持する。**差分は呼び出し元に残し、共通化は
同一契約を持つ処理（スキャン結果の検証、エントリ設定、メッセージ読取）に限る。

1. **SessionReader の生成と再利用**: `switch` は `reader_for_session` で新しい
   `SessionReader` を生成して `self._reader` に設定する。`_rebuild` は既存の
   `self._reader` を再利用する。
2. **`_stopped` の状態遷移**: `switch` は開始時に `_stopped=False` とし、
   スキャン中も True にしない。`_rebuild` は開始時に `_stopped=True` とし、
   成功後に False へ戻す（失敗時は停止したまま）。
3. **既存表示の保持・初期化**: `switch` はスキャン前に `_views` / `_order` /
   `_entries` / `_entry_positions` / `_visible` を初期化する。`_rebuild` は
   既存表示を保持したままスキャンし、成功時に `_replace_views` で置換する
   （開始時は pending のクリアのみ）。
4. **索引不一致時の扱い**: `switch` は `SessionIndexMismatch` で
   `_rebuild(...)` を呼ぶ。`_rebuild` は
   `_stop("索引とログの内容が一致しません（監視停止）", reset=True)` を呼ぶ。
5. **抽出 0 件の note**: `switch` は `"対象イベント未検出（ログ形式または
   内部イベントのみ）"` を設定する。`_rebuild` は渡された note に
   `" / 対象イベント未検出"` を追記する。
6. **generation 更新・翻訳取消・polling 開始**: 両者とも generation を進めて
   旧 generation を `cancel_generation` するが、`_rebuild` は加えて
   `_clear_pending()` を行う。polling 開始はいずれも成功後のみ。

共通化の形:

- `_read_snapshot(generation)` : scan（`OSError`/`SessionError` 捕捉）→
  unsupported / thread_changed 検査 → `_set_scan_entries` → `read_messages`
  までを行い、成否を構造化した結果（成功時のメッセージ列、または失敗理由
  `scan_error` / `unsupported` / `thread_changed` / `read_error` /
  `index_mismatch`）で返す。`_stop` や `_rebuild` の呼び出しはしない。
- 復旧・停止の判断、`_stopped` の遷移、表示の初期化・保持、note の文言、
  generation 更新と pending クリアは、上記 1〜6 に従い呼び出し元に残す。
- 挙動を切り替えるためだけのブール引数やモードフラグを `_read_snapshot` に
  追加しない。差分は呼び出し元の分岐として表現する。

#### その他の抽出

- `_poll_loop` (25)
  - `_handle_poll_result(result, generation)` : denied/missing/rebuilt/
    thread_changed/messages の分岐を移動。
- `add_history` (22)
  - `_read_history_page(selected, generation)` : 読み取りとエラー整形。
  - `_prepend_views(messages)` : 重複除外・view 追加・order 更新・schedule。
- `retry` (17)
  - `_retry_view(view)` を抽出。
- `_ingest` (14)
  - `_update_view(view, message, entry)` と `_append_view(message, entry)`。
- `_on_unit_done` (12)
  - `_accepts_unit_result(ref)` に前提検査をまとめる。

### 5.6 `translation.py`（合計 78、最大 21）

- `TranslationScheduler.run` (21)
  - `_next_task_or_wait()` : pause/queue 空/Event 待ちをまとめる。
  - `_process_task(task)` : 1 セグメント翻訳と再投入・完了処理。
- `TranslationClient.translate` (14)
  - `_parse_completion(body)` : choices/finish_reason/message/content の検証。
  - `_post_completion(payload)` : HTTP 例外の変換。

### 5.7 `cli.py`（合計 55、最大 24）

- `run_doctor` (24)
  - `_doctor_roots(...)` : root 状態の判定と表示。
  - `_doctor_claude_counts(...)`、
  - `_doctor_cache(config)`、`_doctor_translation(config)`。
  - 元関数は「各診断を順に呼び、ok を集約する」だけにする。
  - **全診断実行契約の維持**: 設定・root・Claude 件数・キャッシュ・翻訳
    サーバーの各診断は、先行する診断が失敗しても実行する。短絡評価
    （早期 return や `and`/`or` による診断省略）を導入しない。`ok` の集約と
    終了コードの意味は現行どおり。

### 5.8 `config.py`（合計 37、最大 17）

- `validate_base_url` (17)
  - `_split_base_url(raw)` : urlsplit と scheme/userinfo/query/path 検査。
  - `_validate_address(host)` : IP リテラル検査と loopback/RFC1918 検査。
  - `_normalized_url(parts, host, address)` : 正規化 URL の組み立て。
  - 元関数は 3 呼び出しの直列にする。

### 5.9 `cache.py`（合計 32、最大 14）

- `_ensure` は現状で cognitive 最大値の目標内にある。**責務分離上の明確な利益が
  ない限り変更しない。**変更する場合も §3 の制約と §6 の非同期不変条件
  （該当する場合）を守る。

## 6. 非同期処理の不変条件（P2-04）

`Monitor`（`ui.py`）と `TranslationScheduler`（`translation.py`）の抽出では、
次の不変条件を維持する。**抽出に伴う await 境界、状態遷移、処理順序の変更は
禁止する。**該当箇所を変更しないことが最も安全であり、変更が必要な場合は
characterization test を先に追加する。

### Monitor

- **generation 確認**: `await` を挟む処理（`scan_all` / `read_messages` /
  `poll` / `reconnect`）の前後で `generation != self._generation` を確認し、
  古い処理結果を反映しない。`switch` / `_rebuild` / `add_history` / `retry` /
  `_poll_loop` / `_ingest` の各確認位置を変えない。
- **停止の一元化**: 監視停止は `_stop` を通す。`_stop` は `_stopped=True`、
  generation 更新、`scheduler.cancel_generation`、`_clear_pending`、
  必要時の reset 表示、note 設定、status 通知を行う。
- **自動復帰の禁止**: `_stop` 後は、明示的なセッション再選択（`switch`）まで
  監視・翻訳を再開しない。`_poll_loop` は denied / thread_changed で return し、
  自発的に再試行しない。
- **再構築中の予約禁止**: `_rebuild` 中は `_stopped=True` のため
  `_schedule` / `_submit_unit` は何もしない。この順序を変えない。
- **履歴ページ位置**: `add_history` は `end = len(self._entries) - self._visible`、
  `start = max(0, end - HISTORY_PAGE_SIZE)` で対象を決め、成功時のみ
  `self._visible += len(selected)` とする。失敗時に位置を進めない。
- **例外分類と診断表示**: `SessionReadError.reason`（`denied` / `halted` /
  `missing` / `read` 等）の分類と `_read_error_note` の文言、`PollResult` の
  denied / missing / rebuilt / thread_changed の意味を変えない。

### TranslationScheduler

- **HTTP 開始直前の取消判定**: `_translate_segment` はメモ・キャッシュ確認の後、
  `client.translate` を呼ぶ直前に
  `task.ref.generation <= self._cancelled_through` を再確認する。この位置を
  変えない。
- **await 後の取消確認**: `_translate_segment` の await 後と `run` の
  `_translate_segment` 呼び出し後に、generation の取消判定と結果の
  `ErrorKind.CANCELLED` 化を行う。順序を変えない。
- **queue / deferred / active の整合性**: `submit` は空きがあれば heap へ
  push して `active += 1`、なければ deferred へ積む。`cancel_generation` は
  残存数に応じて `active` を減算し、queue を heapify し、deferred を filter
  して `_promote` する。`_promote` は deferred から queue へ移すたびに
  `active += 1`。`_finish_task` / `_deliver` は `active` を減算して promote
  する。この会計を変えない。
- **Event の clear/set 順序**: `run` は pause 時に `clear` → `wait`、
  タスクなし時に `clear` → （queue/deferred があれば continue、なければ）
  `wait`。`submit` / `_promote` / `reconnect` は `set`。順序を変えない。
- **pause 中の抑止**: `_paused` の間はキューから取り出さない。サーバー
  不達（`server_unavailable`）で pause し、`reconnect` 成功で解除する。
- **状態通知**: `_emit_status` の pending は `active + len(deferred)`。
  `SchedulerStatus` の意味と接続状態の遷移を変えない。

## 7. 検証

1. 各コミット前後で以下を実行する。
   - `uv run pytest -q`
   - `uv run ruff check src tests`
   - `uv run ruff format --check src tests`
   - `git diff --check`
2. 完了時に `omen complexity -p src -f json` を再取得し、以下を確認する。
   - src 全関数の cognitive 最大 ≤ 15（例外は理由付きで記録）
   - src cognitive p95 ≤ 10、かつ cognitive > 10 の関数が 9 個以下（改善目標）
   - src 合計 cognitive ≤ 600（改善目標）
3. 関数名の対応表（before → after）を最終報告に含める。
4. `git diff 507a820 --stat` で、`src/`・追加テスト・設計書以外に意図しない
   変更がないことを確認する。差分の基準は `main` ではなく固定コミット
   507a820 とする。
5. 参考値として、関数数、cyclomatic 合計、コード行数の before/after と増減を
   報告する。新しい合格閾値は設けない。

## 8. 実装者に委ねる点

- ヘルパー名・引数順・docstring の有無。
- 抽出後の関数順序（呼び出し順に並べる）。
- 1 コミットに含めるファイル範囲（ただし各コミットでテスト green）。
- cognitive ≤ 15 を満たすために追加の小抽出が必要かどうかの判断
  （数値合わせだけの抽出は §2.1 で禁止）。
- 40% 削減に届かない場合、残りをどの関数から削るか（まず最大値の降順）。

## 9. レビュー対応表

| 指摘 | 対応箇所 | 内容 |
|---|---|---|
| P2-01 数値目標の位置付け | §2.1 | 原則目標と改善目標を分離。挙動・責務・可読性を優先。例外記録と未達終了条件を明記。数値合わせの小抽出を禁止 |
| P2-02 共通化境界 | §5.5 | `switch` / `_rebuild` の差分 6 点を明記。共通化は scan 検証・エントリ設定・読取に限定。復旧・停止判断は呼び出し元。過剰フラグ禁止 |
| P2-03 メタデータ読取契約 | §5.1 | 未終端 JSON、256 KiB 以上の行の除外、`consumed < 1 MiB` の上限判定を維持。Claude 側と混同しない。読取ループ抽出は意味不変または見送り |
| P2-04 非同期処理の不変条件 | §6 | Monitor / Scheduler の generation、取消、queue 会計、Event 順序、pause、停止一元化、自動復帰禁止、履歴位置、例外分類を明記 |
| 任意: cache `_ensure` | §5.9 | 明確な利益がなければ変更しない |
| 任意: 検証追加 | §7 | `ruff format --check`、`git diff --check` を追加 |
| 任意: 差分基準 | §7.4 | 507a820 固定 |
| 任意: 増減報告 | §7.5 | 関数数・cyclomatic・行数を報告、新閾値なし |
| 任意: `run_doctor` | §5.7 | 全診断実行契約を維持し短絡評価を禁止 |
