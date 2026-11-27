# transpane

既存の Codex CLI / Claude Code CLI をそのまま使いながら、選択したセッションの英語の
進捗・最終回答・応答をローカルの PLaMo 翻訳サーバーで日本語表示する独立 TUI（＋診断 CLI）です。

- Codex / Claude Code 本体の改造、PTY/ANSI の書き換え、入力翻訳、翻訳結果の書き戻しは行いません。
- Codex の rollout JSONL と Claude Code の project transcript JSONL を **読み取り専用** で監視します。
- 原文を先に表示し、翻訳が完了した段落だけを日本語へ更新します。
- 翻訳はローカル HTTP のみ。クラウド API への fallback はありません。
- 翻訳は参考表示です。承認や破壊的操作の判断は各 CLI 側の原文を確認してください。

## 動作環境

- WSL2 上の Linux amd64（Windows ネイティブ、Linux arm64 は未保証）
- 単一の `transpane` 実行ファイル。Go / Python / uv のインストールは不要です
- 既存の Codex CLI（動作確認: `codex-cli 0.169.0`）または Claude Code CLI
  （動作確認: `2.1.285`）
- OpenAI 互換 API を提供するローカルの PLaMo 3 Translate 2B（例: LM Studio）

## インストール

```bash
install -m 0755 ./transpane "$HOME/.local/bin/transpane"
transpane --version
transpane doctor
transpane --source codex
```

配布物は `transpane-linux-amd64.tar.gz` と `SHA256SUMS` です。既存の Python 版
実行ファイルがある場合は上書きせず、`command -v transpane` で選択される実体を
確認してください。ソースからは Go 1.27.4 でビルドできます。

```bash
mise install
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/transpane ./cmd/transpane
```

### Python 版からの移行とロールバック

Go 版は既存の設定ファイル、Codex / Claude Code の transcript、SQLite キャッシュを
そのまま読みます。キャッシュ key・schema・`created_at` は Python 版と互換です。
移行時に DB を削除・再生成する必要はありません。

問題があれば、Python 版を含む固定コミットの worktree で従来どおり起動できます。
Go 版が既存 DB を読み書きした後でも Python 版へ戻れます（双方向の読み書きを
合成 DB で確認しています）。

```bash
git worktree add /tmp/transpane-python f199bb6cce011c80d28f7cdfe91929d62d30c08f
cd /tmp/transpane-python
uv sync --locked
transpane doctor
```

旧 DB の削除・置換は移行手順に含めません。Go 版が使えない場合は
`command -v transpane` を確認し、旧実行ファイルを明示的に選んでください。

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
- 参照する環境変数は `CODEX_HOME`、`CLAUDE_CONFIG_DIR`、`XDG_CONFIG_HOME`、
  `XDG_CACHE_HOME` だけです。
  Codex の `auth.json`、Claude Code の設定ファイルや資格情報は読みません。
- セッション取得元の解決規則:
  - Codex: `CODEX_HOME`（空なら `~/.codex`）配下の `sessions/`
  - Claude Code: `CLAUDE_CONFIG_DIR`（空なら `~/.claude`）配下の `projects/`
- 翻訳設定は provider ごとに分けません。既存 TOML のまま両方を表示できます。

### WSL2 から Windows 側の LM Studio へ接続する

既定は WSL 内の `127.0.0.1:1234` です。Windows 側のサーバーへ接続する場合は、
Windows ホストの RFC1918 アドレスを明示します。

```toml
base_url = "http://192.168.1.50:1234/v1"
```

その IP が自分の Windows ホストであることを利用者が確認してください。任意の LAN
サービスを信頼する設定ではありません。HTTP プロキシ環境変数は無視され
（Go の HTTP Transport で `Proxy: nil`）、redirect も追跡しません。

## 使い方

```bash
# 診断（本文は送信しない）
transpane doctor
transpane --source claude doctor
transpane --source codex doctor

# セッション一覧（本文は出さない。既定は Codex と Claude Code の両方）
transpane sessions
transpane --source claude sessions

# ログ形式と件数の確認（本文は出さない）
transpane inspect --session <thread-id>
transpane --source claude inspect --session <session-id>

# TUI（候補から選択）
transpane
transpane --cwd /path/to/project          # cwd 完全一致で候補を絞る
transpane --source claude                 # Claude Code のみ
transpane --session claude:<session-id>   # provider 付きの明示選択
transpane --session codex:<thread-id>
```

