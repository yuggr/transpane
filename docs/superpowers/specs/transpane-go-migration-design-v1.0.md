# Transpane Go言語転換 設計書

- 版: 1.0
- 作成日: 2026-11-26
- 状態: 承認待ち（実装前spec）
- 対象: https://github.com/yuggr/transpane
- 基準: PR #4レビュー済みHEAD `f199bb6cce011c80d28f7cdfe91929d62d30c08f`
- 確認時のmain: `507a82053c06b21d8b7ddb1eb105010bf173c680`（PR #4はopen、未マージ）
- 推奨作業ブランチ: `feat/go-migration`（PR #4マージ後に作成）

## 1. 目的と設計判断

Python 3.12+ / uv / Textualによる現行TranspaneをGo製のCLI/TUIに置き換え、利用者がGo・Python・uvをインストールせずに単一実行ファイル `transpane` で利用できる状態にする。対象はまずWSL2上のLinux amd64とする。翻訳推論は従来どおり外部のPLaMoサーバーに委ねる。

主目的は配布と起動の簡素化、Go標準ツールチェーンによる品質検査、既存機能の継承である。翻訳速度を改善する目的ではない。処理負荷の多くは外部翻訳モデルに依存する。Python→Goの逐語的な移植は行わず、外部から観測可能な振る舞いと安全性を維持しながらGoの慣用的な構造を採用する。

既存219件のテストは実装者の最終報告値であり、独立実行は未実施。移植開始時に固定したPython基点で再実行する。PR #4のマージ後、基点のsrc・tests・設定の差分を確認する。同等でなければ変更箇所をレビューし、移植基点を更新して承認を得る。未マージPRを混ぜたままmainから作業開始しない。

### 1.1 成果物

- Goモジュール（`go.mod` / `go.sum`）、実装、Goテスト、合成fixture、開発・CI設定。
- Linux amd64向けCGO不要のリリースバイナリとSHA-256チェックサム。
- Python版との差分検証結果、互換性表、インストール・移行・復旧手順。
- 旧Python版は基点コミットで再現可能にし、移植の受入完了後に稼働用コード・Python依存・旧CIを除去する。旧コードの削除、タグ作成、公開、mergeは承認後に別途実施する。

### 1.2 非目標

新機能、入力翻訳、Codex/Claude本体の変更、app-server/Agent SDK、PTYラッパー、Desktop内蔵pane、Web UI、Windowsネイティブ対応、複数セッション同時監視、翻訳並列化、モデル自動選択、クラウドfallback、キャッシュ形式刷新、サーバー管理、既存不具合の便乗修正。静的解析スコアのためだけの抽象化・依存追加は行わない。

### 1.3 互換性の範囲と提案上の判断

CLIの公開引数、終了コード、診断の意味と文言、環境変数、TOML、セッション抽出、時系列、翻訳対象・保護、キャッシュ、キー操作、状態遷移を維持する。TextualとBubble Teaのピクセル・ANSI・行折返しの完全一致は要求しない。視覚表現の差は許容するが、表示情報の欠落、読書位置の移動、制御文字やリンクの実行は許容しない。

以下は本specで提案する意図的な差分であり、黙って互換と扱わない。

1. 配布とTUIライブラリを変更する。`--version` を追加し、設定不要でバージョン・コミットを確認可能にする。
2. 旧generationのHTTPは可能な限りcontextで中断する。既にサーバーへ渡った要求の停止や取り消しは保証しない。旧結果・旧エラーの適用は禁止。Python版は進行中HTTPの完了を許容していたため、切替直後のキャッシュヒット率・通信時間は異なり得る。
3. 読み取り不正型・異常UTF-8・危険なパス/URL等に対し、Goの標準ライブラリが暗黙に受理・置換しないようfail closedにする。既存版のクラッシュ/誤抽出を正解として再現しない。観測した差分を記録し、安全上必要な変更以外は別Issueへ分離する。
4. HTTPのconnect 3秒・read 60秒を保ち、total 90秒を明示的な要求上限として実施する。Python httpxの個別timeout指定は厳密な全体期限と同義ではない。上限適用による差をテストする。
5. 初期リリースはLinux amd64のみ保証する。Linux arm64等への拡大は実機検証後。Goを利用するだけでWindows対応済みと扱わない。

上記は承認対象。その他の互換性変更は事前承認を必要とする。

## 2. 現行実装の契約と移植方針

| 現行モジュール | Goでの責務 | 移植上の注意 |
|---|---|---|
| config.py | config | 厳格なTOML、XDG、IP許可リスト |
| models.py | domain | Provider/Message/Revision/Session/状態 |
| events.py, claude.py | transcript | provider別抽出と発見、内部イベント排除 |
| session.py | transcript | byte offset、境界付きJSONL、索引、readback |
| markdown.py | protection | 元の文字列と範囲の保持、Python正規表現との差 |
| cache.py | cache | SQLite互換、SHA-256キーのバイト互換 |
| translation.py | translate, monitor | HTTP検証と1-workerの順序・取消・再試行 |
| ui.py | monitor, tui | 状態所有、非同期イベント、表示とキー |
| cli.py | cli | 構文、診断、終了コード |

正本は基点のREADME・実装・テスト。過去の設計書は設計判断の経緯として参照し、異なる場合は差異を報告する。本specで明示した差分以外は基点の外部動作を維持する。特にPR #3/#4で修正したgeneration/履歴/再構築の契約を移植の最優先対象とする。

## 3. 技術選定

