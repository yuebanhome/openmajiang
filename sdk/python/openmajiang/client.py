from __future__ import annotations

import asyncio
import copy
import inspect
import json
import random
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Awaitable, Callable

Frame = dict[str, Any]


@dataclass(frozen=True)
class StrategyContext:
    observation: Frame
    decision: Frame
    cancelled: asyncio.Event
    deadline: float  # local monotonic time, including safety margin


Strategy = Callable[[StrategyContext], str | Awaitable[str]]


def _identity(frame: Frame) -> tuple:
    return tuple(frame.get(k) for k in ("match_id", "hand_id", "participant_id", "decision_id"))


def _timestamp(value: str) -> float:
    return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


class DecisionEngine:
    """Receive synchronously so a slow strategy cannot block control messages.

    Strategies must cooperate with asyncio cancellation. Late results are fenced
    even if a strategy catches CancelledError and continues running.
    """

    def __init__(self, strategy: Strategy, send: Callable[[Frame], None], *, auto_flower: bool = True,
                 deadline_margin: float = 0.05, on_error: Callable[[str], None] | None = None,
                 now: Callable[[], float] = time.monotonic):
        self.strategy, self.send = strategy, send
        self.auto_flower, self.margin, self.on_error, self.now = auto_flower, deadline_margin, on_error, now
        self.snapshot: Frame | None = None
        self.current: Frame | None = None
        self.stream, self.seq, self.generation = "", 0, 0
        self.task: asyncio.Task | None = None
        self.cancelled: asyncio.Event | None = None
        self.deadline = 0.0
        self.pending: dict[str, Frame] = {}
        self.submitted: set[tuple] = set()

    def receive(self, frame: Frame) -> None:
        kind = frame.get("type")
        if kind == "command_ack":
            old = self.pending.pop(frame.get("command_id"), None)
            if old:
                self.submitted.add(_identity(old))
            return
        if kind == "command_error":
            old = self.pending.pop(frame.get("command_id"), None)
            code = frame.get("error", {}).get("code", "COMMAND_ERROR")
            if old and code == "STALE_CONTROL" and self.current and _identity(old) == _identity(self.current):
                self._decide(self.current)
            self._error(code)
            return
        if kind == "heartbeat":
            self.send({"type": "heartbeat"})
            return
        if kind == "control_changed":
            self.disconnect()
            self._error("STALE_CONTROL")
            return
        if kind in ("snapshot", "room_snapshot"):
            self._cancel()
            self.current = None
            if not isinstance(frame.get("view_seq"), int) or frame["view_seq"] < 1 or not isinstance(frame.get("stream_id"), str):
                self._resync()
                return
            self.snapshot = copy.deepcopy(frame)
            self.stream, self.seq = frame["stream_id"], frame["view_seq"]
            if frame.get("recorded", {}).get("type") == "command_ack":
                self.receive(frame["recorded"])
            self.retry_pending()
            if kind == "room_snapshot" and frame.get("room", {}).get("status") == "waiting":
                self.send({"type": "ready"})
            if len(self.submitted) > 4096:
                self.submitted.clear()
            return
        if kind != "decision_request":
            return
        snapshot = self.snapshot
        ref = frame.get("observation_ref", {})
        if (snapshot is None or frame.get("stream_id") != self.stream or frame.get("view_seq") != self.seq + 1
                or ref.get("stream_id") != snapshot.get("stream_id") or ref.get("view_seq") != snapshot.get("view_seq")
                or any(frame.get(k) != snapshot.get(k) for k in ("match_id", "hand_id", "participant_id", "seat_id", "seat_assignment_version", "control_epoch"))):
            self._resync()
            return
        self.seq = frame["view_seq"]
        if not isinstance(frame.get("legal_actions"), list) or not frame["legal_actions"]:
            self._resync()
            return
        self.current = copy.deepcopy(frame)
        try:
            remaining = _timestamp(frame["deadline_at"]) - _timestamp(frame["server_time"]) - self.margin
        except (KeyError, TypeError, ValueError):
            self._resync()
            return
        self.deadline = self.now() + remaining
        if remaining <= 0 or _identity(frame) in self.submitted:
            return
        prior = next((p for p in self.pending.values() if _identity(p) == _identity(frame)), None)
        if prior:
            self.send(copy.deepcopy(prior))
            return
        self._decide(self.current)

    def _decide(self, decision: Frame) -> None:
        self._cancel()
        if self.snapshot is None or self.now() >= self.deadline or _identity(decision) in self.submitted:
            return
        generation = self.generation
        cancelled = asyncio.Event()
        self.cancelled = cancelled
        observation = copy.deepcopy(self.snapshot)

        def finish(option_id: str) -> None:
            if cancelled.is_set() or generation != self.generation or self.now() >= self.deadline:
                return
            if not any(o["option_id"] == option_id for o in decision["legal_actions"]):
                self._error("INVALID_STRATEGY_OPTION")
                return
            action = {k: decision[k] for k in ("match_id", "hand_id", "participant_id", "seat_id", "seat_assignment_version", "control_epoch", "decision_id", "window_id")}
            action.update(type="submit_action", protocol_version="1.0", command_id=str(uuid.uuid4()), option_id=option_id)
            # Do not cancel the currently executing task from inside itself.
            self.generation += 1
            cancelled.set()
            self.pending[action["command_id"]] = action
            self.send(copy.deepcopy(action))

        options = decision["legal_actions"]
        if len(options) == 1 and options[0]["type"] == "pass":
            finish(options[0]["option_id"])
            return
        flower = next((o for o in options if o["type"] == "replace_flower"), None)
        if self.auto_flower and flower:
            finish(flower["option_id"])
            return

        async def compute() -> None:
            try:
                async with asyncio.timeout(max(0, self.deadline - self.now())):
                    result = self.strategy(StrategyContext(observation, copy.deepcopy(decision), cancelled, self.deadline))
                    if inspect.isawaitable(result):
                        result = await result
                    finish(result)
            except (asyncio.CancelledError, TimeoutError):
                cancelled.set()
            except Exception:
                if not cancelled.is_set():
                    self._error("STRATEGY_FAILED")
        self.task = asyncio.create_task(compute())

    def _cancel(self) -> None:
        self.generation += 1
        if self.cancelled:
            self.cancelled.set()
        if self.task:
            self.task.cancel()
        self.task = None

    def _error(self, code: str) -> None:
        if self.on_error:
            self.on_error(code)

    def _resync(self) -> None:
        self.disconnect()
        self.send({"type": "resume"})

    def retry_pending(self) -> None:
        if self.snapshot is None:
            return
        for command_id, action in list(self.pending.items()):
            if action["match_id"] == self.snapshot.get("match_id") and action["hand_id"] == self.snapshot.get("hand_id"):
                self.send(copy.deepcopy(action))
            else:
                self.pending.pop(command_id, None)

    def disconnect(self) -> None:
        self._cancel()
        self.snapshot, self.current = None, None
        self.stream, self.seq = "", 0


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, "redirect rejected", headers, fp)