`--source` は `all`（既定）、`codex`、`claude` の 3 値で、subcommand より前に指定します。
`--cwd` と `--session` は排他です。`--cwd` は Linux 側の絶対正規化パスで完全一致します。
セッション ID は provider をまたぐと重複し得るため、その場合は `codex:<id>` /
`claude:<id>` の接頭辞で指定します。raw ID が一意に解決できる場合だけ省略できます。
`--source` と接頭辞が矛盾する場合、未知 ID・重複 ID は終了 2 です。

`doctor` は設定、各 provider の root、キャッシュ、`GET /v1/models` とモデル ID を
確認します。Claude Code の root が読める場合は、直下 transcript のうち ID を確認できた
件数と、先頭 1 MiB 以内で ID を確認できず候補外にした件数も表示します。
翻訳本文は送信しません。終了コードは成功 0、失敗 1、引数・設定不正 2 です。
`all` では少なくとも一方の root が読めれば成功し、他方は警告になります。
明示した provider の root が読めない場合は失敗です。Claude Code 未導入の環境でも
Codex だけを従来どおり利用できます。

### Claude Code のセッション選択例

```bash
# Claude Code のメインセッションだけを一覧
transpane --source claude sessions

# 同じ cwd で Claude Code と transpane を並べる
cd /path/to/project
claude
# 別ペイン（cwd で絞る場合）
transpane --source claude --cwd /path/to/project
# または ID を明示する場合
transpane --source claude --session claude:<session-id>
```

Claude Code の transcript は
`~/.claude/projects/<project>/<session-id>.jsonl`（`CLAUDE_CONFIG_DIR` 指定時は
その配下）です。`subagents/`、`tool-results/`、`sessions-index.json` は読みません。

### 2 ペインでの利用例

```bash
# 左ペイン
cd /path/to/project
codex          # または claude

# 右ペイン
cd /path/to/transpane
transpane --cwd /path/to/project
transpane --source claude --cwd /path/to/project
```

各 CLI の起動・入力・承認・終了操作は変わりません。ビューアを終了・再起動しても
CLI の実行は継続します。ビューアと CLI の間にプロセス親子関係や制御通信はありません。

## キー操作

| キー | 動作 |
|---|---|
| `s` | セッション選択モーダル（取得元 / ID / cwd / 最終更新 / 形式） |
| `o` | 全体の原文 / 翻訳表示を切替（翻訳処理は継続） |
| `r` | 失敗した翻訳の再試行、接続再確認 |
| `h` | 直前の履歴 50 件を追加表示（表示分だけ翻訳） |
| `End` | 最新へ移動し、オートスクロールを再開 |
| `q` | ビューアだけ終了 |

上へスクロールして過去を読んでいる間は、新着で読書位置を動かしません。
画面上部の status に取得元（`Codex` / `Claude Code`）と短縮 ID を表示します。
選択モーダルでは provider、ID、cwd、最終更新、形式を確認できます。
入力欄、承認ボタン、コマンド実行、外部リンク起動は実装していません。

## 表示と翻訳の状態

- 原文を先に表示し、翻訳完了後に同じメッセージを更新します。
- 表示状態: 待機 / 翻訳中 / 翻訳成功（表示なし）/ 部分翻訳 / 翻訳失敗。
- `翻訳失敗` は原文を維持し、エラー分類（接続失敗・タイムアウト・HTTP エラー・
  応答形式不正・空応答・応答未完了）を表示します。
- 自動再試行はしません。`r` で失敗部分だけを再送します。
- サーバーが利用不能（接続失敗・timeout・5xx）の場合は未送信分を保持して停止し、
  `r` で再接続後に再開します。セッションを切り替えても pause は解除されません。
  明示的な `r` による GET 成功後のみ再開します。
- `h` は poll / 読取中でも要求を保持し、現在の読取完了後に履歴を追加します。
  停止中・再構築中の要求は受け付けません。
- 履歴取得中の denied / halted では表示を保持して監視を停止し、`r` / `h` では
  自動復帰しません。`s` で明示的にセッションを選び直した場合だけ復旧します。
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
  実行中に発生したキャッシュ障害も worker から状態 snapshot として Monitor/TUI へ反映します。
- モデルを同じ ID のまま差し替えた場合は自動検知しません。必要ならキャッシュを退避してください。
- Python 版と Go 版は同じ key を生成します。Go 版で保存した翻訳を Python 版が、
  Python 版で保存した翻訳を Go 版が再利用できます。`created_at` は UTC RFC3339 です。

```bash
# 退避（削除しない）
mv ~/.cache/transpane/translations.sqlite3 \
   ~/.cache/transpane/translations.sqlite3.bak-$(date +%Y%m%d)
```

次回起動時に新しい DB が作成され、再翻訳されます。

## セッションログ

### Codex

