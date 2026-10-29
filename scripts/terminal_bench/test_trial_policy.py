"""Evaluator failures must not acquire budget or recovery semantics."""

import copy
import unittest

from scripts.terminal_bench.trial_policy import (
    PolicyError,
    classify_trial,
    verify_compose,
    verify_container,
    verify_proof_directory,
)


class OutcomeTests(unittest.TestCase):
    def setUp(self):
        self.facts = dict(
            accounting={"unknown_usage": False, "outstanding": "0", "accounting_error": False},
            usage_complete=True,
            native_completed=True,
            checkpoint_complete=True,
            deadline_reason="native_completed",
            result={"verifier_result": {"rewards": {"reward": 0.0}}},
            isolation_verified=True,
        )

    def outcome(self, **changes):
        return classify_trial(**(self.facts | changes))

    def test_zero_reward_is_completed_evaluation(self):
        result = self.outcome()
        self.assertEqual((result.ledger_status, result.reason), ("completed", "evaluated"))
        self.assertFalse(result.can_resume_agent)

    def test_grader_exception_is_failed_not_budget_pause(self):
        self.assertEqual(self.outcome(result={"exception_info": {"exception_type": "VerifierError"}}).reason,
                         "grading_failed")
        self.assertEqual(self.outcome(error_type="RuntimeError").ledger_status, "failed")

    def test_budget_pause_needs_explicit_budget_evidence(self):
        self.assertEqual(self.outcome(native_completed=False).ledger_status, "failed")
        self.assertEqual(self.outcome(native_completed=False, budget_denied=True).ledger_status,
                         "budget_paused")
        self.assertEqual(self.outcome(deadline_reason="deadline_exceeded").reason, "agent_deadline")

    def test_budget_does_not_hide_independent_grader_failure(self):
        self.assertEqual(self.outcome(budget_denied=True, error_type="VerifierError").ledger_status, "failed")
        self.assertEqual(self.outcome(native_completed=False, budget_denied=True,
                         result={"exception_info": {"exception_type": "RewardFileNotFoundError"}}).ledger_status,
                         "failed")
        # Harbor keeps the first exception even when its grader then fails.
        self.assertEqual(self.outcome(native_completed=False, budget_denied=True,
                         result={"exception_info": {"exception_type": "NonZeroAgentExitCodeError"}}).reason,
                         "grading_unverified")

    def test_incomplete_checkpoints_and_isolation_remain_blocked(self):
        for change, reason in (({"checkpoint_complete": False}, "checkpoint_unverified"),
                               ({"isolation_verified": False}, "verifier_isolation_unverified"),
                               ({"result": {}}, "grading_unverified")):
            with self.subTest(change=change):
                result = self.outcome(**change)
                self.assertEqual((result.ledger_status, result.reason), ("failed", reason))
                self.assertFalse(result.can_resume_agent)

    def test_unknown_or_unclosed_usage_has_precedence(self):
        for change in ({"accounting": {"unknown_usage": True, "outstanding": "0"}},
                       {"accounting": {"unknown_usage": False, "outstanding": "1"}},
                       {"usage_complete": False}, {"accounting": {}},
                       {"accounting": {"unknown_usage": False, "outstanding": "0", "accounting_error": True}},
                       {"accounting": {"unknown_usage": False, "outstanding": "NaN"}}):
            with self.subTest(change=change):
                self.assertEqual(self.outcome(**change).ledger_status, "unknown_usage")


class IsolationTests(unittest.TestCase):
    def setUp(self):
        self.compose = {"services": {"main": {
            "image": "sha256:fixture", "network_mode": "none", "pull_policy": "never",
            "volumes": [{"type": "bind", "source": "/owned/logs", "target": "/logs/verifier"}],
        }}}
        self.container = {"Id": "fixture-container", "Image": "sha256:fixture",
                          "State": {"Running": True},
                          "HostConfig": {"NetworkMode": "none", "Privileged": False},
                          "NetworkSettings": {"Networks": {"none": {}}},
                          "Mounts": [{"Type": "bind", "Source": "/owned/logs", "Destination": "/logs/verifier"}]}

    def test_actual_network_none_and_only_host_owned_logs(self):
        verify_compose(self.compose, image="sha256:fixture", log_dir="/owned/logs")
        verify_container(self.container, image="sha256:fixture", log_dir="/owned/logs")

    def test_proof_is_outside_all_agent_and_sidecar_host_binds(self):
        for source in ("/owned", "/owned/agent"):
            config = {"services": {"helper": {"volumes": [{"type": "bind", "source": source}]}}}
            with self.assertRaisesRegex(PolicyError, "verifier_proof_exposed"):
                verify_proof_directory("/owned/agent/proofs", config)
        verify_proof_directory("/owned/proofs", self.compose)

    def test_named_bind_and_external_volume_cannot_hide_proof_exposure(self):
        with self.assertRaises(PolicyError):
            verify_proof_directory("/owned/proofs", {"volumes": {"hidden": {
                "driver_opts": {"type": "none", "o": "bind", "device": "/owned"}}}})
        with self.assertRaises(PolicyError):
            verify_proof_directory("/owned/proofs", {"volumes": {"unqualified": {"external": True}}})

    def test_public_sidecar_socket_and_privilege_are_rejected(self):
        for mutation in (
            lambda c: c["services"]["main"].update(network_mode="bridge"),
            lambda c: c["services"].update(helper={"image": "anything"}),
            lambda c: c["services"]["main"].update(privileged=True),
            lambda c: c["services"]["main"].update(pid="host"),
            lambda c: c["services"]["main"].update(cap_add=["NET_ADMIN"]),
            lambda c: c["services"]["main"].update(volumes=[{"type": "bind", "source": "/var/run/docker.sock", "target": "/socket"}]),
        ):
            cfg = copy.deepcopy(self.compose)
            mutation(cfg)
            with self.assertRaises(PolicyError):
                verify_compose(cfg, image="sha256:fixture", log_dir="/owned/logs")

    def test_running_container_is_independently_checked(self):
        for mutation in (
            lambda c: c["HostConfig"].update(NetworkMode="default"),
            lambda c: c.update(Image="sha256:unexpected"),
            lambda c: c["State"].update(Running=False),
            lambda c: c["NetworkSettings"].update(Networks={"bridge": {}}),
            lambda c: c["Mounts"].append({"Type": "bind", "Source": "/", "Destination": "/host"}),
        ):
            row = copy.deepcopy(self.container)
            mutation(row)
            with self.assertRaises(PolicyError):
                verify_container(row, image="sha256:fixture", log_dir="/owned/logs")


if __name__ == "__main__":
    unittest.main()
