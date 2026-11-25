# 複雑度リファクタリング最終レポート

設計書 `docs/superpowers/specs/2026-11-25-complexity-refactor-design.md` と計画書
`docs/superpowers/plans/2026-11-25-complexity-refactor.md` に基づくリファクタリングの結果をまとめる。
計測はすべて `omen complexity -p src -f json`（omen 5.1.0）で、基点は `507a820`、
終点は本レポートを作成したブランチの HEAD である。

## 検証結果

最終 HEAD で次を実行し、すべて成功した。

```
uv run pytest -q                       214 passed in 11.25s
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

| 指標 | before (507a820) | after (HEAD) | 差分 |
| --- | ---: | ---: | ---: |
| 関数数 | 186 | 254 | +68 |
| cognitive 合計 | 1000 | 846 | -154（-15.4%） |
| cyclomatic 合計 | 748 | 840 | +92（+12.3%） |
| cognitive 平均 | 5.38 | 3.33 | -2.05 |
| cognitive p50 | 1 | 2 | +1 |
| cognitive p90 | 17 | 9 | -8 |
| cognitive p95 | 24 | 10 | -14 |
| cognitive 最大 | 57 | 15 | -42 |
| cyclomatic 平均 | 4.02 | 3.31 | -0.71 |
| cyclomatic 最大 | 17 | 11 | -6 |
| cognitive > 10 の関数数 | 32 | 10 | -22 |
| cognitive > 15 の関数数 | 24 | 0 | -24 |

関数数は 36.6% 増えた。
これは Extract Method で元関数の本体をヘルパーへ移した結果である。
omen の `lines` 指標で 2 行以下の関数を数えると 28 個から 31 個へ 3 個増えただけで、短い関数の量産にはなっていない。
cyclomatic 合計が増えたのは関数数が増えたためで、1 関数あたりの平均は 4.02 から 3.31 へ下がっている。

## ファイル別の内訳

cognitive と cyclomatic はファイル内の全関数の合計、関数数はファイル内の関数の個数である。
before の値はベースライン計測、after の値は最終 HEAD の計測による。

| ファイル | cognitive before | cognitive after | cyclomatic before | cyclomatic after | 関数数 before | 関数数 after | after 平均 cognitive |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `session.py` | 241 | 180 | 130 | 157 | 25 | 45 | 4.00 |
| `ui.py` | 207 | 197 | 218 | 234 | 69 | 79 | 2.49 |
| `markdown.py` | 155 | 121 | 88 | 100 | 16 | 28 | 4.32 |
| `events.py` | 98 | 84 | 71 | 83 | 16 | 22 | 3.82 |
| `claude.py` | 90 | 68 | 44 | 57 | 5 | 14 | 4.86 |
| `translation.py` | 78 | 74 | 70 | 76 | 23 | 27 | 2.74 |
| `cli.py` | 55 | 45 | 45 | 48 | 9 | 13 | 3.46 |
| `config.py` | 37 | 38 | 33 | 36 | 4 | 7 | 5.43 |
| `cache.py` | 32 | 32 | 32 | 32 | 10 | 10 | 3.20 |
| `models.py` | 7 | 7 | 17 | 17 | 9 | 9 | 0.78 |
| `__main__.py` | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| `__init__.py` | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 合計 | 1000 | 846 | 748 | 840 | 186 | 254 | 3.33 |

`config.py` だけが cognitive を 1 増やした。
`validate_base_url` の段階分割で、各段階の早期 return が独立した関数の cognitive として数えられるためである。
`cache.py` と `models.py` は設計どおり変更していない。

## 数値目標の達成状況

| 目標 | 判定 | 実測 |
| --- | --- | --- |
| 全関数の cognitive ≤ 15 | 達成 | 最大 15、超過 0 件 |
| cognitive p95 ≤ 10 | 達成 | 10 |
| cognitive 合計 ≤ 600 | 未達 | 846 |

### cognitive > 15 の例外

例外はない。
最大値は 15 で、`read_session_meta`（`session.py:174`）と `_LineSplitter.feed`（`session.py:325`）の 2 関数がこの値に達する。
どちらも設計書 §5.1 の範囲で抽出を止めた結果であり、閾値超えを理由づけて残した例外ではない。

`read_session_meta` は 256 KiB 断片の読み捨てと 1 MiB 上限の判定順序が契約であり（設計書 §5.1 の P2-03）、
IO ループの条件式を動かすと意味が変わるため、payload 判定の抽出に留めた。
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
| `claude.py` | `read_claude_meta` | 11 | 6 |

p95 の実測値は 10 で目標を満たす。
設計書は p95 ≤ 10 の言い換えとして「cognitive > 10 の関数を 9 個以下」と書いたが、
関数数が 186 から 254 に増えたため、この個数の目安は実測の百分位と一致しない。
10 関数が 10 を超え、降順で 7 番目から 10 番目までが 11、11 番目以下は 10 以下であり、p95 の計算結果は 10 になる。

### cognitive 合計 ≤ 600 が未達の理由

合計は 846 で、目標の 600 を 246 上回る。
ベースラインからの削減率は 15.4% で、目標の 40% に届かない。

Extract Method は分岐を削除せず、関数の境界を移すだけである。
cognitive complexity は関数ごとに計算して合計する指標なので、ネストの深さと 1 関数あたりの最大値は下がるが、
分岐の総数はほぼそのまま残る。
実際、最大値は 57 から 15、p95 は 24 から 10 へ下がった一方、合計の削減は 154 に留まった。

合計を 600 以下にするには、分岐そのものを統合するか、1〜2 行の関数へ機械的に切り分けることになる。
前者は挙動変更を伴い、後者は設計書 §2.3 の反目標（ヘルパー分割の目的化）に該当する。
設計書 §2.1 は挙動維持、責務分離、可読性を数値目標より優先し、数値合わせの小抽出を禁じているため、
ここで打ち切り、未達として記録する。

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

`git log --oneline 507a820..HEAD` の出力（古い順）:

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
```

`d2fc71d` と `aa8dd29` は計画書の誤記を直すコントローラの修正である。
`9a48572` は `ui.py` の抽出中に生じた「snapshot 読取失敗時に抽出件数が失われる」不具合を、
ベースラインの挙動へ戻す修正である。修正後もテスト 214 件は green である。

## 差分と行数

`git diff 507a820 --stat` は 13 ファイル、+1977 / -571 を示す。
このうち `src/` は 8 ファイル、+903 / -571、`tests/` は 3 ファイル、+64 / -0、
残りは設計書と計画書である。
想定外のファイルは含まれていない。

`src/transpane/*.py` の行数は 3520 から 3852 へ 332 行増えた（+9.4%）。

| ファイル | before | after |
| --- | ---: | ---: |
| `session.py` | 575 | 669 |
| `markdown.py` | 427 | 467 |
| `claude.py` | 144 | 192 |
| `events.py` | 301 | 339 |
| `translation.py` | 435 | 450 |
| `config.py` | 173 | 187 |
| `cli.py` | 255 | 265 |
| `ui.py` | 917 | 990 |
| `cache.py` | 163 | 163 |
| `models.py` | 126 | 126 |
| `__main__.py` | 4 | 4 |
| `__init__.py` | 0 | 0 |
| 合計 | 3520 | 3852 |

増分の大半は、ヘルパーの関数定義行と、責務ごとに分けた early return の記述である。
主な変更は関数の抽出と移動であり、公開 API のシグネチャと意味は変えていない。