- 対象: `<CODEX_HOME>/sessions/**/rollout-*.jsonl`（既定 `~/.codex`）。
- セッション ID はファイル名ではなく `session_meta.payload.id` から取得します。
- `archived_sessions`、圧縮ファイル、root 外への symlink は対象外です。
- 親が存在する子セッションは一覧から除外します。親が見つからない子セッションは
  「子セッション(親不明)」と表示します。
- 対応する履歴形式は `legacy` と `paginated` です。未知の `history_mode` は
  「未対応」と表示し、本文を送信しません。
- 対象イベントは `event_msg` の許可リストだけです。`response_item`、reasoning、
  tool、system/developer、compaction、subagent 内部通信は対象外です。

### Claude Code

- 対象: `<CLAUDE_CONFIG_DIR>/projects/<project>/<session-id>.jsonl`
  （既定 `~/.claude/projects/`）。project 直下の UUID 名 `.jsonl` だけを読みます。
- `subagents/`、`tool-results/`、`memory/`、`sessions-index.json`、`history.jsonl`、
  設定ファイル、添付ファイル、再帰的な探索は対象外です。
- セッション ID はファイル stem と `sessionId` の一致から確認します。`cwd` は最初の
  適格レコードの絶対パス、`version` はその文字列を使います。
- 表示するのは次だけです。
  - `type=user` の文字列、または content 配列内の `text` ブロック
  - `type=assistant` の content 配列内の `text` ブロック
- 次の行は表示も翻訳もしません。
  - `thinking`、`redacted_thinking`、`tool_use`、`tool_result`
  - `isSidechain`、`isMeta`、`isCompactSummary`、`isVisibleInTranscriptOnly`、
    `isApiErrorMessage` が true のレコード
  - `toolUseResult` を持つ入力
  - `<system-reminder>`、`<command-*>`、`<local-command-*>`、
    `<task-notification>`、`<persisted-output>`、`<bash-stdout>` などの内部 wrapper
  - `message.model` が `<synthetic>` の応答
- 内部 wrapper の判定はタグ名の明示リストです。通常のユーザー入力やモデル応答に
  同じタグ文字列が含まれる場合も、保守的に非表示になります。
- 同じ `message.id` を複数行が共有していても、`uuid` が異なれば別メッセージです。
  同じ `uuid` は重複表示せず、内容が更新された場合だけ同じ位置を更新します。
- `sessionId` が選択 ID と異なる user/assistant レコードを検出すると、監視と新規翻訳を
  停止します。自動で別セッションへ切り替えません。fork 元履歴、rewind 後の分岐、
  compaction 後の有効 context の再構成は行いません。
- Claude Code の進捗報告と最終回答は区別せず、すべて「応答」として表示します。
  `stop_reason` や `message.id` から種別を推測しません。

### 共通

- 追記は 250ms 間隔で増分読み取りします。未終端行・途中の UTF-8 は完成まで待ちます。
  8 MiB を超える行は読み飛ばして診断し、後続の有効行は処理します。
- ファイル消失時は表示を保持して「ログ取得停止」を示します。inode 変更・縮小・置換を
  検出すると索引を再構築します。セッション ID が変わっても自動では切り替えません。
- 履歴読戻し時に索引とログの message ID / revision が一致しなければ、本文を表示・送信
  せずに既存の再構築経路へ切り替えます。再構築後も一致しない場合は監視を停止します。
- 初期表示は末尾 50 件、`h` で 50 件ずつ過去を追加します。切替・再構築時は旧世代の
  未送信翻訳を取り消し、前のセッションの翻訳結果を新しい画面へ適用しません。
- 監視停止（セッション ID 不一致、許可外パス、再構築失敗）後は `r` / `h` による翻訳
  再開を行いません。`s` でセッションを再選択するまで新規翻訳を予約しません。
- Claude Code の transcript は公式に内部形式とされ、リリースで変更され得ます。
  本アプリは実測した許可形式だけを読み、未知形式は無視または「対象イベント未検出」と
  診断します。推測による抽出は行いません。

## 言語ポリシー（任意）

各 CLI のグローバル指示へ手動で設定する場合の例です。本アプリは自動編集しません。

### Codex

```markdown
## Language Policy

- Accept user instructions in Japanese.
- Use English for plans, progress updates, conversational responses,
  final reports, and communication between agents.
- Do not provide a Japanese translation of conversational responses;
  an external presentation layer handles translation.
- Preserve the language required by project instructions and artifacts.
```

### Claude Code

`~/.claude/CLAUDE.md` などへ手動で記載する例です。

