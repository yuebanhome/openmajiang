#!/usr/bin/env python3
"""Real TLS/public-API gate. Creates only disposable test accounts in its server.

No database writes, operator bypass, custom walls or shortened clocks. Mailpit
is the test inbox; every account follows register -> SMTP -> verify -> login.
Only counts completed hands with a consistent, zero-sum result in all 4 views.
"""
from __future__ import annotations

import argparse
import asyncio
from collections import Counter
import http.cookiejar
import json
import multiprocessing as mp
import os
from pathlib import Path
import queue
import re
import secrets
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "python"))
from openmajiang import BotClient, first_legal


class API:
    def __init__(self, base):
        self.base, self.csrf = base, ""
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, method="GET", body=None, bearer=None, expected=None):
        headers = {"Content-Type": "application/json", "Origin": self.base.rstrip("/")}
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        if bearer:
            headers["Authorization"] = "Bearer " + bearer
        req = urllib.request.Request(urllib.parse.urljoin(self.base, path), method=method, headers=headers,
            data=None if body is None else json.dumps(body).encode())
        try:
            with self.opener.open(req, timeout=20) as response:
                status, data = response.status, response.read()
        except urllib.error.HTTPError as exc:
            status, data = exc.code, exc.read()
        if expected is not None and status != expected:
            raise RuntimeError(f"{method} {path.split('?')[0]} expected {expected}, got {status}")
        if status >= 400 and expected is None:
            raise RuntimeError(f"{method} {path.split('?')[0]} returned {status}")
        return json.loads(data) if data else {}


def spectator_socket(base, anonymous, room_id):
    from websockets.sync.client import connect
    ticket = anonymous.request(f"/v1/public/rooms/{room_id}/spectator-ticket", "POST", {}, expected=200)["ticket"]
    url = urllib.parse.urlsplit(base)
    endpoint = urllib.parse.urlunsplit(("wss", url.netloc, "/v1/ws/spectators", urllib.parse.urlencode({"room_id": room_id}), ""))
    with connect(endpoint, open_timeout=10, max_size=1024*1024, proxy=None) as socket:
        socket.send(json.dumps({"type": "authenticate", "ticket": ticket}))
        frame = json.loads(socket.recv(timeout=15))
        if frame.get("type") != "spectator_snapshot":
            raise RuntimeError("anonymous spectator socket did not receive a public snapshot")
        assert_discard_only(frame)


def register_bot(base, mailpit, prefix, index):
    api, inbox = API(base), API(mailpit)
    email, password = f"{prefix}-{index}@example.test", secrets.token_urlsafe(32)
    api.request("/v1/auth/register", "POST", {"email": email, "password": password,
        "name": f"WSS gate {index}", "accept_terms": True}, expected=202)
    token = None
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline and token is None:
        messages = inbox.request("/api/v1/messages?limit=100").get("messages", [])
        for item in messages:
            if any(recipient.get("Address") == email for recipient in item.get("To", [])):
                message = inbox.request("/api/v1/message/" + urllib.parse.quote(item["ID"], safe=""))
                match = re.search(r"#token=([A-Za-z0-9_-]+)", message.get("Text", ""))
                if match:
                    token = match[1]
                    break
        if token is None:
            time.sleep(0.5)
    if token is None:
        raise RuntimeError("verification email did not arrive through SMTP")
    api.request("/v1/auth/verify-email", "POST", {"token": token}, expected=200)
    login = api.request("/v1/auth/login", "POST", {"email": email, "password": password}, expected=200)
    if not login["user"]["verified"]:
        raise RuntimeError("email verification did not authorize account")
    api.csrf = login["csrf_token"]
    bot = api.request("/v1/bots", "POST", {"name": f"WSS external {index}"}, expected=201)["bot"]
    credential = api.request(f"/v1/bots/{bot['id']}/credentials", "POST", {}, expected=201)
    return api, bot["id"], credential["credential_id"], credential["api_key"]


