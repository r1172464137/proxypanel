#!/usr/bin/env python3
"""Dependency-free runner contract tests: uv run scripts/backend-test-unit.py."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("backend_runner", Path(__file__).with_name("backend-test.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class BuildTagContract(unittest.TestCase):
    def entry(self):
        return {
            "id": "integration/test", "packages": ["./internal/agent/supervisor"],
            "run": "^TestRealCoreLifecycleAndUsage$",
            "expected_tests": ["TestRealCoreLifecycleAndUsage"],
            "stories": ["US-PR-011"], "requires": ["xray"], "build_tags": ["xrayreal"],
        }

    def load(self, entry):
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / "manifest.json"
            manifest.write_text(json.dumps([entry]))
            with patch.object(runner, "MANIFEST", manifest):
                return runner.load_selections([])

    def test_discovery_and_execution_use_identical_explicit_tags(self):
        entry = self.entry()
        self.assertEqual(self.load(entry), [entry])
        for discover in (False, True):
            command = runner.command(entry, discover=discover)
            self.assertEqual(command[:3], ["go", "test", "-tags=xrayreal"])
            self.assertEqual(command[-1], entry["packages"][0])
        self.assertIn(entry["run"], runner.command(entry))
        self.assertIn("-list", runner.command(entry, discover=True))

    def test_ordinary_selections_do_not_enable_real_tests_implicitly(self):
        entry = self.entry()
        del entry["build_tags"]
        for discover in (False, True):
            self.assertFalse(any(arg.startswith("-tags") for arg in runner.command(entry, discover)))

    def test_invalid_tags_are_specific_configuration_errors(self):
        for tags in ("xrayreal", ["xrayreal", "xrayreal"], ["a,b"], ["-run=Test"], [1]):
            with self.subTest(tags=tags):
                entry = self.entry()
                entry["build_tags"] = tags
                with self.assertRaisesRegex(ValueError, "invalid build_tags"):
                    self.load(entry)

    def test_real_tag_requires_explicit_core_capability(self):
        entry = self.entry()
        entry["requires"] = []
        with self.assertRaisesRegex(ValueError, "xrayreal requires an explicit xray capability"):
            self.load(entry)

    def test_registered_real_selections_preserve_expected_cases(self):
        entries = {e["id"]: e for e in runner.load_selections([])}
        for identifier, required in {
            "integration/protocol-agent": {"TestProtocolAgentTLSLifecycle"},
            "agent-supervisor/real-lifecycle": {
                "TestRealCoreCrashIsDetectedAndRecovered", "TestRealCoreOverloadStopIsDurable",
                "TestRealCoreLifecycleAndUsage", "TestRealCoreRevokeTerminatesEstablishedConnection",
                "TestRealCoreRunConcurrentLoops", "TestRealRestoreDropsPersistedExpiredUser",
            },
        }.items():
            with self.subTest(identifier=identifier):
                entry = entries[identifier]
                self.assertEqual(entry["build_tags"], ["xrayreal"])
                self.assertTrue(required.issubset(entry["expected_tests"]))
                self.assertIn("xray", entry["requires"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
