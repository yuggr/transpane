# 複雑度リファクタリング最終レポート

設計書 `docs/superpowers/specs/2026-11-25-complexity-refactor-design.md` と計画書
`docs/superpowers/plans/2026-11-25-complexity-refactor.md` に基づくリファクタリングの結果をまとめる。
本レポートの計測値は、コード最終コミット `8d08830`（`refactor(claude): extract head record parsing`）時点のものである。
本レポートを更新するコミットは計測対象に含まない。
基点は `507a820` である。

## 検証結果

`8d08830` で次を実行し、すべて成功した。

```
uv run pytest -q                       214 passed in 11.10s
uv run ruff check src tests            No issues found
uv run ruff format --check src tests   24 files already formatted
git diff --check                       （出力なし、exit 0）
```

`pytest` の件数は、ベースラインの展開ツリーで同じ手順を実行して確認した 209 件に、
後述の characterization test 5 件を加えた 214 件である。
テストの変更は追加のみで、既存テストの削除や改変はない。

## 全体指標の before/after

ベースラインは `git archive 507a820` の展開ツリーを同じ omen で計測して再現した。
設計書に記録された 186 関数、cognitive 合計 1000、cyclomatic 合計 748、最大 57、
p95 24、p90 17 と一致する。
after は `8d08830` の作業ツリーを `omen complexity -p src -f json` で計測した値である。

| 指標 | before (507a820) | after (8d08830) | 差分 |
| --- | ---: | ---: | ---: |
| 関数数 | 186 | 255 | +69 |
| cognitive 合計 | 1000 | 842 | -158（-15.8%） |
| cyclomatic 合計 | 748 | 841 | +93（+12.4%） |
| cognitive 平均 | 5.38 | 3.30 | -2.08 |
| cognitive p50 | 1 | 2 | +1 |
| cognitive p90 | 17 | 9 | -8 |
| cognitive p95 | 24 | 10 | -14 |
| cognitive 最大 | 57 | 15 | -42 |
| cyclomatic 平均 | 4.02 | 3.30 | -0.72 |
| cyclomatic 最大 | 17 | 11 | -6 |
| cognitive > 10 の関数数 | 32 | 9 | -23 |
| cognitive > 15 の関数数 | 24 | 0 | -24 |

関数数は 37.1% 増えた。
これは Extract Method で元関数の本体をヘルパーへ移した結果である。
omen の `lines` 指標で 2 行以下の関数を数えると 28 個から 31 個へ 3 個増えただけで、短い関数の量産にはなっていない。
cyclomatic 合計が増えたのは関数数が増えたためで、1 関数あたりの平均は 4.02 から 3.30 へ下がっている。

## ファイル別の内訳

cognitive と cyclomatic はファイル内の全関数の合計、関数数はファイル内の関数の個数である。
before の値はベースライン計測、after の値は `8d08830` の計測による。

| ファイル | cognitive before | cognitive after | cyclomatic before | cyclomatic after | 関数数 before | 関数数 after | after 平均 cognitive |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `ui.py` | 207 | 197 | 218 | 234 | 69 | 79 | 2.49 |
| `session.py` | 241 | 180 | 130 | 157 | 25 | 45 | 4.00 |
| `markdown.py` | 155 | 121 | 88 | 100 | 16 | 28 | 4.32 |
| `events.py` | 98 | 84 | 71 | 83 | 16 | 22 | 3.82 |
| `translation.py` | 78 | 74 | 70 | 76 | 23 | 27 | 2.74 |
| `claude.py` | 90 | 64 | 44 | 58 | 5 | 15 | 4.27 |
| `cli.py` | 55 | 45 | 45 | 48 | 9 | 13 | 3.46 |
| `config.py` | 37 | 38 | 33 | 36 | 4 | 7 | 5.43 |
| `cache.py` | 32 | 32 | 32 | 32 | 10 | 10 | 3.20 |
| `models.py` | 7 | 7 | 17 | 17 | 9 | 9 | 0.78 |
| `__main__.py` | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| `__init__.py` | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 合計 | 1000 | 842 | 748 | 841 | 186 | 255 | 3.30 |

`config.py` だけが cognitive を 1 増やした。
`validate_base_url` の段階分割で、各段階の早期 return が独立した関数の cognitive として数えられるためである。
`cache.py` と `models.py` は設計どおり変更していない。

## 数値目標の達成状況

| 目標 | 判定 | 実測 |
| --- | --- | --- |
| 全関数の cognitive ≤ 15 | 達成 | 最大 15、超過 0 件 |
| cognitive p95 ≤ 10 | 達成 | 10 |
| cognitive > 10 の関数が 9 個以下 | 達成 | 9 |
| cognitive 合計 ≤ 600 | 未達 | 842 |

