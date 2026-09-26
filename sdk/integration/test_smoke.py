"""Validate gate privacy checks against the shared real-wire fixture."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("wss_smoke", HERE / "wss-smoke.py")
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class GatePrivacyTests(unittest.TestCase):
    def setUp(self):
        self.public = json.loads((HERE.parents[1] / "api/fixtures/spectator.json").read_text())

    def test_public_room_identity_is_not_a_tile(self):
        smoke.assert_discard_only(self.public)

    def test_faces_outside_discard_history_are_rejected(self):
        bad = copy.deepcopy(self.public)
        bad["room"]["seats"][0]["kind"] = "1m"
        with self.assertRaises(RuntimeError):
            smoke.assert_discard_only(bad)

    def test_physical_tile_and_hidden_hand_are_rejected(self):
        for key in ["tile_id", "hand", "flowers", "melds"]:
            bad = copy.deepcopy(self.public)
            bad["view"]["discards"][0][key] = []
            with self.assertRaises(RuntimeError):
                smoke.assert_discard_only(bad)


if __name__ == "__main__":
    unittest.main()
