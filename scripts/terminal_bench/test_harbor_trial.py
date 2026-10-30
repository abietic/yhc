"""Pinned Harbor's actual create path; no Docker or model calls."""

import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import AsyncMock, patch
from types import SimpleNamespace

from harbor.models.task.config import NetworkMode, NetworkPolicy, EnvironmentConfig as TaskEnvironment, TaskConfig as TaskDefinition
from harbor.models.trial.paths import TrialPaths
from harbor.models.task.task import Task
from harbor.models.trial.config import TrialConfig, TaskConfig, EnvironmentConfig
from harbor.trial.trial import Trial

from scripts.terminal_bench.harbor_trial import (
    IsolatedVerifierTrial, IsolatedDockerEnvironment, collect_verifier_proofs, isolated_task,
)
from scripts.terminal_bench.trial_policy import PolicyError


IMAGE = "sha256:" + "1" * 64


class TaskPolicyTests(unittest.TestCase):
    def test_repository_digest_keeps_task_and_agent_configuration(self):
        reference = 'registry.example:5000/team/grader:v1@sha256:' + '1' * 64
        cfg = TaskDefinition.model_validate({
            'environment': {'docker_image': IMAGE},
            'verifier': {'environment_mode': 'separate', 'environment': {'docker_image': reference}},
        })
        task = SimpleNamespace(config=cfg, instruction='ordinary task', paths=object())
        original = cfg.model_dump()
        copied = isolated_task(task)
        self.assertEqual(copied.config.verifier.environment.docker_image, reference)
        self.assertEqual(copied.config.environment, cfg.environment)
        self.assertEqual(cfg.model_dump(), original)
        self.assertIs(copied.paths, task.paths)

    def test_mutable_or_malformed_image_references_are_not_qualified(self):
        for reference in ('registry.example/grader:latest', 'grader', '--image@' + IMAGE,
                          'https://registry.example/grader@' + IMAGE, 'grader@sha256:123',
                          'grader@@' + IMAGE, 'grader@' + IMAGE + '\n'):
            cfg = TaskDefinition.model_validate({'verifier': {'environment_mode': 'separate',
                                                'environment': {'docker_image': reference}}})
            with self.subTest(reference=reference), self.assertRaises(PolicyError):
                isolated_task(SimpleNamespace(config=cfg))

    def test_inherited_steps_are_copied_without_changing_agent_or_files(self):
        cfg = TaskDefinition.model_validate({
            "environment": {"docker_image": IMAGE},
            "verifier": {"environment_mode": "separate"},
            "steps": [{"name": "first"}, {"name": "second", "verifier": {
                "environment": {"docker_image": "sha256:" + "2" * 64}}}],
        })
        task = SimpleNamespace(config=cfg, instruction="ordinary task", paths=object())
        original = cfg.model_dump()
        copied = isolated_task(task)
        self.assertEqual(task.config.model_dump(), original)
        self.assertIs(copied.paths, task.paths)
        self.assertEqual(copied.instruction, task.instruction)
        self.assertEqual(copied.config.environment, task.config.environment)
        for step in copied.config.steps:
            self.assertEqual(step.verifier.network_mode, NetworkMode.NO_NETWORK)
            self.assertEqual(step.verifier.environment.network_mode, NetworkMode.NO_NETWORK)


class ActualTrialCreateTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.task = self.root / "task"
        for name in ("environment", "tests", "solution"):
            (self.task / name).mkdir(parents=True)
        (self.task / "instruction.md").write_text("Generic local fixture.\n")
        (self.task / "environment/Dockerfile").write_text("FROM scratch\n")
        (self.task / "tests/test.sh").write_text("#!/bin/sh\nexit 0\n")
        (self.task / "task.toml").write_text(
            'schema_version = "2.0"\n[task]\nname="local/policy-fixture"\n'
            '[environment]\ndocker_image="' + IMAGE + '"\n'
            '[verifier]\nenvironment_mode="separate"\n')

    def config(self, **kwargs):
        return TrialConfig(task=TaskConfig(path=self.task), trial_name="tb4-policy-create",
                           trials_dir=self.root / "trials", environment=EnvironmentConfig(
                               type="docker", import_path="scripts.terminal_bench.harbor_trial:IsolatedDockerEnvironment",
                               kwargs={"verifier_proof_dir": str(self.root / "proofs")}), **kwargs)

    async def test_factory_uses_policy_before_constructing_native_runtime(self):
        original = hashlib.sha256((self.task / "task.toml").read_bytes()).hexdigest()
        # Stop after the actual classmethod loads and qualifies the task.
        with patch("harbor.trial.single_step.SingleStepTrial", side_effect=RuntimeError("qualified")) as constructor:
            with self.assertRaisesRegex(RuntimeError, "qualified"):
                await IsolatedVerifierTrial.create(self.config())
        task = constructor.call_args.kwargs["_task"]
        self.assertEqual(task.config.verifier.network_mode, NetworkMode.NO_NETWORK)
        self.assertEqual(task.config.verifier.environment.network_mode, NetworkMode.NO_NETWORK)
        self.assertEqual(task.config.environment.network_mode, NetworkMode.PUBLIC)
        self.assertEqual(hashlib.sha256((self.task / "task.toml").read_bytes()).hexdigest(), original)
        self.assertEqual(Task(task_dir=self.task).config.verifier.network_mode, None)

    async def test_shared_verifier_never_constructs_native_runtime(self):
        path = self.task / "task.toml"
        path.write_text(path.read_text().replace('environment_mode="separate"', 'environment_mode="shared"'))
        with patch("harbor.trial.single_step.SingleStepTrial") as constructor:
            with self.assertRaisesRegex(PolicyError, "shared_verifier_unqualified"):
                await IsolatedVerifierTrial.create(self.config())
        constructor.assert_not_called()

    async def test_stock_adapter_cannot_silently_bypass_policy(self):
        cfg = self.config()
        cfg.environment.import_path = "harbor.environments.docker.docker:DockerEnvironment"
        with patch.object(Trial, "_load_task") as load:
            with self.assertRaisesRegex(PolicyError, "isolated_verifier_adapter_required"):
                await IsolatedVerifierTrial.create(cfg)
        load.assert_not_called()

    async def test_agent_visible_proof_path_is_rejected_before_start(self):
        paths = TrialPaths(self.root / "trials" / "fresh")
        with self.assertRaisesRegex(PolicyError, "verifier_proof_exposed"):
            IsolatedDockerEnvironment(environment_dir=self.task / "environment", environment_name="fixture",
                session_id="fresh__env", trial_paths=paths, task_env_config=TaskEnvironment(docker_image=IMAGE),
                network_policy=NetworkPolicy(), verifier_proof_dir=str(paths.agent_dir.resolve() / "proofs"))

    async def test_long_ids_use_the_frameworks_actual_session_identity(self):
        import json
        config = self.config()
        config.trial_name = "long-trial-" + "x" * 100
        trial = SimpleNamespace(config=config, task=Task(task_dir=self.task))
        trial._separate_verifier_session_id = lambda key: Trial._separate_verifier_session_id(trial, key)
        session = trial._separate_verifier_session_id("trial")
        self.assertLess(len(session), len(config.trial_name))
        directory = Path(config.environment.kwargs["verifier_proof_dir"])
        directory.mkdir()
        proof = dict(session_id=session, network_mode="none", compose_verified=True,
                     stage="before_artifact_upload")
        path = directory / (hashlib.sha256(session.encode()).hexdigest() + ".json")
        path.write_text(json.dumps(proof))
        self.assertEqual(collect_verifier_proofs(trial), [proof])
        for broken in ("", '{"session_id":', "[]", "null"):
            path.write_text(broken)
            self.assertEqual(collect_verifier_proofs(trial), [])

    def isolated_environment(self, reference):
        paths = TrialPaths(self.root / 'trials' / 'digest-fixture')
        return IsolatedDockerEnvironment(environment_dir=self.task / 'environment', environment_name='fixture',
            session_id='digest-fixture__verifier', trial_paths=paths, task_env_config=TaskEnvironment(docker_image=reference),
            network_policy=NetworkPolicy(network_mode=NetworkMode.NO_NETWORK), verifier_proof_dir=str(self.root / 'proofs'))

    async def test_named_digest_is_resolved_to_cached_content_id_before_attestation(self):
        from harbor.environments.docker.docker import DockerEnvironment
        reference = 'registry.example/grader@' + IMAGE
        content_id = 'sha256:' + '2' * 64
        env = self.isolated_environment(reference)
        env._compose_verified = True
        row = dict(Id='container', Image=content_id, State={'Running': True},
                   HostConfig={'NetworkMode': 'none'}, NetworkSettings={'Networks': {'none': {}}},
                   Mounts=[dict(Type='bind', Source=env._log_dir, Destination='/logs/verifier', RW=True)])
        try:
            with patch.object(env, '_inspect', AsyncMock(side_effect=[{'Id': content_id, 'Os': 'linux'}, row])) as inspect, \
                 patch.object(DockerEnvironment, 'start', AsyncMock()), \
                 patch.object(DockerEnvironment, '_run_docker_compose_command', AsyncMock(return_value=SimpleNamespace(stdout='container\n'))):
                await env.start(False)
            self.assertEqual(inspect.await_args_list[0].args, ('image', reference))
            import json
            proof = json.loads(env.verifier_proof_path.read_text())
            self.assertEqual(proof['image_id'], content_id)
            self.assertEqual(proof['image_reference'], reference)
        finally:
            env._isolation_overlay_dir.cleanup()

    async def test_invalid_cached_image_stops_before_container_start(self):
        from harbor.environments.docker.docker import DockerEnvironment
        for image in ({'Id': IMAGE, 'Os': 'windows'}, {'Id': 'not-an-image-id', 'Os': 'linux'}, {'Id': None, 'Os': 'linux'},
                      {'Id': 'sha256:' + '2' * 64, 'Os': 'linux'}):
            env = self.isolated_environment(IMAGE)
            try:
                with patch.object(env, '_inspect', AsyncMock(return_value=image)), \
                     patch.object(DockerEnvironment, 'start', AsyncMock()) as start:
                    with self.subTest(image=image), self.assertRaises(PolicyError):
                        await env.start(False)
                    start.assert_not_awaited()
            finally:
                env._isolation_overlay_dir.cleanup()


if __name__ == "__main__":
    unittest.main()