```markdown
## Language policy

- Accept user instructions in their original language, including Japanese.
- Write conversational responses, progress updates, and final reports in English.
- A local presentation layer translates these responses for the user.
- Follow project-specific language requirements for code, comments, documents, and other artifacts.
- Do not alter commands, paths, identifiers, or structured protocol fields for translation.
```

日本語入力は context に残り、既存の日本語仕様書等も変更しません。内部推論の言語を
保証する設定ではありません。リポジトリ固有の言語指定と衝突する場合は適用ルールを
確認してください。

## セキュリティと制約

- `CODEX_HOME` / `CLAUDE_CONFIG_DIR` 配下のセッションファイルは読み取り専用です。
  作成・書換え・削除・移動・chmod は行いません（既存テストで内容と mtime の不変を
  確認しています）。
- 翻訳サーバーへ送るのは許可イベントの assistant 公開テキストだけです。ユーザー入力、
  tool、reasoning、system/developer、子エージェント通信、内部 wrapper、tool 結果は
  送りません。
- PEM 秘密鍵、Bearer token、代表的な API キー形式、認証情報付き URL を含む行は
  送信しません。Unicode 空白を含む Bearer 形式、Unicode 単語境界を含むパス・識別子も
  Python 版と同じ保護対象として扱います。検出の完全性は保証できません。実ログには資格情報が含まれ得るため、
  transcript を診断・issue・fixture・PR へ添付しないでください。翻訳本文の HTTP 確認が
  必要な場合も合成した無害な英文を使ってください。ローカルサーバー側の要求ログ保持は
  利用者が確認してください。
- 診断出力に原文・訳文・HTTP body・トークン・認証情報は出しません。
- 端末の ANSI/OSC 制御コードは表示時に除去します（原文は変更しません）。
- 表示専用です。Claude Code の承認、危険なコマンドの判断、実行可否は標準 CLI の
  原文を確認してください。翻訳の成功は各 CLI の実行成功を意味しません。

## 非対応範囲

- Codex / Claude Code 本体の改造、PTY/ANSI の書換え、独自チャットクライアント
- 各 CLI への入力翻訳、翻訳結果の書き戻し、hooks の自動登録
- Claude Code の Agent SDK / `claude -p` ラッパー / Claude API 呼出
- サブエージェント通信・非公開 thinking・tool I/O・添付画像の翻訳
- Claude Code の plan ファイル、スラッシュコマンド内部処理、agent teams の内部通信
- rewinding / fork 元履歴 / compaction 後の有効 context の再構成
- プロジェクトファイルの自動翻訳
- 翻訳サーバーの起動・管理、課金 API、Web UI
- 複数セッションの同時監視・同時翻訳（切り替えは可能）
- Windows ネイティブでの動作、Linux arm64、WSL から Windows 側 Claude Code 履歴の暗黙探索
- 入力翻訳、PTY ラッパー、Codex/Claude 本体の起動・承認・kill

## 動作確認済み環境

- Codex CLI: `codex-cli 0.169.0`
- Codex 実ログ: 16 ファイルを読み取り専用で確認。すべて `history_mode = "paginated"`、
  対象イベントは `event_msg / item_completed`（UserMessage / AgentMessage / Plan 形式）。
  `legacy` と `Plan` は合成 fixture で検証（実ログでは未観測）。
- Claude Code CLI: `2.1.285`（`claude --version`）
- Claude Code 実ログ: `~/.claude/projects` 配下の 146 個の直下 transcript を
  読み取り専用・本文非出力で構造確認。transcript の `version` は
  `2.1.205`〜`2.1.259` を観測しました。確認した形は次のとおりです。
  - `type` は `user` / `assistant` のほか `attachment`、`system`、`summary` 等
  - `sessionId` は全件ファイル stem と一致、`uuid` は重複レコードあり
  - user は文字列または content 配列、assistant は content 配列
  - ブロックは `text` / `thinking` / `tool_use` / `tool_result`
  - `isSidechain` / `isMeta` / `isCompactSummary` / `isVisibleInTranscriptOnly` /
    `isApiErrorMessage` は bool
  - `message.model` に `<synthetic>` を観測
- 上記の実ログ本文はコピーせず、公開テキスト・tool 出力・内部注入の除外は
  合成 fixture と mock で検証しています。Claude Code の実画面・実翻訳・実切替は
  このリポジトリの自動テストでは未確認です（利用者側の手動試験が必要）。
- transcript 形式は公式に内部形式であり、上記以外の版・形の互換性は保証しません。
- 翻訳: LM Studio 上の `plamo-3-translate-2b`（`/v1/models` で ID 確認）。
- 遅延（Python 版の参考値。合成ログ・同一マシン・ウォーム状態、5 回）:
  - 原文表示: 中央値 100ms、最大 281ms
  - 翻訳完了: 中央値 507ms、最大 685ms
  - 参考: モデル初回ロード時は 1 リクエスト約 11.6 秒