| 項目 | 採用 | 理由・制約 |
|---|---|---|
| Go | 1.27系のサポート中の安定パッチ | 2026-11-26時点の公式リリース系列。Phase 0で正確なパッチを固定。依存・lintとの互換を確認 |
| CLI | 標準 `flag` + 小さな引数解析 | 3サブコマンドと少数フラグに専用フレームワークは不要。argparse互換をテスト |
| TUI | `charm.land/bubbletea/v2` | Model/Update/View、イベント駆動 |
| viewport | `charm.land/bubbles/v2/viewport` | スクロール・サイズ変更。実測してanchor復元を実装 |
| 装飾 | `charm.land/lipgloss/v2`（必要範囲のみ） | 幅・スタイル、端末表示 |
| Markdown | `github.com/yuin/goldmark` v1系 | ASTのsource segmentとtable拡張。v2への移行は今回不要。実際の互換性はPhase 0で確認 |
| TOML | `github.com/pelletier/go-toml/v2` | 厳密な未知キー拒否 |
| HTTP/JSON | `net/http`, `encoding/json` | 公式APIのみ。OpenAI SDK不要 |
| SQLite | `database/sql` + `modernc.org/sqlite` | CGO不要・既存DB利用可能な候補。実DB互換はテスト必須 |
| 並行処理 | context, channel, container/heap | 単一状態所有者、単一HTTP worker。外部queue不要 |
| テスト | testing, httptest, 標準fuzz, race detector | 大規模なテストフレームワーク不要 |
| 開発ツール | mise, golangci-lint v2, govulncheck, Omen | バージョン固定と再現可能な検証 |

GoとRustの性能差は本用途の選定理由としない。GoはCGO不要配布、簡潔な型付き並行処理、標準テスト・配布手順が目的に適合する。SQLiteを独自ファイル形式へ変えない。TUIの自作端末制御、Markdown全体の独自パーサー、Pythonランタイム埋込み、FFIは採用しない。

正確な依存バージョンはPhase 0で公式リリース、ライセンス、脆弱性、対応Goバージョン、CGO=0ビルドを確認して固定する。Go 1.27をサポートするlint版が利用できない場合は、サポート中のGo 1.26安定パッチとの組合せを比較し、理由を記録して承認する。`@latest` をCI・再現手順に残さない。

## 4. 構成・依存方向

```text
cmd/transpane/main.go                 # os.Exit、signal context、CLI呼出
internal/
  cli/                                # 引数、doctor/sessions/inspect、TUI起動
  config/                             # 設定、URL検証、定数
  domain/                             # 値型、列挙値、共通識別子
  transcript/                         # discovery / codex / claude / reader / index
  protection/                         # Markdown AST、span、marker、分割・復元
  cache/                              # SQLite、キー、権限・障害
  translate/                          # HTTP、応答分類（状態管理を持たない）
  monitor/                            # セッション状態、履歴、翻訳スケジューリング
  tui/                                # Bubble Tea、描画・キー・選択画面
  testutil/                           # 必要な場合のみ共有fixture構築
testdata/compat/                      # 合成ログ、期待値、cache golden
scripts/compat/                      # 開発専用Python oracle・差分実行
go.mod / go.sum
mise.toml / .golangci.yml
.github/workflows/                    # check/build。公開操作は別承認
```

`cmd → cli → tui/monitor → transcript/protection/cache/translate → domain/config` の一方向。tuiはHTTPやファイルを直接操作しない。monitorはBubble Teaに依存しない。CLI診断は共有discovery/read/cache/clientを直接呼ぶ。パッケージ数は責務の境界で決める。providerは2種類の具体的な実装と狭い内部interface/型分岐で足りる。汎用plugin registry、DI container、イベントバスを追加しない。

### 4.1 データ型（概念）

```go
type Provider string // codex | claude
type SessionKey struct { Provider Provider; ID string; Path string }
type MessageKey struct { ThreadID string; ID string }
type Message struct {
    ThreadID, ID, Revision string
    Sequence int64 // byte offset
    Timestamp string
    Kind MessageKind
    Original string
}
type TaskRef struct {
    Session SessionKey
    Generation uint64
    Message MessageKey
    Revision string
    UnitID int
}
type IndexEntry struct {
    ID, Revision string
    Start, End int64 // half-open byte range
    Timestamp string
    Kind MessageKind
}
```

`Revision` = UTF-8 originalのSHA-256小文字hex。SessionInfoのcwd/version/mode/parent/sourceは欠損を明示し、空文字の意味を基点で確認する。更新時刻はfilesystem mtime。providerを含むSessionKeyを結果照合に用いる。型・定数は必要なものだけexportし、公開Go APIは提供しない。

## 5. CLI・設定・診断

実行ファイル名は `transpane`。以下の構文を維持する。

```sh
transpane --help
transpane --version
transpane [--source all|codex|claude] [--cwd PATH | --session ID]
transpane [--source all|codex|claude] sessions
transpane [--source all|codex|claude] doctor
transpane [--source all|codex|claude] inspect --session ID
```

`--source`はsubcommandより前、既定all。raw IDは一意に解決できる場合のみ、`codex:<id>`/`claude:<id>`を受理。provider内の同一ID複数pathはCLIで曖昧エラー、TUIは行のSessionKeyで選択。接頭辞とsource矛盾、未知ID、設定・引数不正は2。`--cwd`と`--session`排他、cwdはLinux絶対正規化パスで完全一致。既存のargparseが受理するヘルプ・フラグ位置・終了コードはgolden化し、許容構文の意図しない縮小を避ける。

| 項目 | 規則 |
|---|---|
| 設定 | `${XDG_CONFIG_HOME:-~/.config}/transpane/config.toml` |
| Codex | `${CODEX_HOME:-~/.codex}/sessions` |
| Claude | `${CLAUDE_CONFIG_DIR:-~/.claude}/projects` |
| DB | `${XDG_CACHE_HOME:-~/.cache}/transpane/translations.sqlite3` |
| base_url | `http://127.0.0.1:1234/v1` |
| model | 必須・空白除去。別モデルを推測しない |
| form | `polite`（既定）/ `plain` |

TOMLは`[translation]`のbase_url/model/form文字列のみ。未知キー・型不正は拒否。TOML parserのデコード時に欠損・空・非文字列を区別する。環境変数が空なら既定、ホーム展開を維持。認証ファイル、他アプリ設定、モデル一覧を設定探索に利用しない。

URLはhttp、IP literal、loopbackまたはIPv4 RFC1918（10/8,172.16/12,192.168/16）、path `/v1`（末尾slash正規化）だけ。userinfo/host名/query/fragment/公開IP/不正ポートは拒否。IPv6 loopbackは括弧で正規化。`net/netip`で明示allowlistを行い、`IsPrivate`だけで許可しない。IPv4-mapped IPv6、zone、ポート0、空query等の境界は基点と比較し、安全側の差分を明示する。Proxy環境変数は無効、redirectは追跡しない。ネットワーク先を正規化結果に限定する。

