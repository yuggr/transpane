"""Command line entry points: doctor, sessions, inspect and the TUI."""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
from datetime import datetime
from pathlib import Path

from .cache import TranslationCache
from .config import Config, ConfigError, load_config
from .models import AgentProvider
from .session import (
    SessionError,
    claude_transcript_counts,
    discover_all_sessions,
    reader_for_session,
    select_session,
)
from .translation import TranslationClient, TranslationFailure
from .ui import TranspaneApp

EXIT_OK = 0
EXIT_FAILURE = 1
EXIT_USAGE = 2

SOURCES = {
    "all": None,
    "codex": AgentProvider.CODEX,
    "claude": AgentProvider.CLAUDE,
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="transpane",
        description="Codex / Claude Code のセッションログを日本語表示する独立 TUI",
    )
    parser.add_argument(
        "--source",
        choices=tuple(SOURCES),
        default="all",
        help="対象の取得元: all（既定）/ codex / claude。subcommand より前に指定する",
    )
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--cwd", help="完全一致で絞り込む cwd（Linux 側の絶対パス）")
    selection.add_argument(
        "--session",
        dest="session_id",
        help="明示的に選択するセッションID（codex:<id> / claude:<id> も可）",
    )
    subparsers = parser.add_subparsers(dest="command")

    subparsers.add_parser("doctor", help="設定・読取・キャッシュ・翻訳サーバーを診断する")
    subparsers.add_parser("sessions", help="セッション一覧を表示する（本文なし）")
    inspect_parser = subparsers.add_parser(
        "inspect", help="セッションの形式とイベント件数を表示する（本文なし）"
    )
    inspect_parser.add_argument("--session", dest="inspect_session", required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        config = load_config()
    except ConfigError as exc:
        print(f"設定エラー: {exc}", file=sys.stderr)
        return EXIT_USAGE

    source = SOURCES[args.source]
    if args.command == "doctor":
        return run_doctor(config, source)
    if args.command == "sessions":
        return run_sessions(config, source)
    if args.command == "inspect":
        return run_inspect(config, args.inspect_session, source)
    return run_tui(config, args.session_id, args.cwd, source)


def _root_state(path: Path) -> str:
    if not path.exists():
        return "missing"
    if not path.is_dir():
        return "invalid"
    if not os.access(path, os.R_OK | os.X_OK):
        return "unreadable"
    return "ok"


_ROOT_MESSAGES = {
    "missing": "存在しません",
    "invalid": "ディレクトリではありません",
    "unreadable": "読み取れません",
}


def _print_root(label: str, state: str, required: bool) -> None:
    if state == "ok":
        print(f"  [OK] 読み取り可能 ({label})")
    elif required:
        print(f"  [NG] {_ROOT_MESSAGES[state]} ({label})")
    else:
        print(f"  [WARN] {_ROOT_MESSAGES[state]} ({label}: この取得元は利用できません)")


def run_doctor(config: Config, source: AgentProvider | None = None) -> int:
    ok = True
    print(f"設定ファイル: {config.config_path or 'なし（既定値）'}")
    print(f"Codex home: {config.codex_home}")
    print(f"Codex セッションディレクトリ: {config.sessions_root}")
    print(f"Claude home: {config.claude_home}")
    print(f"Claude プロジェクトディレクトリ: {config.claude_projects_root}")

    codex_state = _root_state(config.sessions_root)
    claude_state = _root_state(config.claude_projects_root)
    if source is None:
        if codex_state == "ok" and claude_state == "ok":
            ok = True
        elif codex_state != "ok" and claude_state != "ok":
            ok = False
        _print_root("Codex", codex_state, required=False)
        _print_root("Claude Code", claude_state, required=False)
    elif source is AgentProvider.CODEX:
        ok = codex_state == "ok"
        _print_root("Codex", codex_state, required=True)
        _print_root("Claude Code", claude_state, required=False)
    else:
        ok = claude_state == "ok"
        _print_root("Codex", codex_state, required=False)
        _print_root("Claude Code", claude_state, required=True)

    if claude_state == "ok" and source in (None, AgentProvider.CLAUDE):
        confirmed, unconfirmed = claude_transcript_counts(config.claude_projects_root)
        line = f"  Claude transcript: ID確認済み {confirmed} 件"
        if unconfirmed:
            line += f" / ID未確認 {unconfirmed} 件（候補外）"
        print(line)

    cache = TranslationCache(config.cache_path)
    status = cache.open()
    print(f"キャッシュ: {status.path}")
    if status.available:
        print("  [OK] 利用可能")
    else:
        print(f"  [NG] {status.message}")
        ok = False

    print(f"翻訳サーバー: {config.base_url} / model={config.model} / form={config.form}")
    try:
        models = asyncio.run(_list_models(config))
    except TranslationFailure as exc:
        print(f"  [NG] {exc.kind.label}")
        ok = False
    else:
        print(f"  [OK] 接続できました（モデル {len(models)} 件）")
        if config.model in models:
            print(f"  [OK] モデル {config.model} が見つかりました")
        else:
            print(f"  [NG] モデル {config.model} が /v1/models にありません")
            for model_id in models:
                print(f"        - {model_id}")
            ok = False

    return EXIT_OK if ok else EXIT_FAILURE


async def _list_models(config: Config) -> list[str]:
    client = TranslationClient(config.base_url, config.model, config.form)
    try:
        return await client.list_models()
    finally:
        await client.aclose()


def run_sessions(config: Config, source: AgentProvider | None = None) -> int:
    sessions = discover_all_sessions(config.sessions_root, config.claude_projects_root, source)
    if not sessions:
        print("セッションが見つかりません")
        return EXIT_OK
    print("取得元\tID\t最終更新\t形式\t区分\tcwd")
    for session in sessions:
        updated = datetime.fromtimestamp(session.updated_at).strftime("%Y-%m-%d %H:%M:%S")
        print(
            f"{session.provider.label}\t{session.thread_id}\t{updated}\t"
            f"{session.mode_label}\t{session.origin_label or '-'}\t{session.cwd or '-'}"
        )
    return EXIT_OK


def run_inspect(config: Config, ref: str, source: AgentProvider | None = None) -> int:
    sessions = discover_all_sessions(config.sessions_root, config.claude_projects_root, source)
    try:
        session = select_session(sessions, ref, source=source)
    except SessionError as exc:
        print(f"セッションエラー: {exc}", file=sys.stderr)
        return EXIT_USAGE

    reader = reader_for_session(session, config.sessions_root, config.claude_projects_root)
    try:
        result = reader.scan_all()
    except (OSError, SessionError):
        print("セッションエラー: ログを読み取れません", file=sys.stderr)
        return EXIT_FAILURE

    print(f"取得元: {session.provider.label}")
    print(f"セッション: {session.thread_id}")
    print(f"cwd: {session.cwd or '-'}")
    print(f"version: {session.cli_version or '不明'}")
    print(f"形式: {result.history_mode or '未対応'}")
    if result.unsupported_mode is not None:
        print(f"未対応の history_mode: {result.unsupported_mode}")
    if result.thread_changed is not None:
        print("セッションID不一致を検出しました（本文は送信しません）")
    stats = result.stats
    print(f"レコード数: {stats.records}")
    print(f"イベント数: {stats.events}")
    print(f"抽出数: {stats.extracted}")
    print(f"無視数: {stats.ignored}")
    print(f"不正行数: {stats.invalid_lines}")
    print(f"巨大行数: {stats.oversized_lines}")
    if result.unsupported_mode is not None:
        print("未対応形式のため本文の翻訳は行いません。")
    elif stats.extracted == 0:
        print(
            "対象イベント未検出（抽出0件）。ログ形式または内部イベントのみの可能性を確認してください。"
        )
    return EXIT_OK


def run_tui(
    config: Config,
    session_id: str | None,
    cwd: str | None,
    source: AgentProvider | None = None,
) -> int:
    initial_session = None
    if session_id is not None:
        sessions = discover_all_sessions(config.sessions_root, config.claude_projects_root, source)
        try:
            initial_session = select_session(sessions, session_id, source=source)
        except SessionError as exc:
            print(f"セッションエラー: {exc}", file=sys.stderr)
            return EXIT_USAGE

    app = TranspaneApp(config, initial_session=initial_session, cwd_filter=cwd, source=source)
    app.run()
    return EXIT_OK


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
