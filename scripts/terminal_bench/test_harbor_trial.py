"""Pinned Harbor's actual create path; no Docker or model calls."""

import asyncio
import hashlib
import os
from pathlib import Path
import tempfile
import sys
import unittest
from unittest.mock import AsyncMock, patch
from types import SimpleNamespace

from harbor.models.task.config import NetworkMode, NetworkPolicy, EnvironmentConfig as TaskEnvironment, TaskConfig as TaskDefinition
from harbor.models.trial.paths import TrialPaths
from harbor.models.task.task import Task
from harbor.models.trial.config import TrialConfig, TaskConfig, EnvironmentConfig
from harbor.trial.trial import Trial
from harbor.environments.docker.docker import DockerEnvironment

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
        proof = dict(version=2, session_id=session, network_mode="none", compose_verified=True,
                     stage="before_artifact_upload", log_storage='container_private')
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

    async def test_private_logs_do_not_mount_the_framework_host_directory(self):
        paths = TrialPaths(self.root / 'trials' / 'private-logs')
        env = IsolatedDockerEnvironment(environment_dir=self.task / 'environment', environment_name='fixture',
            session_id='private-logs__verifier', trial_paths=paths, task_env_config=TaskEnvironment(docker_image=IMAGE),
            mounts=Trial._verifier_env_mounts(SimpleNamespace(paths=paths), TaskEnvironment(docker_image=IMAGE)),
            network_policy=NetworkPolicy(network_mode=NetworkMode.NO_NETWORK), verifier_proof_dir=str(self.root / 'proofs'))
        try:
            self.assertFalse(env.capabilities.mounted)
            self.assertEqual(env._mounts, [])
        finally:
            env._isolation_overlay_dir.cleanup()

    async def test_host_bound_legacy_proof_does_not_qualify_private_logs(self):
        import json
        config = self.config()
        trial = SimpleNamespace(config=config, task=Task(task_dir=self.task))
        trial._separate_verifier_session_id = lambda key: Trial._separate_verifier_session_id(trial, key)
        session = trial._separate_verifier_session_id('trial')
        directory = Path(config.environment.kwargs['verifier_proof_dir'])
        directory.mkdir()
        proof = dict(version=1, session_id=session, network_mode='none', compose_verified=True,
                     stage='before_artifact_upload')
        (directory / (hashlib.sha256(session.encode()).hexdigest() + '.json')).write_text(json.dumps(proof))
        self.assertEqual(collect_verifier_proofs(trial), [])

    async def test_unexpected_mounts_are_rejected_instead_of_silently_removed(self):
        paths = TrialPaths(self.root / 'trials' / 'foreign-mount')
        for mount in ({'type': 'bind', 'source': '/foreign', 'target': '/logs/verifier'},
                      {'type': 'bind', 'source': str(paths.verifier_dir.resolve()), 'target': '/foreign'},
                      {'type': 'volume', 'source': 'foreign', 'target': '/logs/verifier'}):
            with self.subTest(mount=mount), self.assertRaisesRegex(PolicyError, 'verifier_mount_request_unqualified'):
                IsolatedDockerEnvironment(environment_dir=self.task / 'environment', environment_name='fixture',
                    session_id='foreign-mount__verifier', trial_paths=paths, task_env_config=TaskEnvironment(docker_image=IMAGE),
                    mounts=[mount], network_policy=NetworkPolicy(network_mode=NetworkMode.NO_NETWORK),
                    verifier_proof_dir=str(self.root / 'proofs'))

    async def test_public_agent_mounts_keep_their_original_contract(self):
        paths = TrialPaths(self.root / 'trials' / 'public-agent')
        mounts = [{'type': 'bind', 'source': str(paths.agent_dir.resolve()), 'target': '/logs/agent'}]
        env = IsolatedDockerEnvironment(environment_dir=self.task / 'environment', environment_name='fixture',
            session_id='public-agent', trial_paths=paths, task_env_config=TaskEnvironment(docker_image=IMAGE),
            mounts=mounts, network_policy=NetworkPolicy(), verifier_proof_dir=str(self.root / 'proofs'))
        self.assertTrue(env.capabilities.mounted)
        self.assertEqual(env._mounts, mounts)

    async def test_named_digest_is_resolved_to_cached_content_id_before_attestation(self):
        from harbor.environments.docker.docker import DockerEnvironment
        reference = 'registry.example/grader@' + IMAGE
        content_id = 'sha256:' + '2' * 64
        env = self.isolated_environment(reference)
        env._compose_verified = True
        row = dict(Id='container', Image=content_id, State={'Running': True},
                   HostConfig={'NetworkMode': 'none'}, NetworkSettings={'Networks': {'none': {}}},
                   Mounts=[])
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


