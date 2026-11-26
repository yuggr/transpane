"""Textual presentation layer and the session/translation orchestration."""

from __future__ import annotations

import asyncio
import re
from contextlib import suppress
from dataclasses import dataclass, field
from datetime import datetime
from typing import Protocol

from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Vertical, VerticalScroll
from textual.css.query import NoMatches
from textual.screen import ModalScreen
from textual.widgets import Markdown, OptionList, Static
from textual.widgets.option_list import Option

from .cache import TranslationCache
from .config import (
    HISTORY_PAGE_SIZE,
    INITIAL_MESSAGE_COUNT,
    POLL_INTERVAL_SECONDS,
    SESSION_LIST_INTERVAL_SECONDS,
    Config,
)
from .markdown import ProtectedDoc, protect_document
from .models import (
    AgentProvider,
    Message,
    MessageKind,
    SessionInfo,
    TranslationState,
    format_timestamp,
)
from .session import (
    IndexEntry,
    PollResult,
    ScanResult,
    SessionError,
    SessionIndexMismatch,
    SessionReader,
    SessionReadError,
    discover_all_sessions,
    reader_for_session,
    sessions_for_cwd,
)
from .translation import (
    SchedulerStatus,
    TaskRef,
    TranslationClient,
    TranslationScheduler,
)

_OSC_RE = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")
_ANSI_RE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
_CONTROL_RE = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]")


def sanitize_for_display(text: str) -> str:
    text = _OSC_RE.sub("", text)
    text = _ANSI_RE.sub("", text)
    text = text.replace("\x1b", "")
    return _CONTROL_RE.sub("", text)


def initial_state(doc: ProtectedDoc) -> TranslationState:
    if doc.translatable_units:
        return TranslationState.ORIGINAL
    if doc.has_conservative_exclusion:
        return TranslationState.PARTIAL
    return TranslationState.SKIPPED


@dataclass
class MessageView:
    message: Message
    doc: ProtectedDoc
    state: TranslationState
    translations: dict[int, str] = field(default_factory=dict)
    pending: set[int] = field(default_factory=set)
    failed: set[int] = field(default_factory=set)
    error_labels: set[str] = field(default_factory=set)

    def display_text(self) -> str:
        if self.translations:
            return self.doc.render(self.translations)
        return self.message.original

    def refresh_state(self) -> None:
        if self.pending:
            self.state = (
                TranslationState.TRANSLATING if self.translations else TranslationState.QUEUED
            )
            return
        translatable = self.doc.translatable_units
        if not translatable:
            self.state = initial_state(self.doc)
            return
        succeeded = sum(1 for unit in translatable if unit.unit_id in self.translations)
        if succeeded == len(translatable):
            self.state = (
                TranslationState.PARTIAL
                if self.doc.has_conservative_exclusion
                else TranslationState.TRANSLATED
            )
        elif succeeded:
            self.state = TranslationState.PARTIAL
        elif self.failed:
            self.state = TranslationState.FAILED
        elif self.doc.has_conservative_exclusion:
            self.state = TranslationState.PARTIAL
        else:
            self.state = TranslationState.ORIGINAL


@dataclass(frozen=True, slots=True)
class ViewerStatus:
    server: str
    pending: int
    last_update: str
    note: str | None = None
    error: str | None = None
    cache_note: str | None = None
    session: str | None = None


class ViewerListener(Protocol):
    def on_reset(self, session: SessionInfo | None, views: list[MessageView]) -> None: ...

    def on_added(self, view: MessageView) -> None: ...

    def on_prepended(self, views: list[MessageView]) -> None: ...

    def on_updated(self, view: MessageView) -> None: ...

    def on_status(self, status: ViewerStatus) -> None: ...


@dataclass(slots=True)
class SnapshotOutcome:
    messages: list[Message] | None = None
    failure: str | None = None
    unsupported_mode: str | None = None
    read_error: SessionReadError | None = None
    extracted: int = 0
    scan: ScanResult | None = None