`doctor`: root状態を各1回取得。allは少なくとも1つ利用可能、指定providerは必須。Claude rootが利用可能かつsourceがall/claudeの場合、ID確認済/未確認件数を表示。先行診断失敗でもキャッシュとGET modelsを実行し、モデルID一致確認。成功0/診断失敗1/引数設定不正2。

`sessions`: provider/ID/更新/形式/区分/cwd、本文なし。空一覧は0。`inspect`: provider/ID/cwd/version/形式/不一致と件数、本文なし。未知形式は既定の診断を維持。HTTP送信やキャッシュ生成をしない。実際の文言と順序は基点CLI goldenと照合する。`--help`/`--version`は設定・ログ・ネットワークアクセス前に処理する。

## 6. Transcript発見・抽出

### 6.1 Codex

`<CODEX_HOME>/sessions/**/rollout-*.jsonl` を辞書順で走査し、archived_sessions、symlinkディレクトリ、symlinkファイル、root外を除外。通常ファイルのみ読み取る。`session_meta.payload.id`からIDを確認し、親が一覧に存在する子を除外、親不明は現行ラベル。mtime降順、同値は基点のタイブレークで決定。

メタ読取は現行の意図的な境界を維持する。256KiB単位のreadline、1MiBは次の読み取りを開始する条件。改行なしで256KiB以上の断片は論理行を改行または上限まで捨てる。256KiB未満のEOF未終端JSONは解析可能なら採用する。1回の読み取りで1MiBを超える現行挙動を維持する。Claudeの厳密な1MiB完全行規則と混同しない。

| 履歴形式 | 許可イベント |
|---|---|
| legacy（欠損/null/空も含む） | `event_msg`の`user_message`/`agent_message`、`item_completed`のPlan |
| paginated | `event_msg/item_completed`の`UserMessage`（content.type=text）/`AgentMessage`（content.type=Text）/`Plan` |
| 未知形式 | unsupported、本文送信停止 |

文字列の空白のみは除外。Codex content配列は適格なtextを区切りなしで結合。legacy IDはthreadID:byteOffset、PlanにIDがなければ同じfallback。paginatedのUser/Agentは有効ID必須。phase commentary/final_answerを進捗/最終へ、その他は応答。`response_item`、tool、reasoning、system/developer、内部通信は非表示・非送信。

### 6.2 Claude Code

`<CLAUDE_CONFIG_DIR>/projects/<project>/<uuid>.jsonl`の直下通常ファイルのみ。projectとファイルのsymlink、root外、nested dirs、subagents/tool-results/memory、設定、index、history、添付は除外。先頭1MiB以内の完全行でtype=user/assistant、sessionIdがstemと一致、isSidechainが欠損またはfalseの最初の適格レコードを確認する。cwdは絶対パスのみ、versionは文字列。ID未確認は候補外。project名をcwdへ逆変換しない。

- type=user/assistant、sessionId一致、非空uuid、message object、role一致。`uuid`を表示IDに採用し`message.id`を使わない。
- isSidechain/isMeta/isCompactSummary/isVisibleInTranscriptOnly/isApiErrorMessageがtrueまたは存在する非bool値なら除外。除外判定はID不一致判定より先。
- message.model=`<synthetic>`は両roleで除外。userのtoolUseResult非nullは全体除外。
- userの文字列またはtext配列、assistantのtext配列のみ許可。userのimage、assistantのthinking/redacted_thinking/tool_useを無視して適格textを抽出する。未知・不正blockまたはuserのtool_resultを含む場合はレコード全体を除外する。textは非空白のみ、複数を `\n\n` で結合。内部wrapper検査はtextを区切りなしに連結した文字列にも実施する。
- 内部wrapper明示リスト: `<system-reminder>`, `<local-command-caveat>`, `<local-command-stdout>`, `<local-command-stderr>`, `<command-message>`, `<command-name>`, `<command-args>`, `<task-notification>`, `<persisted-output>`, `<bash-stdout>`。該当レコードは全体除外。
- assistantはすべて応答（phase推定なし）。同uuid同revisionは重複表示せず、異revisionは最初の位置を更新する。別uuid同内容は別表示。対象レコードのID不一致は監視停止。

未知のスキーマを推測しない。JSON配列/数値/不正UTF-8、不正型のblockなどは無視・件数計上し、panicさせない。JSONLに個人情報が含まれるため、実ログをfixtureに転用しない。

### 6.3 共通Reader・索引

ファイルはread-only。`bufio.Scanner`の既定64KiB上限に依存せず、1MiBチャンク、byte offsetを管理するbounded line splitterを実装する。未終端のline/UTF-8は次回まで保持。最大8MiB（改行を含む既存判定）を超える論理行は残りを改行まで読み捨て、巨大行1件として記録する。通常行のメモリ保持は最大8MiBとチャンク程度。JSON解析前にUTF-8妥当性を検証する。byte offsetはint64、Unicode文字数は別管理。

初期scanは全ログを索引化するが、本文保持は表示対象のみ。Codexは基点の索引追記、Claudeはuuid重複置換。`IndexEntry`にID/offset/end/revision/timestamp/kind。statsはrecords/events/extracted/ignored/invalid_lines/oversized_lines。`read_messages`はseek/read、再抽出、IDとrevision照合。空entriesは正常な空結果。未対応/halted/denied/missing/read/index_mismatchを区別する。

pollは250ms、1回の最大読取1MiB、同一readerのscan/poll/readbackは同時に実行しない。ファイル消失は表示保持と再試行、inode/device変更または縮小は再構築、ID変更は停止。履歴readbackの不一致は1回再構築し、再構築後も不一致なら停止。readbackのmissing/readは位置を進めず手動再試行、denied/halted/unsupportedは停止。