- Go 版の実測性能は未測定です。翻訳の支配要因は外部モデル生成時間であり、
  本移行は速度改善を目的としていません。

## 開発

Go 版の開発・検証は `mise` で固定した toolchain を使います。

```bash
mise install
mise run check        # go vet + go test + golangci-lint
mise run race         # CGO_ENABLED=1 go test -race ./...
mise run fuzz         # 30 秒ずつの fuzz
mise run vuln         # govulncheck
mise run build        # CGO_ENABLED=0 Linux amd64
```

リリースバイナリは CGO 不要です。`go mod verify`、`golangci-lint fmt --diff`、
`git diff --check` も CI で実行します。Python 版は移植完了まで固定基点の
オラクルとして残し、通常の Go CI は Python / uv を要求しません。

```text
cmd/transpane/           # os.Exit, signal context
internal/
  cli/                   # 引数・doctor/sessions/inspect・TUI起動
  config/                # 厳格TOML・XDG・URL allowlist
  domain/                # 値型・ラベル・revision
  transcript/            # 発見・抽出・byte offset reader/index
  protection/            # Markdown AST・span・marker・分割/復元
  cache/                 # SQLite・Python互換key
  translate/             # HTTP client
  monitor/               # 状態所有・poll・scheduler
  tui/                   # Bubble Tea 表示・キー・picker
testdata/compat/         # 合成 fixture と Python oracle golden
scripts/compat/          # 開発専用 Python oracle
```

### 実装上の判断

- 翻訳単位は「翻訳対象ブロック内の 1 物理行」です。改行・接頭辞・マーカーを
  構造的に保持するため、複数行ブロックや入れ子構造は原文のまま残します。
- 監視・表示・キューの可変状態は `internal/monitor` の単一イベントループが所有します。
  TUI はキーとサイズだけを渡し、不変 snapshot を受け取ります。
- TUI のセッション選択は行単位です（同一 ID のファイルが複数ある場合でも
  人間がファイルを選べます）。`--session` の重複 ID は仕様どおりエラーです。
- provider 固有処理は発見・メタデータ解釈・イベント抽出だけです。HTTP、Markdown 保護、
  キャッシュ、TUI は Codex / Claude Code で共通です。翻訳キャッシュのキーに provider を
  含めないため、同一の保護済み英文は両者で再利用されます。
- 翻訳ジョブの取消はセッション ID ではなく世代番号で行います。同じ raw ID を持つ
  provider / ファイルへ切り替えても、旧世代の未送信本文を送ったり結果を混ぜたりしません。
- Go の `net/http` は proxy を使わず redirect を追跡しません。connect 3 秒、
  read idle 60 秒、要求全体 90 秒を適用します。既にサーバーへ渡った要求の停止は
  保証しません。
- Markdown 保護は goldmark AST と手続き的な span 判定を使います。Go RE2 に
  ない lookbehind / backreference は前文字判定と同長 delimiter 走査で置き換え、
  不確かな span は原文保持に倒します。

## トラブルシューティング

| 症状 | 確認 |
|---|---|
| `doctor` でモデルが見つからない | `curl -s http://127.0.0.1:1234/v1/models` の ID を `model` に設定 |
| 接続失敗になる | WSL の `127.0.0.1` は Windows 側に届きません。Windows ホストの RFC1918 IP を設定 |
| Codex のセッションが出ない | `CODEX_HOME`、`sessions/**/rollout-*.jsonl`、`session_meta` を確認 |
| Claude Code のセッションが出ない | `CLAUDE_CONFIG_DIR` / `~/.claude/projects`、`<project>/<uuid>.jsonl` を確認。`transpane --source claude doctor` で root を確認 |
| `対象イベント未検出` と出る | 形式変更、未対応の content、内部 wrapper や synthetic 応答だけのセッションなどの可能性があります。`inspect` で件数を確認し、本文は送信されません |
| `未対応の履歴形式` と出る | Codex 更新で形式が変わった可能性があります。`inspect` で件数を確認し、本文は送信されません |
| Claude Code で応答が出ない | thinking / tool_use / tool_result / subagent / 内部注入は表示対象外です。通常の assistant text だけが表示されます |
| 翻訳が `部分翻訳` のまま | 保護マーカーの復元失敗・長文分割不可・一部セグメント失敗。`r` で再試行 |
| キャッシュの警告が出る | `~/.cache/transpane` の権限を確認。既存 DB は自動変更・削除されません |
```
