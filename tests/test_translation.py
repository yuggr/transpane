from __future__ import annotations

import asyncio
import contextlib
import json
import threading

import httpx
import pytest

from transpane.cache import TranslationCache, cache_key
from transpane.markdown import Segment
from transpane.translation import (
    ConnectionState,
    ErrorKind,
    SchedulerStatus,
    TaskRef,
    TranslationClient,
    TranslationFailure,
    TranslationScheduler,
)

BASE = "http://127.0.0.1:1234/v1"


def ok_response(text: str, finish_reason: str = "stop") -> httpx.Response:
    return httpx.Response(
        200,
        json={
            "choices": [
                {"message": {"role": "assistant", "content": text}, "finish_reason": finish_reason}
            ]
        },
    )


def make_client(handler) -> TranslationClient:
    return TranslationClient(
        BASE, "plamo-3-translate-2b", "polite", transport=httpx.MockTransport(handler)
    )


def run(coro):
    return asyncio.run(coro)


def test_translate_request_shape_and_response():
    captured = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["body"] = json.loads(request.content)
        return ok_response("保存層にはアトミック書き込みが必要です。")

    async def scenario():
        client = make_client(handler)
        try:
            return await client.translate("The storage layer requires atomic writes.")
        finally:
            await client.aclose()

    translated = run(scenario())
    assert translated == "保存層にはアトミック書き込みが必要です。"
    assert captured["url"] == f"{BASE}/chat/completions"
    body = captured["body"]
    assert body["model"] == "plamo-3-translate-2b"
    assert body["stream"] is False
    assert body["messages"][0] == {
        "role": "system",
        "content": "[plamo-translate]\nsource_lang=en\ntarget_lang=ja\nform=polite",
    }
    assert body["messages"][1] == {
        "role": "user",
        "content": "The storage layer requires atomic writes.",
    }


@pytest.mark.parametrize(
    ("response", "kind"),
    [
        (httpx.Response(500, json={}), ErrorKind.HTTP),
        (httpx.Response(404, json={}), ErrorKind.HTTP),
        (httpx.Response(200, content=b"not json"), ErrorKind.INVALID),
        (httpx.Response(200, json={"choices": []}), ErrorKind.INVALID),
        (
            httpx.Response(
                200, json={"choices": [{"message": {"content": "  "}, "finish_reason": "stop"}]}
            ),
            ErrorKind.EMPTY,
        ),
        (ok_response("done", finish_reason="length"), ErrorKind.INCOMPLETE),
    ],
)
def test_translate_failures(response, kind):
    async def scenario():
        client = make_client(lambda request: response)
        try:
            await client.translate("Hello")
        finally:
            await client.aclose()

    with pytest.raises(TranslationFailure) as excinfo:
        run(scenario())
    assert excinfo.value.kind is kind


def test_timeout_and_connect_failures():
    def timeout_handler(request):
        raise httpx.ReadTimeout("slow", request=request)

    def connect_handler(request):
        raise httpx.ConnectError("no", request=request)

    cases = ((timeout_handler, ErrorKind.TIMEOUT), (connect_handler, ErrorKind.CONNECT))
    for handler, kind in cases:

        async def scenario(handler=handler):
            client = make_client(handler)
            try:
                await client.translate("Hello")
            finally:
                await client.aclose()

        with pytest.raises(TranslationFailure) as excinfo:
            run(scenario())
        assert excinfo.value.kind is kind
        assert excinfo.value.server_unavailable


def test_redirect_is_not_followed():
    calls = []

    def handler(request):
        calls.append(str(request.url))
        return httpx.Response(302, headers={"Location": "http://example.com/evil"})

    async def scenario():
        client = make_client(handler)
        try:
            await client.translate("Hello")
        finally:
            await client.aclose()

    with pytest.raises(TranslationFailure) as excinfo:
        run(scenario())
    assert excinfo.value.kind is ErrorKind.HTTP
    assert len(calls) == 1


