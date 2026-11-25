"""Configuration resolution and the loopback-only URL constraint."""

from __future__ import annotations

import ipaddress
import os
import tomllib
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit

DEFAULT_BASE_URL = "http://127.0.0.1:1234/v1"
DEFAULT_FORM = "polite"
FORMS = ("polite", "plain")

SOURCE_LANG = "en"
TARGET_LANG = "ja"

CACHE_SCHEMA_VERSION = 1
TRANSLATION_POLICY_VERSION = 1

# Initial values from the design document; change only with measurements.
POLL_INTERVAL_SECONDS = 0.25
SESSION_LIST_INTERVAL_SECONDS = 2.0
CONNECT_TIMEOUT_SECONDS = 3.0
READ_TIMEOUT_SECONDS = 60.0
TOTAL_TIMEOUT_SECONDS = 90.0
MAX_SEGMENT_CHARS = 1000
INITIAL_MESSAGE_COUNT = 50
HISTORY_PAGE_SIZE = 50
MAX_PENDING_JOBS = 32
MAX_LINE_BYTES = 8 * 1024 * 1024
MAX_POLL_BYTES = 1024 * 1024
INITIAL_SCAN_CHUNK_BYTES = 1024 * 1024

_RFC1918 = (
    ipaddress.ip_network("10.0.0.0/8"),
    ipaddress.ip_network("172.16.0.0/12"),
    ipaddress.ip_network("192.168.0.0/16"),
)


class ConfigError(Exception):
    """A configuration value is missing or violates the documented contract."""


@dataclass(frozen=True, slots=True)
class Config:
    codex_home: Path
    sessions_root: Path
    base_url: str
    model: str
    form: str
    cache_path: Path
    config_path: Path | None


def validate_base_url(raw: str) -> str:
    """Validate and normalize the translation server URL.

    Only HTTP, IP literals, loopback or RFC1918 hosts, and the /v1 path are
    accepted. Host names, userinfo, query strings and fragments are rejected.
    """
    if not isinstance(raw, str) or not raw:
        raise ConfigError("translation.base_url が空です")
    try:
        parts = urlsplit(raw)
    except ValueError as exc:
        raise ConfigError("translation.base_url を解析できません") from exc

    if parts.scheme != "http":
        raise ConfigError("translation.base_url は http:// で始まる必要があります")
    if "@" in parts.netloc:
        raise ConfigError("translation.base_url に userinfo を含められません")
    if parts.query or parts.fragment:
        raise ConfigError("translation.base_url に query/fragment を含められません")
    path = parts.path.rstrip("/") or "/"
    if path != "/v1":
        raise ConfigError("translation.base_url のパスは /v1 に限定されます")

    host = parts.hostname
    if host is None:
        raise ConfigError("translation.base_url にホストがありません")
    try:
        address = ipaddress.ip_address(host)
    except ValueError as exc:
        raise ConfigError("translation.base_url は IP リテラルである必要があります") from exc

    if not _is_local_or_private(address):
        raise ConfigError("translation.base_url は loopback または RFC1918 のみ許可されます")

    try:
        port = parts.port or 80
    except ValueError as exc:
        raise ConfigError("translation.base_url のポートが不正です") from exc

    host_part = f"[{host}]" if address.version == 6 else host
    return f"http://{host_part}:{port}/v1"


def _is_local_or_private(address: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    if address.is_loopback:
        return True
    if address.version != 4:
        return False
    return any(address in network for network in _RFC1918)


def _load_toml(path: Path) -> dict:
    try:
        with path.open("rb") as handle:
            data = tomllib.load(handle)
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise ConfigError(f"設定ファイルを読み込めません: {path}") from exc
    if not isinstance(data, dict):
        raise ConfigError("設定ファイルの形式が不正です")
    return data


def load_config(env: Mapping[str, str] | None = None) -> Config:
    environment = os.environ if env is None else env

    codex_home = Path(environment.get("CODEX_HOME") or "~/.codex").expanduser()
    config_home = Path(environment.get("XDG_CONFIG_HOME") or "~/.config").expanduser()
    cache_home = Path(environment.get("XDG_CACHE_HOME") or "~/.cache").expanduser()

    config_path = config_home / "codex-ja" / "config.toml"
    data: dict = {}
    existing_path: Path | None = None
    if config_path.exists():
        data = _load_toml(config_path)
        existing_path = config_path

    unknown_top = set(data) - {"translation"}
    if unknown_top:
        raise ConfigError(f"不明な設定項目です: {', '.join(sorted(unknown_top))}")

    translation = data.get("translation", {})
    if not isinstance(translation, dict):
        raise ConfigError("[translation] はテーブルである必要があります")
    unknown = set(translation) - {"base_url", "model", "form"}
    if unknown:
        raise ConfigError(f"不明な設定項目です: {', '.join(sorted(unknown))}")

    for key, value in translation.items():
        if not isinstance(value, str):
            raise ConfigError(f"translation.{key} は文字列である必要があります")

    model = translation.get("model", "").strip()
    if not model:
        raise ConfigError("translation.model は必須です（/v1/models の実IDを設定してください）")

    form = translation.get("form", DEFAULT_FORM)
    if form not in FORMS:
        raise ConfigError("translation.form は polite または plain を指定してください")

    base_url = validate_base_url(translation.get("base_url", DEFAULT_BASE_URL))

    return Config(
        codex_home=codex_home,
        sessions_root=codex_home / "sessions",
        base_url=base_url,
        model=model,
        form=form,
        cache_path=cache_home / "codex-ja" / "translations.sqlite3",
        config_path=existing_path,
    )