def _scan_failure(scan: ScanResult) -> SnapshotOutcome | None:
    """Return the outcome for a scan-level failure, if the scan reported one."""
    if scan.unsupported_mode is not None:
        return SnapshotOutcome(failure="unsupported", unsupported_mode=scan.unsupported_mode)
    if scan.thread_changed is not None:
        return SnapshotOutcome(failure="thread_changed")
    return None


class Monitor:
    """Owns the reader, message store and translation scheduler for one session."""

    def __init__(
        self,
        config: Config,
        listener: ViewerListener,
        transport=None,
    ) -> None:
        self._config = config
        self._listener = listener
        self._session: SessionInfo | None = None
        self._reader: SessionReader | None = None
        self._generation = 0
        self._entries: list[IndexEntry] = []
        self._entry_positions: dict[str, int] = {}
        self._visible = 0
        self._views: dict[tuple[str, str], MessageView] = {}
        self._order: list[tuple[str, str]] = []
        self._poll_task: asyncio.Task | None = None
        self._worker_task: asyncio.Task | None = None
        self._session_label: str | None = None
        self._stopped = False

        self._cache = TranslationCache(config.cache_path)
        self._cache_status = self._cache.open()
        self._client = TranslationClient(
            config.base_url, config.model, config.form, transport=transport
        )
        self._scheduler_status = SchedulerStatus(paused=False, pending=0, error=None)
        self._scheduler = TranslationScheduler(
            self._client,
            self._cache,
            config.base_url,
            config.model,
            config.form,
            self._on_unit_done,
            self._on_scheduler_status,
        )
        self._note: str | None = None
        self._last_update = datetime.now().strftime("%H:%M:%S")

    @property
    def session(self) -> SessionInfo | None:
        return self._session

    @property
    def views(self) -> list[MessageView]:
        return [self._views[key] for key in self._order]

    async def start(self, session: SessionInfo | None) -> None:
        self._ensure_worker()
        if session is not None:
            await self.switch(session)

    def _ensure_worker(self) -> None:
        if self._worker_task is None or self._worker_task.done():
            self._worker_task = asyncio.create_task(self._scheduler.run())

    async def switch(self, session: SessionInfo) -> None:
        self._ensure_worker()
        old_generation = self._generation
        self._generation += 1
        generation = self._generation
        self._scheduler.cancel_generation(old_generation)
        if self._poll_task is not None:
            self._poll_task.cancel()
            self._poll_task = None

        self._session = session
        self._session_label = f"{session.provider.label} / {session.display_id}"
        self._stopped = False
        self._views.clear()
        self._order.clear()
        self._entries = []
        self._entry_positions = {}
        self._visible = 0
        self._note = None
        self._last_update = datetime.now().strftime("%H:%M:%S")

        reader = reader_for_session(
            session, self._config.sessions_root, self._config.claude_projects_root
        )
        self._reader = reader
        outcome = await self._scan_snapshot(generation)
        if outcome is None:
            return
        if outcome.failure == "scan_error":
            self._stop("ログを読み取れません（許可範囲外の可能性）", reset=True)
            return
        if outcome.failure == "unsupported":
            self._stop(
                f"未対応の履歴形式: {outcome.unsupported_mode}（本文は送信しません）",
                reset=True,
            )
            return
        if outcome.failure == "thread_changed":
            self._stop(
                "ログのセッションIDが変更されました（自動切替しません）",
                reset=True,
            )
            return
        if generation != self._generation:
            return
        if outcome.extracted == 0:
            self._note = "対象イベント未検出（ログ形式または内部イベントのみ）"
        outcome = await self._read_snapshot_messages(outcome, generation)
        if outcome is None:
            return
        if outcome.failure == "index_mismatch":
            await self._rebuild(generation, note="索引不一致のため再構築しました")
            return
        if outcome.failure == "read_error":
            error = outcome.read_error
            assert error is not None
            self._stop(self._read_error_note(error), reset=True)
            return

        self._replace_views(session, outcome.messages or [])
        self._emit_status()
        self._poll_task = asyncio.create_task(self._poll_loop(generation))

    async def _scan_reader(
        self, reader: SessionReader, generation: int
    ) -> tuple[ScanResult | None, SnapshotOutcome | None]:
        """Scan ``reader`` for ``generation``; return a failure outcome when it fails."""
        try:
            scan = await asyncio.to_thread(reader.scan_all)
        except (OSError, SessionError):
            if generation != self._generation:
                return None, None
            return None, SnapshotOutcome(failure="scan_error")
        if generation != self._generation:
            return None, None
        return scan, None

    async def _scan_snapshot(self, generation: int) -> SnapshotOutcome | None:
        reader = self._reader
        if reader is None:
            return None
        scan, failure = await self._scan_reader(reader, generation)
        if scan is None:
            return failure
        failure = _scan_failure(scan)
        if failure is not None:
            return failure
        outcome = SnapshotOutcome(extracted=scan.stats.extracted, scan=scan)
        return outcome

    async def _read_snapshot_messages(
        self, outcome: SnapshotOutcome, generation: int
    ) -> SnapshotOutcome | None:
        reader = self._reader
        if reader is None or outcome.scan is None:
            return None
        selected = self._set_scan_entries(outcome.scan)
        try:
            messages = await asyncio.to_thread(reader.read_messages, selected) if selected else []
        except SessionIndexMismatch:
            outcome.failure = "index_mismatch"
            return outcome
        except SessionReadError as exc:
            outcome.failure = "read_error"
            outcome.read_error = exc
            return outcome
        if generation != self._generation:
            return None
        outcome.messages = messages
        return outcome

    async def add_history(self) -> None:
        if self._stopped:
            return
        self._ensure_worker()
        if self._session is None or self._reader is None:
            return
        if self._visible >= len(self._entries):
            return
        generation = self._generation
        end = len(self._entries) - self._visible
        start = max(0, end - HISTORY_PAGE_SIZE)
        selected = self._entries[start:end]
        messages = await self._read_history_page(selected, generation)
        if messages is None:
            return
        if generation != self._generation or self._stopped:
            return
        self._visible += len(selected)
        self._prepend_views(messages)
        self._emit_status()

    async def _read_history_page(
        self, selected: list[IndexEntry], generation: int
    ) -> list[Message] | None:
        if self._reader is None:
            return None
        try:
            return await asyncio.to_thread(self._reader.read_messages, selected)
        except SessionIndexMismatch:
            if generation == self._generation:
                await self._rebuild(generation, note="索引不一致のため再構築しました")
            return None
        except SessionReadError as exc:
            if generation != self._generation:
                return None
            if exc.reason in ("missing", "read"):
                self._note = (
                    "ログ取得停止（ファイルが見つかりません）"
                    if exc.reason == "missing"
                    else "ログを読み取れません"
                )
                self._emit_status()
                return None
            self._stop(self._read_error_note(exc))
            return None

    def _prepend_views(self, messages: list[Message]) -> list[MessageView]:
        added: list[MessageView] = []
        for message in messages:
            key = (message.thread_id, message.message_id)
            if key in self._views:
                continue
            view = self._make_view(message)
            self._views[key] = view
            added.append(view)
        if not added:
            return added
        self._order = [
            (view.message.thread_id, view.message.message_id) for view in added
        ] + self._order
        self._listener.on_prepended(added)
        for view in added:
            self._schedule(view, priority=2)
        return added

    async def retry(self) -> None:
        if self._stopped:
            return
        self._ensure_worker()
        if self._session is None:
            return
        generation = self._generation
        if self._scheduler.paused:
            if not await self._scheduler.reconnect():
                self._note = "翻訳サーバーへの再接続に失敗しました"
                self._emit_status()
                return
            if generation != self._generation or self._stopped:
                return
            self._note = None
        for view in list(self._views.values()):
            if view.message.kind is MessageKind.USER:
                continue
            self._retry_view(view)
        self._emit_status()

    def _retry_view(self, view: MessageView) -> None:
        failed_units = [unit for unit in view.doc.translatable_units if unit.unit_id in view.failed]
        if not failed_units:
            return
        for unit in failed_units:
            view.failed.discard(unit.unit_id)
            view.pending.add(unit.unit_id)
            self._submit_unit(view, unit, priority=0)
        view.error_labels.clear()
        view.refresh_state()
        self._listener.on_updated(view)

    async def close(self) -> None:
        self._generation += 1
        if self._poll_task is not None:
            self._poll_task.cancel()
            self._poll_task = None
        if self._worker_task is not None:
            self._worker_task.cancel()
            self._worker_task = None
        await self._scheduler.close()

    async def _poll_loop(self, generation: int) -> None:
        while generation == self._generation:
            await asyncio.sleep(POLL_INTERVAL_SECONDS)
            if generation != self._generation or self._reader is None:
                return
            try:
                result = await asyncio.to_thread(self._reader.poll)
            except OSError:
                self._note = "ログを読み取れません"
                self._emit_status()
                continue
            if not await self._handle_poll_result(result, generation):
                return

    async def _handle_poll_result(self, result: PollResult, generation: int) -> bool:
        if generation != self._generation:
            return False
        if result.denied:
            self._stop("監視停止（許可範囲外のパスです）")
            return False
        if result.missing:
            self._note = "ログ取得停止（ファイルが見つかりません）"
            self._emit_status()
            return True
        if result.rebuilt:
            await self._rebuild(generation)
            return False
        if result.thread_changed is not None:
            self._stop("ログのセッションIDが変更されました（自動切替しません）")
            return False
        if result.messages:
            if self._note is not None:
                self._note = None
            await self._ingest(result.messages, result.entries, generation)
        elif self._note is not None:
            self._note = None
        self._emit_status()
        return True

    async def _rebuild(
        self, generation: int, note: str = "ログが置換/縮小されたため再構築しました"
    ) -> None:
        if self._session is None or self._reader is None:
            return
        if generation != self._generation:
            return
        old_generation = self._generation
        self._generation += 1
        generation = self._generation
        self._stopped = True
        self._scheduler.cancel_generation(old_generation)
        self._clear_pending()
        outcome = await self._scan_snapshot(generation)
        if outcome is None:
            return
        if outcome.failure == "scan_error":
            self._stop("ログを読み取れません（許可範囲外の可能性）", reset=True)
            return
        if outcome.failure == "unsupported":
            self._stop(
                f"未対応の履歴形式: {outcome.unsupported_mode}（本文は送信しません）",
                reset=True,
            )
            return
        if outcome.failure == "thread_changed":
            self._stop(
                "ログのセッションIDが変更されました（自動切替しません）",
                reset=True,
            )
            return
        if generation != self._generation:
            return
        self._note = note
        if outcome.extracted == 0:
            self._note += " / 対象イベント未検出"
        outcome = await self._read_snapshot_messages(outcome, generation)
        if outcome is None:
            return
        if outcome.failure == "index_mismatch":
            self._stop(
                "索引とログの内容が一致しません（監視停止）",
                reset=True,
            )
            return
        if outcome.failure == "read_error":
            error = outcome.read_error
            assert error is not None
            self._stop(self._read_error_note(error), reset=True)
            return
        self._stopped = False
        self._replace_views(self._session, outcome.messages or [])
        self._emit_status()
        self._poll_task = asyncio.create_task(self._poll_loop(generation))

    async def _ingest(
        self, messages: list[Message], entries: list[IndexEntry], generation: int
    ) -> None:
        if self._stopped:
            return
        for message, entry in zip(messages, entries, strict=True):
            if generation != self._generation:
                return
            key = (message.thread_id, message.message_id)
            view = self._views.get(key)
            if view is not None:
                self._update_view(view, message, entry)
                continue
            if message.message_id in self._entry_positions:
                self._store_entry(entry)
                continue
            self._append_view(message, entry)

    def _update_view(self, view: MessageView, message: Message, entry: IndexEntry) -> None:
        if view.message.revision == message.revision:
            return
        view.message = message
        view.doc = protect_document(message.original)
        view.translations.clear()
        view.failed.clear()
        view.pending.clear()
        view.error_labels.clear()
        view.state = (
            TranslationState.SKIPPED
            if message.kind is MessageKind.USER
            else initial_state(view.doc)
        )
        self._store_entry(entry)
        self._listener.on_updated(view)
        self._schedule(view, priority=0)

    def _append_view(self, message: Message, entry: IndexEntry) -> None:
        key = (message.thread_id, message.message_id)
        view = self._make_view(message)
        self._views[key] = view
        self._order.append(key)
        self._store_entry(entry)
        self._visible += 1
        self._listener.on_added(view)
        self._schedule(view, priority=0)

    def _store_entry(self, entry: IndexEntry) -> None:
        position = self._entry_positions.get(entry.message_id)
        if position is None:
            self._entry_positions[entry.message_id] = len(self._entries)
            self._entries.append(entry)
            return
        self._entries[position] = entry

    def _set_scan_entries(self, scan: ScanResult) -> list[IndexEntry]:
        self._entries = list(scan.entries)
        self._entry_positions = {
            entry.message_id: index for index, entry in enumerate(self._entries)
        }
        self._visible = min(INITIAL_MESSAGE_COUNT, len(self._entries))
        return self._entries[len(self._entries) - self._visible :]

    def _replace_views(self, session: SessionInfo, messages: list[Message]) -> None:
        self._views.clear()
        self._order.clear()
        views: list[MessageView] = []
        for message in messages:
            view = self._make_view(message)
            self._views[(message.thread_id, message.message_id)] = view
            self._order.append((message.thread_id, message.message_id))
            views.append(view)
        self._listener.on_reset(session, views)
        for view in reversed(views):
            self._schedule(view, priority=1)

    @staticmethod
    def _read_error_note(exc: SessionReadError) -> str:
        if exc.reason == "denied":
            return "監視停止（許可範囲外のパスです）"
        if exc.reason == "halted":
            return "ログのセッションIDが変更されました（自動切替しません）"
        if exc.reason == "missing":
            return "ログを読み取れません（ファイルが見つかりません）"
        return "ログを読み取れません"

    def _stop(self, note: str, *, reset: bool = False) -> None:
        self._stopped = True
        self._generation += 1
        self._scheduler.cancel_generation(self._generation)
        self._clear_pending()
        if reset:
            self._views.clear()
            self._order.clear()
            self._entries = []
            self._entry_positions = {}
            self._visible = 0
            if self._session is not None:
                self._listener.on_reset(self._session, [])
        self._note = note
        self._emit_status()

    def _clear_pending(self) -> None:
        for view in self._views.values():
            if not view.pending:
                continue
            view.pending.clear()
            view.refresh_state()
            self._listener.on_updated(view)

    def _make_view(self, message: Message) -> MessageView:
        doc = protect_document(message.original)
        state = TranslationState.SKIPPED if message.kind is MessageKind.USER else initial_state(doc)
        return MessageView(message=message, doc=doc, state=state)

    def _schedule(self, view: MessageView, priority: int) -> None:
        if self._stopped or view.message.kind is MessageKind.USER:
            return
        units = view.doc.translatable_units
        if not units:
            return
        view.state = TranslationState.QUEUED
        for unit in units:
            view.pending.add(unit.unit_id)
            self._submit_unit(view, unit, priority)

    def _submit_unit(self, view: MessageView, unit, priority: int) -> None:
        if self._stopped:
            return
        ref = TaskRef(
            thread_id=view.message.thread_id,
            message_id=view.message.message_id,
            revision=view.message.revision,
            generation=self._generation,
            unit_id=unit.unit_id,
        )
        self._scheduler.submit(ref, unit.segments, priority)
        self._listener.on_updated(view)

    def _on_unit_done(
        self, ref: TaskRef, ok: bool, translated: str | None, error: str | None
    ) -> None:
        view = self._result_view(ref)
        if view is None:
            return
        view.pending.discard(ref.unit_id)
        if ok and translated is not None:
            view.translations[ref.unit_id] = translated
        else:
            view.failed.add(ref.unit_id)
            if error:
                view.error_labels.add(error)
        view.refresh_state()
        self._last_update = datetime.now().strftime("%H:%M:%S")
        self._listener.on_updated(view)
        self._emit_status()

    def _result_view(self, ref: TaskRef) -> MessageView | None:
        if self._stopped or ref.generation != self._generation:
            return None
        if self._session is None or ref.thread_id != self._session.thread_id:
            return None
        view = self._views.get((ref.thread_id, ref.message_id))
        if view is None or view.message.revision != ref.revision:
            return None
        unit_ids = {unit.unit_id for unit in view.doc.translatable_units}
        if ref.unit_id not in unit_ids:
            return None
        return view

    def _on_scheduler_status(self, status: SchedulerStatus) -> None:
        self._scheduler_status = status
        self._emit_status()

    def _emit_status(self) -> None:
        self._listener.on_status(
            ViewerStatus(
                server=self._scheduler_status.connection.label,
                pending=self._scheduler_status.pending,
                last_update=self._last_update,
                note=self._note,
                error=self._scheduler_status.error,
                cache_note=(
                    None
                    if self._cache_status.available
                    else f"{self._cache_status.message}（再起動後は再翻訳が必要です）"
                ),
                session=self._session_label,
            )
        )