class BotClient:
    def __init__(self, base_url: str, api_key: str, strategy: Strategy, *, rulesets: list[dict] | None = None,
                 queue: Frame | None = None, auto_flower: bool = True, on_state: Callable[[str], None] | None = None):
        parsed = urllib.parse.urlsplit(base_url)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password:
            raise ValueError("invalid base URL")
        if parsed.scheme == "http" and parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
            raise ValueError("remote Bot connections require HTTPS")
        self.base_url, self.api_key, self.queue, self.on_state = base_url, api_key, queue, on_state
        self.rulesets = rulesets or [{"id": "openmajiang.mcr", "version": "1.0.0"}]
        self.session, self.expires = "", 0.0
        self.outgoing: asyncio.Queue[Frame] = asyncio.Queue(maxsize=64)
        self.engine = DecisionEngine(strategy, self._send, auto_flower=auto_flower)

    def _send(self, frame: Frame) -> None:
        try:
            self.outgoing.put_nowait(frame)
        except asyncio.QueueFull:
            self.engine.disconnect()
            raise RuntimeError("outgoing queue overflow")

    async def _request(self, path: str, method: str = "GET", body: Any = None, *, long_key: bool = False) -> Any:
        token = self.api_key if long_key else self.session
        def request() -> Any:
            req = urllib.request.Request(urllib.parse.urljoin(self.base_url, path), method=method,
                headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
                data=None if body is None else json.dumps(body).encode())
            try:
                with urllib.request.build_opener(_NoRedirect()).open(req, timeout=15) as response:
                    return json.load(response)
            except urllib.error.HTTPError as exc:
                if exc.code == 401:
                    self.session = ""
                raise RuntimeError(f"HTTP_{exc.code}") from None
        return await asyncio.to_thread(request)

    async def _authenticate(self) -> None:
        if self.session and time.time() < self.expires - 60:
            return
        data = await self._request("/v1/bot-sessions", "POST", {"protocol_version": "1.0", "rulesets": self.rulesets}, long_key=True)
        self.session, self.expires = data["session_token"], _timestamp(data["expires_at"])

    async def leave_queue(self) -> None:
        await self._request("/v1/bot/queue", "DELETE", {})

    async def run(self, stop: asyncio.Event) -> None:
        attempt, queued = 0, False
        try:
            while not stop.is_set():
                try:
                    await self._authenticate()
                    active = await self._request("/v1/bot/active-match")
                    if not active.get("room"):
                        if self.queue and not queued:
                            await self._request("/v1/bot/queue", "POST", self.queue)
                            queued = True
                        await _sleep(1, stop)
                        continue
                    queued = False
                    await self._connect(active["room"]["id"], stop)
                    attempt = 0
                    if self.queue and not self.queue.get("continuous"):
                        return
                except asyncio.CancelledError:
                    raise
                except Exception:
                    if stop.is_set():
                        return
                    if self.on_state:
                        self.on_state("reconnecting")
                    attempt += 1
                    await _sleep(min(30, 0.5 * 2 ** min(attempt, 6)) * random.uniform(0.75, 1.25), stop)
        finally:
            self.engine.disconnect()

    async def _connect(self, room_id: str, stop: asyncio.Event) -> None:
        from websockets.asyncio.client import connect
        url = urllib.parse.urlsplit(urllib.parse.urljoin(self.base_url, "/v1/ws/bots"))
        target = urllib.parse.urlunsplit(("wss" if url.scheme == "https" else "ws", url.netloc, url.path, urllib.parse.urlencode({"room_id": room_id}), ""))
        # No outgoing messages may cross a disconnected snapshot barrier.
        while not self.outgoing.empty():
            self.outgoing.get_nowait()
        async with connect(target, additional_headers={"Authorization": "Bearer " + self.session},
                           max_size=1024 * 1024, open_timeout=10, proxy=None) as socket:
            if self.on_state:
                self.on_state("connected")
            completed = False

            async def sender():
                while True:
                    await socket.send(json.dumps(await self.outgoing.get()))

            async def retry():
                while True:
                    await asyncio.sleep(0.5)
                    self.engine.retry_pending()

            async def closer():
                await _sleep(max(0, self.expires - time.time() - 30), stop)
                await socket.close()

            tasks = [asyncio.create_task(sender()), asyncio.create_task(retry()), asyncio.create_task(closer())]
            try:
                async for raw in socket:
                    frame = json.loads(raw)
                    self.engine.receive(frame)
                    if frame.get("type") == "snapshot" and frame.get("status") in ("completed", "aborted", "early_ended"):
                        completed = True
                        await socket.close()
                        break
            finally:
                for task in tasks:
                    task.cancel()
                await asyncio.gather(*tasks, return_exceptions=True)
                self.engine.disconnect()
            if not completed and not stop.is_set():
                raise RuntimeError("CONNECTION_LOST")


async def _sleep(seconds: float, stop: asyncio.Event) -> None:
    try:
        await asyncio.wait_for(stop.wait(), timeout=seconds)
    except TimeoutError:
        pass


def first_legal(context: StrategyContext) -> str:
    """Connectivity baseline, not a strong Mahjong policy."""
    options = context.decision["legal_actions"]
    choice = next((o for o in options if o["type"] == "hu"), None)
    choice = choice or next((o for o in options if o["type"] == "discard"), options[0])
    return choice["option_id"]
