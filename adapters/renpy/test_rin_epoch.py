import json
import unittest

from rin_epoch import (
    EpochRuntime,
    EpochStateError,
    MAX_SESSIONS,
    merge_persistent_ledgers,
    proposal_matches_epoch,
    wire_epoch,
)


class EpochRuntimeTests(unittest.TestCase):
    def test_process_restart_advances_host_without_reusing_timeline(self):
        first = EpochRuntime(None)
        saved = first.bind(None, "save.one", "chapter.one")
        second = EpochRuntime(first.persistent_ledger)
        rebound = second.bind(saved, "save.one", "chapter.one")

        self.assertEqual(rebound["host"], saved["host"] + 1)
        self.assertEqual(rebound["timeline"], saved["timeline"])

    def test_load_forks_above_persistent_and_saved_generations(self):
        runtime = EpochRuntime(None)
        original = runtime.bind(None, "save.one", "chapter.one")
        later = runtime.fork(original, "manual")
        loaded = runtime.after_load(original)

        self.assertGreater(loaded["timeline"], later["timeline"])
        self.assertEqual(loaded["fork_reason"], "load")

    def test_rollback_forks_once_per_rollback_sequence(self):
        runtime = EpochRuntime(None)
        original = runtime.bind(None, "save.one", "chapter.one")

        first, changed = runtime.observe_rollback(original, True)
        repeated, repeated_changed = runtime.observe_rollback(first, True)
        runtime.observe_rollback(repeated, False)
        second, second_changed = runtime.observe_rollback(original, True)

        self.assertTrue(changed)
        self.assertFalse(repeated_changed)
        self.assertEqual(repeated, first)
        self.assertTrue(second_changed)
        self.assertGreater(second["timeline"], first["timeline"])

    def test_world_change_advances_world_and_timeline(self):
        runtime = EpochRuntime(None)
        original = runtime.bind(None, "save.one", "chapter.one")
        changed = runtime.bind(original, "save.one", "chapter.two")

        self.assertEqual(changed["world"], original["world"] + 1)
        self.assertGreater(changed["timeline"], original["timeline"])

    def test_state_and_ledger_are_plain_json(self):
        runtime = EpochRuntime(None)
        state = runtime.bind(None, "save.one", "chapter.one")

        self.assertEqual(json.loads(json.dumps(state)), state)
        self.assertEqual(
            json.loads(json.dumps(runtime.persistent_ledger)),
            runtime.persistent_ledger,
        )

    def test_malformed_state_and_ledger_fail_closed(self):
        with self.assertRaises(EpochStateError):
            EpochRuntime({"version": 1, "host": 0, "timelines": []})

        runtime = EpochRuntime(None)
        with self.assertRaises(EpochStateError):
            runtime.bind({"version": 1}, "save.one", "chapter.one")
        with self.assertRaises(EpochStateError):
            runtime.bind(None, "UPPERCASE", "chapter.one")

    def test_timeline_ledger_is_bounded(self):
        ledger = {
            "version": 1,
            "host": 1,
            "timelines": {
                "save." + str(index): 1
                for index in range(MAX_SESSIONS)
            },
        }
        runtime = EpochRuntime(ledger)

        with self.assertRaisesRegex(EpochStateError, "full"):
            runtime.bind(None, "save.overflow", "chapter.one")

    def test_persistent_merge_never_lowers_a_generation(self):
        old = {
            "version": 1,
            "host": 8,
            "timelines": {"save.one": 3},
        }
        new = {
            "version": 1,
            "host": 5,
            "timelines": {"save.one": 7, "save.two": 2},
        }

        merged = merge_persistent_ledgers(old, new, None)

        self.assertEqual(merged["host"], 8)
        self.assertEqual(
            merged["timelines"],
            {"save.one": 7, "save.two": 2},
        )

    def test_proposal_epoch_must_match_window_and_every_offer(self):
        runtime = EpochRuntime(None)
        state = runtime.bind(None, "save.one", "chapter.one")
        epoch = wire_epoch(state)
        request = {
            "session_id": "save.one",
            "decision_window": {"epoch": dict(epoch)},
            "offers": [
                {"expected_epoch": dict(epoch)},
                {"expected_epoch": dict(epoch)},
            ],
        }

        self.assertTrue(proposal_matches_epoch(request, state))

        forked = runtime.fork(state, "rollback")
        self.assertFalse(proposal_matches_epoch(request, forked))

        request["offers"][1]["expected_epoch"]["timeline"] += 1
        self.assertFalse(proposal_matches_epoch(request, state))

    def test_proposal_epoch_rejects_incomplete_or_unbound_data(self):
        runtime = EpochRuntime(None)
        state = runtime.bind(None, "save.one", "chapter.one")

        self.assertFalse(proposal_matches_epoch({}, state))
        self.assertFalse(
            proposal_matches_epoch(
                {
                    "session_id": "save.one",
                    "decision_window": {"epoch": wire_epoch(state)},
                    "offers": [],
                },
                state,
            )
        )


if __name__ == "__main__":
    unittest.main()