class MessageScroll(VerticalScroll):
    def watch_scroll_y(self, old_value: float, new_value: float) -> None:
        super().watch_scroll_y(old_value, new_value)
        with suppress(Exception):
            self.app._on_message_scroll(self)


class SessionScreen(ModalScreen[SessionInfo | None]):
    BINDINGS = [Binding("escape", "cancel", "閉じる")]

    def __init__(self, sessions: list[SessionInfo]) -> None:
        super().__init__()
        self._sessions = sessions

    def compose(self) -> ComposeResult:
        with Vertical(id="session-dialog"):
            yield Static(
                "セッションを選択（取得元 / ID / cwd / 最終更新 / 形式）", id="session-title"
            )
            options = []
            for index, session in enumerate(self._sessions):
                label = (
                    f"{session.provider.label}  {session.thread_id}  {session.cwd or 'cwd不明'}  "
                    f"{datetime.fromtimestamp(session.updated_at).strftime('%Y-%m-%d %H:%M:%S')}  "
                    f"{session.mode_label}"
                )
                if session.origin_label:
                    label += f"  {session.origin_label}"
                options.append(Option(label, id=str(index)))
            yield OptionList(*options, id="session-list")

    def on_mount(self) -> None:
        self.query_one("#session-list", OptionList).focus()

    def on_option_list_option_selected(self, event: OptionList.OptionSelected) -> None:
        event.stop()
        self.dismiss(self._sessions[int(event.option.id)])

    def action_cancel(self) -> None:
        self.dismiss(None)