symlink/包含/通常ファイル/選択IDを発見時と読み取り時に検査。Linuxのファイルハンドルとstatで置換を識別し、root外へ追従しない設計にする。可能な場合はGoの `os.Root` 等のroot相対操作とno-followの組合せを評価するが、symlink規則とTOCTOU（検査・使用の時間差）をテストする。任意の同一ユーザーによる競合書換えへの完全防御は保証しない。root外・特殊ファイルは読取拒否。ファイルを作成/削除/移動/chmodしない。

## 7. Markdown保護・Unicode互換

現行Pythonの `markdown.py` を正本とする。Goldmark ASTのsource segmentから物理行と親コンテナを特定し、翻訳可能範囲を決める。元文字列の再serializeは禁止。ASTパーサーの差を安全側に扱い、判定不能は原文保持。最初にfixtureによる適合検証を行う。

対象は単一物理行のtop-level段落、単純リスト、単純引用、ATX見出し。複数行ブロック、入れ子リスト、コードフェンス、インデントコード、表、HTML、画像、数式、構造化データ、独立commandを保持する。prefix/suffix、見出し閉じmarker、改行、空白を保持する。実際のブロック分類は基点fixtureで比較し、READMEとコードの差は報告する。

1. 全文を原文として保存する。物理行を抽出し、secret/Japanese/no_letters/command/structured/codeを現行の優先順で判定する。秘密パターン（PEM、Bearer、AKIA、GitHub、Slack、sk-、認証URL等）は全文送信前の安全境界として維持する。
2. インラインコード、リンク全体、URL、パス、拡張子付きファイル名、CLI switch、識別子、関数呼出、バージョン等を原文のspanから抽出。開始位置昇順、同じ開始は長いspan優先、重複は現行順序で排除。`@@CJ0@@`等で置換し、原文にprefix衝突があれば保守的に除外する。
3. Go RE2はPythonのlookbehind、backreference、Unicodeの `\w`/`\b` をそのまま再現できない。Python regexを機械的に置換しない。バッククォートの同長delimiterは手続き的に走査し、lookbehind境界はruneの前文字で判定する。Unicode単語境界/空白を互換fixtureで検証する。安全性のため不確かなspanは原文保持。高価なbacktrackingライブラリを安易に追加しない。
4. セグメントは最大1000 Unicode code point（rune、byte長ではない）。marker範囲内を切断せず、句読点優先→空白境界の順で後方探索。元の区切り空白をtrailingとして保持。安全に分割できない文は保守的除外。
5. 翻訳の非空、改行・CR・制御文字なし、追加Markdown構造なし、markerが完全一致・一度・同順序であることを検証。失敗は原文、キャッシュしない。キャッシュ取得時にも検証。全segmentが成功したunitのみ連結・marker復元・表示更新する。部分unit失敗は原文維持、他unitは成功分を表示。

データ型: `ProtectedDoc{Source, Lines, Units}`、`Unit{ID, LineIndex, Prefix, Suffix, SourceText, Segments, Markers, SkipReason, Conservative}`、`Segment{Text, Markers, Trailing}`。変換・分割・復元は純粋関数とする。byte offset/rune indexは明確に区別する。日本語混在・特殊Unicode・CRLF・tab・絵文字・marker衝突のgoldenを設ける。

## 8. 翻訳HTTPとキャッシュ

### 8.1 HTTP

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

`POST /v1/chat/completions`のみ。診断と再接続は`GET /v1/models`。HTTP bodyにprovider、会話履歴、原文メタデータ、tool、資格情報を追加しない。モデルは設定IDそのまま。レスポンスは2xx、JSON object、choices[0] object、finish_reason=stop、message.content非空文字列、trim後検証。モデル一覧はdata配列内の文字列idのみ。応答bodyはclose、サイズ上限（提案: 4MiB。上限超過はINVALIDとし、Phase 0で承認）を設ける。

標準http.TransportのProxy=nil、CheckRedirectはErrUseLastResponse、認証なし、TLS/クラウドfallbackなし。connect 3s、読取無通信60s、要求全体90s。`ResponseHeaderTimeout`だけではbody読取timeoutを保証しない。context期限と読取deadlineを実装・テストする。redirect/status分類は基点と一致。自動再送を導入しない（HTTP transportの再試行条件も確認）。POST/GETは共通の単一通信workerで直列化する。HTTP診断が本文を出さないことを検証する。

| ErrorKind | 意味 | pause |
|---|---|---|
| connect | 接続失敗 | yes |
| timeout | タイムアウト | yes |
| http | 非2xx | 5xx以上のみ |
| invalid | 応答形式・marker不正 | no |
| empty | 空応答 | no |
| incomplete | finish_reason不正 | no |
| cancelled | 旧generation・終了 | no、画面適用しない |

接続状態は未確認/接続成功/通信失敗。4xxや応答不正は通信できているため接続成功側、接続失敗/timeout/5xxは通信失敗。pause中は予約済み未送信タスク保持、自動再試行なし。`r`によるGET成功後だけ再開する。現行reconnectはモデル一覧取得成功を条件とし、doctorのみモデルID一致を確認する。この差を黙って変更しない。

### 8.2 SQLite互換

既存path/schemaを使用する。新DBを作る前に権限検査、directory 0700、file 0600。既存group/other権限が広い場合は自動chmodせず無効化する。新規ファイルはO_CREATE|O_EXCL等で権限を固定し、親directoryも確認する。SQLiteのsidecarが必要な場合も権限とsymlinkを確認。既存DBは削除・置換・VACUUM・journal mode変更・schema migrationしない。`busy_timeout=500ms`、single connection、SQL parameter binding。ロック/破損/権限/書込失敗でプロセス内のDB利用を停止し、メモリcacheのみへ切替、診断を出す。生のSQLiteエラーや本文はログに出さない。

