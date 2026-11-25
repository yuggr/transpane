# transpane

既存の Codex CLI / TUI をそのまま使いながら、選択したセッションの英語の進捗・最終回答を
ローカルの PLaMo 翻訳サーバーで日本語表示する独立 TUI（＋診断 CLI）です。

- Codex 本体の改造、PTY/ANSI の書き換え、入力翻訳、翻訳結果の書き戻しは行いません。
- Codex の rollout JSONL を **読み取り専用** で監視します。
- 原文を先に表示し、翻訳が完了した段落だけを日本語へ更新します。
- 翻訳はローカル HTTP のみ。クラウド API への fallback はありません。
- 翻訳は参考表示です。承認や破壊的操作の判断は Codex 側の原文を確認してください。

## 動作環境

- WSL2 上の Linux（Windows ネイティブは対象外）
- Python 3.12 以上
- [uv](https://docs.astral.sh/uv/)
- 既存の Codex CLI（動作確認: `codex-cli 0.169.0`）
- OpenAI 互換 API を提供するローカルの PLaMo 3 Translate 2B（例: LM Studio）

## インストール

```bash
cd /path/to/transpane
uv sync --locked
uv run transpane --help
```

`uv run transpane` は `uv.lock` に固定された依存で動作します。

## 設定

設定ファイル: `${XDG_CONFIG_HOME:-~/.config}/transpane/config.toml`

```toml
[translation]
base_url = "http://127.0.0.1:1234/v1"
model = "plamo-3-translate-2b"
form = "polite"   # polite | plain
```

- `model` は必須です。推測で別モデルを選ぶことはありません。
- 実際のモデル ID はサーバーで確認してください。

```bash
curl -s http://127.0.0.1:1234/v1/models
```

- `base_url` は HTTP・IP リテラル・パス `/v1` のみ許可されます。
  loopback（`127.0.0.0/8`, `::1`）または RFC1918（`10/8`, `172.16/12`, `192.168/16`）だけです。
  ホスト名、userinfo、query、fragment、公開 IP は拒否されます。
- 不明な設定項目はエラーになります（設定項目は上の 3 つだけです）。
- 参照する環境変数は `CODEX_HOME`、`XDG_CONFIG_HOME`、`XDG_CACHE_HOME` だけです。
  Codex の `auth.json` や認証設定は読みません。

### WSL2 から Windows 側の LM Studio へ接続する

既定は WSL 内の `127.0.0.1:1234` です。Windows 側のサーバーへ接続する場合は、
Windows ホストの RFC1918 アドレスを明示します。

```toml
base_url = "http://192.168.1.50:1234/v1"
```

その IP が自分の Windows ホストであることを利用者が確認してください。任意の LAN
サービスを信頼する設定ではありません。HTTP プロキシ環境変数は無視され
（`trust_env=False`）、redirect も追跡しません。

## 使い方

```bash
# 診断（本文は送信しない）
uv run transpane doctor

# セッション一覧（本文は出さない）
uv run transpane sessions

# ログ形式と件数の確認（本文は出さない）
uv run transpane inspect --session <thread-id>

# TUI（候補から選択）
uv run transpane
uv run transpane --cwd /path/to/project   # cwd 完全一致で候補を絞る
uv run transpane --session <thread-id>    # 明示選択（確認済み扱い）
```

`--cwd` と `--session` は排他です。未知 ID・重複 ID はエラー終了します。
`--cwd` は Linux 側の絶対正規化パスで完全一致します。

`doctor` は設定、セッションディレクトリ、キャッシュ、`GET /v1/models` とモデル ID を
確認します。翻訳本文は送信しません。終了コードは成功 0、失敗 1、引数・設定不正 2 です。

### 2 ペインでの利用例

```bash
# 左ペイン
cd /path/to/project
codex

# 右ペイン
cd /path/to/transpane
uv run transpane --cwd /path/to/project
```

Codex の起動・入力・承認・終了操作は変わりません。ビューアを終了・再起動しても
Codex の実行は継続します。ビューアと Codex の間にプロセス親子関係や制御通信はありません。

## キー操作

| キー | 動作 |
|---|---|
| `s` | セッション選択モーダル（ID / cwd / 最終更新 / 履歴形式） |
| `o` | 全体の原文 / 翻訳表示を切替（翻訳処理は継続） |
| `r` | 失敗した翻訳の再試行、接続再確認 |
| `h` | 直前の履歴 50 件を追加表示（表示分だけ翻訳） |
| `End` | 最新へ移動し、オートスクロールを再開 |
| `q` | ビューアだけ終了 |

上へスクロールして過去を読んでいる間は、新着で読書位置を動かしません。
入力欄、承認ボタン、コマンド実行、外部リンク起動は実装していません。

## 表示と翻訳の状態

- 原文を先に表示し、翻訳完了後に同じメッセージを更新します。
- 表示状態: 待機 / 翻訳中 / 翻訳成功（表示なし）/ 部分翻訳 / 翻訳失敗。
- `翻訳失敗` は原文を維持し、エラー分類（接続失敗・タイムアウト・HTTP エラー・
  応答形式不正・空応答・応答未完了）を表示します。
- 自動再試行はしません。`r` で失敗部分だけを再送します。
- サーバーが利用不能（接続失敗・timeout・5xx）の場合は未送信分を保持して停止し、
  `r` で再接続後に再開します。
- 日本語入力、日本語を含む混在文、コード、秘密情報と判定した行は送信しません。

### Markdown の保護範囲

翻訳するのは、次に該当する **1 行の通常文** だけです。

- トップレベルの段落
- 単純なリスト項目（入れ子なし）
- 単純な引用（1 段）
- ATX 見出し

次のブロックは原文のまま保持します。

- コードフェンス、インデントコード、HTML ブロック、表、画像
- 入れ子リスト、複数行にまたがる段落・リスト項目・引用
- 数式、構造化データ、独立したコマンド行（`$ ...`）

インラインコード、Markdown リンク（ラベル含む）、URL、パス、ファイル名、
CLI オプション、環境変数、snake_case / camelCase の識別子、関数呼び出し、
バージョン表記は ASCII マーカーに置き換えてモデルへ送りません。復元時に
マーカーの欠落・重複・順序変更・未知マーカーを検査し、1 つでも不一致なら
その段落は原文のまま表示します。

長い文は 1000 Unicode code point 以下へ文境界・空白境界で分割します。
マーカーを分断する分割や、安全に分割できない長文は原文保持とします。

## 翻訳サーバー

`POST /v1/chat/completions`（`stream: false`）だけを使用します。

```json
{
  "model": "plamo-3-translate-2b",
  "messages": [
    {"role": "system", "content": "[plamo-translate]\nsource_lang=en\ntarget_lang=ja\nform=polite"},
    {"role": "user", "content": "The storage layer requires atomic writes."}
  ],
  "stream": false
}
```

- 2xx かつ `choices[0].message.content` が非空文字列であることを検証します。
- `finish_reason` は `stop` のみ受け入れます。
- 会話履歴、tool 結果、system/developer プロンプト、reasoning は送信しません。
- 同時 HTTP 要求は 1、保留キューは最大 32 件です。入りきらない分は保持し、
  空きができ次第処理します。
- 同一内容のセグメントはメモリとキャッシュで共有し、HTTP 呼び出しを重複させません。

## キャッシュ

- 場所: `${XDG_CACHE_HOME:-~/.cache}/transpane/translations.sqlite3`
- ディレクトリ 0700、DB 0600。既存の権限が広い場合は自動変更せず、診断して利用停止します。
- 保存するのは翻訳成功結果だけです。元の transcript や入力文字列そのものは保存しません。
- cache key は `[cache_schema_version, translation_policy_version, base_url, model_id,
  source_lang, target_lang, form, protected_source_segment]` の SHA-256 です。
  モデル・文体・ポリシー・サーバーが変われば別キーになります。
- 破損・ロック・書込不可の場合は診断を出し、既存 DB を削除せずにメモリ処理へ切り替えます。
- モデルを同じ ID のまま差し替えた場合は自動検知しません。必要ならキャッシュを退避してください。

```bash
# 退避（削除しない）
mv ~/.cache/transpane/translations.sqlite3 \
   ~/.cache/transpane/translations.sqlite3.bak-$(date +%Y%m%d)
```

次回起動時に新しい DB が作成され、再翻訳されます。

## セッションログ

- 対象: `<CODEX_HOME>/sessions/**/rollout-*.jsonl`（既定 `~/.codex`）。
- セッション ID はファイル名ではなく `session_meta.payload.id` から取得します。
- `archived_sessions`、圧縮ファイル、root 外への symlink は対象外です。
- 親が存在する子セッションは一覧から除外します。親が見つからない子セッションは
  「子セッション(親不明)」と表示します。
- 対応する履歴形式は `legacy` と `paginated` です。未知の `history_mode` は
  「未対応」と表示し、本文を送信しません。
- 対象イベントは `event_msg` の許可リストだけです。`response_item`、reasoning、
  tool、system/developer、compaction、subagent 内部通信は対象外です。
- 追記は 250ms 間隔で増分読み取りします。未終端行・途中の UTF-8 は完成まで待ちます。
  8 MiB を超える行は読み飛ばして診断し、後続の有効行は処理します。
- ファイル消失時は表示を保持して「ログ取得停止」を示します。inode 変更・縮小・置換を
  検出すると索引を再構築します。スレッド ID が変わっても自動では切り替えません。

## Codex 側の言語ポリシー（任意）

Codex のグローバル指示へ手動で設定する場合の例です。本アプリは自動編集しません。

```markdown
## Language Policy

- Accept user instructions in Japanese.
- Use English for plans, progress updates, conversational responses,
  final reports, and communication between agents.
- Do not provide a Japanese translation of conversational responses;
  an external presentation layer handles translation.
- Preserve the language required by project instructions and artifacts.
```

日本語入力は context に残り、既存の日本語仕様書等も変更しません。内部推論の言語を
保証する設定ではありません。リポジトリ固有の言語指定と衝突する場合は適用ルールを
確認してください。

## セキュリティと制約

- `CODEX_HOME` 配下のセッションファイルは読み取り専用です。作成・書換え・削除・移動・
  chmod は行いません（`A24` のテストで内容と mtime の不変を確認しています）。
- 翻訳サーバーへ送るのは許可イベントの自然言語本文だけです。ユーザー入力、tool、
  reasoning、system/developer、子エージェント通信は送りません。
- PEM 秘密鍵、Bearer token、代表的な API キー形式、認証情報付き URL を含む行は
  送信しません。検出の完全性は保証できません。ローカルサーバー側の要求ログ保持は
  利用者が確認してください。
- 診断出力に原文・訳文・HTTP body・トークン・認証情報は出しません。
- 端末の ANSI/OSC 制御コードは表示時に除去します（原文は変更しません）。
- 翻訳の成功は Codex の実行成功を意味しません。

## 非対応範囲

- Codex 本体の改造、PTY/ANSI の書換え、独自チャットクライアント
- Codex への入力翻訳、翻訳結果の書き戻し
- サブエージェント通信・非公開推論・tool I/O の翻訳
- プロジェクトファイルの自動翻訳
- 翻訳サーバーの起動・管理、課金 API、Web UI
- 複数セッションの同時監視（切り替えは可能）
- Windows ネイティブでの動作

## 動作確認済み環境

- Codex CLI: `codex-cli 0.169.0`
- 実ログ: 16 ファイルを読み取り専用で確認。すべて `history_mode = "paginated"`、
  対象イベントは `event_msg / item_completed`（UserMessage / AgentMessage / Plan 形式）。
  `legacy` と `Plan` は合成 fixture で検証（実ログでは未観測）。
- 翻訳: LM Studio 上の `plamo-3-translate-2b`（`/v1/models` で ID 確認）。
- 遅延（合成ログ・同一マシン・ウォーム状態、5 回）:
  - 原文表示: 中央値 100ms、最大 281ms
  - 翻訳完了: 中央値 507ms、最大 685ms
  - 参考: モデル初回ロード時は 1 リクエスト約 11.6 秒

## 開発

```bash
uv sync --locked
uv run pytest -q
uv run ruff check src tests
uv run ruff format --check src tests
```

```text
src/transpane/
├── cli.py           # 引数・診断CLI
├── config.py        # 設定解決・URL制約・内部定数
├── models.py        # dataclass / enum
├── session.py       # セッション発見・増分読み取り
├── events.py        # JSONL 許可リスト変換
├── markdown.py      # 構造保護・分割・復元検証
├── cache.py         # SQLite キャッシュ
├── translation.py   # HTTP client / scheduler
└── ui.py            # メッセージストア / Monitor / Textual UI
```

### 実装上の判断

- 翻訳単位は「翻訳対象ブロック内の 1 物理行」です。改行・接頭辞・マーカーを
  構造的に保持するため、複数行ブロックや入れ子構造は原文のまま残します。
- 仕様のモジュール一覧にコントローラが存在しないため、セッション監視と
  スケジューラの結線は `ui.py` 内の `Monitor` が担います。
- TUI のセッション選択は行単位です（同一 thread ID の rollout が複数ある場合でも
  人間がファイルを選べます）。`--session` の重複 ID は仕様どおりエラーです。

## トラブルシューティング

| 症状 | 確認 |
|---|---|
| `doctor` でモデルが見つからない | `curl -s http://127.0.0.1:1234/v1/models` の ID を `model` に設定 |
| 接続失敗になる | WSL の `127.0.0.1` は Windows 側に届きません。Windows ホストの RFC1918 IP を設定 |
| セッションが出ない | `CODEX_HOME`、`sessions/**/rollout-*.jsonl`、`session_meta` を確認 |
| `未対応の履歴形式` と出る | Codex 更新で形式が変わった可能性があります。`inspect` で件数を確認し、本文は送信されません |
| 翻訳が `部分翻訳` のまま | 保護マーカーの復元失敗・長文分割不可・一部セグメント失敗。`r` で再試行 |
| キャッシュの警告が出る | `~/.cache/transpane` の権限を確認。既存 DB は自動変更・削除されません |
```