def test_proxy_environment_is_ignored(monkeypatch):
    import threading
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            self.rfile.read(length)
            body = json.dumps(
                {
                    "choices": [
                        {
                            "message": {"content": "訳"},
                            "finish_reason": "stop",
                        }
                    ]
                }
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            return

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    monkeypatch.setenv("HTTP_PROXY", "http://127.0.0.1:9")
    monkeypatch.setenv("HTTPS_PROXY", "http://127.0.0.1:9")
    monkeypatch.setenv("ALL_PROXY", "http://127.0.0.1:9")
    try:
        port = server.server_address[1]

        async def scenario():
            client = TranslationClient(f"http://127.0.0.1:{port}/v1", "m", "polite")
            try:
                return await client.translate("Hello")
            finally:
                await client.aclose()

        assert run(scenario()) == "訳"
    finally:
        server.shutdown()
        server.server_close()


def test_list_models():
    def handler(request):
        assert str(request.url) == f"{BASE}/models"
        return httpx.Response(200, json={"data": [{"id": "plamo-3-translate-2b"}]})

    async def scenario():
        client = make_client(handler)
        try:
            return await client.list_models()
        finally:
            await client.aclose()

    assert run(scenario()) == ["plamo-3-translate-2b"]


def ref(message_id: str = "m1", generation: int = 1, thread: str = "t") -> TaskRef:
    return TaskRef(
        thread_id=thread,
        message_id=message_id,
        revision="r",
        generation=generation,
        unit_id=0,
    )


def segment(text: str = "Hello world.", trailing: str = "") -> Segment:
    return Segment(text=text, markers=(), trailing=trailing)


async def wait_until(predicate, timeout: float = 3.0) -> None:
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while loop.time() < deadline:
        if predicate():
            return
        await asyncio.sleep(0.01)
    raise AssertionError("condition not met in time")


def make_scheduler(handler, tmp_path, outcomes, statuses, **kwargs):
    client = make_client(handler)
    cache = TranslationCache(tmp_path / "cache" / "db.sqlite3")
    cache.open()
    scheduler = TranslationScheduler(
        client,
        cache,
        BASE,
        "plamo-3-translate-2b",
        "polite",
        on_unit_done=lambda ref, ok, text, err: outcomes.append((ref, ok, text, err)),
        on_status=statuses.append,
        **kwargs,
    )
    return scheduler, cache


async def stop(scheduler, task):
    task.cancel()
    with contextlib.suppress(asyncio.CancelledError):
        await task
    await scheduler.close()


def test_scheduler_success_and_status(tmp_path):
    outcomes: list = []
    statuses: list[SchedulerStatus] = []

    def handler(request):
        return ok_response("訳 " + json.loads(request.content)["messages"][1]["content"])

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, statuses)
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref(), [segment()])
        await wait_until(lambda: len(outcomes) == 1)
        await stop(scheduler, task)

    run(scenario())
    assert outcomes[0][0].message_id == "m1"
    assert outcomes[0][1] is True
    assert outcomes[0][2] == "訳 Hello world."
    assert outcomes[0][3] is None
    assert any(status.pending == 1 for status in statuses)


def test_scheduler_uses_cache_without_http(tmp_path):
    calls = []

    def handler(request):
        calls.append(1)
        return ok_response("訳 Hello world.")

    async def first_run():
        outcomes: list = []
        statuses: list = []
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, statuses)
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref(), [segment()])
        await wait_until(lambda: len(outcomes) == 1)
        await stop(scheduler, task)

    async def second_run():
        outcomes: list = []
        statuses: list = []
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, statuses)
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref(), [segment()])
        await wait_until(lambda: len(outcomes) == 1)
        await stop(scheduler, task)
        return outcomes

    run(first_run())
    calls_after_first = len(calls)
    outcomes = run(second_run())
    assert len(calls) == calls_after_first
    assert outcomes[0][2] == "訳 Hello world."