```sql
CREATE TABLE IF NOT EXISTS translations (
  cache_key TEXT PRIMARY KEY,
  translated TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

cache keyは以下のPython `json.dumps(list, ensure_ascii=False)` のUTF-8 bytesのSHA-256（小文字hex）。バージョンは両方1。

```text
[1, 1, "http://127.0.0.1:1234/v1", "plamo-3-translate-2b", "en", "ja", "polite", "Example @@CJ0@@."]
```

Goの`json.Marshal`をそのまま使うと空白、HTML escape、U+2028/U+2029などが異なる。配列8要素専用のPython互換serializerを実装し、`,`後にASCII space、文字列のquote/backslash/control escaping、非ASCII保持をgoldenで比較する。入力はvalid UTF-8。cache key自体をPython版のDBから再利用可能にする。成功したmarker検証済みsegmentのみ保存する。created_atはUTC RFC3339（基点の+00:00表記との互換確認）。同じkeyはINSERT OR REPLACE。原文/transcript/ユーザー入力を保存しない。cache policyの意味を変えた場合は承認を受けてversionを上げ、既存keyの上書きはしない。

Python→Go、Go→Pythonの双方向読み書きを合成DBで確認する。実DBをコピーして検証する場合は利用者の許可が必要。開発者の実cacheを削除しない。

## 9. 並行処理と監視状態

### 9.1 状態所有と通信

監視・表示・queueの可変状態は一箇所（monitorのevent loop）で所有する。TUIのUpdateはキー/resizeをmonitorへ渡し、immutable snapshotまたは差分を受け取りViewを更新する。UI goroutineも独自の可変状態（viewport/選択）だけを所有。workerから直接UIやmonitorのmap/sliceを変更しない。結果イベントのsnapshotはdeep copyか所有権移譲で競合を避ける。

worker構成は reader I/O（選択中1操作ずつ）、translation I/O（1 segmentまたはGETずつ）、必要時のセッション発見（重複起動禁止）。monitorはI/Oを待ってUIをブロックしない。readerは専有、世代を跨いで同じ可変readerを共有しない。pollとhistoryが重なれば直列化。選択変更時は旧reader contextを取り消し、旧結果を破棄。終了時はcontext cancel、timer停止、worker join、SQLite close、terminal復元。goroutineは要求ごとに無制限に生成しない。

```text
TUI key / timer / reader result / translation result
                 |
                 v
          monitor event loop ----> immutable ViewSnapshot ----> TUI
           |                  |
           | read request     | segment / reconnect request
           v                  v
      reader worker       translation worker
      (readonly IO)       cache lookup -> authorization -> HTTP -> cache
           |                  |
           +---- typed result-+
```

各コマンド/結果にはSessionKey・Generation（必要に応じRevision/UnitID）を含める。monitorは成功・失敗・取消・状態通知の全経路で現在世代と識別子を照合する。channel送信はcontext選択で終了可能にする。queue状態のmutexとイベントループの二重所有を避ける。

### 9.2 状態遷移

| 状態 | 入力 | 動作 | 遷移 |
|---|---|---|---|
| Idle | switch | generation進め、旧取消、新reader、表示初期化 | Loading |
| Loading | scan/read成功 | 末尾50件を原文表示、翻訳予約、poll開始 | Watching |
| Loading | index_mismatch | generation進め、再構築 | Rebuilding |
| Loading | 他の確定失敗 | stop、表示reset、理由通知 | Stopped |
| Watching | poll追記 | ID/revisionに従い反映、翻訳予約 | Watching |
| Watching | missing/read一時障害 | 表示保持、理由通知、poll継続 | Watching |
| Watching | inode/縮小/履歴索引不一致 | 旧取消、pending解除、表示保持 | Rebuilding |
| Watching | denied/ID不一致 | stop、旧ジョブ取消、停止理由保持 | Stopped |
| Rebuilding | scan成功 | note設定→索引設定→readbackの順 | Rebuilding |
| Rebuilding | readback成功 | 停止解除、表示置換、翻訳予約、poll開始 | Watching |
| Rebuilding | scan/read失敗 | stop、表示reset | Stopped |
| Stopped | h/r/poll/旧結果 | 無視、監視・翻訳を自動再開しない | Stopped |
| 任意 | switch | 明示選択のみ新世代へ | Loading |
| 任意 | q/終了 | context取消、worker終了、terminal復元 | Closed |

Loading中は新表示が未確定なので履歴・retryを実行しない（重複操作は安全側に拒否する提案）。再構築中は新規翻訳予約禁止。stopは一箇所に集約しgeneration更新、取消、pending解除、必要なreset、note・status通知を行う。古い失敗が新セッションをstop/resetしない。停止理由を古いpoll/reconnectの結果で上書きしない。成功した再構築と手動switchのみ有効世代にpollを開始する。note更新はPR #4の順序を守る。

### 9.3 翻訳スケジューラ

- 1 job=1 Unit。job内複数segmentを順に処理。全体同時HTTP=1。active最大32（queue+実行中）、超過はdeferred FIFO。pending表示はactive+deferred。対象を消失させない。
- priority 0=新着・改訂・手動retry、1=初期表示（新しいものから）、2=追加履歴。heap順は(priority, sequence)。deferredは現行FIFO昇格。segment処理ごとに再投入し、割込可能にする。sequenceはjob作成時に付与する。
- job送信→昇格→実行→完了/取消の会計はmonitorのみが更新する。activeはqueue+inflightで検算可能にし、deferredは含めない。失敗したunitは手動retryまで再予約しない。paused中は新segment dispatchを行わない。deferredは表示したunit数に比例し、固定長32のchannelを総ジョブ数上限と誤解しない。
- キャッシュ照会（非同期）後に、HTTP開始許可を現行generation・revision・unit IDと照合して取得する。開始許可と世代取消の順序をmonitorで直列化し、workerはcontext取消も直前確認する。旧世代が許可を得た後に取消された場合はinflight扱いとし中断を試みる。送信済み内容の撤回は保証しない。取消観測後に新規POSTを開始しない。
- HTTP結果取得後、世代確認→marker検証→cache保存（成功済み・有効世代のみを推奨）→結果通知。古い通知で新世代のpause、note、queue、表示を変えない。未送信旧jobは除去、残りsegmentは処理しない。改訂後の旧revisionの結果も破棄する。
- 通常のretryはfailed unitだけを予約。pauseの場合はGETで接続確認し、成功後に再開。GET結果も世代照合。inflightのslotはworker終了通知まで保持し、二重減算しない。

擬似コード:

```text
handle_result(event):
    release owned in-flight slot exactly once (by job/operation ID)
    if event.generation != current or event.session != current_session:
        promote_current_if_possible(); return
    if event is read failure: apply only current transition
    if event is segment result:
        verify message revision and unit identity
        if invalidated: discard, no further segment
        else: update unit result / pause / queue as appropriate
    publish snapshot
