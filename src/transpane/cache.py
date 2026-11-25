"""SQLite cache for successful, marker-validated segment translations."""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import stat
from dataclasses import dataclass
from pathlib import Path

from .config import (
    CACHE_SCHEMA_VERSION,
    SOURCE_LANG,
    TARGET_LANG,
    TRANSLATION_POLICY_VERSION,
)

_CREATE_TABLE = """
CREATE TABLE IF NOT EXISTS translations (
    cache_key TEXT PRIMARY KEY,
    translated TEXT NOT NULL,
    created_at TEXT NOT NULL
)
"""


class CacheError(Exception):
    pass


@dataclass(frozen=True, slots=True)
class CacheStatus:
    available: bool
    path: str
    message: str | None = None


def cache_key(base_url: str, model: str, form: str, protected_segment: str) -> str:
    material = json.dumps(
        [
            CACHE_SCHEMA_VERSION,
            TRANSLATION_POLICY_VERSION,
            base_url,
            model,
            SOURCE_LANG,
            TARGET_LANG,
            form,
            protected_segment,
        ],
        ensure_ascii=False,
    )
    return hashlib.sha256(material.encode("utf-8")).hexdigest()


class TranslationCache:
    """Translation cache with non-destructive failure handling.

    A corrupt, locked or wrongly-permissioned cache is disabled for the
    lifetime of the process and never deleted or rewritten.
    """

    def __init__(self, path: Path) -> None:
        self.path = path
        self._available = True
        self._message: str | None = None
        self._initialized = False

    def open(self) -> CacheStatus:
        if self._initialized:
            return self.status()
        self._initialized = True
        try:
            self._ensure()
        except (CacheError, OSError, sqlite3.Error) as exc:
            self._disable(self._classify(exc))
        return self.status()

    def status(self) -> CacheStatus:
        return CacheStatus(
            available=self._available,
            path=str(self.path),
            message=self._message,
        )

    def get(self, key: str) -> str | None:
        if not self._available:
            return None
        try:
            connection = sqlite3.connect(self.path, timeout=0.5)
            try:
                row = connection.execute(
                    "SELECT translated FROM translations WHERE cache_key = ?",
                    (key,),
                ).fetchone()
            finally:
                connection.close()
        except sqlite3.Error as exc:
            self._disable(self._classify(exc))
            return None
        if row is None or not isinstance(row[0], str):
            return None
        return row[0]

    def put(self, key: str, translated: str, created_at: str) -> None:
        if not self._available:
            return
        try:
            connection = sqlite3.connect(self.path, timeout=0.5)
            try:
                with connection:
                    connection.execute(
                        "INSERT OR REPLACE INTO translations "
                        "(cache_key, translated, created_at) VALUES (?, ?, ?)",
                        (key, translated, created_at),
                    )
            finally:
                connection.close()
        except sqlite3.Error as exc:
            self._disable(self._classify(exc))

    def close(self) -> None:
        return

    def _ensure(self) -> None:
        directory = self.path.parent
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        directory_mode = stat.S_IMODE(directory.stat().st_mode)
        if directory_mode & 0o077:
            raise CacheError("キャッシュディレクトリの権限が広すぎます")

        if self.path.exists():
            file_mode = stat.S_IMODE(self.path.stat().st_mode)
            if file_mode & 0o077:
                raise CacheError("キャッシュDBの権限が広すぎます")
        else:
            descriptor = os.open(self.path, os.O_CREAT | os.O_WRONLY, 0o600)
            os.close(descriptor)
            os.chmod(self.path, 0o600)

        connection = sqlite3.connect(self.path, timeout=0.5)
        try:
            with connection:
                connection.execute(_CREATE_TABLE)
        finally:
            connection.close()

    def _disable(self, message: str) -> None:
        if self._available:
            self._available = False
            self._message = message

    @staticmethod
    def _classify(exc: BaseException) -> str:
        if isinstance(exc, CacheError):
            return str(exc)
        if isinstance(exc, sqlite3.DatabaseError):
            text = str(exc).lower()
            if "locked" in text or "busy" in text:
                return "キャッシュDBがロックされています"
            return "キャッシュDBを読み書きできません（破損の可能性）"
        return "キャッシュを利用できません"
