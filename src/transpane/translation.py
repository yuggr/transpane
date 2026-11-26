"""PLaMo HTTP client and the single-worker translation scheduler."""

from __future__ import annotations

import asyncio
import heapq
from collections import deque
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any

import httpx

from .cache import TranslationCache, cache_key
from .config import (
    CONNECT_TIMEOUT_SECONDS,
    MAX_PENDING_JOBS,
    READ_TIMEOUT_SECONDS,
    SOURCE_LANG,
    TARGET_LANG,
    TOTAL_TIMEOUT_SECONDS,
)
from .markdown import Segment, validate_segment_translation


class ErrorKind(StrEnum):
    CONNECT = "connect"
    TIMEOUT = "timeout"
    HTTP = "http"
    INVALID = "invalid"
    EMPTY = "empty"
    INCOMPLETE = "incomplete"
    CANCELLED = "cancelled"

    @property
    def label(self) -> str:
        labels = {
            ErrorKind.CONNECT: "接続失敗",
            ErrorKind.TIMEOUT: "タイムアウト",
            ErrorKind.HTTP: "HTTPエラー",
            ErrorKind.INVALID: "応答形式不正",
            ErrorKind.EMPTY: "空応答",
            ErrorKind.INCOMPLETE: "応答未完了",
            ErrorKind.CANCELLED: "取消",
        }
        return labels[self]


class TranslationFailure(Exception):
    def __init__(self, kind: ErrorKind, status: int | None = None) -> None:
        super().__init__(kind.value)
        self.kind = kind
        self.status = status

    @property
    def server_unavailable(self) -> bool:
        if self.kind in (ErrorKind.CONNECT, ErrorKind.TIMEOUT):
            return True
        return self.kind is ErrorKind.HTTP and self.status is not None and self.status >= 500


