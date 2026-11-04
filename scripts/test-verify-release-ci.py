#!/usr/bin/env python3
"""Exercise release authorization, API pagination and ancestry without a network."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.error

spec = importlib.util.spec_from_file_location("release_gate", Path(__file__).with_name("verify-release-ci.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)

SHA = "a" * 40
MAIN_SHA = "b" * 40
REPO = "example/urmail"


def candidate(event="workflow_dispatch", branch="main"):
    return dict(id=42, head_sha=SHA, head_branch=branch, event=event,
                repository={"full_name": REPO}, head_repository={"full_name": REPO},
                status="completed", conclusion="success")


def required_jobs():
    return [dict(name=name, status="completed", conclusion="success")
            for name in sorted(gate.REQUIRED_JOBS)]


def comparison(status="ahead", base=SHA, merge=SHA):
    return dict(status=status, base_commit={"sha": base}, merge_base_commit={"sha": merge})


class ReleaseGateTests(unittest.TestCase):
    def test_exact_commit_and_every_required_job(self):
        run, jobs = candidate(), required_jobs()
        self.assertTrue(gate.verify_run(run, jobs, SHA, REPO))
        self.assertFalse(gate.verify_run(run, jobs, MAIN_SHA, REPO))
        self.assertFalse(gate.verify_run(run, jobs[:-1], SHA, REPO))
        for field, value in [("event", "pull_request"), ("head_branch", "other"),
                             ("conclusion", "failure"), ("status", "in_progress")]:
            self.assertFalse(gate.verify_run(dict(run, **{field: value}), jobs, SHA, REPO))
        for state in ("skipped", "failure", "cancelled", None):
            changed = [dict(job) for job in jobs]
            changed[0]["conclusion"] = state
            self.assertFalse(gate.verify_run(run, changed, SHA, REPO))

    def test_failed_duplicate_required_job_cannot_be_hidden(self):
        jobs = required_jobs()
        jobs.append(dict(jobs[0], conclusion="failure"))
        self.assertFalse(gate.verify_run(candidate(), jobs, SHA, REPO))

    def test_tag_push_requires_current_tag_target(self):
        run, jobs = candidate("push", "v0.5.0"), required_jobs()
        self.assertTrue(gate.verify_run(run, jobs, SHA, REPO, SHA))
        self.assertFalse(gate.verify_run(run, jobs, SHA, REPO))
        self.assertFalse(gate.verify_run(run, jobs, SHA, REPO, MAIN_SHA))
        self.assertFalse(gate.verify_run(candidate("push", "main"), jobs, SHA, REPO, SHA))
        self.assertFalse(gate.verify_run(candidate("pull_request", "v0.5.0"), jobs, SHA, REPO, SHA))

    def test_foreign_and_missing_repository_sources_are_rejected(self):
        for field in ("repository", "head_repository"):
            for value in ({"full_name": "attacker/urmail"}, None, {}):
                self.assertFalse(gate.verify_run(dict(candidate(), **{field: value}), required_jobs(), SHA, REPO))
        for branch in ("", None, "v0.5\nother"):
            self.assertFalse(gate.trusted_source(candidate("push", branch), SHA, REPO))

    def test_main_ancestry_is_independent_of_run_branch_name(self):
        main = {"object": {"type": "commit", "sha": MAIN_SHA}}
        with patch.object(gate, "fetch", side_effect=[main, comparison()]) as fetch:
            gate.require_main_ancestry(REPO, SHA)
            self.assertEqual(fetch.call_args_list[1].args[0], f"/repos/{REPO}/compare/{SHA}...{MAIN_SHA}")
        for bad in (comparison("behind"), comparison("diverged"),
                    comparison(base=MAIN_SHA), comparison(merge=MAIN_SHA)):
            with patch.object(gate, "fetch", side_effect=[main, bad]):
                with self.assertRaises(ValueError):
                    gate.require_main_ancestry(REPO, SHA)
        with patch.object(gate, "fetch", return_value={"object": {"type": "tag", "sha": MAIN_SHA}}):
            with self.assertRaises(ValueError):
                gate.require_main_ancestry(REPO, SHA)

    def test_current_main_commit_is_accepted(self):
        with patch.object(gate, "fetch", side_effect=[
            {"object": {"type": "commit", "sha": SHA}}, comparison("identical")
        ]):
            gate.require_main_ancestry(REPO, SHA)

    def test_lightweight_and_annotated_tag_targets(self):
        with patch.object(gate, "fetch", return_value={"object": {"type": "commit", "sha": SHA}}):
            self.assertEqual(gate.tag_commit(REPO, "v0.5.0"), SHA)
        with patch.object(gate, "fetch", side_effect=[
            {"object": {"type": "tag", "sha": MAIN_SHA}},
            {"object": {"type": "commit", "sha": SHA}},
        ]) as fetch:
            self.assertEqual(gate.tag_commit(REPO, "v0.5.0/build"), SHA)
            self.assertTrue(fetch.call_args_list[0].args[0].endswith("v0.5.0%2Fbuild"))
        for bad in ({"type": "tree", "sha": SHA}, {"type": "tag", "sha": "../secret"}):
            with patch.object(gate, "fetch", return_value={"object": bad}):
                with self.assertRaises(ValueError):
                    gate.tag_commit(REPO, "v0.5.0")
        with patch.object(gate, "fetch", return_value={"object": {"type": "tag", "sha": SHA}}):
            with self.assertRaises(ValueError):
                gate.tag_commit(REPO, "v0.5.0")

    def test_pagination_reads_next_page_and_is_bounded(self):
        with patch.object(gate, "fetch", side_effect=[{"jobs": [0] * 100}, {"jobs": [1]}]) as fetch:
            self.assertEqual(list(gate.pages("/jobs", "jobs")), [0] * 100 + [1])
            self.assertTrue(fetch.call_args_list[1].args[0].endswith("page=2"))
        with patch.object(gate, "fetch", return_value={"jobs": [0] * 100}):
            with self.assertRaises(ValueError):
                list(gate.pages("/jobs?state=all", "jobs"))
        with patch.object(gate, "fetch", return_value={"jobs": "not a page"}):
            with self.assertRaises(ValueError):
                list(gate.pages("/jobs", "jobs"))

    def test_full_gate_finds_runs_and_jobs_beyond_first_page(self):
        unrelated = dict(candidate(), head_sha=MAIN_SHA)
        paths = []

        def fetch(path):
            paths.append(path)
            if path.endswith("/git/ref/heads/main"):
                return {"object": {"type": "commit", "sha": MAIN_SHA}}
            if "/compare/" in path:
                return comparison()
            if "/workflows/ci.yaml/runs?" in path:
                return {"workflow_runs": [unrelated] * 100 if path.endswith("page=1") else [candidate()]}
            if "/runs/42/jobs?" in path:
                return {"jobs": [{"name": "extra", "status": "completed", "conclusion": "success"}] * 100
                        if path.endswith("page=1") else required_jobs()}
            self.fail("unexpected API request")

        with patch.object(gate, "fetch", side_effect=fetch):
            self.assertEqual(gate.verify_release(REPO, SHA), 42)
        self.assertTrue(any("workflows/ci.yaml/runs?" in p and p.endswith("page=2") for p in paths))
        self.assertTrue(any("runs/42/jobs?" in p and p.endswith("page=2") for p in paths))

    def test_no_ancestry_proof_means_no_workflow_queries(self):
        with patch.object(gate, "fetch", side_effect=[
            {"object": {"type": "commit", "sha": MAIN_SHA}}, comparison("diverged")
        ]) as fetch:
            with self.assertRaises(ValueError):
                gate.verify_release(REPO, SHA)
            self.assertEqual(fetch.call_count, 2)

    def test_deleted_or_moved_tag_cannot_authorize_release(self):
        for response in (None, {"object": {"type": "commit", "sha": MAIN_SHA}}):
            def fetch(path):
                if path.endswith("/git/ref/heads/main"):
                    return {"object": {"type": "commit", "sha": MAIN_SHA}}
                if "/compare/" in path:
                    return comparison()
                if "/workflows/ci.yaml/runs?" in path:
                    return {"workflow_runs": [candidate("push", "v0.5.0")]}
                if "/git/ref/tags/" in path:
                    if response is None:
                        raise urllib.error.HTTPError(path, 404, "not found", {}, None)
                    return response
                self.fail("invalid tag must not authorize job queries")
            with patch.object(gate, "fetch", side_effect=fetch):
                with self.assertRaises(ValueError):
                    gate.verify_release(REPO, SHA)


    def test_waiting_until_run_succeeds(self):
        poll_count = [0]
        sleeps = []

        def fetch(path):
            if path.endswith("/git/ref/heads/main"):
                return {"object": {"type": "commit", "sha": MAIN_SHA}}
            if "/compare/" in path:
                return comparison()
            if "/workflows/ci.yaml/runs?" in path:
                poll_count[0] += 1
                if poll_count[0] == 1:
                    return {"workflow_runs": [dict(candidate(), status="in_progress", conclusion=None)]}
                return {"workflow_runs": [candidate()]}
            if "/runs/42/jobs?" in path:
                if poll_count[0] == 1:
                    return {"jobs": [dict(j, status="in_progress", conclusion=None) for j in required_jobs()]}
                return {"jobs": required_jobs()}
            self.fail(f"unexpected API request: {path}")

        with patch.object(gate, "fetch", side_effect=fetch):
            res = gate.verify_release(
                REPO, SHA, timeout_seconds=10, poll_interval=1, sleeper=lambda s: sleeps.append(s)
            )
            self.assertEqual(res, 42)
        self.assertEqual(poll_count[0], 2)
        self.assertEqual(sleeps, [1])

    def test_waiting_timeout_raises_error(self):
        simulated_time = [1000.0]

        def fake_monotonic():
            return simulated_time[0]

        def fake_sleep(sec):
            simulated_time[0] += sec + 10.0

        def fetch(path):
            if path.endswith("/git/ref/heads/main"):
                return {"object": {"type": "commit", "sha": MAIN_SHA}}
            if "/compare/" in path:
                return comparison()
            if "/workflows/ci.yaml/runs?" in path:
                return {"workflow_runs": [dict(candidate(), status="in_progress", conclusion=None)]}
            if "/runs/42/jobs?" in path:
                return {"jobs": [dict(j, status="in_progress", conclusion=None) for j in required_jobs()]}
            self.fail(f"unexpected API request: {path}")

        with patch.object(gate, "fetch", side_effect=fetch):
            with patch("time.monotonic", side_effect=fake_monotonic):
                with self.assertRaises(gate.VerificationTimeoutError) as ctx:
                    gate.verify_release(
                        REPO, SHA, timeout_seconds=5, poll_interval=1, sleeper=fake_sleep
                    )
                self.assertIsInstance(ctx.exception, ValueError)
                self.assertIsInstance(ctx.exception, TimeoutError)

    def test_failure_during_retry_raises_error(self):
        poll_count = [0]
        sleeps = []

        def fetch(path):
            if path.endswith("/git/ref/heads/main"):
                return {"object": {"type": "commit", "sha": MAIN_SHA}}
            if "/compare/" in path:
                return comparison()
            if "/workflows/ci.yaml/runs?" in path:
                poll_count[0] += 1
                if poll_count[0] == 1:
                    return {"workflow_runs": [dict(candidate(), run_attempt=2, status="in_progress", conclusion=None)]}
                return {"workflow_runs": [dict(candidate(), run_attempt=2, status="completed", conclusion="failure")]}
            if "/runs/42/jobs?" in path:
                return {"jobs": [dict(j, run_attempt=2, status="in_progress", conclusion=None) for j in required_jobs()]}
            self.fail(f"unexpected API request: {path}")

        with patch.object(gate, "fetch", side_effect=fetch):
            with self.assertRaises(ValueError) as ctx:
                gate.verify_release(
                    REPO, SHA, timeout_seconds=10, poll_interval=1, sleeper=lambda s: sleeps.append(s)
                )
            self.assertIn("failed with conclusion 'failure'", str(ctx.exception))
        self.assertEqual(poll_count[0], 2)

    def test_required_job_failure_during_retry_fails_closed_immediately(self):
        sleeps = []

        def fetch(path):
            if path.endswith("/git/ref/heads/main"):
                return {"object": {"type": "commit", "sha": MAIN_SHA}}
            if "/compare/" in path:
                return comparison()
            if "/workflows/ci.yaml/runs?" in path:
                return {"workflow_runs": [dict(candidate(), run_attempt=2, status="in_progress", conclusion=None)]}
            if "/runs/42/jobs?" in path:
                jobs = [dict(j, run_attempt=2, status="in_progress", conclusion=None) for j in required_jobs()]
                jobs[0]["conclusion"] = "failure"
                return {"jobs": jobs}
            self.fail(f"unexpected API request: {path}")

        with patch.object(gate, "fetch", side_effect=fetch):
            with self.assertRaises(ValueError) as ctx:
                gate.verify_release(
                    REPO, SHA, timeout_seconds=10, poll_interval=1, sleeper=lambda s: sleeps.append(s)
                )
            self.assertIn("failed with conclusion 'failure'", str(ctx.exception))
        self.assertEqual(len(sleeps), 0)

    def test_cross_attempt_stitching_forbidden_in_verify_run(self):
        req = required_jobs()
        attempt1_jobs = [dict(j, run_attempt=1) for j in req[:6]]
        attempt2_jobs = [dict(j, run_attempt=2) for j in req[6:]]
        mixed_jobs = attempt1_jobs + attempt2_jobs
        run_attempt2 = dict(candidate(), run_attempt=2)

        self.assertFalse(gate.verify_run(run_attempt2, mixed_jobs, SHA, REPO))
        self.assertFalse(gate.verify_run(dict(candidate(), run_attempt=1), mixed_jobs, SHA, REPO))

        attempt2_all = [dict(j, run_attempt=2) for j in req]
        attempt2_all[0]["conclusion"] = "failure"
        attempt1_all = [dict(j, run_attempt=1) for j in req]
        self.assertFalse(gate.verify_run(run_attempt2, attempt1_all + attempt2_all, SHA, REPO))

    def test_attempt_changed_mid_wait_fails_closed(self):
        poll_count = [0]
        sleeps = []

        def fetch(path):
            if path.endswith("/git/ref/heads/main"):
                return {"object": {"type": "commit", "sha": MAIN_SHA}}
            if "/compare/" in path:
                return comparison()
            if "/workflows/ci.yaml/runs?" in path:
                poll_count[0] += 1
                if poll_count[0] == 1:
                    return {"workflow_runs": [dict(candidate(), run_attempt=1, status="in_progress", conclusion=None)]}
                return {"workflow_runs": [dict(candidate(), run_attempt=2, status="in_progress", conclusion=None)]}
            if "/runs/42/jobs?" in path:
                return {"jobs": [dict(j, run_attempt=1, status="in_progress", conclusion=None) for j in required_jobs()]}
            self.fail(f"unexpected API request: {path}")

        with patch.object(gate, "fetch", side_effect=fetch):
            with self.assertRaises(ValueError) as ctx:
                gate.verify_release(
                    REPO, SHA, timeout_seconds=10, poll_interval=1, sleeper=lambda s: sleeps.append(s)
                )
            self.assertIn("cross-attempt stitching is forbidden", str(ctx.exception))

    def test_immediate_unrecoverable_failure_conclusions(self):
        for conclusion in ("failure", "cancelled", "timed_out"):
            sleeps = []

            def fetch(path):
                if path.endswith("/git/ref/heads/main"):
                    return {"object": {"type": "commit", "sha": MAIN_SHA}}
                if "/compare/" in path:
                    return comparison()
                if "/workflows/ci.yaml/runs?" in path:
                    return {"workflow_runs": [dict(candidate(), status="completed", conclusion=conclusion)]}
                self.fail(f"unexpected API request: {path}")

            with patch.object(gate, "fetch", side_effect=fetch):
                with self.assertRaises(ValueError):
                    gate.verify_release(
                        REPO, SHA, timeout_seconds=10, poll_interval=1, sleeper=lambda s: sleeps.append(s)
                    )
            self.assertEqual(len(sleeps), 0)

if __name__ == "__main__":
    unittest.main()