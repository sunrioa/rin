import copy
import json
import tempfile
import unittest
from pathlib import Path

from tools import run_sdk_sidecar_corpus


class SidecarCorpusTests(unittest.TestCase):
    def test_committed_corpus_is_valid_and_materializes_below_limit(self):
        corpus = run_sdk_sidecar_corpus.load_corpus()
        request = run_sdk_sidecar_corpus.materialize_request(corpus, "python")
        encoded = run_sdk_sidecar_corpus.compact_json(request)
        self.assertNotIn("__client__", encoded)
        self.assertLess(len(encoded.encode()), corpus["request_body_limit"])
        self.assertEqual(request["request_id"], "create.python")
        self.assertEqual(request["session_id"], "session.python")
        self.assertEqual(request["actors"][0]["id"], "npc.python")

    def test_invalid_or_unbounded_corpus_is_rejected(self):
        corpus = run_sdk_sidecar_corpus.load_corpus()
        for mutate in (
            lambda value: value.update(version=2),
            lambda value: value.update(request_body_limit=1),
            lambda value: value.update(slow_response_ms=1),
            lambda value: value.update(create_session=[]),
            lambda value: value["expectations"].pop("timeout"),
        ):
            invalid = copy.deepcopy(corpus)
            mutate(invalid)
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "corpus.json"
                path.write_text(json.dumps(invalid), encoding="utf-8")
                with self.assertRaises(ValueError):
                    run_sdk_sidecar_corpus.load_corpus(path)

    def test_client_name_cannot_escape_protocol_identifiers(self):
        corpus = run_sdk_sidecar_corpus.load_corpus()
        for value in ("", "../bad", "with space", "雨"):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    run_sdk_sidecar_corpus.materialize_request(corpus, value)

    def test_language_selection_is_closed(self):
        self.assertEqual(
            run_sdk_sidecar_corpus.parse_languages("python,lua"),
            ("python", "lua"),
        )
        with self.assertRaises(Exception):
            run_sdk_sidecar_corpus.parse_languages("python,rust")


if __name__ == "__main__":
    unittest.main()