@unittest.skipUnless(os.name == 'posix', 'ready-barrier process fixture uses POSIX signals')
class ProcessCollectionTests(unittest.IsolatedAsyncioTestCase):
    async def paused_process(self):
        process = await asyncio.create_subprocess_exec(sys.executable, '-c',
            'import signal;print("READY",flush=True);signal.pause()',
            stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE)

        async def cleanup():
            if process.returncode is None:
                process.kill()
            await process.communicate()
        self.addAsyncCleanup(cleanup)
        self.assertEqual(await asyncio.wait_for(process.stdout.readline(), 5), b'READY\n')
        return process

    def observed(self, process, started):
        class Observed:
            def __getattr__(self, name):
                return getattr(process, name)

            async def communicate(self, input=None):
                started.set()
                return await process.communicate(input=input)
        return Observed()

    async def test_cancelled_buffered_client_is_reaped_before_cancel_returns(self):
        process = await self.paused_process()
        started = asyncio.Event()
        task = asyncio.create_task(IsolatedDockerEnvironment._collect_buffered_output(
            self.observed(process, started), timeout_sec=None))
        await asyncio.wait_for(started.wait(), 5)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        self.assertIsNotNone(process.returncode, 'collector returned while its client was still live')

    async def test_repeated_cancel_during_cleanup_still_joins_the_client(self):
        process = await self.paused_process()
        started, cleaning, release = asyncio.Event(), asyncio.Event(), asyncio.Event()
        terminate = DockerEnvironment._terminate_process

        async def delayed_cleanup(client):
            cleaning.set()
            await release.wait()
            await terminate(client)
        try:
            with patch.object(DockerEnvironment, '_terminate_process', delayed_cleanup):
                task = asyncio.create_task(IsolatedDockerEnvironment._collect_buffered_output(
                    self.observed(process, started), timeout_sec=None))
                await asyncio.wait_for(started.wait(), 5)
                task.cancel()
                await asyncio.wait_for(cleaning.wait(), 5)
                task.cancel()
                release.set()
                with self.assertRaises(asyncio.CancelledError):
                    await task
            self.assertIsNotNone(process.returncode)
        finally:
            release.set()
            if not task.done():
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)

    async def test_explicit_command_timeout_retains_failure_and_reaps_client(self):
        process = await self.paused_process()
        with self.assertRaisesRegex(RuntimeError, 'Command timed out'):
            await IsolatedDockerEnvironment._collect_buffered_output(process, timeout_sec=0.05)
        self.assertIsNotNone(process.returncode)

    async def test_collection_error_preserves_error_after_reaping_client(self):
        process = await self.paused_process()
        started = asyncio.Event()
        client = self.observed(process, started)

        async def broken_communication(input=None):
            raise OSError('controlled read failure')
        client.communicate = broken_communication
        with self.assertRaisesRegex(OSError, 'controlled read failure'):
            await IsolatedDockerEnvironment._collect_buffered_output(client, timeout_sec=None)
        self.assertIsNotNone(process.returncode)

    async def test_client_exit_race_does_not_replace_original_cancel(self):
        process = await self.paused_process()
        started = asyncio.Event()

        async def already_exited(client):
            client.terminate()
            await client.wait()
            raise ProcessLookupError('controlled exit race')
        with patch.object(DockerEnvironment, '_terminate_process', already_exited):
            task = asyncio.create_task(IsolatedDockerEnvironment._collect_buffered_output(
                self.observed(process, started), timeout_sec=None))
            await asyncio.wait_for(started.wait(), 5)
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
        self.assertIsNotNone(process.returncode)

    async def test_unverified_cleanup_is_bounded_and_never_returns_normal_cancel(self):
        process = await self.paused_process()
        started, cleaning = asyncio.Event(), asyncio.Event()

        async def never_finishes(_client):
            cleaning.set()
            await asyncio.Event().wait()
        with patch.object(DockerEnvironment, '_terminate_process', never_finishes):
            task = asyncio.create_task(IsolatedDockerEnvironment._collect_buffered_output(
                self.observed(process, started), timeout_sec=None))
            await asyncio.wait_for(started.wait(), 5)
            task.cancel()
            await asyncio.wait_for(cleaning.wait(), 5)
            with self.assertRaisesRegex(PolicyError, 'managed_client_cleanup_unverified'):
                await asyncio.wait_for(task, 8)
        # The fixture's own cleanup kills it; failed teardown grants no success.
        self.assertIsNone(process.returncode)

    async def test_normal_nonzero_exit_preserves_output_and_status(self):
        process = await asyncio.create_subprocess_exec(sys.executable, '-c',
            'import sys;print("output");print("error",file=sys.stderr);sys.exit(7)',
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        result = await IsolatedDockerEnvironment._collect_buffered_output(process, timeout_sec=5)
        self.assertEqual((result.return_code, result.stdout, result.stderr), (7, 'output\n', 'error\n'))

    async def test_streamed_collector_already_has_matching_cancel_cleanup(self):
        process = await self.paused_process()
        entered = asyncio.Event()

        async def on_output(_line, _stream):
            entered.set()
        # paused_process consumed READY; send an owned callback barrier instead
        # of waiting for another child output that can never arrive.
        async def observed_stream():
            entered.set()
            return await IsolatedDockerEnvironment._collect_streamed_output(
                process, timeout_sec=None, on_output=on_output)
        task = asyncio.create_task(observed_stream())
        await asyncio.wait_for(entered.wait(), 5)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        self.assertIsNotNone(process.returncode)


if __name__ == "__main__":
    unittest.main()
