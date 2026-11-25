"""Opt-in real Harbor exclusion collection and isolated byte-chain calibration."""
import asyncio
import hashlib
import json
import logging
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
import uuid

from scripts.terminal_bench.artifact_closeout import CloseoutError, attest_sidecar_artifact, verify_artifacts
from scripts.terminal_bench.artifact_integrity import attest_docker_artifact


class FixtureEnvironment:
    """Only trusted fixture commands; the real Harbor collector owns filtering."""
    capabilities = SimpleNamespace(mounted=False)

    def __init__(self, docker, cid, *, sidecars=None):
        self.docker, self.cid = docker, cid
        self.sidecars = sidecars or {}
        self.locale = None

    def _container(self, service):
        return self.cid if service in (None, 'main') else self.sidecars[service]

    async def service_exec(self, command, *, service=None, **kwargs):
        env = ['--env', 'LC_ALL=' + self.locale] if self.locale else []
        p = await asyncio.to_thread(self.docker, 'exec', *env, self._container(service),
                                    '/bin/sh', '-c', command, check=False)
        return SimpleNamespace(return_code=p.returncode, stdout=p.stdout, stderr=p.stderr)

    async def service_is_dir(self, source, **kwargs):
        return (await self.service_exec('test -d ' + shlex.quote(source), **kwargs)).return_code == 0

    async def service_download_file(self, *, source_path, target_path, service=None):
        await asyncio.to_thread(self.docker, 'cp', self._container(service) + ':' + str(source_path),
                                str(target_path))

    async def service_download_dir(self, *, source_dir, target_dir, **kwargs):
        await asyncio.to_thread(self.docker, 'cp', self.cid + ':' + source_dir + '/.', str(target_dir))

    async def service_download_dir_with_exclusions(self, **kwargs):
        from harbor.environments.base import BaseEnvironment
        await BaseEnvironment._download_dir_with_exclusions_impl(self, **kwargs)

    async def empty_dirs(self, paths, **kwargs):
        for p in paths:
            assert p.startswith('/tmp/yhc-artifact-')
            result = await self.service_exec('rm -rf -- ' + shlex.quote(p) + ' && mkdir -p -- ' + shlex.quote(p))
            assert result.return_code == 0, 'fixture target cleanup failed'

    async def upload_dir(self, *, source_dir, target_dir):
        await asyncio.to_thread(self.docker, 'cp', str(source_dir) + '/.', self.cid + ':' + target_dir)

    async def upload_file(self, *, source_path, target_path):
        await asyncio.to_thread(self.docker, 'cp', str(source_path), self.cid + ':' + str(target_path))

    async def ensure_dirs(self, paths, **kwargs):
        # This control only uploads its file to an existing fixture path.
        assert list(paths) == ['/tmp']
        assert (await self.service_exec('test -d /tmp')).return_code == 0

@unittest.skipUnless(os.environ.get('YHC_ARTIFACT_EXCLUSIONS_DOCKER_TEST') == '1',
                     'explicit cached GNU tar / Harbor opt-in required')