def test_scheduler_rejects_invalid_marker_output_and_does_not_cache(tmp_path):
    outcomes: list = []

    def handler(request):
        return ok_response("マーカーなし")

    seg = Segment(text="Hello @@CJ0@@", markers=("@@CJ0@@",), trailing="")

    async def scenario():
        scheduler, cache = make_scheduler(handler, tmp_path, outcomes, [])
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref(), [seg])
        await wait_until(lambda: len(outcomes) == 1)
        await stop(scheduler, task)
        key = cache_key(BASE, "plamo-3-translate-2b", "polite", "Hello @@CJ0@@")
        return cache.get(key)

    cached = run(scenario())
    assert outcomes[0][1] is False
    assert outcomes[0][3] == ErrorKind.INVALID.value
    assert cached is None


def test_scheduler_dedupes_identical_segments(tmp_path):
    calls = []
    outcomes: list = []

    def handler(request):
        calls.append(1)
        return ok_response("訳 Hello world.")

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, [])
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref("m1"), [segment()], priority=1)
        scheduler.submit(ref("m2"), [segment()], priority=1)
        await wait_until(lambda: len(outcomes) == 2)
        await stop(scheduler, task)

    run(scenario())
    assert len(calls) == 1
    assert all(outcome[1] for outcome in outcomes)


def test_scheduler_queue_cap_preserves_deferred_work(tmp_path):
    outcomes: list = []

    def handler(request):
        return ok_response("訳 " + json.loads(request.content)["messages"][1]["content"])

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, [], max_pending=3)
        task = asyncio.create_task(scheduler.run())
        for index in range(10):
            scheduler.submit(ref(f"m{index}"), [segment(f"Text {index}.")], priority=2)
        assert len(scheduler._queue) <= 3
        await wait_until(lambda: len(outcomes) == 10, timeout=5)
        await stop(scheduler, task)

    run(scenario())
    assert {outcome[0].message_id for outcome in outcomes} == {f"m{i}" for i in range(10)}


def test_scheduler_pauses_on_server_error_and_resumes(tmp_path):
    outcomes: list = []
    statuses: list[SchedulerStatus] = []
    fail = {"value": True}
    models_mode = {"value": "ok"}

    def handler(request):
        if request.method == "GET":
            if models_mode["value"] == "404":
                return httpx.Response(404, json={})
            if models_mode["value"] == "invalid":
                return httpx.Response(200, content=b"not json")
            if models_mode["value"] == "500":
                return httpx.Response(500, json={})
            return httpx.Response(200, json={"data": [{"id": "m"}]})
        if fail["value"]:
            return httpx.Response(503, json={})
        return ok_response("訳 Done.")

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, statuses)
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref("m1"), [segment("First.")])
        await wait_until(lambda: len(outcomes) == 1)
        assert scheduler.paused is True
        assert outcomes[0][1] is False
        assert statuses[-1].connection is ConnectionState.FAILED

        scheduler.submit(ref("m2"), [segment("Second.")])
        await asyncio.sleep(0.05)
        assert len(outcomes) == 1

        # A server response, even an error response, proves the transport works.
        models_mode["value"] = "404"
        assert await scheduler.reconnect() is False
        assert scheduler.paused is True
        assert statuses[-1].connection is ConnectionState.CONNECTED
        assert statuses[-1].error == "HTTPエラー"

        models_mode["value"] = "invalid"
        assert await scheduler.reconnect() is False
        assert scheduler.paused is True
        assert statuses[-1].connection is ConnectionState.CONNECTED
        assert statuses[-1].error == "応答形式不正"

        models_mode["value"] = "500"
        assert await scheduler.reconnect() is False
        assert scheduler.paused is True
        assert statuses[-1].connection is ConnectionState.FAILED
        assert statuses[-1].error == "HTTPエラー"

        models_mode["value"] = "ok"
        fail["value"] = False
        assert await scheduler.reconnect() is True
        assert scheduler.paused is False
        assert statuses[-1].connection is ConnectionState.CONNECTED
        assert statuses[-1].error is None
        await wait_until(lambda: len(outcomes) == 2)
        assert outcomes[1][1] is True
        await stop(scheduler, task)

    run(scenario())