def bot_process(index, base, key, match_format, output, stop_process):
    if index == 3:
        return typescript_process(index, base, key, match_format, output, stop_process)
    async def run():
        stop = asyncio.Event()
        decisions, acks, errors, states = set(), set(), Counter(), Counter()
        injected = False
        last_emit = 0.0

        def emit_snapshot(frame):
            nonlocal injected, last_emit
            kind = frame.get("type")
            if kind == "decision_request":
                decisions.add((frame.get("match_id"), frame.get("decision_id")))
            if kind == "command_ack":
                acks.add(frame.get("command_id"))
            if kind != "snapshot":
                return
            if frame.get("recorded", {}).get("type") == "command_ack":
                acks.add(frame["recorded"].get("command_id"))
            view = frame.get("view", {})
            if not injected and index == 0 and len(view.get("discards", [])) >= 10 and view.get("wall_remaining", 0) > 30:
                injected = True
                asyncio.create_task(client.reconnect())
                output.put({"type": "injected_disconnect", "bot": index})
            if view.get("result") is not None or time.monotonic() - last_emit >= 10:
                last_emit = time.monotonic()
                result = view.get("result")
                output.put({"type": "snapshot", "bot": index, "match_id": frame.get("match_id"),
                    "room_id": frame.get("room", {}).get("id"), "hand_index": frame.get("hand_index"),
                    "status": frame.get("status"), "interrupted": frame.get("platform_interrupted", False),
                    "phase": view.get("phase"), "result": None if result is None else {
                        "method": result.get("method"), "winner_seat": result.get("winner_seat"),
                        "score_deltas": result.get("score_deltas")},
                    "decisions": len(decisions), "acks": len(acks), "errors": dict(errors),
                    "self_timeout_count": frame.get("self_timeout_count"),
                    "reaction_timeout_count": frame.get("reaction_timeout_count")})

        def state(value):
            states[value] += 1
            output.put({"type": "connection", "bot": index, "state": value, "count": states[value]})

        client = BotClient(base, key, first_legal, queue={"ruleset_id": "openmajiang.mcr", "ruleset_version": "1.0.0",
            "match_format": match_format, "continuous": True}, on_frame=emit_snapshot,
            on_state=state, on_error=lambda code: errors.update([code]))

        async def watch_stop():
            while not stop_process.is_set():
                await asyncio.sleep(0.1)
            stop.set()

        watcher = asyncio.create_task(watch_stop())
        try:
            await client.run(stop)
        finally:
            watcher.cancel()
            try:
                await client.leave_queue()
            except Exception:
                pass
            output.put({"type": "stopped", "bot": index, "decisions": len(decisions),
                        "acks": len(acks), "errors": dict(errors), "states": dict(states)})
    try:
        asyncio.run(run())
    except BaseException as exc:
        # Only an exception class is emitted; never credentials, frames or URLs.
        output.put({"type": "worker_error", "bot": index, "error": type(exc).__name__})


