from __future__ import annotations

import json

import pytest

from transpane import cli
from transpane.translation import ErrorKind, TranslationFailure

SECRET_BODY = "SECRET BODY SHOULD NOT BE PRINTED"
USER_TEXT = "保存処理を調べて"
CLAUDE_ID = "11111111-1111-4111-8111-111111111111"


def meta(thread_id: str = "t") -> dict:
    return {
        "timestamp": "2026-11-25T01:00:00Z",
        "type": "session_meta",
        "payload": {
            "id": thread_id,
            "cwd": "/tmp/project",
            "cli_version": "0.169.0",
            "history_mode": "paginated",
        },
    }


def records() -> list[dict]:
    return [
        meta(),
        {
            "timestamp": "2026-11-25T01:00:01Z",
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "item": {
                    "type": "UserMessage",
                    "id": "u1",
                    "content": [{"type": "text", "text": USER_TEXT}],
                },
            },
        },
        {
            "timestamp": "2026-11-25T01:00:02Z",
            "type": "event_msg",
            "payload": {
                "type": "item_completed",
                "item": {
                    "type": "AgentMessage",
                    "id": "a1",
                    "phase": "commentary",
                    "content": [{"type": "Text", "text": SECRET_BODY}],
                },
            },
        },
        {"type": "response_item", "payload": {"type": "message", "role": "assistant"}},
    ]