### cognitive > 15 の例外

例外はない。
最大値は 15 で、`read_session_meta`（`session.py:174`）と `_LineSplitter.feed`（`session.py:325`）の 2 関数がこの値に達する。
どちらも設計書 §5.1 の範囲で抽出を止めた結果であり、閾値超えを理由づけて残した例外ではない。

`read_session_meta` は 256 KiB 断片の読み捨てと 1 MiB 上限の判定順序が契約であり（設計書 §5.1 の P2-03）、
1 行分の `readline` と巨大行の読み捨て判断を `_read_meta_step` へ、payload 判定を `_session_meta_payload` へ移したが、条件式と評価順序は変えていない。
契約は characterization test 3 件で固定しており、全ブランチレビューの差分確認でも挙動の差は検出されていない。
`_LineSplitter.feed` は状態遷移（`dropping` と `buf` の更新順）を読み取れる粒度に保つ必要があり、これ以上の分割を見送った。

### cognitive > 10 の関数

| ファイル | 関数 | cognitive | cyclomatic |
| --- | --- | ---: | ---: |
| `session.py` | `read_session_meta` | 15 | 7 |
| `session.py` | `_LineSplitter.feed` | 15 | 7 |
| `config.py` | `load_config` | 13 | 10 |
| `ui.py` | `retry` | 12 | 8 |
| `ui.py` | `_rebuild` | 12 | 11 |
| `events.py` | `_validate_claude_record` | 12 | 10 |
| `translation.py` | `_translate_segment` | 11 | 8 |
| `events.py` | `_extract_event_msg` | 11 | 7 |
| `cli.py` | `run_inspect` | 11 | 9 |

p95 の実測値は 10 で、10 を超える関数は 9 個である。
設計書の改善目標「p95 ≤ 10、かつ cognitive > 10 の関数が 9 個以下」の両方を満たす。
降順では 7 番目から 9 番目までが 11、10 番目以下は 10 以下であり、p95 の計算結果も 10 になる。

### cognitive 合計 ≤ 600 が未達の理由

合計は 842 で、目標の 600 を 242 上回る。
ベースラインからの削減率は 15.8% で、目標の 40% に届かない。

Extract Method は分岐を削除せず、関数の境界を移すだけである。
cognitive complexity は関数ごとに計算して合計する指標なので、ネストの深さと 1 関数あたりの最大値は下がるが、
分岐の総数はほぼそのまま残る。
実際、最大値は 57 から 15、p95 は 24 から 10 へ下がった一方、合計の削減は 158 に留まった。

合計を 600 以下にするには、分岐そのものを統合するか、1〜2 行の関数へ機械的に切り分けることになる。
前者は挙動変更を伴い、後者は設計書 §2.3 の反目標（ヘルパー分割の目的化）に該当する。
設計書 §2.1 は挙動維持、責務分離、可読性を数値目標より優先し、数値合わせの小抽出を禁じているため、
ここで打ち切り、未達として記録する。

## 関数名の対応表

設計書 §7.3 が求める before → after の対応を、主要なホットスポットについて示す。
cognitive はその関数単体の値で、括弧内は `507a820` から `8d08830` への変化である。