def typescript_process(index, base, key, match_format, output, stop_process):
    """One independently running Node.js SDK Bot supervised by the harness."""
    entry = Path(__file__).with_name("ts-worker.mjs")
    process = subprocess.Popen(["node", str(entry)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True, bufsize=1)
    def read_events():
        for line in process.stdout:
            try:
                event = json.loads(line)
                if event.get("bot") != index:
                    raise ValueError("wrong worker")
                output.put(event)
            except Exception:
                output.put({"type": "worker_error", "bot": index, "error": "TS_INVALID_OUTPUT"})
    def discard_stderr():
        # Dependencies may print warnings; never forward raw process text that
        # could include a credential or transport frame into CI artifacts.
        for _ in process.stderr:
            pass
    reader = threading.Thread(target=read_events, daemon=True)
    reader.start()
    threading.Thread(target=discard_stderr, daemon=True).start()
    try:
        process.stdin.write(json.dumps({"index": index, "base_url": base, "api_key": key, "match_format": match_format})+"\n")
        process.stdin.flush()
        while process.poll() is None and not stop_process.wait(0.1):
            pass
        if process.poll() is None:
            process.stdin.write('{"type":"stop"}\n')
            process.stdin.flush()
            try:
                process.wait(timeout=12)
            except subprocess.TimeoutExpired:
                process.terminate()
                process.wait(timeout=3)
        elif process.returncode != 0:
            output.put({"type": "worker_error", "bot": index, "error": "TS_RUNTIME_FAILED"})
    finally:
        if process.poll() is None:
            process.kill()
        reader.join(timeout=2)


def assert_discard_only(value, path=()):
    if isinstance(value, dict):
        for key, child in value.items():
            if key in {"hand", "own_hand", "flowers", "melds", "winning_hand", "winning_tile", "fan_items",
                       "decomposition", "trigger", "seed", "wall", "drawn_tile_id", "legal_actions", "tile_id", "tiles"}:
                raise RuntimeError("anonymous view contains forbidden field: " + key)
            # Room seat identity uses kind=human|bot; it is not a card face.
            identity_kind = path == ("room", "seats") and child in ("human", "bot")
            if key == "kind" and "discards" not in path and not identity_kind:
                raise RuntimeError("anonymous card face occurs outside discard history")
            assert_discard_only(child, path + (key,))
    elif isinstance(value, list):
        for child in value:
            assert_discard_only(child, path)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--mailpit-url", required=True)
    parser.add_argument("--hands", type=int, default=100)
    parser.add_argument("--format", choices=("standard_16", "practice_1", "practice_4"), default="standard_16")
    parser.add_argument("--timeout", type=int, default=6900)
    parser.add_argument("--max-timeouts", type=int, default=12, help="bounded allowance for the one injected disconnect")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    if urllib.parse.urlsplit(args.base_url).scheme != "https":
        parser.error("this gate requires HTTPS/WSS; use a trusted test CA rather than disabling verification")
    if args.hands < 1:
        parser.error("hands must be positive")
    if not (Path(__file__).resolve().parents[1]/"typescript/dist/index.js").exists():
        parser.error("build the TypeScript SDK first: cd sdk/typescript && npm ci && npm run build")
    report = {"passed": False, "build_sha": os.environ.get("GITHUB_SHA", "local-unidentified"),
        "ruleset": "openmajiang.mcr@1.0.0", "clock_profile": "production_defaults", "transport": "wss",
        "match_format": args.format, "requested_hands": args.hands, "external_bot_processes": 4,
        "sdk_runtimes": {"python": 3, "typescript": 1}}
    start = time.monotonic()
    processes, credentials = [], []
    context = mp.get_context("spawn")
    events, stop = context.Queue(), context.Event()
    observations, canonical, complete_matches, terminal_seen = {}, {}, set(), {}
    timeout_counters, metrics, connections = {}, {}, Counter()
    privacy_checked, interrupted, injected = set(), False, False
    failure = None
    try:
        prefix = "wss-" + secrets.token_hex(6)
        for index in range(4):
            credentials.append(register_bot(args.base_url, args.mailpit_url, prefix, index))
        report["registered_and_verified_accounts"] = 4
        for index, (_, _, _, key) in enumerate(credentials):
            process = context.Process(target=bot_process, args=(index, args.base_url, key, args.format, events, stop))
            process.start()
            processes.append(process)
        anonymous = API(args.base_url)
        next_progress = 0.0
        while time.monotonic() - start < args.timeout:
            try:
                event = events.get(timeout=1)
            except queue.Empty:
                if any(not process.is_alive() for process in processes):
                    raise RuntimeError("an external Bot process exited before the gate completed")
                continue
            kind, index = event["type"], event.get("bot")
            if kind == "worker_error":
                raise RuntimeError("Bot worker failed: " + event["error"])
            if kind == "connection" and event["state"] == "connected":
                connections[index] = event["count"]
            if kind == "injected_disconnect":
                injected = True
            if kind != "snapshot":
                continue
            metrics[index] = {k: event[k] for k in ("decisions", "acks", "errors")}
            interrupted = interrupted or event["interrupted"]
            match_id, hand = event["match_id"], event["hand_index"]
            timeout_counters[(index, match_id)] = (event["self_timeout_count"], event["reaction_timeout_count"])
            if event["status"] not in ("active", "completed"):
                raise RuntimeError("match ended abnormally: " + str(event["status"]))
            if event["result"] is not None:
                key = (match_id, hand)
                scores = event["result"]["score_deltas"]
                if not isinstance(scores, list) or len(scores) != 4 or sum(score["delta"] for score in scores) != 0:
                    raise RuntimeError("completed hand has invalid zero-sum settlement")
                value = json.dumps(event["result"], sort_keys=True)
                if key in canonical and canonical[key] != value:
                    raise RuntimeError("participant settlement views disagree")
                canonical[key] = value
                observations.setdefault(key, set()).add(index)
                if key not in privacy_checked:
                    public = anonymous.request(f"/v1/public/rooms/{event['room_id']}/spectator")
                    assert_discard_only(public)
                    spectator_socket(args.base_url, anonymous, event['room_id'])
                    privacy_checked.add(key)
                if event["status"] == "completed":
                    terminal_seen.setdefault(match_id, set()).add(index)
                    if len(terminal_seen[match_id]) == 4:
                        complete_matches.add(match_id)
            completed = sum(len(seen) == 4 for seen in observations.values())
            if time.monotonic() >= next_progress:
                print(json.dumps({"completed_hands": completed, "completed_matches": len(complete_matches),
                                  "elapsed_seconds": round(time.monotonic() - start)}), flush=True)
                next_progress = time.monotonic() + 30
            if completed >= args.hands and event["status"] == "completed" and match_id in complete_matches:
                break
        else:
            raise RuntimeError("WSS gate timed out before target completed hands")
        completed = sum(len(seen) == 4 for seen in observations.values())
        if completed < args.hands or not complete_matches:
            raise RuntimeError("not enough fully observed completed hands")
        if not injected or connections[0] < 2:
            raise RuntimeError("injected disconnect did not recover")
        if interrupted:
            raise RuntimeError("platform interruption occurred in default-clock gate")
        missing = any(a is None or b is None for a, b in timeout_counters.values())
        if missing:
            raise RuntimeError("server timeout counters are missing from Bot-authorized snapshots")
        total_timeouts = sum(a + b for a, b in timeout_counters.values())
        if total_timeouts > args.max_timeouts:
            raise RuntimeError("server timeout count exceeds injected-disconnect allowance")
        prohibited = {"INVALID_OPTION", "FORBIDDEN_SEAT", "IDEMPOTENCY_CONFLICT", "INVALID_STRATEGY_OPTION", "STRATEGY_FAILED"}
        if any(prohibited.intersection(metric["errors"]) for metric in metrics.values()):
            raise RuntimeError("Bot protocol or strategy produced an invalid command")
        # Public replay remains discard-only after game completion.
        for match_id in complete_matches:
            after = 0
            while True:
                page = anonymous.request(f"/v1/public/matches/{match_id}/replay?after={after}&limit=200")
                assert_discard_only(page)
                if not page.get("frames"):
                    break
                if page["next_after"] <= after:
                    raise RuntimeError("public replay cursor did not advance")
                after = page["next_after"]
        report["passed"] = True
    except BaseException as exc:
        failure = str(exc) if isinstance(exc, RuntimeError) else type(exc).__name__
    finally:
        stop.set()
        for process in processes:
            process.join(timeout=20)
            if process.is_alive():
                process.terminate()
                process.join(timeout=5)
        # Prove revocation cascades to a *fresh*, previously working short token.
        revocations = 0
        for api, bot_id, credential_id, key in credentials:
            try:
                short = api.request("/v1/bot-sessions", "POST", {"protocol_version": "1.0",
                    "rulesets": [{"id": "openmajiang.mcr", "version": "1.0.0"}]}, bearer=key, expected=201)["session_token"]
                api.request("/v1/bot/active-match", bearer=short, expected=200)
                api.request(f"/v1/bots/{bot_id}/credentials/{credential_id}", "DELETE", {}, expected=200)
                api.request("/v1/bot/active-match", bearer=short, expected=401)
                api.request("/v1/bot-sessions", "POST", {"protocol_version": "1.0", "rulesets": [{"id": "openmajiang.mcr", "version": "1.0.0"}]}, bearer=key, expected=401)
                revocations += 1
            except Exception:
                failure = failure or "credential revocation verification failed"
        if credentials and revocations != 4:
            failure = failure or "not all four credentials were revoked"
        report.update(completed_hands=sum(len(seen) == 4 for seen in observations.values()),
            completed_matches=len(complete_matches), anonymous_privacy_checks=len(privacy_checked),
            reconnect_injected=injected, connections=dict(connections), platform_interrupted=interrupted,
            server_self_timeouts=sum(a or 0 for a, _ in timeout_counters.values()),
            server_reaction_timeouts=sum(b or 0 for _, b in timeout_counters.values()),
            server_timeout_counters_complete=bool(timeout_counters) and all(a is not None and b is not None for a,b in timeout_counters.values()),
            bot_metrics=metrics, credential_revocations_verified=revocations,
            elapsed_seconds=round(time.monotonic() - start, 2))
        if failure:
            report["passed"], report["failure"] = False, failure
        Path(args.report).parent.mkdir(parents=True, exist_ok=True)
        Path(args.report).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"passed": report["passed"], "completed_hands": report["completed_hands"],
            "elapsed_seconds": report["elapsed_seconds"], "failure": failure}, ensure_ascii=False), flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
