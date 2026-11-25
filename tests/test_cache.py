from __future__ import annotations

import os
import sqlite3
import stat

import pytest

from codex_ja.cache import TranslationCache, cache_key


def test_cache_roundtrip_and_permissions(tmp_path):
    path = tmp_path / "private" / "translations.sqlite3"
    cache = TranslationCache(path)
    status = cache.open()
    assert status.available, status.message
    assert stat.S_IMODE(path.parent.stat().st_mode) == 0o700
    assert stat.S_IMODE(path.stat().st_mode) == 0o600

    cache.put("key", "訳 @@CJ0@@", "2026-11-25T00:00:00+00:00")
    assert cache.get("key") == "訳 @@CJ0@@"
    assert cache.get("missing") is None

    reopened = TranslationCache(path)
    assert reopened.open().available
    assert reopened.get("key") == "訳 @@CJ0@@"


def test_cache_key_changes_with_every_contract_value():
    base = cache_key("http://127.0.0.1:1234/v1", "m", "polite", "hello")
    assert base == cache_key("http://127.0.0.1:1234/v1", "m", "polite", "hello")
    assert base != cache_key("http://127.0.0.1:1234/v1", "other", "polite", "hello")
    assert base != cache_key("http://127.0.0.1:1234/v1", "m", "plain", "hello")
    assert base != cache_key("http://127.0.0.1:1234/v1", "m", "polite", "hello!")
    assert base != cache_key("http://127.0.0.1:9999/v1", "m", "polite", "hello")


def test_wide_directory_permissions_disable_cache_without_chmod(tmp_path):
    directory = tmp_path / "wide"
    directory.mkdir(mode=0o755)
    os.chmod(directory, 0o755)
    cache = TranslationCache(directory / "translations.sqlite3")
    status = cache.open()
    assert not status.available
    assert "権限" in (status.message or "")
    assert stat.S_IMODE(directory.stat().st_mode) == 0o755
    assert cache.get("x") is None
    cache.put("x", "y", "now")


def test_wide_file_permissions_disable_cache(tmp_path):
    directory = tmp_path / "cache"
    directory.mkdir(mode=0o700)
    path = directory / "db.sqlite3"
    path.write_bytes(b"")
    os.chmod(path, 0o644)
    cache = TranslationCache(path)
    status = cache.open()
    assert not status.available
    assert stat.S_IMODE(path.stat().st_mode) == 0o644


def test_corrupt_database_is_disabled_and_preserved(tmp_path):
    directory = tmp_path / "cache"
    directory.mkdir(mode=0o700)
    path = directory / "db.sqlite3"
    path.write_bytes(b"this is not sqlite")
    os.chmod(path, 0o600)

    cache = TranslationCache(path)
    status = cache.open()
    assert not status.available
    assert path.read_bytes() == b"this is not sqlite"


def test_locked_database_is_disabled(tmp_path):
    directory = tmp_path / "cache"
    directory.mkdir(mode=0o700)
    path = directory / "db.sqlite3"
    cache = TranslationCache(path)
    assert cache.open().available

    locker = sqlite3.connect(path, timeout=0.1)
    try:
        locker.execute("BEGIN EXCLUSIVE")
        assert cache.get("x") is None
        assert cache.status().available is False
    finally:
        locker.rollback()
        locker.close()


@pytest.mark.parametrize("key", ["", "a" * 10, "日本語"])
def test_cache_handles_unusual_keys(tmp_path, key):
    cache = TranslationCache(tmp_path / "c" / "db.sqlite3")
    assert cache.open().available
    cache.put(key, "value", "now")
    assert cache.get(key) == "value"