| ファイル | before の関数（cognitive） | after の関数（cognitive） | 主な抽出先 |
| --- | --- | --- | --- |
| `session.py` | `read_session_meta` (57) | `read_session_meta` (15) | `_session_meta_payload`, `_discard_rest_of_oversized_line`, `_read_meta_step` |
| `session.py` | `scan_all` (34) | `scan_all` (2) | `_scan_file`, `_scan_line`, `_append_scan_entry`, `_history_mode_label` |
| `session.py` | `read_messages` (28) | `read_messages` (8) | `_ensure_readable`, `_read_entry` |
| `session.py` | `_process_line` (24) | `_process_line` (5) | `_process_claude_line`, `_process_session_meta`, `_process_codex_line` |
| `session.py` | `poll` (17) | `poll` (9) | `_poll_precheck` |
| `session.py` | `_LineSplitter.feed` (15) | `_LineSplitter.feed` (15) | なし（状態遷移と上限判定を維持） |
| `session.py` | `iter_rollout_files` (21) | `iter_rollout_files` (9) | `_is_rollout_file_name`, `_within_root`, `_kept_subdirectories`, `_rollout_path` |
| `markdown.py` | `_allowed_lines` (45) | `_allowed_lines` (7) | `_container_depth`, `_is_single_line_block`, `_heading_allowed`, `_paragraph_container`, `_record_allowed`, `_has_nested_item`, `_nested_list_ranges`, `_drop_allowed_ranges` |
| `markdown.py` | `split_protected` (36) | `split_protected` (9) | `_find_punctuation_cut`, `_find_whitespace_cut`, `_find_cut` |
| `markdown.py` | `protect_document` (22) | `protect_document` (10) | `_protect_core` |
| `claude.py` | `iter_claude_files` (35) | `iter_claude_files` (4) | `_is_contained`, `_is_transcript_child`, `_iter_project_transcripts`, `_iter_projects` |
| `claude.py` | `read_claude_meta` (23) | `read_claude_meta` (5) | `_is_eligible_record`, `_meta_values`, `_parse_head_record` |
| `claude.py` | `_iter_head_lines` (20) | `_iter_head_lines` (4) | `_read_head_chunk`, `_read_head_line`, `_iter_complete_lines` |
| `events.py` | `Extractor.extract` (18) | `Extractor.extract` (1) | `_extract_event_msg` |
| `events.py` | `_extract_item` (14) | `_extract_item` (8) | `_extract_plan_item` |
| `events.py` | `_content_text` (25) | `_content_text` (8) | `_texts_from_blocks`, `_block_text` |
| `events.py` | `ClaudeExtractor.extract` (16) | `ClaudeExtractor.extract` (3) | `_validate_claude_record`, `_claude_text` |
| `ui.py` | `switch` (17) | `switch` (10) | `_scan_reader`, `_read_snapshot`, `_scan_failure` |
| `ui.py` | `_rebuild` (19) | `_rebuild` (12) | `_read_snapshot`（`switch` と共通） |
| `ui.py` | `_poll_loop` (25) | `_poll_loop` (10) | `_handle_poll_result` |
| `ui.py` | `add_history` (22) | `add_history` (7) | `_read_history_page`, `_prepend_views` |
| `ui.py` | `retry` (17) | `retry` (12) | `_retry_view` |
| `ui.py` | `_ingest` (14) | `_ingest` (10) | `_update_view`, `_append_view` |
| `ui.py` | `_on_unit_done` (12) | `_on_unit_done` (6) | `_result_view` |
| `translation.py` | `run` (21) | `run` (4) | `_next_task_or_wait`, `_process_task` |
| `translation.py` | `translate` (14) | `translate` (3) | `_post_completion`, `_parse_completion` |
| `cli.py` | `run_doctor` (24) | `run_doctor` (3) | `_doctor_roots`, `_doctor_claude_counts`, `_doctor_cache`, `_doctor_translation` |
| `config.py` | `validate_base_url` (17) | `validate_base_url` (1) | `_split_base_url`, `_validate_address`, `_normalized_url` |

Task 8.5 の hardening では、それまでに抽出したヘルパーをさらに分割した。
対象は `_record_allowed`、`_nested_list_ranges`、`_find_cut`、`_iter_head_lines` などで、
`_heading_allowed`、`_paragraph_container`、`_has_nested_item`、`_find_punctuation_cut`、`_find_whitespace_cut`、`_read_head_line`、`_iter_complete_lines` を追加した。
Task 8.6（`8d08830`）は `read_claude_meta` から `_parse_head_record` を抽出し、この関数を 11 から 5 へ下げた。

## 追加した characterization test

挙動等価性を固定するため、次の 5 件を追加した。
いずれもリファクタリング前の現行挙動を確認してから追加しており、red-green のためのテストではない。
追加した 5 件をベースラインの `src/` に対して実行し、すべて pass することも確認した。

`tests/test_session.py`（3 件）:

- `test_read_session_meta_accepts_unterminated_final_record`：EOF の未終端行を解析する契約を固定する。
- `test_read_session_meta_skips_line_at_256k_boundary`：256 KiB 境界の行を読み捨てる契約を固定する。
- `test_read_session_meta_drains_oversized_head_instead_of_parsing_tail`：巨大行の後半を解析しない契約を固定する。

`tests/test_cli.py`（1 件）:

- `test_doctor_runs_cache_diagnostics_when_translation_fails`：モデル一覧の失敗後もキャッシュ診断を実行する契約を固定する。

`tests/test_claude_ui.py`（1 件）:

- `test_switch_index_mismatch_rebuilds_instead_of_stopping`：索引不一致時に停止せず再構築する経路を固定する。

## コミット一覧

`git log --oneline 507a820..8d08830` の出力（古い順、16 件）:

