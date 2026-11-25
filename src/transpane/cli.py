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
from .session import (
    SessionError,
    SessionReader,
    discover_sessions,
    select_session,
)
from .translation import TranslationClient, TranslationFailure
from .ui import TranspaneApp

EXIT_OK = 0
EXIT_FAILURE = 1
EXIT_USAGE = 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="transpane",
        description="Codex のセッションログを日本語表示する独立 TUI",
    )
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--cwd", help="完全一致で絞り込む cwd（Linux 側の絶対パス）")
    selection.add_argument("--session", dest="session_id", help="明示的に選択する thread ID")
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

    if args.command == "doctor":
        return run_doctor(config)
    if args.command == "sessions":
        return run_sessions(config)
    if args.command == "inspect":
        return run_inspect(config, args.inspect_session)
    return run_tui(config, args.session_id, args.cwd)


def run_doctor(config: Config) -> int:
    ok = True
    print(f"設定ファイル: {config.config_path or 'なし（既定値）'}")
    print(f"Codex home: {config.codex_home}")
    print(f"セッションディレクトリ: {config.sessions_root}")

    if not config.sessions_root.is_dir():
        print("  [NG] セッションディレクトリが存在しません")
        ok = False
    elif not os.access(config.sessions_root, os.R_OK | os.X_OK):
        print("  [NG] セッションディレクトリを読み取れません")
        ok = False
    else:
        print("  [OK] 読み取り可能")

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


def run_sessions(config: Config) -> int:
    sessions = discover_sessions(config.sessions_root)
    if not sessions:
        print("セッションが見つかりません")
        return EXIT_OK
    print("ID\t最終更新\t履歴形式\t区分\tcwd")
    for session in sessions:
        updated = datetime.fromtimestamp(session.updated_at).strftime("%Y-%m-%d %H:%M:%S")
        print(
            f"{session.thread_id}\t{updated}\t{session.mode_label}\t"
            f"{session.origin_label or '-'}\t{session.cwd or '-'}"
        )
    return EXIT_OK


def run_inspect(config: Config, thread_id: str) -> int:
    sessions = discover_sessions(config.sessions_root)
    try:
        session = select_session(sessions, thread_id)
    except SessionError as exc:
        print(f"セッションエラー: {exc}", file=sys.stderr)
        return EXIT_USAGE

    reader = SessionReader(Path(session.path), session.thread_id)
    try:
        result = reader.scan_all()
    except OSError:
        print("セッションエラー: ログを読み取れません", file=sys.stderr)
        return EXIT_FAILURE

    print(f"セッション: {session.thread_id}")
    print(f"cwd: {session.cwd or '-'}")
    print(f"形式: {result.history_mode or '未対応'}")
    if result.unsupported_mode is not None:
        print(f"未対応の history_mode: {result.unsupported_mode}")
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
        print("対象イベントが0件です。ログ形式を確認してください。")
    return EXIT_OK


def run_tui(config: Config, session_id: str | None, cwd: str | None) -> int:
    initial_session = None
    if session_id is not None:
        sessions = discover_sessions(config.sessions_root)
        try:
            initial_session = select_session(sessions, session_id)
        except SessionError as exc:
            print(f"セッションエラー: {exc}", file=sys.stderr)
            return EXIT_USAGE

    app = TranspaneApp(config, initial_session=initial_session, cwd_filter=cwd)
    app.run()
    return EXIT_OK


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