class ArtifactExclusionsDockerTests(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def docker(cls, *args, check=True):
        result = subprocess.run(['docker', *args], capture_output=True, text=True, timeout=90)
        if check and result.returncode:
            raise AssertionError('owned artifact fixture operation failed: ' + result.stderr[:300])
        return result

    @classmethod
    def setUpClass(cls):
        cls.owner = uuid.uuid4().hex
        cls.containers = []
        cls.addClassCleanup(cls.remove_owned)
        image = os.environ['YHC_ARTIFACT_EXCLUSIONS_TEST_IMAGE']
        assert re.fullmatch(r'sha256:[0-9a-f]{64}', image), 'exact cached image ID required'
        row, = json.loads(cls.docker('image', 'inspect', image).stdout)
        assert row['Os'] == 'linux'
        for _ in range(3):
            cid = cls.docker('create', '--pull=never', '--platform', 'linux/' + row['Architecture'],
                '--network', 'none', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                '--user', str(os.getuid()),
                '--memory', '256m', '--cpus', '1', '--pids-limit', '64',
                '--label', 'yhc.artifact-owner=' + cls.owner,
                '--entrypoint', '/bin/sleep', image, 'infinity').stdout.strip()
            cls.containers.append(cid)
            cls.docker('start', cid)
            actual, = json.loads(cls.docker('inspect', cid).stdout)
            assert actual['Image'] == image and actual['Mounts'] == []
            assert actual['HostConfig']['NetworkMode'] == 'none'
            assert actual['HostConfig']['CapDrop'] == ['ALL']
            assert 'no-new-privileges' in actual['HostConfig']['SecurityOpt']
            assert 'GNU tar' in cls.docker('exec', cid, 'tar', '--version').stdout

    @classmethod
    def remove_owned(cls):
        for cid in cls.containers:
            actual, = json.loads(cls.docker('inspect', cid).stdout)
            assert actual['Config']['Labels']['yhc.artifact-owner'] == cls.owner
            cls.docker('rm', '--force', cid)

    async def asyncSetUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='yhc-artifact-exclusion-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.guest = '/tmp/yhc-artifact-' + uuid.uuid4().hex
        self.fixture = self.root / 'fixture'
        app = self.fixture / 'app'
        app.mkdir(parents=True)
        for name, data in {'keep.py': b'keep', 'module.pyc': b'cache',
                           'frontend/node_modules/pkg.js': b'cache',
                           'frontend/keep.py': b'front', 'backend/cache1.tmp': b'cache',
                           'node_modules-extra/keep.js': b'keep',
                           'node_modules/pkg.js': b'cache',
                           'file with space\n雪.txt': b'bytes', '雪.txt': b'unicode'}.items():
            p = app / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(data)
        (app / 'empty').mkdir()
        (self.fixture / 'native.jsonl').write_text('{}\n')
        self.docker('cp', str(self.fixture), self.containers[0] + ':' + self.guest)
        remote = '/tmp/yhc-capture-' + uuid.uuid4().hex + '.tar.gz'
        self.docker('exec', self.containers[0], 'tar', '-czf', remote, '-C', '/', self.guest.lstrip('/'))
        self.archive = self.root / 'capture.tar.gz'
        self.docker('cp', self.containers[0] + ':' + remote, str(self.archive))
        self.source = self.guest + '/app'
        self.sender = FixtureEnvironment(self.docker, self.containers[0])
        self.receiver = FixtureEnvironment(self.docker, self.containers[1])

    async def collect(self, patterns, label):
        from harbor.models.task.config import ArtifactConfig
        from harbor.trial.artifact_handler import ArtifactHandler
        folder = self.root / label
        handler = ArtifactHandler(artifacts=[ArtifactConfig(source=self.source, exclude=patterns)],
                                  logger=logging.getLogger('artifact-exclusion-fixture'))
        manifest = await handler.download_artifacts(self.sender, folder,
            source_artifacts_dir=PurePosixPath('/logs/artifacts'))
        await handler.upload_artifacts(self.receiver, folder,
            source_artifacts_dir=PurePosixPath('/logs/artifacts'),
            target_artifacts_dir=PurePosixPath('/logs/artifacts'))
        collected = folder / self.source.lstrip('/')
        projection = await attest_docker_artifact(collected, self.source, self.containers[1])
        declared = [dict(source=self.source, exclude=patterns)]
        checkpoint = dict(capture='complete', captured_before_grading=True, workspace_in_archive=True,
            native_session_files=[dict(path=self.guest + '/native.jsonl')], cwd=self.guest,
            checkpoint_sha256=hashlib.sha256(self.archive.read_bytes()).hexdigest(), declared_artifacts=declared)
        proof = dict(target=self.source, container_id=self.containers[1], network_mode='none', session_id='fixture__verifier__trial',
            stage='after_upload_before_official_grading', source_and_verifier_bytes_match=True, projection=projection)
        result = verify_artifacts(folder, self.archive, declared, manifest.to_json_data(), checkpoint,
                                  [proof], [proof], session_id='fixture__verifier__trial')
        self.assertTrue(result['artifact_chain_verified'])
        self.assertFalse(result['can_resume_agent'])
        return folder, checkpoint, manifest, proof, declared

    async def test_real_gnu_tar_globs_match_full_capture_projection(self):
        # Each expected tree is obtained by real Harbor + Linux GNU tar, not the
        # helper's matcher. This includes roots, slash-spanning and unanchored globs.
        for i, patterns in enumerate([[], ['node_modules'], ['*.pyc'], ['backend/cache?.tmp'],
            ['front*pkg.js'], ['./frontend/*'], ['*'], ['.'], ['?.txt'],
            ['NODE_MODULES'], ['*a*b*'], ['frontend/*/pkg.?s'],
            ['node_modules', '*.pyc', '*.tmp']]):
            with self.subTest(patterns=patterns):
                await self.collect(patterns, 'case-' + str(i))

    async def test_unverified_c_locale_unicode_matching_stays_blocked(self):
        self.sender.locale = 'C'
        with self.assertRaisesRegex(CloseoutError, '^checkpoint_and_collected_artifacts_differ$'):
            await self.collect(['?.txt'], 'c-locale')

    async def test_uploaded_and_checkpoint_tampering_are_rejected(self):
        folder, checkpoint, manifest, proof, declared = await self.collect(['node_modules', '*.pyc'], 'binding')
        checkpoint['declared_artifacts'] = [dict(source=self.source, exclude=['keep.py'])]
        with self.assertRaises(CloseoutError):
            verify_artifacts(folder, self.archive, declared, manifest.to_json_data(), checkpoint,
                             [proof], [proof], session_id='fixture__verifier__trial')
        target = self.source + '/keep.py'
        self.docker('exec', self.containers[1], '/bin/sh', '-c', 'printf bad > ' + shlex.quote(target))
        with self.assertRaisesRegex(ValueError, 'artifact_bytes_mismatch'):
            await attest_docker_artifact(folder / self.source.lstrip('/'), self.source, self.containers[1])

    async def test_sidecar_file_follows_real_harbor_collection_and_isolated_upload(self):
        from harbor.models.task.config import ArtifactConfig
        from harbor.trial.artifact_handler import ArtifactHandler

        main, verifier, sidecar = self.containers
        path = self.guest + '-snapshot'
        for cid, contents in ((main, 'decoy'), (sidecar, 'snapshot')):
            self.docker('exec', cid, '/bin/sh', '-c', 'printf %s ' + shlex.quote(contents)
                        + ' > ' + shlex.quote(path))
        declared = [self.source, dict(source=path, service='queue')]
        sender = FixtureEnvironment(self.docker, main, sidecars={'queue': sidecar})
        logdir = PurePosixPath('/logs/artifacts')
        handler = ArtifactHandler(artifacts=[self.source, ArtifactConfig(**declared[1])],
                                  logger=logging.getLogger('service-artifact-fixture'))
        folder = self.root / 'sidecar-artifacts'
        manifest = await handler.download_artifacts(sender, folder, source_artifacts_dir=logdir)
        host = folder / path.lstrip('/')
        self.assertEqual(host.read_bytes(), b'snapshot')
        session = 'fixture-sidecar__verifier__trial'
        source_proofs = [await attest_sidecar_artifact(folder, path, 'queue', sidecar, session_id=session)]
        with self.assertRaisesRegex(ValueError, 'artifact_bytes_mismatch'):
            await attest_sidecar_artifact(folder, path, 'queue', main, session_id=session)
        await handler.upload_artifacts(self.receiver, folder, source_artifacts_dir=logdir,
                                       target_artifacts_dir=logdir)
        isolated = dict(container_id=verifier, network_mode='none', session_id=session)
        transfers = [dict(isolated, target=target, stage='after_upload_before_official_grading',
                          source_and_verifier_bytes_match=True,
                          projection=await attest_docker_artifact(folder / target.lstrip('/'), target, verifier))
                     for target in (self.source, path)]
        checkpoint = dict(capture='complete', captured_before_grading=True, workspace_in_archive=True,
                          native_session_files=[dict(path=self.guest + '/native.jsonl')], cwd=self.guest,
                          checkpoint_sha256=hashlib.sha256(self.archive.read_bytes()).hexdigest(),
                          declared_artifacts=declared)

        def verify(collected, rows, containers):
            return verify_artifacts(collected, self.archive, declared, rows, checkpoint, transfers,
                                    [isolated], session_id=session, sidecar_containers=containers,
                                    sidecar_proofs=source_proofs)

        result = verify(folder, manifest.to_json_data(), {'queue': sidecar})
        self.assertEqual(result['sidecar_artifacts'][path], 'queue')
        self.assertFalse(result['can_resume_agent'])
        score = await self.receiver.service_exec('test "$(cat -- ' + shlex.quote(path) + ')" = snapshot')
        self.assertEqual(score.return_code, 0)
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_proof_mismatch'):
            verify(folder, manifest.to_json_data(), {'queue': main})
        attested = source_proofs[0].pop('source_and_collected_bytes_match')
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_proof_mismatch'):
            verify(folder, manifest.to_json_data(), {'queue': sidecar})
        source_proofs[0]['source_and_collected_bytes_match'] = attested

        self.docker('exec', sidecar, 'rm', '--', path)
        handler.begin_collection()
        missing_dir = self.root / 'missing-sidecar-artifacts'
        missing = await handler.download_artifacts(sender, missing_dir, source_artifacts_dir=logdir)
        self.assertFalse((missing_dir / path.lstrip('/')).exists())
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_absence_unverified'):
            verify(missing_dir, missing.to_json_data(), {'queue': sidecar})

    async def test_missing_sidecar_file_is_verified_in_source_and_disconnected_verifier(self):
        from harbor.models.task.config import ArtifactConfig
        from harbor.trial.artifact_handler import ArtifactHandler

        main, verifier, sidecar = self.containers
        path = self.guest + '-missing-snapshot'
        # A same-path decoy in main must not contribute to this declaration.
        self.docker('exec', main, 'touch', path)
        declaration = dict(source=path, service='queue')
        handler = ArtifactHandler(artifacts=[ArtifactConfig(**declaration)],
                                  logger=logging.getLogger('missing-sidecar-fixture'))
        sender = FixtureEnvironment(self.docker, main, sidecars={'queue': sidecar})
        folder = self.root / 'verified-missing-sidecar'
        logdir = PurePosixPath('/logs/artifacts')
        quoted = shlex.quote(path)
        absent = f'test -d /tmp && test ! -e {quoted} && test ! -L {quoted}'
        self.docker('exec', sidecar, '/bin/sh', '-ec', absent)
        manifest = await handler.download_artifacts(sender, folder, source_artifacts_dir=logdir)
        self.docker('exec', sidecar, '/bin/sh', '-ec', absent)
        self.assertFalse((folder / path.lstrip('/')).exists())
        await handler.upload_artifacts(self.receiver, folder, source_artifacts_dir=logdir,
                                       target_artifacts_dir=logdir)
        self.docker('exec', verifier, '/bin/sh', '-ec', absent)
        session = 'fixture-missing-sidecar__verifier__trial'
        isolation = dict(container_id=verifier, network_mode='none', session_id=session)
        proof = dict(source=path, service='queue', container_id=sidecar, session_id=session,
                     stage='after_upload_before_official_grading',
                     source_absent_before_and_after_collection=True,
                     verifier_artifact_absent=True, verifier_container_id=verifier)
        checkpoint = dict(capture='complete', captured_before_grading=True, workspace_in_archive=True,
                          native_session_files=[dict(path=self.guest + '/native.jsonl')], cwd=self.guest,
                          checkpoint_sha256=hashlib.sha256(self.archive.read_bytes()).hexdigest(),
                          declared_artifacts=[declaration])
        verified = verify_artifacts(folder, self.archive, [declaration], manifest.to_json_data(),
                                    checkpoint, [], [isolation], session_id=session,
                                    sidecar_containers={'queue': sidecar}, sidecar_proofs=[proof])
        self.assertEqual(verified['missing_artifacts'], [path])
        self.assertFalse(verified['can_resume_agent'])


if __name__ == '__main__':
    unittest.main()