```
8341c11 docs: add complexity refactor design and plan
31681ab refactor(session): extract discovery, scan, and line helpers
d2fc71d docs: fix omen path selectors in refactor plan
08be0c1 refactor(session): extract remaining per-iteration bodies
587aff2 refactor(markdown): split block classification and segment protection
68ddc8f refactor(claude): extract transcript discovery and meta helpers
af82073 refactor(events): extract content and record validation helpers
37fd8d3 refactor(translation): extract scheduler loop and response parsing
afeeeae refactor(config): split base URL validation stages
371e9b2 refactor(cli): split doctor diagnostics without short-circuiting
aa8dd29 docs: correct final expected test count
577be76 refactor(ui): extract monitor scan, poll, and history helpers
9a48572 fix(ui): preserve extracted count on snapshot read failures
9463ba2 refactor: harden remaining complexity hotspots
248b90d docs: report complexity refactor results
8d08830 refactor(claude): extract head record parsing
```

`d2fc71d` と `aa8dd29` は計画書の誤記を直すコントローラの修正である。
`9a48572` は `ui.py` の抽出中に生じた「snapshot 読取失敗時に抽出件数が失われる」不具合を、
ベースラインの挙動へ戻す修正である。
`248b90d` は本レポートの初版、`8d08830` は `read_claude_meta` を追加で分割した最終コードコミットである。
本レポートの改訂コミットはこの一覧と計測対象に含まれない。

## 差分と行数

`git diff 507a820 8d08830 --stat` は 14 ファイル、+2185 / -577 を示す。
このうち `src/` は 8 ファイル、+913 / -577、`tests/` は 3 ファイル、+64 / -0、
残りは設計書、計画書、本レポートの初版（`248b90d`）である。
想定外のファイルは含まれていない。

`src/transpane/*.py` の行数は 3520 から 3856 へ 336 行増えた（+9.5%）。

| ファイル | before | after |
| --- | ---: | ---: |
| `session.py` | 575 | 669 |
| `markdown.py` | 427 | 467 |
| `claude.py` | 144 | 196 |
| `events.py` | 301 | 339 |
| `translation.py` | 435 | 450 |
| `config.py` | 173 | 187 |
| `cli.py` | 255 | 265 |
| `ui.py` | 917 | 990 |
| `cache.py` | 163 | 163 |
| `models.py` | 126 | 126 |
| `__main__.py` | 4 | 4 |
| `__init__.py` | 0 | 0 |
| 合計 | 3520 | 3856 |

増分の大半は、ヘルパーの関数定義行と、責務ごとに分けた early return の記述である。
主な変更は関数の抽出と移動であり、公開 API のシグネチャと意味は変えていない。

## PR #4 レビュー対応（2026-11-26）

PR #4 のレビュー指摘に基づき、以下の2件のみを修正した。追加の複雑度低減は行っていない。

### P2-01 再構築時の note 更新順序

- `Monitor._read_snapshot` を `_scan_snapshot`（scan・検証）と
  `_read_snapshot_messages`（索引設定・メッセージ読取）に分割し、`_rebuild`（および
  `switch`）がスキャン成功後、索引設定と `read_messages` の前に `_note` を設定する
  基点の順序へ戻した。
- `SnapshotOutcome` に `scan` を追加し、scan と read の間でスキャン結果を保持する。
  generation、停止状態、例外分類、再構築契約は変更していない。
- 回帰テスト: `test_rebuild_sets_note_before_reading_messages`
  （修正前コードで失敗することを確認済み）。

### P2-02 doctor の root 状態重複取得

- `_doctor_roots` が `(ok, claude_state)` を返し、`_doctor_claude_counts` がその結果を
  受け取るように変更した。Claude root の `_root_state` は 1 回だけ取得する。
- 診断の順序、表示文言、終了コード、短絡評価による診断省略禁止は変更していない。
- 回帰テスト: `test_doctor_reads_each_root_state_once`、
  `test_doctor_translation_runs_when_cache_unavailable`。

### 検証（修正後）

- `uv run pytest -q`: 217 passed（既存209 + 追加8）
- `uv run ruff check src tests`: clean
- `uv run ruff format --check src tests`: clean
- `git diff --check`: clean
- `omen complexity -p src -f json`: 関数256、cognitive 合計 846、p95 10、最大 15、
  cognitive >10 の関数 10。

>10 の関数数は設計 §7.2 の改善目標（9以下）を1件超過する。これは基点の note 順序へ
戻した結果 `_rebuild` の cognitive が 12 から 13 へ増えたためであり、レビュー指示に従い
数値合わせの追加分割は行っていない。
