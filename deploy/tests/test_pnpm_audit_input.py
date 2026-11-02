"""Exercise the audit gate through its real CLI with project-local fixtures."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
CHECKER = ROOT / "tools/check_pnpm_audit_exceptions.py"


class AuditInputTests(unittest.TestCase):
    def setUp(self):
        cache = ROOT / ".cache"
        cache.mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="audit-input-", dir=cache)
        self.addCleanup(self.temp.cleanup)
        self.fixture = Path(self.temp.name)

    def check(self, audit, exceptions="version: 1\nexceptions:\n", raw=False):
        audit_path = self.fixture / "audit.json"
        audit_path.write_text(audit if raw else json.dumps(audit), encoding="utf-8")
        exceptions_path = self.fixture / "exceptions.yml"
        exceptions_path.write_text(exceptions, encoding="utf-8")
        return subprocess.run(
            [sys.executable, "-B", str(CHECKER), "--audit", str(audit_path),
             "--exceptions", str(exceptions_path)],
            cwd=ROOT, env={**os.environ, "TMPDIR": str(self.fixture)},
            capture_output=True, text=True, timeout=10,
        )

    def assert_invalid(self, result):
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("audit", result.stderr.lower())
        self.assertNotIn("Traceback", result.stderr)
        self.assertLess(len(result.stderr), 500)
        self.assertNotIn("validated", result.stdout)

    def test_registry_error_cannot_be_accepted_as_a_clean_audit(self):
        for audit in ({"error": {"code": "E503", "summary": "registry unavailable"}},
                      {"error": {"code": "E503"}, "advisories": {}}):
            with self.subTest(audit=audit):
                self.assert_invalid(self.check(audit))

    def test_missing_or_malformed_audit_structure_fails_closed(self):
        for audit in ({}, [], None, {"metadata": {}}, {"advisories": []},
                      {"vulnerabilities": None}, {"advisories": {"1": {}}},
                      {"advisories": {"1": "invalid"}},
                      {"advisories": {"1": {"module_name": "package", "severity": "unknown", "id": 1}}},
                      {"vulnerabilities": {"package": {"severity": "high", "via": []}}},
                      {"vulnerabilities": {"package": {"severity": "high", "via": [{}]}}},
                      {"vulnerabilities": {"package": {"severity": "low", "via": ["dependency", {}]}}}):
            with self.subTest(audit=audit):
                self.assert_invalid(self.check(audit))

    def test_malformed_json_has_a_concise_failure(self):
        self.assert_invalid(self.check('{"advisories":', raw=True))

    def test_valid_clean_audit_collections_are_accepted(self):
        for field in ("advisories", "vulnerabilities"):
            with self.subTest(field=field):
                self.assertEqual(self.check({field: {}}).returncode, 0)

    def test_pnpm9_report_keeps_existing_repository_exceptions(self):
        # This checked-in pnpm 9 report contains two high xlsx advisories.
        result = subprocess.run(
            [sys.executable, "-B", str(CHECKER), "--audit", str(ROOT / "frontend/audit.json"),
             "--exceptions", str(ROOT / ".github/audit-exceptions.yml")],
            cwd=ROOT, env={**os.environ, "TMPDIR": str(self.fixture)},
            capture_output=True, text=True, timeout=10,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_high_and_critical_still_require_matching_unexpired_exceptions(self):
        for severity in ("high", "critical"):
            audit = {"advisories": {"1": {"module_name": "package", "severity": severity,
                                           "github_advisory_id": "GHSA-test"}}}
            exception = ("version: 1\nexceptions:\n  - package: package\n"
                         "    advisory: GHSA-test\n    severity: " + severity + "\n"
                         "    mitigation: upgrade\n    expires_on: 2999-01-01\n")
            with self.subTest(severity=severity):
                self.assertNotEqual(self.check(audit).returncode, 0)
                self.assertEqual(self.check(audit, exception).returncode, 0)
                self.assertNotEqual(self.check(audit, exception.replace("2999", "2000")).returncode, 0)
                self.assertNotEqual(self.check(audit, exception.replace("GHSA-test", "GHSA-other")).returncode, 0)

    def test_lower_severities_keep_existing_threshold(self):
        for severity in ("info", "low", "moderate"):
            audit = {"advisories": {"1": {"module_name": "package", "severity": severity,
                                           "id": 1}}}
            with self.subTest(severity=severity):
                self.assertEqual(self.check(audit).returncode, 0)

    def test_vulnerabilities_format_does_not_skip_high_advisories(self):
        audit = {"vulnerabilities": {"package": {"severity": "high", "via": [
            {"source": 123, "name": "package", "title": "Example advisory"}]}}}
        result = self.check(audit)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing exceptions", result.stderr)


if __name__ == "__main__":
    unittest.main()
