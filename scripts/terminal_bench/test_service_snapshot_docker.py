"""Offline cold round trip: source containers AND volumes are destroyed first."""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from pathlib import Path

from scripts.terminal_bench.cold_recovery import ATTEMPT_LABEL, COHORT_LABEL, ROLE_LABEL, DockerServices
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.service_snapshot import ServiceSnapshots, SnapshotError, digest
from scripts.terminal_bench.test_service_snapshot import contract


@unittest.skipUnless(os.environ.get("YHC_COLD_RECOVERY_DOCKER_TEST") == "1", "explicit offline Docker opt-in")
class ColdDockerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="yhc-cold-image-")
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.build_root = Path(cls.tmp.name).resolve()
        base = os.environ.get("YHC_RECOVERY_DOCKER_IMAGE", "yhc-terminal-bench-python:3.13.14-cache")
        base_id = cls.docker("image", "inspect", base, "--format", "{{.Id}}")
        helper = "yhc-cold-build-" + uuid.uuid4().hex[:12]
        cls.image = "yhc-service-cold-proof:" + uuid.uuid4().hex[:12]
        # Commit only our own fixture code into a local cached base. No Docker
        # build frontend/registry resolution, package installation, or networking.
        cls.docker("run", "--pull", "never", "--network", "none", "--name", helper,
                   "--entrypoint", "/bin/mkdir", base_id, "-p", "/opt/checkpoint")
        try:
            directory = Path(__file__).parent
            cls.docker("cp", str(directory / "service_clock.py"), helper + ":/opt/checkpoint/service_clock.py")
            cls.docker("cp", str(directory / "testdata/checkpoint_service.py"), helper + ":/opt/checkpoint/service.py")
            cls.docker("commit", helper, cls.image)
        finally:
            cls.docker("rm", "--force", helper)
        cls.addClassCleanup(cls.docker, "image", "rm", cls.image)

    @staticmethod
    def docker(*argv):
        return subprocess.run(["docker", *argv], check=True, capture_output=True, text=True, timeout=40).stdout.strip()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="yhc-cold-roundtrip-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.cohort = str(uuid.uuid4())
        self.plan = contract()
        self.plan["clock_skew_ns"] = 100_000_000
        for spec in self.plan["services"].values():
            spec["command"] = ["/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "rpc"]
        self.plan_path = self.root / "contract.json"
        write_report(self.plan_path, self.plan)
        self.snapshot = self.root / "snapshot"
        self.control = self.root / "control"
        self.control.mkdir(mode=0o700)

    def compose(self, project, definition, *args):
        return self.docker("compose", "--project-name", project, "-f", str(definition), *args)

    def provision(self, *, target=False, fault=""):
        project = "yhc-cold-proof-" + uuid.uuid4().hex[:12]
        attempt = str(uuid.uuid4()) if target else None
        services = {}
        for name, spec in self.plan["services"].items():
            labels = {COHORT_LABEL: self.cohort, ROLE_LABEL: "owned-service"}
            if attempt:
                labels[ATTEMPT_LABEL] = attempt
            services[name] = dict(image=self.image, pull_policy="never", read_only=True,
                                  entrypoint=["/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "server"],
                                  environment=dict(CLOCK_POLICY=spec["clock_policy"], RESTORE_FAULT=fault if name == ("main" if fault == "activate" else "api") else ""),
                                  labels=labels, network_mode="none", cap_drop=["ALL"], mem_limit="128m", cpus=0.5,
                                  stop_grace_period="1s", volumes=["state_" + name + ":/state"],
                                  tmpfs=["/tmp:rw,noexec,nosuid,size=16m"])
        definition = self.root / (project + ".json")
        write_report(definition, dict(services=services, volumes={"state_" + name: {} for name in services}))
        self.addCleanup(self.compose, project, definition, "down", "--volumes", "--remove-orphans")
        self.compose(project, definition, "up", "--detach", "--pull", "never", "--no-build")
        for name in services:
            for index in range(40):
                try:
                    self.business(project, definition, name, "state")
                    break
                except subprocess.CalledProcessError:
                    if index == 39:
                        raise
                    time.sleep(0.1)
        return project, definition, attempt

    def business(self, project, definition, service, operation):
        container = self.compose(project, definition, "ps", "--quiet", service)
        return json.loads(self.docker("exec", container, "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", operation))

    def capture_and_destroy_source(self):
        project, definition, _ = self.provision()
        observed = {}
        for count, name in enumerate(sorted(self.plan["services"]), 2):
            for _ in range(count):
                self.business(project, definition, name, "mutate")
            observed[name] = self.business(project, definition, name, "state")
        captured = self.cli("capture", "--project", project, "--definition", str(definition), "--cohort-id", self.cohort)
        self.anchor = captured["snapshot_sha256"]
        manifest = json.loads((self.snapshot / "manifest.json").read_text())
        self.compose(project, definition, "down", "--volumes", "--remove-orphans")
        ids = [r["container_id"] for r in manifest["source"]["containers"]]
        missing = subprocess.run(["docker", "inspect", *ids], capture_output=True)
        self.assertNotEqual(missing.returncode, 0)
        return manifest, observed

    def cli(self, operation, *args):
        env = {k: v for k, v in os.environ.items() if not any(word in k.upper() for word in ("API_KEY", "AUTH_TOKEN", "LIVE_TEST"))}
        result = subprocess.run([sys.executable, "-m", "scripts.terminal_bench.cold_recovery", operation,
                                 "--contract", str(self.plan_path), "--snapshot", str(self.snapshot),
                                 "--control-dir", str(self.control), *args],
                                env=env, check=True, capture_output=True, text=True, timeout=120)
        return json.loads(result.stdout)

    def test_destroyed_two_service_state_and_clock_restore_then_real_business_requests(self):
        manifest, observed = self.capture_and_destroy_source()
        project, definition, attempt = self.provision(target=True)
        for name in observed:
            initial = self.business(project, definition, name, "state")
            self.assertEqual((initial["memory"], initial["durable"]), (0, 0))
        receipt_path = self.root / "restore-receipt.json"
        result = self.cli("restore", "--project", project, "--definition", str(definition), "--attempt-id", attempt,
                          "--snapshot-sha256", self.anchor, "--activate", "--receipt", str(receipt_path))
        self.assertEqual(result["status"], "services_active_verified")
        receipt = json.loads(receipt_path.read_text())
        self.assertFalse(receipt["can_resume_agent"])
        self.assertTrue(receipt["service_clocks_verified"])
        self.assertFalse(set(manifest["source"]["owned_volumes"]) & set(receipt["target"]["owned_volumes"]))
        for name, original in observed.items():
            restored = self.business(project, definition, name, "state")
            self.assertEqual((restored["memory"], restored["durable"]), (original["memory"], original["durable"]))
            self.assertNotEqual(restored["instance"], original["instance"])
            self.assertEqual(self.business(project, definition, name, "next")["value"], original["next_random"])
            clock = receipt["expected_clocks"][name]
            self.assertGreaterEqual(restored["logical_ns"], clock["logical_ns"])
            if name == "api":
                saved = json.loads((self.snapshot / "state.api.json").read_text())["clock"]
                self.assertGreater(clock["logical_ns"], saved["logical_ns"] + 1_000_000_000)
        evidence_dir = os.environ.get("YHC_COLD_RECOVERY_PROOF_DIR")
        if evidence_dir:
            write_report(Path(evidence_dir) / "cold-roundtrip.json", dict(manifest=manifest, receipt=receipt,
                                                                          business_oracle="pass", real_provider_requests=0))

    def test_partial_memory_rng_and_clock_restore_never_release_any_service(self):
        self.capture_and_destroy_source()
        for fault in ("memory", "rng", "clock"):
            with self.subTest(fault=fault):
                project, definition, attempt = self.provision(target=True, fault=fault)
                backend = DockerServices(project, definition, self.plan)
                with self.assertRaisesRegex(SnapshotError, "probe_mismatch"):
                    ServiceSnapshots(self.plan, backend, control_dir=self.control).restore(
                        self.snapshot, attempt, expected_snapshot_sha256=self.anchor, activate=True)
                handles = self.compose(project, definition, "ps", "--all", "--quiet").split()
                states = json.loads(self.docker("inspect", *handles))
                self.assertTrue(all(r["State"]["Running"] is False for r in states))
                self.assertFalse((self.control / ("activation." + self.anchor + ".json")).exists())
                self.compose(project, definition, "down", "--volumes", "--remove-orphans")

    def failed_activation(self, fault, code):
        self.capture_and_destroy_source()
        project, definition, attempt = self.provision(target=True, fault=fault)
        backend = DockerServices(project, definition, self.plan)
        with self.assertRaisesRegex(SnapshotError, code):
            ServiceSnapshots(self.plan, backend, control_dir=self.control).restore(
                self.snapshot, attempt, expected_snapshot_sha256=self.anchor, activate=True)
        ids = self.compose(project, definition, "ps", "--all", "--quiet").split()
        self.assertTrue(all(not r["State"]["Running"] for r in json.loads(self.docker("inspect", *ids))))
        self.assertTrue((self.control / ("activation." + self.anchor + ".json")).exists())

    def test_actual_container_active_clock_drift_is_quarantined(self):
        self.failed_activation("active_clock", "active_clock_configuration")

    def test_actual_container_partial_activation_is_quarantined(self):
        self.failed_activation("activate", "service_operation_rejected")


if __name__ == "__main__":
    unittest.main()