def test_scheduler_cancels_old_generation_and_does_not_revive_it(tmp_path):
    outcomes: list = []
    calls = []

    def handler(request):
        calls.append(1)
        return ok_response("訳 Done.")

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, [])
        scheduler.submit(ref("m1", generation=1), [segment()])
        scheduler.cancel_generation(1)
        scheduler.submit(ref("m1", generation=1), [segment()])
        task = asyncio.create_task(scheduler.run())
        await asyncio.sleep(0.1)
        assert outcomes == []
        assert calls == []

        scheduler.submit(ref("m2", generation=2), [segment()])
        await wait_until(lambda: len(outcomes) == 1)
        assert calls == [1]
        assert outcomes[0][0].generation == 2
        await stop(scheduler, task)

    run(scenario())


def test_scheduler_cancel_during_cache_lookup_does_not_post(tmp_path):
    outcomes: list = []
    calls: list = []
    entered = threading.Event()
    release = threading.Event()

    def handler(request):
        calls.append(1)
        return ok_response("訳 Done.")

    def blocking_get(key):
        entered.set()
        release.wait(timeout=3)
        return None

    async def scenario():
        scheduler, cache = make_scheduler(handler, tmp_path, outcomes, [])
        cache.get = blocking_get
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref("m1", generation=1), [segment()])
        assert await asyncio.to_thread(entered.wait, 3)
        scheduler.cancel_generation(1)
        release.set()
        await wait_until(lambda: len(outcomes) == 1)
        await stop(scheduler, task)

    run(scenario())
    assert calls == []
    assert outcomes[0][2] is None
    assert outcomes[0][3] == ErrorKind.CANCELLED.value


def test_scheduler_stops_followup_segments_after_cancellation(tmp_path):
    outcomes: list = []
    sent: list[str] = []
    started = asyncio.Event()
    gate = asyncio.Event()

    async def handler(request):
        text = json.loads(request.content)["messages"][1]["content"]
        sent.append(text)
        if text == "First.":
            started.set()
            await gate.wait()
        return ok_response("訳 " + text)

    segments = [
        Segment(text="First.", markers=(), trailing=" "),
        Segment(text="Second.", markers=(), trailing=""),
    ]

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, [])
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref("m1", generation=1), segments)
        await asyncio.wait_for(started.wait(), timeout=3)
        scheduler.cancel_generation(1)
        gate.set()
        await wait_until(lambda: len(outcomes) == 1)
        await asyncio.sleep(0.05)
        assert sent == ["First."]
        assert outcomes[0][2] is None
        assert outcomes[0][3] == ErrorKind.CANCELLED.value
        await stop(scheduler, task)

    run(scenario())


def test_scheduler_partial_failure_marks_unit_failed(tmp_path):
    outcomes: list = []
    statuses: list[SchedulerStatus] = []

    def handler(request):
        text = json.loads(request.content)["messages"][1]["content"]
        if "bad" in text:
            return httpx.Response(400, json={})
        return ok_response("訳 " + text)

    segments = [
        Segment(text="Good.", markers=(), trailing=" "),
        Segment(text="bad.", markers=(), trailing=""),
    ]

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, statuses)
        task = asyncio.create_task(scheduler.run())
        scheduler.submit(ref(), segments)
        await wait_until(lambda: len(outcomes) == 1)
        await stop(scheduler, task)

    run(scenario())
    assert outcomes[0][1] is False
    assert outcomes[0][2] is None
    assert statuses[-1].connection is ConnectionState.CONNECTED
    assert statuses[-1].paused is False


def test_scheduler_never_exceeds_one_http_request(tmp_path):
    outcomes: list = []
    active = {"value": 0, "max": 0}

    async def handler(request):
        active["value"] += 1
        active["max"] = max(active["max"], active["value"])
        await asyncio.sleep(0.02)
        active["value"] -= 1
        return ok_response("訳")

    async def scenario():
        scheduler, _ = make_scheduler(handler, tmp_path, outcomes, [])
        task = asyncio.create_task(scheduler.run())
        for index in range(5):
            scheduler.submit(ref(f"m{index}"), [segment(f"Text {index}.")])
        await wait_until(lambda: len(outcomes) == 5, timeout=5)
        await stop(scheduler, task)

    run(scenario())
    assert active["max"] == 1
    assert len(outcomes) == 5