```

旧結果のslot解放は必要だが、旧世代のUI/接続状態/メッセージには作用させない。Race Detectorはデータ競合を検出するが、論理的な順序ミスを保証しない。Event/channel barrierによる決定論的テストでPR #3/#4の失敗経路を固定する。

## 10. TUI

Bubble Tea v2のUpdate/Viewを採用。上部status、中央のscrollable message表示、下部キーガイド、セッション選択画面を提供する。テキストは原文を直ちに表示し、成功したunitだけ同じmessage位置で更新する。翻訳失敗時も原文は消さない。

| キー | 動作 |
|---|---|
| s | provider/ID/cwd/更新/形式の選択。候補更新間隔2秒、選択時の行SessionKeyを保持 |
| o | 原文/翻訳切替、翻訳は継続 |
| r | 失敗のみ再試行、必要時再接続 |
| h | 過去50件追加、表示対象のみ翻訳 |
| End | 最新へ移動、auto scroll再開 |
| q / Ctrl+C | ビューアのみ終了、端末復元 |

上下・PageUp/Down、マウススクロールは現行TUI相当。表示タイトルの種類: あなた/進捗報告/最終回答/計画/応答。時刻はローカルHH:MM:SS、解析不能は時刻不明。接続・pending・note・cache診断、providerと短縮IDを表示。未翻訳/待機/翻訳中/部分翻訳/翻訳失敗、成功/skipのラベル省略を維持する。

表示位置はmessage IDとその内側の物理/表示行anchorで管理する。末尾追従中のみ新着を追う。スクロール中は新着・翻訳による行高変化・履歴prepend・resizeでanchorを可能な範囲で保つ。revision更新は同じ位置。履歴追加時は成功分のみcursorを進め、表示済みIDを重複させない。新着unique追加でvisibleを進める。本文の横幅はterminalのセル幅（CJK/絵文字）を用いる。

Markdownは保護用ASTと描画を分離。見出し・リスト・引用・コード・表が読み取れる安全な描画とする。初期実装は原文Markdown記法を含む表示も許容し、Textualと完全同一のレンダリングは不要。コード/リンクラベル/URL/空白等の情報を欠落させない。HTML実行・リンク起動・画像取得を行わない。原文と訳文の両方からANSI/OSC/C0/C1制御コードを除去してから、アプリ自身のスタイルを付与。タブ/改行は表示用に扱い、元文字列を変更しない。OSC 8/52や不完全ESC等の危険入力を実画面テストする。

TUIの入力は操作キーのみ。エージェントのプロセス起動・入力・承認・kill・IPCを実装しない。

## 11. セキュリティとデータ境界

- transcriptの読取専用、資格情報・tool出力・内部思考・user入力の非送信は厳格な受入条件。既存のallowlistを維持する。未知スキーマを翻訳対象に広げない。
- URL/transportでローカル通信のみ、DNS/環境proxy/redirectを禁止。RFC1918は利用者が管理するホストを明示した場合のみ利用する。LAN内の相手が信頼できることを保証する仕組みではない。
- キャッシュ・診断・HTTP・TUIの安全境界を独立テストする。ログに本文、cache内容、HTTP body、資格情報、panic dumpを含めない。CLIの既存診断で必要なpath/id/countだけを出す。例外やネットワークエラーは分類済みの文言へ変換する。
- 実際のログ・DB・個人の設定・認証情報をgitやCI artifactへ入れない。実環境を検証する際は合成無害文を用い、実ログの内容は報告しない。
- symlinkやroot外操作の検査が不可能な環境は利用不可として扱う。Linux前提の権限をWindowsで暗黙に無効化しない。

## 12. 検証戦略（Python版をoracleにする）

### 12.1 基準固定と契約マッピング

Phase 0でPython基点を別worktree（読み取り専用扱い）に固定し、`uv sync --locked`、pytest、ruff、Omenを実行。219件のテストを実際のnode ID単位で一覧化し、Goテスト/fixture/手動検証への対応表を作る。件数一致は要求しない。Python内部実装固有のassertは外部契約へ置換し、該当なしの理由を記録する。Go版で同じ不具合を検出できることを優先する。

合成fixtureを共有し、開発専用のPython oracleを固定基点に対して実行する。oracleはJSONで構造化結果（messages/id/revision/offset/kind/stats、protected segments、marker restore、cache key、CLI stdout/stderr/exit、HTTP requestと状態遷移）を出す。Goで同じfixtureを処理して比較。時刻・絶対一時path・ANSI等は意味のある項目のみ正規化する。差分は本specの許可事項か修正必須かを判定し、期待値を変更して通すことは禁止。

oracle生成は開発時だけ必要。確定goldenはGoテストから直接読み、リリースビルド・利用時・通常のGo CIにPythonを要求しない。fixtureに実ログを含めない。既存Pythonテストは移植完了まで変更・削除しない。

### 12.2 受入条件

| ID | 条件 | 検証 |
|---|---|---|
| G01 | 設定/TOML/env/URL正規化・未知キー、CLI grammar/exit一致 | golden/table-driven |
| G02 | Codex discovery、親子、symlink、legacy/paginated/Plan、未知履歴拒否 | oracle fixture |
| G03 | Claude direct UUID、1MiB、flags、内部wrapper、tool/synthetic排除、uuid改訂 | oracle fixture |
| G04 | Codex 256KiB/1MiB/EOF未終端とClaude完全行の差を維持 | boundary fixtures |
| G05 | JSONL未終端/UTF-8/巨大行/offset/不正型、後続回復、panicなし | unit/fuzz |
| G06 | index ID/revision、同サイズrewrite、置換/縮小、欠損/復旧、root外拒否 | temp filesystem |
| G07 | 初期50、h=50、新着unique、revision、重複ID、失敗時cursor保持 | monitor state tests |
| G08 | 保護・span優先順位・Unicode境界・1000 rune分割・復元 | differential golden/fuzz |
| G09 | user/tool/secret/内部文のHTTP非送信 | httptestのbody assert |
| G10 | POST/GET契約、timeout/4xx/5xx/invalid/empty/incomplete/marker分類 | httptest |
| G11 | proxy/redirect/非local拒否、制御文字・OSC等の非実行 | unit/httptest/PTY |
| G12 | cache key byte互換、SQLite双方向、権限、破損/lock/read-only、非破壊 | golden + temp DB |
| G13 | 同一segment再利用、失敗非cache、policy/model/form分離 | unit/httptest |
| G14 | 同時HTTP1、active<=32、deferred保持、優先順位、segment再投入、会計一致 | deterministic scheduler |
| G15 | cache照会中の切替で旧POST禁止、HTTP中の取消、残りsegment/旧結果抑止 | barrier tests |
| G16 | 同一ID別provider/path、A→B→A、同一ID再選択、旧revision混入なし | barrier tests |
| G17 | 旧scan/read/poll/reconnect成功・失敗を新世代へ適用しない。switch/rebuildのindex_mismatchとread_errorを全組合せで検証 | barrier tests |
| G18 | denied/ID不一致後の停止維持、正常ファイル復旧でも自動再開禁止、明示switchで復帰 | monitor tests |
| G19 | rebuild中のretry/history禁止、note→索引→readの順、失敗reset、停止理由維持 | barrier tests |
| G20 | pause/reconnect、未送信保持、手動retryは失敗unitのみ、4xxと5xxを区別 | scheduler tests |
| G21 | doctor全診断実行、root各1回、sessions/inspect本文なし・HTTPなし | CLI golden |
| G22 | TUIヘッダー/本文の実描画、原文先行、翻訳置換、キー、picker snapshot、scroll anchor | headless/PTY + manual |
| G23 | transcript bytes/mtime/mode不変、CLIの起動停止なし | integration |
| G24 | race、fuzz、goroutine終了、close後送信なし、terminal復元 | race + lifecycle/PTY |
| G25 | CGO=0単一バイナリ、Go/Python/uvなしのWSL相当環境でhelp/doctor/TUI、release checksum | clean env build/run |
| G26 | 実WSLのCodex/Claudeと実PLaMoで手動確認、cache再利用、遅延計測 | manual |

G01〜G25を自動化可能な範囲で実行し、G22/G25の実端末確認とG26を別途記録する。実機がない場合はPARTIAL。特に初期読み取り失敗と再構築失敗を全組合せで確認し、旧例外による新セッション停止の回帰を防ぐ。待機時間だけで競合を再現せず、channel/eventで制御する。

### 12.3 性能とリソース

測定は同一WSL機、同一合成fixture、同一翻訳サーバー・モデル・cache条件で行う。起動→最初の原文、完全JSONL行書込→原文表示、原文→訳文、cold/warm、RSS、バイナリサイズ、CPU idleを報告する。モデル生成時間と監視遅延を分離する。通常の新着原文は500ms以内を目標（250ms poll）。翻訳遅延はモデル依存なので固定合格秒数を設けない。初期scan・巨大ログ・長文でUI操作が止まらないことを確認する。数値達成のために安全性を下げない。

## 13. Go開発・品質検査

開発依存をmise等で固定。go.mod/go.sumをcommit、`go mod tidy`後の差分なし、`go mod verify`。CIのtoolchainは固定、`GOTOOLCHAIN=local`等で意図しない更新を防ぐ。公式Goと互換のgolangci-lintを固定する。

```sh
go mod verify
go test ./...
go vet ./...
golangci-lint run ./...
golangci-lint fmt --diff
CGO_ENABLED=1 go test -race ./...
govulncheck ./...
# 各fuzz targetを個別に指定し、時間を限定して実行する例
# go test ./internal/protection -run '^$' -fuzz '^FuzzProtect$' -fuzztime 30s
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/transpane ./cmd/transpane
git diff --check
```

Race Detectorは開発・CIでCGO=1とCコンパイラが必要。リリースはCGO=0で別ジョブ。Python不要の環境で起動検証する。fuzz targetは実際に存在する名前をmise taskへ設定し、JSONL splitter、抽出、marker保護・復元、URLを優先。race/fuzzの失敗は修正必須。

初期golangci-lint v2設定の候補:

```yaml
version: "2"
linters:
  default: standard
  enable:
    - bodyclose
    - contextcheck
    - errorlint
    - gosec
    - nolintlint
    - sqlclosecheck