class TranslationClient:
    def __init__(
        self,
        base_url: str,
        model: str,
        form: str,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._form = form
        timeout = httpx.Timeout(
            TOTAL_TIMEOUT_SECONDS,
            connect=CONNECT_TIMEOUT_SECONDS,
            read=READ_TIMEOUT_SECONDS,
        )
        self._http = httpx.AsyncClient(
            timeout=timeout,
            trust_env=False,
            follow_redirects=False,
            transport=transport,
        )

    def _system_prompt(self) -> str:
        return (
            "[plamo-translate]\n"
            f"source_lang={SOURCE_LANG}\n"
            f"target_lang={TARGET_LANG}\n"
            f"form={self._form}"
        )

    async def list_models(self) -> list[str]:
        try:
            response = await self._http.get(f"{self._base_url}/models")
        except httpx.TimeoutException as exc:
            raise TranslationFailure(ErrorKind.TIMEOUT) from exc
        except httpx.HTTPError as exc:
            raise TranslationFailure(ErrorKind.CONNECT) from exc
        if not 200 <= response.status_code < 300:
            raise TranslationFailure(ErrorKind.HTTP, response.status_code)
        try:
            payload = response.json()
        except ValueError as exc:
            raise TranslationFailure(ErrorKind.INVALID) from exc
        data = payload.get("data") if isinstance(payload, dict) else None
        if not isinstance(data, list):
            raise TranslationFailure(ErrorKind.INVALID)
        models = [
            entry["id"]
            for entry in data
            if isinstance(entry, dict) and isinstance(entry.get("id"), str)
        ]
        return models

    async def translate(self, text: str) -> str:
        payload = {
            "model": self._model,
            "messages": [
                {"role": "system", "content": self._system_prompt()},
                {"role": "user", "content": text},
            ],
            "stream": False,
        }
        response = await self._post_completion(payload)
        if not 200 <= response.status_code < 300:
            raise TranslationFailure(ErrorKind.HTTP, response.status_code)
        try:
            body = response.json()
        except ValueError as exc:
            raise TranslationFailure(ErrorKind.INVALID) from exc
        return self._parse_completion(body)

    async def _post_completion(self, payload: dict) -> httpx.Response:
        try:
            return await self._http.post(f"{self._base_url}/chat/completions", json=payload)
        except httpx.TimeoutException as exc:
            raise TranslationFailure(ErrorKind.TIMEOUT) from exc
        except httpx.HTTPError as exc:
            raise TranslationFailure(ErrorKind.CONNECT) from exc

    def _parse_completion(self, body: Any) -> str:
        if not isinstance(body, dict):
            raise TranslationFailure(ErrorKind.INVALID)
        choices = body.get("choices")
        if not isinstance(choices, list) or not choices or not isinstance(choices[0], dict):
            raise TranslationFailure(ErrorKind.INVALID)
        choice = choices[0]
        if choice.get("finish_reason") != "stop":
            raise TranslationFailure(ErrorKind.INCOMPLETE)
        message = choice.get("message")
        if not isinstance(message, dict):
            raise TranslationFailure(ErrorKind.INVALID)
        content = message.get("content")
        if not isinstance(content, str):
            raise TranslationFailure(ErrorKind.INVALID)
        if not content.strip():
            raise TranslationFailure(ErrorKind.EMPTY)
        return content.strip()

    async def aclose(self) -> None:
        await self._http.aclose()


@dataclass(frozen=True, slots=True)
class TaskRef:
    thread_id: str
    message_id: str
    revision: str
    generation: int
    unit_id: int


class ConnectionState(StrEnum):
    UNKNOWN = "unknown"
    CONNECTED = "connected"
    FAILED = "failed"

    @property
    def label(self) -> str:
        labels = {
            ConnectionState.UNKNOWN: "未確認",
            ConnectionState.CONNECTED: "接続成功",
            ConnectionState.FAILED: "通信失敗",
        }
        return labels[self]


@dataclass(frozen=True, slots=True)
class SchedulerStatus:
    paused: bool
    pending: int
    error: str | None
    connection: ConnectionState = ConnectionState.UNKNOWN


@dataclass(slots=True)
class _UnitTask:
    ref: TaskRef
    segments: tuple[Segment, ...]
    priority: int
    sequence: int
    next_index: int = 0
    results: list[tuple[bool, str | None, str | None]] = field(default_factory=list)


OnUnitDone = Callable[[TaskRef, bool, str | None, str | None], None]
OnStatus = Callable[[SchedulerStatus], None]


class TranslationScheduler:
    """One HTTP worker; bounded pending queue; no automatic retries."""

    def __init__(
        self,
        client: TranslationClient,
        cache: TranslationCache,
        base_url: str,
        model: str,
        form: str,
        on_unit_done: OnUnitDone,
        on_status: OnStatus,
        max_pending: int = MAX_PENDING_JOBS,
    ) -> None:
        self._client = client
        self._cache = cache
        self._base_url = base_url
        self._model = model
        self._form = form
        self._on_unit_done = on_unit_done
        self._on_status = on_status
        self._max_pending = max_pending

        self._queue: list[tuple[int, int, _UnitTask]] = []
        self._deferred: deque[_UnitTask] = deque()
        self._active = 0
        self._sequence = 0
        self._wake = asyncio.Event()
        self._closed = False
        self._paused = False
        self._connection = ConnectionState.UNKNOWN
        self._error: str | None = None
        self._cancelled_through = -1
        self._memo: dict[str, str] = {}

    def submit(
        self,
        ref: TaskRef,
        segments: Sequence[Segment],
        priority: int = 0,
    ) -> None:
        if not segments or ref.generation <= self._cancelled_through:
            return
        task = _UnitTask(
            ref=ref,
            segments=tuple(segments),
            priority=priority,
            sequence=self._next_sequence(),
        )
        if self._active < self._max_pending:
            heapq.heappush(self._queue, (priority, task.sequence, task))
            self._active += 1
            self._wake.set()
        else:
            self._deferred.append(task)
        self._emit_status()

    def cancel_generation(self, generation: int) -> None:
        """Drop every queued job from ``generation`` or older.

        The HTTP request already in flight is allowed to finish, but its
        remaining segments and delivery are suppressed.
        """
        if generation <= self._cancelled_through:
            return
        self._cancelled_through = generation
        kept = [item for item in self._queue if item[2].ref.generation > generation]
        self._active = max(0, self._active - (len(self._queue) - len(kept)))
        self._queue = kept
        heapq.heapify(self._queue)
        self._deferred = deque(task for task in self._deferred if task.ref.generation > generation)
        self._promote()
        self._emit_status()

    async def reconnect(self) -> bool:
        try:
            await self._client.list_models()
        except TranslationFailure as exc:
            if exc.server_unavailable:
                self._connection = ConnectionState.FAILED
            else:
                # The server answered, so the transport itself is reachable.
                self._connection = ConnectionState.CONNECTED
            self._error = exc.kind.label
            self._emit_status()
            return False
        self._paused = False
        self._connection = ConnectionState.CONNECTED
        self._error = None
        self._wake.set()
        self._emit_status()
        return True

    @property
    def paused(self) -> bool:
        return self._paused

    async def run(self) -> None:
        while not self._closed:
            task = await self._next_task_or_wait()
            if task is None:
                break
            await self._process_task(task)

    async def _next_task_or_wait(self) -> _UnitTask | None:
        while not self._closed:
            if self._paused:
                self._wake.clear()
                await self._wake.wait()
                continue
            task = self._pop_task()
            if task is not None:
                return task
            self._wake.clear()
            if self._queue or self._deferred:
                continue
            await self._wake.wait()
        return None

    async def _process_task(self, task: _UnitTask) -> None:
        if task.ref.generation <= self._cancelled_through:
            self._finish_task(task, cancelled=True)
            return

        ok, translated, error = await self._translate_segment(task)
        task.results.append((ok, translated, error))
        task.next_index += 1

        if task.ref.generation <= self._cancelled_through:
            task.results[-1] = (False, None, ErrorKind.CANCELLED.value)
            task.next_index = len(task.segments)
            self._deliver(task)
            return

        if task.next_index < len(task.segments):
            heapq.heappush(self._queue, (task.priority, task.sequence, task))
            self._wake.set()
            return

        self._deliver(task)

    async def close(self) -> None:
        self._closed = True
        self._wake.set()
        await self._client.aclose()
        self._cache.close()

    def _next_sequence(self) -> int:
        self._sequence += 1
        return self._sequence

    def _pop_task(self) -> _UnitTask | None:
        self._promote()
        if not self._queue:
            return None
        _, _, task = heapq.heappop(self._queue)
        return task

    def _promote(self) -> None:
        while self._deferred and self._active < self._max_pending:
            task = self._deferred.popleft()
            heapq.heappush(self._queue, (task.priority, task.sequence, task))
            self._active += 1
            self._wake.set()

    def _finish_task(self, task: _UnitTask, cancelled: bool = False) -> None:
        self._active = max(0, self._active - 1)
        if cancelled:
            self._promote()
            self._emit_status()

    def _deliver(self, task: _UnitTask) -> None:
        self._active = max(0, self._active - 1)
        self._promote()
        all_ok = bool(task.results) and all(ok for ok, _, _ in task.results)
        translated: str | None = None
        error: str | None = None
        if all_ok:
            pieces: list[str] = []
            for (_, text, _), segment in zip(task.results, task.segments, strict=True):
                pieces.append((text or "") + segment.trailing)
            translated = "".join(pieces)
        else:
            error = next((err for ok, _, err in task.results if not ok), "invalid")
        if task.ref.generation <= self._cancelled_through:
            translated = None
            error = ErrorKind.CANCELLED.value
        self._on_unit_done(task.ref, all_ok, translated, error)
        self._emit_status()

    async def _translate_segment(self, task: _UnitTask) -> tuple[bool, str | None, str | None]:
        segment = task.segments[task.next_index]
        key = cache_key(self._base_url, self._model, self._form, segment.text)

        memoized = self._memo.get(key)
        if memoized is not None and validate_segment_translation(memoized, segment.markers):
            return True, memoized, None

        cached = await asyncio.to_thread(self._cache.get, key)
        if cached is not None and validate_segment_translation(cached, segment.markers):
            self._memo[key] = cached
            return True, cached, None

        if task.ref.generation <= self._cancelled_through:
            return False, None, ErrorKind.CANCELLED.value

        try:
            translated = await self._client.translate(segment.text)
        except TranslationFailure as exc:
            if exc.server_unavailable:
                self._paused = True
                self._connection = ConnectionState.FAILED
                self._error = exc.kind.label
            else:
                # The server answered, so the transport itself is reachable.
                self._connection = ConnectionState.CONNECTED
            self._emit_status()
            return False, None, exc.kind.value

        self._connection = ConnectionState.CONNECTED
        if not validate_segment_translation(translated, segment.markers):
            return False, None, ErrorKind.INVALID.value

        self._memo[key] = translated
        created_at = datetime.now(UTC).isoformat()
        await asyncio.to_thread(self._cache.put, key, translated, created_at)
        return True, translated, None

    def _emit_status(self) -> None:
        pending = self._active + len(self._deferred)
        self._on_status(
            SchedulerStatus(
                paused=self._paused,
                pending=pending,
                error=self._error,
                connection=self._connection,
            )
        )