class MessageWidget(Vertical):
    def __init__(self, view: MessageView, original_mode: bool = False) -> None:
        super().__init__()
        self.view = view
        self._original_mode = original_mode

    def compose(self) -> ComposeResult:
        yield Static(self._header(), classes="msg-header")
        yield Markdown(sanitize_for_display(self._body()), classes="msg-body", open_links=False)

    def set_original_mode(self, original_mode: bool) -> None:
        self._original_mode = original_mode
        self.refresh_view()

    def refresh_view(self) -> None:
        with suppress(NoMatches):
            self.query_one(".msg-header", Static).update(self._header())
            self.query_one(".msg-body", Markdown).update(sanitize_for_display(self._body()))

    def _body(self) -> str:
        if self._original_mode:
            return self.view.message.original
        return self.view.display_text()

    def _header(self) -> str:
        parts = [format_timestamp(self.view.message.timestamp), self.view.message.kind.label]
        if self._original_mode:
            parts.append("[原文]")
        elif self.view.state is not TranslationState.SKIPPED:
            label = self.view.state.label
            if label:
                parts.append(f"[{label}]")
        if self.view.error_labels:
            parts.append(f"({'/'.join(sorted(self.view.error_labels))})")
        return "  ".join(parts)


class TranspaneApp(App[None]):
    CSS = """
    Screen { layout: vertical; }
    #status { height: 1; background: $panel; padding: 0 1; }
    #messages { height: 1fr; padding: 0 1; }
    #footer { height: 1; background: $panel; padding: 0 1; }
    MessageWidget { height: auto; margin-bottom: 1; }
    .msg-header { color: $accent; text-style: bold; }
    #session-dialog { width: 90%; height: 70%; background: $surface; padding: 1 2; }
    #session-title { height: 2; }
    #session-list { height: 1fr; }
    """

    BINDINGS = [
        Binding("s", "sessions", "セッション", priority=True),
        Binding("o", "toggle_original", "原文/翻訳", priority=True),
        Binding("r", "retry", "再試行", priority=True),
        Binding("h", "history", "履歴追加", priority=True),
        Binding("end", "latest", "最新", priority=True),
        Binding("q", "quit", "終了", priority=True),
    ]

    def __init__(
        self,
        config: Config,
        initial_session: SessionInfo | None = None,
        cwd_filter: str | None = None,
        transport=None,
        source: AgentProvider | None = None,
    ) -> None:
        super().__init__()
        self._config = config
        self._initial_session = initial_session
        self._cwd_filter = cwd_filter
        self._transport = transport
        self._source = source
        self._monitor: Monitor | None = None
        self._widgets: dict[tuple[str, str], MessageWidget] = {}
        self._picker_running = False
        self._original_mode = False
        self._auto_scroll = True
        self._sessions: list[SessionInfo] = []

    def compose(self) -> ComposeResult:
        yield Static("transpane", id="status")
        yield MessageScroll(id="messages")
        yield Static(
            "s: セッション  o: 原文/翻訳  r: 再試行  h: 履歴追加  End: 最新  q: 終了",
            id="footer",
        )

    def on_mount(self) -> None:
        self._monitor = Monitor(self._config, self, transport=self._transport)
        if self._initial_session is not None:
            self.run_worker(self._start_session(self._initial_session), exclusive=False)
        else:
            self.run_worker(self._pick_initial_session(), exclusive=False)
        self.set_interval(SESSION_LIST_INTERVAL_SECONDS, self._refresh_session_list)

    async def on_unmount(self) -> None:
        if self._monitor is not None:
            await self._monitor.close()

    async def _start_session(self, session: SessionInfo) -> None:
        assert self._monitor is not None
        await self._monitor.switch(session)

    async def _pick_initial_session(self) -> None:
        sessions = await asyncio.to_thread(
            discover_all_sessions,
            self._config.sessions_root,
            self._config.claude_projects_root,
            self._source,
        )
        self._sessions = self._filter_sessions(sessions)
        if not self._sessions:
            self._set_status_text("セッションが見つかりません。s で再検索できます。")
            return
        await self._open_picker()

    async def _open_picker(self) -> None:
        if self._picker_running or self._monitor is None:
            return
        self._picker_running = True
        try:
            picked = await self.push_screen_wait(SessionScreen(self._sessions))
        finally:
            self._picker_running = False
        if picked is not None:
            await self._monitor.switch(picked)

    def _filter_sessions(self, sessions: list[SessionInfo]) -> list[SessionInfo]:
        if self._cwd_filter is None:
            return sessions
        return sessions_for_cwd(sessions, self._cwd_filter)

    async def _refresh_session_list(self) -> None:
        if self._monitor is not None and self._monitor.session is not None:
            return
        if isinstance(self.screen, SessionScreen) or self._picker_running:
            return
        self.run_worker(self._discover_and_pick(), exclusive=False)

    async def _discover_and_pick(self) -> None:
        sessions = await asyncio.to_thread(
            discover_all_sessions,
            self._config.sessions_root,
            self._config.claude_projects_root,
            self._source,
        )
        self._sessions = self._filter_sessions(sessions)
        if not self._sessions:
            self._set_status_text("セッションが見つかりません。s で再検索できます。")
            return
        await self._open_picker()

    def action_sessions(self) -> None:
        self.run_worker(self._discover_and_pick(), exclusive=False)

    async def action_toggle_original(self) -> None:
        self._original_mode = not self._original_mode
        for widget in self._widgets.values():
            widget.set_original_mode(self._original_mode)

    async def action_retry(self) -> None:
        if self._monitor is not None:
            await self._monitor.retry()

    async def action_history(self) -> None:
        if self._monitor is not None:
            await self._monitor.add_history()

    def action_latest(self) -> None:
        self._auto_scroll = True
        self._scroll_end()

    def _on_message_scroll(self, container: MessageScroll) -> None:
        self._auto_scroll = container.is_vertical_scroll_end

    def on_reset(self, session: SessionInfo | None, views: list[MessageView]) -> None:
        if self._monitor is not None and session is not None:
            self.sub_title = (
                f"{session.provider.label} / {session.thread_id} / {session.cwd or 'cwd不明'}"
            )
        container = self.query_one("#messages", MessageScroll)
        for widget in list(self._widgets.values()):
            widget.remove()
        self._widgets.clear()
        for view in views:
            self._mount_message(view, container)
        if self._auto_scroll:
            self.call_after_refresh(self._scroll_end)

    def on_added(self, view: MessageView) -> None:
        container = self.query_one("#messages", MessageScroll)
        self._mount_message(view, container)
        if self._auto_scroll:
            self.call_after_refresh(self._scroll_end)

    def on_prepended(self, views: list[MessageView]) -> None:
        container = self.query_one("#messages", MessageScroll)
        for view in reversed(views):
            self._mount_message(view, container, before=0 if container.children else None)

    def on_updated(self, view: MessageView) -> None:
        key = (view.message.thread_id, view.message.message_id)
        widget = self._widgets.get(key)
        if widget is not None:
            widget.refresh_view()
            return
        self.on_added(view)

    def on_status(self, status: ViewerStatus) -> None:
        text = f"{status.session} | " if status.session else ""
        text += f"PLaMo: {status.server}"
        if status.error:
            text += f" ({status.error})"
        text += f" | 待ち: {status.pending} | 最終更新: {status.last_update}"
        if status.note:
            text += f" | {status.note}"
        if status.cache_note:
            text += f" | キャッシュ: {status.cache_note}"
        self._set_status_text(text)

    def _mount_message(
        self,
        view: MessageView,
        container: MessageScroll,
        before: int | None = None,
    ) -> None:
        widget = MessageWidget(view, original_mode=self._original_mode)
        self._widgets[(view.message.thread_id, view.message.message_id)] = widget
        if before is None:
            container.mount(widget)
        else:
            container.mount(widget, before=before)

    def _scroll_end(self) -> None:
        with suppress(Exception):
            self.query_one("#messages", MessageScroll).scroll_end(animate=False)

    def _set_status_text(self, text: str) -> None:
        with suppress(Exception):
            self.query_one("#status", Static).update(text)
