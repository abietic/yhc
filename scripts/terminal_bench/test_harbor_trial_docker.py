"""Opt-in, cached-image-only Docker evidence for the actual Harbor trial path."""

import asyncio
import ipaddress
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from uuid import uuid4

from harbor.agents.base import BaseAgent
from harbor.environments.docker.docker import _sanitize_docker_compose_project_name
from harbor.models.trial.config import AgentConfig, EnvironmentConfig, TaskConfig, TrialConfig
from harbor.models.task.config import EnvironmentConfig as TaskEnvironment, NetworkMode, NetworkPolicy
from harbor.models.trial.paths import TrialPaths
from harbor.trial.trial import Trial

from scripts.terminal_bench.harbor_trial import IsolatedDockerEnvironment, IsolatedVerifierTrial, collect_verifier_proofs


class FixtureAgent(BaseAgent):
    @staticmethod
    def name():
        return "offline-verifier-fixture"

    def version(self):
        return "1"

    async def setup(self, environment):
        pass

    async def run(self, instruction, environment, context):
        # Trusted deterministic fixture, never a model-produced host program.
        await environment.exec("mkdir -p /workspace; printf '%s' '" + instruction.strip()
                               + "' > /workspace/proof")


@unittest.skipUnless(os.environ.get("YHC_BENCH_DOCKER_TEST") == "1", "explicit local Docker opt-in required")
class DockerTrialTests(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def setUpClass(cls):
        cls.base = os.environ["YHC_BENCH_CACHED_IMAGE"]
        # This must already be an immutable cached image. No pull or downloads.
        assert cls.base.startswith("sha256:")
        info = json.loads(cls.docker("image", "inspect", cls.base))[0]
        platform = info["Os"] + "/" + info["Architecture"]
        cls.temporary = tempfile.TemporaryDirectory(prefix="yhc-verifier-test-")
        cls.root = Path(cls.temporary.name).resolve()
        build = cls.root / "build"
        build.mkdir()
        (build / "test.sh").write_text("""#!/bin/sh
set -eu
test ! -e /sys/class/net/eth0
value=$(cat /workspace/proof)
if test "$value" = grader-failure; then exit 7; fi
mkdir -p /logs/verifier
if test "$value" = zero; then printf '0\\n'; else printf '1\\n'; fi > /logs/verifier/reward.txt
""")
        # Create from the local image ID and copy the trusted fixture into a
        # never-started container. BuildKit may resolve FROM through a registry
        # even with --pull=false; this path cannot pull or run build steps.
        container = cls.docker("create", "--network", "none", "--pull", "never", "--platform", platform,
                               cls.base)
        try:
            cls.docker("cp", str(build), container + ":/tests")
            cls.image = cls.docker("commit", "--change", "ENTRYPOINT []", "--change",
                                   'CMD ["sh", "-c", "sleep infinity"]', container)
        finally:
            cls.docker("rm", container)

    @classmethod
    def tearDownClass(cls):
        if getattr(cls, "image", None):
            cls.docker("image", "rm", cls.image)
        cls.temporary.cleanup()

    @staticmethod
    def docker(*args):
        return subprocess.check_output(["docker", *args], text=True, stderr=subprocess.PIPE, timeout=180).strip()

    @unittest.skipUnless(os.environ.get("YHC_BENCH_CACHED_REFERENCE"), "explicit cached digest reference required")
    async def test_repository_digest_starts_with_the_cached_content_id(self):
        reference = os.environ["YHC_BENCH_CACHED_REFERENCE"]
        expected = json.loads(self.docker("image", "inspect", reference))[0]["Id"]
        root = self.root / 'repository-reference'
        directory = root / 'environment'
        directory.mkdir(parents=True)
        paths = TrialPaths(root / 'trial')
        config = TaskEnvironment(docker_image=reference)
        environment = IsolatedDockerEnvironment(environment_dir=directory, environment_name='generic-pinned-image',
            session_id='tb4-verifier-reference-' + uuid4().hex[:12], trial_paths=paths,
            task_env_config=config, mounts=Trial._verifier_env_mounts(SimpleNamespace(paths=paths), config),
            network_policy=NetworkPolicy(network_mode=NetworkMode.NO_NETWORK), verifier_proof_dir=str(root / 'proofs'))
        try:
            await environment.start(False)
            proof = json.loads(environment.verifier_proof_path.read_text())
            self.assertEqual(proof['image_id'], expected)
            self.assertEqual(proof['image_reference'], reference)
            self.assertEqual(proof['network_mode'], 'none')
        finally:
            await environment.stop(delete=True)

    @unittest.skipUnless(os.environ.get('YHC_BENCH_DOCKER_UID_TEST') == '1',
                         'explicit cached Python/nobody image opt-in required')
    async def test_unprivileged_child_cannot_access_private_verifier_logs(self):
        root = self.root / 'private-log-permissions'
        directory = root / 'environment'
        directory.mkdir(parents=True)
        paths = TrialPaths(root / 'trial')
        config = TaskEnvironment(docker_image=self.base)
        environment = IsolatedDockerEnvironment(environment_dir=directory, environment_name='generic-private-logs',
            session_id='tb4-verifier-private-' + uuid4().hex[:12], trial_paths=paths,
            task_env_config=config, mounts=Trial._verifier_env_mounts(SimpleNamespace(paths=paths), config),
            network_policy=NetworkPolicy(network_mode=NetworkMode.NO_NETWORK), verifier_proof_dir=str(root / 'proofs'))
        child = """import os,pathlib,json
p=pathlib.Path('/logs/verifier');row=dict(uid=os.getuid())
try:(p/'owned-probe').read_text();row['read']=True
except PermissionError:row['read']=False
try:(p/'untrusted-probe').write_text('x');row['write']=True
except PermissionError:row['write']=False
print(json.dumps(row))
"""
        parent = ("import os,pathlib,pwd,subprocess,sys; p=pathlib.Path('/logs/verifier');"
                  "p.mkdir(parents=True,exist_ok=True);p.chmod(0o700);(p/'owned-probe').write_text('owned');"
                  "n=pwd.getpwnam('nobody')\n"
                  "def drop():\n os.setgroups([])\n os.setgid(n.pw_gid)\n os.setuid(n.pw_uid)\n"
                  "r=subprocess.run([sys.executable,'-c'," + repr(child) + "],preexec_fn=drop,"
                  "capture_output=True,text=True,check=True);print(r.stdout,end='')")
        try:
            await environment.start(False)
            result = await environment.exec('python3 -c ' + shlex.quote(parent), user='root')
            self.assertEqual(result.return_code, 0, result.stderr)
            observed = json.loads(result.stdout)
            self.assertNotEqual(observed['uid'], 0)
            self.assertFalse(observed['read'], observed)
            self.assertFalse(observed['write'], observed)
            self.assertFalse((paths.verifier_dir / 'owned-probe').exists())
            paths.verifier_dir.mkdir(parents=True, exist_ok=True)
            await environment.download_dir('/logs/verifier', paths.verifier_dir)
            self.assertEqual((paths.verifier_dir / 'owned-probe').read_text(), 'owned')
            self.assertFalse((paths.verifier_dir / 'untrusted-probe').exists())
        finally:
            await environment.stop(delete=True)

    async def test_actual_upload_grading_and_failure_are_isolated(self):
        from harbor.trial.artifact_handler import ArtifactHandler
        upload = ArtifactHandler.upload_artifacts
        for mode in ("ok", "zero", "grader-failure"):
            with self.subTest(mode=mode):
                root = self.root / mode
                task = root / "task"
                for name in ("environment", "tests"):
                    (task / name).mkdir(parents=True)
                (task / "instruction.md").write_text(mode)
                (task / "environment/Dockerfile").write_text("FROM " + self.base + "\n")
                networks = self.docker("network", "ls", "-q").split()
                existing = json.loads(self.docker("network", "inspect", *networks)) if networks else []
                used = [ipaddress.ip_network(c["Subnet"]) for row in existing
                        for c in (row.get("IPAM", {}).get("Config") or []) if c.get("Subnet")]
                subnet = next(n for n in ipaddress.ip_network("10.253.248.0/21").subnets(new_prefix=26)
                              if not any(u.version == 4 and n.overlaps(u) for u in used))
                (task / "environment/docker-compose.yaml").write_text(
                    "networks:\n  default:\n    ipam:\n      config:\n        - subnet: " + str(subnet) + "\n")
                (task / "task.toml").write_text(
                    'schema_version="2.0"\nartifacts=["/workspace"]\n'
                    '[task]\nname="local/verifier-contract"\n'
                    '[environment]\ndocker_image="' + self.base + '"\ncpus=1\nmemory_mb=512\n'
                    '[verifier]\nenvironment_mode="separate"\ntimeout_sec=30\n'
                    '[verifier.environment]\ndocker_image="' + self.image + '"\ncpus=1\nmemory_mb=512\n')
                sample = "tb4-verifier-pf-" + uuid4().hex[:12]
                if mode == "zero":
                    sample += "-long-identity-" + "x" * 50
                cfg = TrialConfig(task=TaskConfig(path=task), trial_name=sample, trials_dir=root / "trials",
                    agent=AgentConfig(import_path=__name__ + ":FixtureAgent"),
                    environment=EnvironmentConfig(type="docker", delete=True,
                        import_path="scripts.terminal_bench.harbor_trial:IsolatedDockerEnvironment",
                        kwargs={"verifier_proof_dir": str(root / "proofs")}))
                seen = []

                async def checked_upload(handler, target_env, **kwargs):
                    proof = json.loads(target_env.verifier_proof_path.read_text())
                    self.assertEqual(proof["stage"], "before_artifact_upload")
                    info = json.loads(self.docker("inspect", proof["container_id"]))[0]
                    self.assertEqual(info["HostConfig"]["NetworkMode"], "none")
                    self.assertFalse((await target_env.exec("test -e /workspace/proof")).return_code == 0)
                    seen.append(proof)
                    return await upload(handler, target_env, **kwargs)

                trial = await IsolatedVerifierTrial.create(cfg)
                try:
                    with patch.object(ArtifactHandler, "upload_artifacts", checked_upload):
                        result = await asyncio.wait_for(trial.run(), timeout=180)
                    self.assertEqual(len(seen), 1, str(result.exception_info))
                    self.assertEqual(collect_verifier_proofs(trial), seen)
                    if mode == "grader-failure":
                        self.assertIsNotNone(result.exception_info)
                        self.assertIsNone(result.verifier_result)
                    else:
                        self.assertIsNone(result.exception_info)
                        self.assertEqual(result.verifier_result.rewards["reward"], float(mode != "zero"))
                finally:
                    # Only this UUID's resources; never prune shared Docker state.
                    sessions = (trial.agent_environment.session_id, trial._separate_verifier_session_id("trial"))
                    for session in sessions:
                        project = _sanitize_docker_compose_project_name(session)
                        ids = self.docker("ps", "-aq", "--filter", "label=com.docker.compose.project=" + project).split()
                        for identity in ids:
                            self.docker("rm", "-f", identity)
                        ids = self.docker("network", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project).split()
                        for identity in ids:
                            self.docker("network", "rm", identity)


if __name__ == "__main__":
    unittest.main()
