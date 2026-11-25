from __future__ import annotations

import pytest

from transpane.config import ConfigError, load_config, validate_base_url


def test_load_config_reads_all_values(tmp_path):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text(
        '[translation]\nbase_url = "http://192.168.1.9:1234/v1"\n'
        'model = "plamo-3-translate-2b"\nform = "plain"\n',
        encoding="utf-8",
    )
    env = {
        "CODEX_HOME": str(tmp_path / "codex"),
        "CLAUDE_CONFIG_DIR": str(tmp_path / "claude"),
        "XDG_CONFIG_HOME": str(tmp_path / "config"),
        "XDG_CACHE_HOME": str(tmp_path / "cache"),
    }
    config = load_config(env)
    assert config.base_url == "http://192.168.1.9:1234/v1"
    assert config.model == "plamo-3-translate-2b"
    assert config.form == "plain"
    assert config.sessions_root == tmp_path / "codex" / "sessions"
    assert config.claude_home == tmp_path / "claude"
    assert config.claude_projects_root == tmp_path / "claude" / "projects"
    assert config.cache_path == tmp_path / "cache" / "transpane" / "translations.sqlite3"


def test_claude_config_dir_falls_back_when_empty(tmp_path, monkeypatch):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text('[translation]\nmodel = "m"\n', encoding="utf-8")
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    env = {
        "CLAUDE_CONFIG_DIR": "",
        "XDG_CONFIG_HOME": str(tmp_path / "config"),
    }
    config = load_config(env)
    assert config.claude_home == tmp_path / "home" / ".claude"
    assert config.claude_projects_root == tmp_path / "home" / ".claude" / "projects"


def test_missing_model_is_an_error(tmp_path):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text("[translation]\nform = 'polite'\n", encoding="utf-8")
    env = {"XDG_CONFIG_HOME": str(tmp_path / "config")}
    with pytest.raises(ConfigError, match="model"):
        load_config(env)


def test_unknown_keys_are_errors(tmp_path):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    path = config_dir / "config.toml"
    path.write_text('[translation]\nmodel = "m"\nworker_count = 4\n', encoding="utf-8")
    with pytest.raises(ConfigError, match="不明な設定項目"):
        load_config({"XDG_CONFIG_HOME": str(tmp_path / "config")})

    path.write_text('model = "m"\n', encoding="utf-8")
    with pytest.raises(ConfigError, match="不明な設定項目"):
        load_config({"XDG_CONFIG_HOME": str(tmp_path / "config")})


def test_invalid_form_is_rejected(tmp_path):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text(
        '[translation]\nmodel = "m"\nform = "casual"\n', encoding="utf-8"
    )
    with pytest.raises(ConfigError, match="form"):
        load_config({"XDG_CONFIG_HOME": str(tmp_path / "config")})


def test_broken_toml_is_reported(tmp_path):
    config_dir = tmp_path / "config" / "transpane"
    config_dir.mkdir(parents=True)
    (config_dir / "config.toml").write_text("[translation\n", encoding="utf-8")
    with pytest.raises(ConfigError):
        load_config({"XDG_CONFIG_HOME": str(tmp_path / "config")})


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1:1234/v1",
        "http://127.0.0.1/v1",
        "http://[::1]:8080/v1",
        "http://10.1.2.3:1234/v1",
        "http://172.16.0.9:1234/v1",
        "http://192.168.0.20:1234/v1",
        "http://127.0.0.1:1234/v1/",
    ],
)
def test_valid_urls_are_normalized(url):
    normalized = validate_base_url(url)
    assert normalized.endswith("/v1")
    assert normalized.startswith("http://")


@pytest.mark.parametrize(
    "url",
    [
        "https://127.0.0.1:1234/v1",
        "http://localhost:1234/v1",
        "http://8.8.8.8/v1",
        "http://172.32.0.1/v1",
        "http://192.169.0.1/v1",
        "http://user:pass@127.0.0.1:1234/v1",
        "http://127.0.0.1:1234/v1?x=1",
        "http://127.0.0.1:1234/v1#frag",
        "http://127.0.0.1:1234/v2",
        "http://127.0.0.1:1234/v1/extra",
        "http://[fe80::1]:1234/v1",
        "ftp://127.0.0.1/v1",
        "",
    ],
)
def test_rejected_urls(url):
    with pytest.raises(ConfigError):
        validate_base_url(url)
