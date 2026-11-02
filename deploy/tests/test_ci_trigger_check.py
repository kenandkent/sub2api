import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_trigger_check", ROOT / "deploy/check-mysubapi-ci.py")
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)
SHA = "a" * 40


def run(**changes):
    item = dict(id=17, head_sha=SHA, head_branch="mysubapi", path=ci.WORKFLOW,
                event="push", status="queued", conclusion=None)
    return dict(item, **changes)


class TriggerCheckTests(unittest.TestCase):
    def test_ignores_old_sha_branch_and_different_workflow(self):
        other = [run(head_sha="b" * 40), run(head_branch="main"),
                 run(path=".github/workflows/other.yml"), run(event="pull_request")]
        self.assertIsNone(ci.select_run({"workflow_runs": other}, SHA))

    def test_accepts_exact_sha_push_and_manual_recovery(self):
        for event in ("push", "workflow_dispatch"):
            self.assertEqual(ci.select_run({"workflow_runs": [run(event=event)]}, SHA)["id"], 17)

    def test_prefers_newest_matching_run(self):
        self.assertEqual(ci.select_run({"workflow_runs": [run(), run(id=18)]}, SHA)["id"], 18)

    def test_malformed_api_response_is_not_no_trigger(self):
        with self.assertRaises(ci.APIError):
            ci.select_run({"message": "API unavailable"}, SHA)

    def test_no_run_fails_after_wait_instead_of_reporting_success(self):
        clock = [0.0]
        def pause(seconds): clock[0] += seconds
        with self.assertRaises(ci.NoRun):
            ci.wait_for_run(lambda: {"workflow_runs": []}, SHA, 2, 1,
                            clock=lambda: clock[0], pause=pause)
        self.assertEqual(clock[0], 2)

    def test_created_run_does_not_require_deployment_to_have_completed(self):
        item = ci.wait_for_run(lambda: {"workflow_runs": [run()]}, SHA, 0, 1)
        self.assertEqual(item["status"], "queued")

    def test_completed_mode_waits_for_result_and_preserves_failure(self):
        sequence = iter(({"workflow_runs": [run()]}, {"workflow_runs": [run(status="completed", conclusion="failure")]}))
        clock = [0.0]
        def pause(seconds): clock[0] += seconds
        item = ci.wait_for_run(lambda: next(sequence), SHA, 2, 1, completed=True,
                              clock=lambda: clock[0], pause=pause)
        self.assertEqual(item["conclusion"], "failure")

    def test_api_failure_is_not_swallowed_as_missing_run(self):
        def fail(): raise ci.APIError("HTTP 403")
        with self.assertRaises(ci.APIError):
            ci.wait_for_run(fail, SHA, 0, 1)


if __name__ == "__main__":
    unittest.main()
