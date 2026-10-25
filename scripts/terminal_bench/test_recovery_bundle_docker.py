"""Real destroyed-runtime recovery using the production provider-free YHC CLI."""

import json
import os
import subprocess
import unittest
import uuid
from pathlib import Path

from scripts.terminal_bench.cold_recovery import DockerServices
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.recovery_bundle import RecoveryBundle
from scripts.terminal_bench.service_snapshot import ServiceSnapshots, SnapshotError
from scripts.terminal_bench.test_recovery_bundle import history
from scripts.terminal_bench import test_service_snapshot_docker as service_fixture
from scripts.terminal_bench.workspace_recovery import ATTEMPT, WORKSPACE_COHORT, WORKSPACE_ROLE, DockerWorkspace


@unittest.skipUnless(os.environ.get("YHC_NATIVE_RECOVERY_DOCKER_TEST") == "1", "explicit native cold Docker opt-in")
class NativeColdDockerTests(unittest.TestCase):
    docker = staticmethod(service_fixture.ColdDockerTests.docker)
    compose = service_fixture.ColdDockerTests.compose
    provision = service_fixture.ColdDockerTests.provision
    business = service_fixture.ColdDockerTests.business
    cli = service_fixture.ColdDockerTests.cli
    setUp = service_fixture.ColdDockerTests.setUp

    @classmethod
    def setUpClass(cls):
        binary = Path(os.environ["YHC_NATIVE_RECOVERY_BINARY"]).resolve()
        seed = Path(os.environ["YHC_NATIVE_RECOVERY_SEED"]).resolve()
        if not binary.is_file() or not seed.is_file():
            raise RuntimeError("compiled Linux fixture inputs required")
        # The cached generic fixture has Git and system Python. Add only an
        # interpreter alias in an owned immutable image; no package install.
        compat = "yhc-native-base:" + uuid.uuid4().hex[:12]
        setup = "yhc-native-base-" + uuid.uuid4().hex[:12]
        cached = os.environ.get("YHC_NATIVE_RECOVERY_BASE", "yhc-generic-concurrency:20261020-arm64")
        handle = cls.docker("create", "--user", "0:0", "--network", "none", "--name", setup, "--entrypoint", "/bin/sh", cached,
                            "-c", "mkdir -p /usr/local/bin && ln -s /usr/bin/python3 /usr/local/bin/python3")
        try:
            cls.docker("start", "--attach", handle)
            cls.docker("commit", "--change", "USER 0:0", handle, compat)
        finally:
            cls.docker("rm", "--force", handle)
        cls.addClassCleanup(cls.docker, "image", "rm", compat)
        previous = os.environ.get("YHC_RECOVERY_DOCKER_IMAGE")
        os.environ["YHC_RECOVERY_DOCKER_IMAGE"] = compat
        try:
            service_fixture.ColdDockerTests.setUpClass.__func__(cls)
        finally:
            if previous is None:
                os.environ.pop("YHC_RECOVERY_DOCKER_IMAGE", None)
            else:
                os.environ["YHC_RECOVERY_DOCKER_IMAGE"] = previous
        base = cls.image
        cls.image = "yhc-native-cold-proof:" + uuid.uuid4().hex[:12]
        helper = "yhc-native-image-" + uuid.uuid4().hex[:12]
        cls.docker("run", "--pull", "never", "--network", "none", "--name", helper, "--entrypoint", "/bin/mkdir", base, "-p", "/opt/yhc")
        try:
            cls.docker("cp", str(binary), helper + ":/opt/yhc/yhc")
            cls.docker("cp", str(seed), helper + ":/opt/checkpoint/seed-native")
            cls.docker("cp", str(Path(__file__).parent / "workspace_io.py"), helper + ":/opt/checkpoint/workspace_io.py")
            cls.docker("commit", helper, cls.image)
        finally:
            cls.docker("rm", "--force", helper)
        cls.addClassCleanup(cls.docker, "image", "rm", cls.image)

    def remove_workspace(self, container, volume):
        present = self.docker("ps", "--all", "--quiet", "--no-trunc", "--filter", "id=" + container).split()
        if container in present:
            self.docker("rm", "--force", container)
        volumes = self.docker("volume", "ls", "--quiet", "--filter", "name=^" + volume + "$").split()
        if volume in volumes:
            self.docker("volume", "rm", volume)

    def provision_workspace(self, *, target=False):
        attempt = self.attempt if target else None
        volume = "yhc-native-state-" + uuid.uuid4().hex[:12]
        self.docker("volume", "create", "--label", WORKSPACE_COHORT + "=" + self.cohort, volume)
        name = "yhc-native-space-" + uuid.uuid4().hex[:12]
        labels = ["--label", WORKSPACE_COHORT + "=" + self.cohort, "--label", WORKSPACE_ROLE + "=idle-native-workspace"]
        if attempt:
            labels += ["--label", ATTEMPT + "=" + attempt]
        container = self.docker("run", "--detach", "--pull", "never", "--read-only", "--cap-drop", "ALL",
                                "--network", "none", "--ipc", "private", "--memory", "384m", "--cpus", "1",
                                "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--mount", "type=volume,src=" + volume + ",dst=/workspace",
                                "--workdir", "/workspace", "--name", name, *labels, "--entrypoint", "/bin/sleep", self.image, "infinity")
        self.addCleanup(self.remove_workspace, container, volume)
        return DockerWorkspace(container, "/workspace", self.cohort, attempt), volume

    def capture_joint(self):
        self.session, self.attempt = str(uuid.uuid4()), str(uuid.uuid4())
        source_project, source_definition, _ = self.provision()
        observed = {}
        for name in self.plan["services"]:
            self.business(source_project, source_definition, name, "mutate")
            observed[name] = self.business(source_project, source_definition, name, "state")
        source, volume = self.provision_workspace()
        self.docker("exec", "--workdir", "/workspace", source.container, "/opt/checkpoint/seed-native", self.session)
        git_status = self.docker("exec", "--workdir", "/workspace", source.container, "git", "status", "--porcelain=v1")
        original_rpc = source.rpc
        def source_rpc(operation, **body):
            value = original_rpc(operation, **body)
            if operation == "native_probe" and value.get("validation_complete") is not True:
                debug = json.loads(self.docker("exec", "--workdir", "/workspace", source.container, "/usr/bin/env",
                    "HOME=/tmp/yhc-proof-home", "YHC_SESSION_CATALOG=/workspace/.yhc/session-roots.json",
                    "/opt/yhc/yhc", "sessions", "resume", self.session, "--recovery-proof", "--output-format", "json"))
                raise AssertionError(json.dumps({"blocking_reasons": value.get("blocking_reasons"),
                                                "fixture_warnings": debug.get("result", {}).get("warnings"),
                                                "native_usage": value.get("native_usage")}))
            return value
        source.rpc = source_rpc
        captured = self.cli("capture", "--project", source_project, "--definition", str(source_definition), "--cohort-id", self.cohort)
        self.service_digest = captured["snapshot_sha256"]
        source_services = ServiceSnapshots(self.plan, DockerServices(source_project, source_definition, self.plan), control_dir=self.control)
        self.bundle_path = self.root / "bundle"
        captured = RecoveryBundle(source_services, source).capture(
            self.bundle_path, self.snapshot, self.service_digest, self.session, history(self.session))
        self.bundle_digest = captured["bundle_sha256"]
        manifest = json.loads((self.bundle_path / "manifest.json").read_text())
        old_service = json.loads((self.snapshot / "manifest.json").read_text())
        old_ids = [r["container_id"] for r in old_service["source"]["containers"]] + [source.container]
        self.compose(source_project, source_definition, "down", "--volumes", "--remove-orphans")
        self.remove_workspace(source.container, volume)
        missing = subprocess.run(["docker", "inspect", *old_ids], capture_output=True)
        self.assertNotEqual(missing.returncode, 0)
        self.assertNotIn(volume, self.docker("volume", "ls", "--quiet").split())
        return manifest, observed, git_status

    def target_bundle(self):
        project, definition, unused_attempt = self.provision(target=True)
        # Existing generic fixture provisions an independent attempt; bind this
        # cohort's service and workspace targets to that exact same attempt.
        self.attempt = unused_attempt
        workspace, _ = self.provision_workspace(target=True)
        services = ServiceSnapshots(self.plan, DockerServices(project, definition, self.plan), control_dir=self.control)
        return RecoveryBundle(services, workspace)

    def test_destroyed_workspace_services_native_session_and_usage_restore_together(self):
        manifest, observed, git_status = self.capture_joint()
        bundle = self.target_bundle()
        receipt = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertEqual(receipt["status"], "restored_components_verified")
        self.assertFalse(receipt["can_resume_agent"])
        self.assertTrue(receipt["workspace_restored"] and receipt["native_session_loaded"] and receipt["cumulative_usage_restored"])
        self.assertEqual(receipt["accounting"]["totals"]["total_tokens"], 28)
        self.assertEqual(receipt["native_proof"]["native_usage"]["total_tokens"], 18)
        self.assertNotEqual(receipt["native_proof"]["process_nonce"], manifest["native_proof"]["process_nonce"])
        self.assertEqual(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container, "git", "status", "--porcelain=v1"), git_status)
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        for name, original in observed.items():
            handle = bundle.services.backend.handles[name]
            actual = json.loads(self.docker("exec", handle, "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original["memory"], original["durable"], original["next_random"]))
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / "joint-roundtrip.json", dict(manifest=manifest, receipt=receipt,
                         git_status_preserved=True, committed_effect_count=1, source_containers_and_volumes_destroyed=True,
                         provider_requests=0))

    def test_actual_native_loader_failure_quarantines_workspace_and_service_targets(self):
        self.capture_joint()
        bundle = self.target_bundle()
        original = bundle.workspace.rpc
        def corrupt_before_native_load(operation, **body):
            if operation == "native_probe":
                code = "from pathlib import Path; Path('/workspace/.yhc/transcripts/" + self.session + ".jsonl').write_text('corrupt native record\\n')"
                self.docker("exec", bundle.workspace.container, "/usr/local/bin/python3", "-I", "-c", code)
            return original(operation, **body)
        bundle.workspace.rpc = corrupt_before_native_load
        with self.assertRaises(SnapshotError):
            bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        service_ids = [r["container_id"] for r in json.loads((self.control / ("bundle-restore." + self.bundle_digest + ".json")).read_text())["services"]["containers"]]
        rows = json.loads(self.docker("inspect", bundle.workspace.container, *service_ids))
        self.assertTrue(all(not r["State"]["Running"] for r in rows))
        self.assertFalse((self.control / ("activation." + self.service_digest + ".json")).exists())


if __name__ == "__main__":
    unittest.main()