formatters:
  enable:
    - gofumpt
    - goimports
```

gocognit/gocyclo/dupl/revive/gocritic等はPhase 0で設定スキーマ・誤検知・既存との重複を確認し、必要なものだけ追加する。Omenは関数複雑度、依存、重複、hotspotの分析として実行。GoとPythonの指標は直接比較しない。Go内の目標はcognitive原則15以下を目安とし、合理的な例外を記録。品質指標のためだけに関数を増やさない。gocognitとOmenの計算法が一致するとは仮定しない。nolintは具体的根拠と対象限定、全体除外は禁止。既知脆弱性は評価し、未解決の到達可能な重大脆弱性はリリースを止める。

## 14. 移行手順と段階別完了条件

| Phase | 作業 | 完了条件 |
|---|---|---|
| 0 | 基点固定、現行テスト再実行、依存version決定、Goldmark/SQLite/Bubble Teaの小さな実証 | 互換性表、219件の実node ID対応、Go CGO=0試験ビルド、境界fixture、未達/差異一覧 |
| 1 | Go module、config/domain、provider discovery/extractor、reader/index | G01〜G06の該当テスト。実装はCLIから独立 |
| 2 | protection、cache、HTTP | G08〜G13、Python双方向cache、非送信/安全性 |
| 3 | monitor state machine、scheduler、history、取消 | G07、G14〜G20、G24の並行部分。全regression、race |
| 4 | CLI、TUI、実画面 | G21〜G23、G24、既存キー・画面情報・スクロール |
| 5 | 統合、比較、配布 | G01〜G26再評価、clean環境、実WSL/サーバー、README、差異報告 |
| 6 | 切替・旧実装整理（承認後） | Goを唯一の稼働実装に切替、Python由来の旧CI/依存を除去、rollback可能、最終検証 |

Phaseごとにコミットを責務単位で分け、通過した検査・未実施を記録する。移植中は同一リポジトリの作業ブランチでPythonをoracleとして残し、Go側は`cmd/transpane`/`internal`で開発する。移植完了後の二重保守は禁止。テストを件数のために増やさない。Phase 0で必要な差異を承認されるまで公開互換性を壊す変更に進まない。

### 14.1 配布と切替

バイナリの開発時起動は`go run ./cmd/transpane`、利用時は`transpane`のみ。リリース成果物候補 `transpane-linux-amd64.tar.gz`（実行ファイル、LICENSE/必要なNOTICE、簡単なREADME）、`SHA256SUMS`。実行ファイルは単一、システムライブラリやGo/Pythonランタイムの別途導入なしで動作することを`file`/`ldd`等とclean環境で確認する。圧縮パッカーは使用しない。

```sh
# 既存設定・DBを移動せずに利用する例
install -m 0755 ./transpane "$HOME/.local/bin/transpane"
transpane --version
transpane doctor
transpane --source codex
```

インストール先に既存のPython版実行ファイルがある場合は利用者に確認し、勝手に上書きしない。`command -v transpane`で選択実行ファイルを確認する。旧版は固定コミットのworktreeから`uv run transpane`で復帰できる。Goが既存DBを読み書きした後でもPythonに戻れることをテストする。旧DBの削除・再生成を移行手順に含めない。

CIはLinux amd64のtest/lint/race/vuln/buildとartifact生成を行う。GitHub Releaseへの公開、タグ作成、merge、実ユーザー環境へのinstallは明示承認後のみ。別OSをサポートしたとの記載は実機テスト後に限る。

## 15. 実環境確認・Definition of Done

実WSL2で、実Codex/Claudeの各1セッションを日本語入力・無害な英文応答で確認。tool呼出や内部ログが送信されないことを合成ログで併せて確認する。原文→訳文、o、s、h、End、r、q、同じcacheを再起動で再利用、画面の折返し・縦方向切れ、サーバー停止相当（mock可・実サーバー停止は許可時のみ）、他providerへの切替を確認する。Codex DesktopのSSH経由ログは現行と同じセッション形式なら対象になり得るが、実機未確認のため対応済みとしない。

完了条件:

- 承認済みspec、固定基点、依存/ツールchain、全対象契約の検証結果が存在する。
- G01〜G26が合格、または承認済みの明示的例外。未検証をPASSとしない。
- Go単一バイナリでWSL2利用可能、Python/uv/Go runtimeを要求しない。cache互換とrollbackを実証。
- 既存安全境界、セッション切替、停止・取消、履歴、Markdown保護を維持。新しい重大な不具合がない。
- README、設定例、導入、診断、キー、制約、既存cache移行、rollbackが更新され、機密情報が含まれない。
- 承認後に旧Python稼働実装と依存を整理し、最終Go検査を再実行。公開/mergeは別承認。

実装者の最終報告形式:

```text
STATUS: COMPLETE | PARTIAL | BLOCKED
BASE / HEAD:
PHASES:
DEPENDENCIES / TOOLCHAIN:
CHANGED FILES / ARCHITECTURE:
PYTHON BASELINE RESULTS + contract mapping:
G01-G26: PASS/FAIL/NOT RUN + evidence
DIFFERENTIAL RESULTS / approved differences:
TEST / RACE / LINT / FUZZ / VULN / BUILD:
BINARY SIZE / CHECKSUM / RUNTIME DEPENDENCIES:
WSL / CODEX / CLAUDE / PLaMo manual evidence:
PERFORMANCE (cold/warm; original vs translation):
CACHE COMPATIBILITY / ROLLBACK:
UNRESOLVED RISKS / NOT VERIFIED:
```

Phase 6未承認または実環境未確認ならPARTIAL。AIによる自己評価を検証結果の代用にしない。重大な互換性不一致や不確実な安全境界があれば停止して報告する。数値目標の未達だけを理由に追加リファクタリングしない。

## 16. 参照資料

現行の正本（確認した固定コミット）:

- https://github.com/yuggr/transpane/tree/f199bb6cce011c80d28f7cdfe91929d62d30c08f
- https://github.com/yuggr/transpane/blob/f199bb6cce011c80d28f7cdfe91929d62d30c08f/README.md
- https://github.com/yuggr/transpane/blob/f199bb6cce011c80d28f7cdfe91929d62d30c08f/docs/superpowers/specs/2026-11-25-complexity-refactor-design.md
- https://github.com/yuggr/transpane/pull/3 （Claude Code対応、停止・取消契約）
- https://github.com/yuggr/transpane/pull/4#issuecomment-7180380609 （最終修正・219テスト報告）

外部技術資料（2026-11-26確認）:

- Go release policy/history: https://go.dev/doc/devel/release
- Go 1.27 release notes: https://go.dev/doc/go1.27
- Go race detector: https://go.dev/doc/articles/race_detector
- net/http: https://pkg.go.dev/net/http
- Bubble Tea v2 migration: https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md
- Bubbles viewport: https://pkg.go.dev/charm.land/bubbles/v2/viewport
- Goldmark v1 AST: https://pkg.go.dev/github.com/yuin/goldmark/ast
- Goldmark v1 extensions: https://pkg.go.dev/github.com/yuin/goldmark/extension
- go-toml: https://github.com/pelletier/go-toml
- modernc SQLite: https://pkg.go.dev/modernc.org/sqlite
- golangci-lint v2 configuration: https://golangci-lint.run/docs/configuration/file/
- golangci-lint Go version support: https://golangci-lint.run/docs/welcome/faq/

本specは既存実装・レビュー結果からの設計提案。依存のビルド、Goでの試作、実機検証、テスト実行、Omen再計測は未実施。Phase 0で検証・固定する。
