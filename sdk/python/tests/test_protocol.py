import asyncio
import copy
import json
from pathlib import Path
import unittest

from openmajiang import DecisionEngine, first_legal

FIXTURE = json.loads((Path(__file__).resolve().parents[2] / "fixtures" / "decision.json").read_text())


class ProtocolTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.engines = []

    def engine(self, strategy=first_legal, **options):
        sent = []
        engine = DecisionEngine(strategy, sent.append, **options)
        self.engines.append(engine)
        return engine, sent

    async def asyncTearDown(self):
        for engine in self.engines:
            engine.disconnect()
        await asyncio.sleep(0)

    async def test_snapshot_barrier_and_ack_controls(self):
        calls = []
        engine, sent = self.engine(lambda c: calls.append(c) or "discard_1")
        snapshot, decision = copy.deepcopy(FIXTURE).values()
        engine.receive(decision)
        await asyncio.sleep(0)
        self.assertEqual(calls, [])
        self.assertEqual(sent[-1]["type"], "resume")
        engine.receive(snapshot)
        engine.receive({"type": "command_ack", "command_id": "other", "view_seq": 900})
        engine.receive(decision)
        await asyncio.sleep(0)
        self.assertEqual(len(calls), 1)
        self.assertEqual(sent[-1]["option_id"], "discard_1")

    async def test_retry_preserves_command_and_recorded_deduplicates(self):
        engine, sent = self.engine()
        snapshot, decision = copy.deepcopy(FIXTURE).values()
        engine.receive(snapshot)
        engine.receive(decision)
        await asyncio.sleep(0)
        original = copy.deepcopy(sent[-1])
        engine.retry_pending()
        self.assertEqual(sent[-1], original)
        engine.receive({"type": "command_ack", "command_id": original["command_id"], "status": "recorded"})
        self.assertEqual(len(engine.pending), 0)
        engine.receive(snapshot)
        engine.receive(decision)
        await asyncio.sleep(0)
        self.assertEqual(len(sent), 2)

    async def test_stale_identity_never_calls_strategy(self):
        for field in ("control_epoch", "hand_id", "participant_id", "seat_assignment_version"):
            calls = []
            engine, sent = self.engine(lambda c: calls.append(c) or "discard_1")
            snapshot, decision = copy.deepcopy(FIXTURE).values()
            decision[field] = "wrong"
            engine.receive(snapshot)
            engine.receive(decision)
            await asyncio.sleep(0)
            self.assertEqual(calls, [])
            self.assertEqual(sent[-1]["type"], "resume")

    async def test_only_pass_and_optional_flower(self):
        for kind in ("pass", "replace_flower"):
            calls = []
            engine, sent = self.engine(lambda c: calls.append(c) or "x")
            snapshot, decision = copy.deepcopy(FIXTURE).values()
            decision["legal_actions"] = [{"type": kind, "option_id": "x"}]
            engine.receive(snapshot)
            engine.receive(decision)
            await asyncio.sleep(0)
            self.assertEqual(calls, [])
            self.assertEqual(sent[-1]["option_id"], "x")
        calls = []
        engine, _ = self.engine(lambda c: calls.append(c) or "x", auto_flower=False)
        snapshot, decision = copy.deepcopy(FIXTURE).values()
        decision["legal_actions"] = [{"type": "replace_flower", "option_id": "x"}]
        engine.receive(snapshot)
        engine.receive(decision)
        await asyncio.sleep(0)
        self.assertEqual(len(calls), 1)

    async def test_cancelled_strategy_late_result_is_fenced(self):
        observed = []
        async def stubborn(context):
            observed.append(context)
            try:
                await asyncio.sleep(3600)
            except asyncio.CancelledError:
                return "discard_1"
        engine, sent = self.engine(stubborn)
        snapshot, decision = copy.deepcopy(FIXTURE).values()
        engine.receive(snapshot)
        engine.receive(decision)
        await asyncio.sleep(0)
        engine.receive(dict(snapshot, view_seq=3))
        await asyncio.sleep(0)
        self.assertTrue(observed[0].cancelled.is_set())
        self.assertEqual(sent, [])

    async def test_expiry_and_invalid_option(self):
        engine, sent = self.engine(lambda c: "invalid-option")
        snapshot, decision = copy.deepcopy(FIXTURE).values()
        engine.receive(snapshot)
        engine.receive(decision)
        await asyncio.sleep(0)
        self.assertEqual(sent, [])
        engine.disconnect()
        decision["deadline_at"] = decision["server_time"]
        engine.receive(snapshot)
        engine.receive(decision)
        await asyncio.sleep(0)
        self.assertEqual(sent, [])


if __name__ == "__main__":
    unittest.main()