@pytest.fixture
def env(tmp_path, monkeypatch):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text(
        '[translation]\nbase_url = "http://127.0.0.1:1234/v1"\nmodel = "plamo-3-translate-2b"\n',
        encoding="utf-8",
    )
    sessions = tmp_path / "codex" / "sessions" / "2026" / "11" / "25"
    sessions.mkdir(parents=True)
    path = sessions / "rollout-t.jsonl"
    with path.open("wb") as handle:
        for record in records():
            handle.write(json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n")
    monkeypatch.setenv("CODEX_HOME", str(tmp_path / "codex"))
    monkeypatch.setenv("CLAUDE_CONFIG_DIR", str(tmp_path / "claude"))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    monkeypatch.setenv("XDG_CACHE_HOME", str(tmp_path / "cache"))
    return tmp_path


def claude_records() -> list[dict]:
    return [
        {
            "type": "user",
            "sessionId": CLAUDE_ID,
            "uuid": "00000000-0000-4000-8000-000000000001",
            "cwd": "/tmp/claude-demo",
            "version": "2.1.285",
            "timestamp": "2026-11-25T01:00:01Z",
            "message": {"role": "user", "content": USER_TEXT},
        },
        {
            "type": "assistant",
            "sessionId": CLAUDE_ID,
            "uuid": "00000000-0000-4000-8000-000000000002",
            "timestamp": "2026-11-25T01:00:02Z",
            "message": {
                "id": "msg_a",
                "role": "assistant",
                "model": "claude-sonnet-5",
                "content": [{"type": "text", "text": SECRET_BODY}],
            },
        },
        {
            "type": "user",
            "sessionId": CLAUDE_ID,
            "uuid": "00000000-0000-4000-8000-000000000003",
            "message": {
                "role": "user",
                "content": [{"type": "tool_result", "content": "DO_NOT_SEND_TOOL_RESULT"}],
            },
        },
    ]


def write_claude(env, records: list[dict] | None = None) -> None:
    path = env / "claude" / "projects" / "demo" / f"{CLAUDE_ID}.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb") as handle:
        for record in records if records is not None else claude_records():
            handle.write(json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n")


def test_sessions_lists_metadata_without_body(env, capsys):
    code = cli.main(["sessions"])
    out = capsys.readouterr().out
    assert code == 0
    assert "t" in out
    assert "/tmp/project" in out
    assert "paginated" in out
    assert USER_TEXT not in out
    assert SECRET_BODY not in out


def test_inspect_reports_counts_without_body(env, capsys):
    code = cli.main(["inspect", "--session", "t"])
    out = capsys.readouterr().out
    assert code == 0
    assert "形式: paginated" in out
    assert "レコード数: 4" in out
    assert "抽出数: 2" in out
    assert "無視数: 1" in out
    assert USER_TEXT not in out
    assert SECRET_BODY not in out
    assert not (env / "cache" / "transpane" / "translations.sqlite3").exists()


def test_inspect_unknown_session(env, capsys):
    code = cli.main(["inspect", "--session", "missing"])
    captured = capsys.readouterr()
    assert code == 2
    assert "見つかりません" in captured.err


def test_doctor_success(env, capsys, monkeypatch):
    async def fake_models(config):
        return ["plamo-3-translate-2b", "other"]

    monkeypatch.setattr(cli, "_list_models", fake_models)
    code = cli.main(["doctor"])
    out = capsys.readouterr().out
    assert code == 0
    assert "[OK] モデル plamo-3-translate-2b" in out


def test_doctor_missing_model(env, capsys, monkeypatch):
    async def fake_models(config):
        return ["other-model"]

    monkeypatch.setattr(cli, "_list_models", fake_models)
    code = cli.main(["doctor"])
    out = capsys.readouterr().out
    assert code == 1
    assert "other-model" in out


def test_doctor_translation_failure(env, capsys, monkeypatch):
    async def fake_models(config):
        raise TranslationFailure(ErrorKind.CONNECT)

    monkeypatch.setattr(cli, "_list_models", fake_models)
    code = cli.main(["doctor"])
    out = capsys.readouterr().out
    assert code == 1
    assert "接続失敗" in out


def test_config_errors_exit_2(env, monkeypatch, capsys):
    config_path = env / "config" / "transpane" / "config.toml"
    config_path.write_text("[translation]\nmodel = ''\n", encoding="utf-8")
    code = cli.main(["sessions"])
    assert code == 2
    assert "設定エラー" in capsys.readouterr().err


def test_exclusive_selection_flags(env):
    with pytest.raises(SystemExit) as excinfo:
        cli.main(["--session", "t", "--cwd", "/tmp/project"])
    assert excinfo.value.code == 2


def test_sessions_empty(env, capsys):
    for path in (env / "codex" / "sessions").rglob("*.jsonl"):
        path.unlink()
    code = cli.main(["sessions"])
    assert code == 0
    assert "見つかりません" in capsys.readouterr().out


def test_help_exits_zero():
    with pytest.raises(SystemExit) as excinfo:
        cli.main(["--help"])
    assert excinfo.value.code == 0


def test_sessions_lists_both_providers_and_filters(env, capsys):
    write_claude(env)
    code = cli.main(["sessions"])
    out = capsys.readouterr().out
    assert code == 0
    assert "Claude Code" in out
    assert "Codex" in out
    assert CLAUDE_ID in out
    assert "claude-jsonl" in out
    assert USER_TEXT not in out
    assert SECRET_BODY not in out

    code = cli.main(["--source", "claude", "sessions"])
    out = capsys.readouterr().out
    assert code == 0
    assert CLAUDE_ID in out
    assert "paginated" not in out

    code = cli.main(["--source", "codex", "sessions"])
    out = capsys.readouterr().out
    assert code == 0
    assert "paginated" in out
    assert CLAUDE_ID not in out


def test_inspect_claude_without_body(env, capsys):
    write_claude(env)
    code = cli.main(["--source", "claude", "inspect", "--session", CLAUDE_ID])
    out = capsys.readouterr().out
    assert code == 0
    assert "Claude Code" in out
    assert "形式: claude-jsonl" in out
    assert "version: 2.1.285" in out
    assert "レコード数: 3" in out
    assert "抽出数: 2" in out
    assert "無視数: 1" in out
    assert USER_TEXT not in out
    assert SECRET_BODY not in out
    assert not (env / "cache" / "transpane" / "translations.sqlite3").exists()


def test_prefixed_ids_and_source_conflicts(env, capsys):
    write_claude(env)
    code = cli.main(["inspect", "--session", "codex:t"])
    assert code == 0
    assert "形式: paginated" in capsys.readouterr().out

    code = cli.main(["inspect", "--session", f"claude:{CLAUDE_ID}"])
    assert code == 0
    assert "形式: claude-jsonl" in capsys.readouterr().out

    code = cli.main(["--source", "codex", "inspect", "--session", f"claude:{CLAUDE_ID}"])
    captured = capsys.readouterr()
    assert code == 2
    assert "矛盾" in captured.err

    code = cli.main(["--source", "claude", "inspect", "--session", "codex:t"])
    assert code == 2

    code = cli.main(["inspect", "--session", "missing"])
    assert code == 2


def test_ambiguous_raw_id_requires_provider_prefix(env, capsys):
    session_dir = env / "codex" / "sessions" / "2026" / "11" / "25"
    path = session_dir / f"rollout-{CLAUDE_ID}.jsonl"
    record = meta(CLAUDE_ID)
    with path.open("wb") as handle:
        handle.write(json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n")
    write_claude(env)

    code = cli.main(["inspect", "--session", CLAUDE_ID])
    assert code == 2
    assert "重複" in capsys.readouterr().err

    code = cli.main(["--source", "claude", "inspect", "--session", CLAUDE_ID])
    assert code == 0


def test_doctor_source_root_rules(env, capsys, monkeypatch):
    async def fake_models(config):
        return ["plamo-3-translate-2b"]

    monkeypatch.setattr(cli, "_list_models", fake_models)

    code = cli.main(["--source", "claude", "doctor"])
    out = capsys.readouterr().out
    assert code == 1
    assert "[NG]" in out

    write_claude(env)
    code = cli.main(["--source", "claude", "doctor"])
    assert code == 0
    assert "[OK] 読み取り可能" in capsys.readouterr().out

    monkeypatch.setenv("CODEX_HOME", str(env / "missing-codex"))
    code = cli.main(["--source", "codex", "doctor"])
    assert code == 1

    code = cli.main(["doctor"])
    assert code == 0

    monkeypatch.setenv("CLAUDE_CONFIG_DIR", str(env / "missing-claude"))
    code = cli.main(["doctor"])
    assert code == 1


def test_doctor_reports_unconfirmed_claude_transcripts(env, capsys, monkeypatch):
    async def fake_models(config):
        return ["plamo-3-translate-2b"]

    monkeypatch.setattr(cli, "_list_models", fake_models)
    write_claude(env)
    other = "22222222-2222-4222-8222-222222222222"
    path = env / "claude" / "projects" / "demo" / f"{other}.jsonl"
    path.write_bytes(
        b"".join(
            json.dumps(record, ensure_ascii=False).encode("utf-8") + b"\n"
            for record in claude_records()
        )
    )
    code = cli.main(["--source", "claude", "doctor"])
    out = capsys.readouterr().out
    assert code == 0
    assert "ID確認済み 1 件" in out
    assert "ID未確認 1 件" in out


def test_source_must_precede_subcommand(env):
    with pytest.raises(SystemExit) as excinfo:
        cli.main(["sessions", "--source", "claude"])
    assert excinfo.value.code == 2


def test_doctor_runs_cache_diagnostics_when_translation_fails(env, capsys, monkeypatch):
    async def fake_models(config):
        raise TranslationFailure(ErrorKind.CONNECT)

    monkeypatch.setattr(cli, "_list_models", fake_models)
    code = cli.main(["doctor"])
    out = capsys.readouterr().out
    assert code == 1
    assert "接続失敗" in out
    assert "キャッシュ:" in out
