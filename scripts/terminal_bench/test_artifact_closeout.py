"""Closed native-capture/collection/upload contracts, without guest execution."""
import copy
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from scripts.terminal_bench.artifact_closeout import (
    CloseoutError, artifact_sources, project_native_session_artifacts, verify_artifacts,
)
from scripts.terminal_bench.artifact_integrity import source_manifest


class NativeSessionProjectionTests(unittest.TestCase):
    def test_projection_retains_existing_excludes_and_leaves_sidecar_paths_alone(self):
        sidecar = {'source': '/work/project/.yhc/transcripts', 'service': 'events'}
        entries = [{'source': '/work/', 'exclude': ['*.cache']}, '/out', sidecar]
        original = copy.deepcopy(entries)
        projected = project_native_session_artifacts(entries, '/work/project')
        self.assertEqual(projected, [
            {'source': '/work/', 'exclude': ['*.cache', './project/.yhc/transcripts']},
            '/out',
            sidecar,
        ])
        self.assertEqual(project_native_session_artifacts(projected, '/work/project'), projected)
        self.assertEqual(entries, original)

    def test_projection_rejects_ambiguous_workspace_paths(self):
        for workspace in ('relative', '/work/../project', '//work', '/work/app*'):
            with self.subTest(workspace=workspace), self.assertRaises(CloseoutError):
                project_native_session_artifacts(['/work'], workspace)


class ArtifactCloseoutTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.collected = self.root / 'artifacts'
        self.tree = self.collected / 'app/src'
        self.tree.mkdir(parents=True)
        (self.tree / 'empty').mkdir()
        (self.tree / 'new.txt').write_bytes(b'new\0content\n')
        (self.collected / 'app/config').write_bytes(b'config')
        self.sources = ['/app/config', '/app/src']
        self.entries = [('app', None), ('app/config', b'config'), ('app/src', None),
                        ('app/src/empty', None), ('app/src/new.txt', b'new\0content\n')]
        self.rows = [dict(source=p, destination='artifacts'+p, type=t, status='ok', service=None)
                     for p, t in zip(self.sources, ['file', 'directory'])]
        self.archive = self.root / 'capture.tar.gz'
        self.checkpoint = dict(capture='complete', captured_before_grading=True,
                               workspace_in_archive=True, cwd='/app',
                               declared_artifacts=self.sources,
                               native_session_files=[dict(path='state/session')])
        self.proofs = []
        for target in self.sources:
            entries = source_manifest(self.collected / target.lstrip('/'))
            files = [e for e in entries.values() if e['type'] == 'file']
            projection = dict(scope='uploaded_projection', file_count=len(files),
                              directory_count=len(entries)-len(files),
                              bytes=sum(e['bytes'] for e in files),
                              manifest_sha256=hashlib.sha256(json.dumps(entries, sort_keys=True,
                                  separators=(',', ':')).encode()).hexdigest(),
                              additional_target_entries_unverified=0)
            self.proofs.append(dict(session_id='sample__verifier__trial', target=target,
                container_id='a'*64, network_mode='none',
                stage='after_upload_before_official_grading', projection=projection,
                source_and_verifier_bytes_match=True))
        self.isolation = [dict(container_id='a'*64, network_mode='none',
                               session_id='sample__verifier__trial')]
        self.write_archive()

    def write_archive(self):
        with tarfile.open(self.archive, 'w:gz') as stream:
            for name, data in self.entries:
                member = tarfile.TarInfo(name)
                if isinstance(data, tarfile.TarInfo):
                    stream.addfile(data)
                elif data is None:
                    member.type = tarfile.DIRTYPE
                    stream.addfile(member)
                else:
                    member.size = len(data)
                    stream.addfile(member, io.BytesIO(data))
        self.checkpoint['checkpoint_sha256'] = hashlib.sha256(self.archive.read_bytes()).hexdigest()

    def verify(self):
        return verify_artifacts(self.collected, self.archive, self.sources, self.rows,
                                self.checkpoint, self.proofs, self.isolation,
                                session_id='sample__verifier__trial')

    def test_directory_addition_deletion_and_empty_directory_closeout(self):
        result = self.verify()
        self.assertEqual(result['present_artifacts']['/app/src']['file_count'], 1)
        self.assertEqual(result['present_artifacts']['/app/src']['directory_count'], 2)
        self.assertEqual(result['missing_artifacts'], [])
        self.assertTrue(result['artifact_chain_verified'])
        self.assertFalse(result['can_resume_agent'])

    def test_new_or_missing_collected_bytes_cannot_be_hidden_by_transfer_proof(self):
        for mutation in ('add', 'delete', 'edit'):
            with self.subTest(mutation=mutation):
                path = self.tree / 'new.txt'
                path.write_bytes(b'new\0content\n')
                if mutation == 'add':
                    (self.tree / 'extra').write_bytes(b'not captured')
                elif mutation == 'delete':
                    path.unlink()
                else:
                    path.write_bytes(b'changed')
                with self.assertRaises(CloseoutError): self.verify()
                (self.tree / 'extra').unlink(missing_ok=True)

    def test_missing_artifact_requires_workspace_capture_and_archive_absence(self):
        self.sources.append('/app/absent')
        self.rows.append(dict(source='/app/absent', type='file', status='failed', service=None))
        self.assertEqual(self.verify()['missing_artifacts'], ['/app/absent'])
        self.entries.append(('app/absent', b'present'))
        self.write_archive()
        with self.assertRaises(CloseoutError): self.verify()

    def test_missing_directory_can_be_proved_but_failed_present_directory_cannot(self):
        self.sources.append('/app/absent-dir')
        self.rows.append(dict(source='/app/absent-dir', type='directory', status='missing', service=None))
        self.assertEqual(self.verify()['missing_artifacts'], ['/app/absent-dir'])
        self.entries.append(('app/absent-dir/child', b'present'))
        self.write_archive()
        with self.assertRaises(CloseoutError): self.verify()

    def test_empty_directory_is_present_and_requires_upload_proof(self):
        (self.tree / 'new.txt').unlink()
        self.entries = [e for e in self.entries if e[0] != 'app/src/new.txt']
        self.rows[1]['status'] = 'empty'
        self.write_archive()
        # A stale projection must not pass just because there are no files.
        with self.assertRaises(CloseoutError): self.verify()
        entries = source_manifest(self.tree)
        self.proofs[1]['projection'].update(file_count=0, directory_count=2, bytes=0,
            manifest_sha256=hashlib.sha256(json.dumps(entries, sort_keys=True,
                separators=(',', ':')).encode()).hexdigest())
        # An `empty` collection row must mean an actually empty directory.
        with self.assertRaises(CloseoutError): self.verify()
        self.rows[1]['status'] = 'ok'
        result = self.verify()
        self.assertIn('/app/src', result['present_artifacts'])
        self.assertEqual(result['missing_artifacts'], [])

    def test_regular_file_only_original_contract_still_closes(self):
        self.sources.pop()
        self.rows.pop()
        self.proofs.pop()
        result = self.verify()
        self.assertEqual(result['present_artifacts']['/app/config']['file_count'], 1)
        self.assertEqual(result['present_artifacts']['/app/config']['directory_count'], 0)

    def test_present_declared_outputs_outside_cwd_require_the_same_byte_chain(self):
        self.sources.append('/outside/result')
        self.sources.append('/outside/empty')
        (self.collected / 'outside').mkdir()
        (self.collected / 'outside/result').write_bytes(b'result')
        (self.collected / 'outside/empty').mkdir()
        self.rows.extend([dict(source='/outside/result', type='file', status='ok'),
                          dict(source='/outside/empty', type='directory', status='empty')])
        self.entries.extend([('outside/result', b'result'), ('outside/empty', None)])
        self.write_archive()
        for target, kind in [('/outside/result', 'file'), ('/outside/empty', 'directory')]:
            entries = source_manifest(self.collected / target.lstrip('/'))
            self.proofs.append(dict(self.proofs[0], target=target, projection=dict(
                scope='uploaded_projection', file_count=int(kind == 'file'),
                directory_count=int(kind == 'directory'), bytes=6 if kind == 'file' else 0,
                manifest_sha256=hashlib.sha256(json.dumps(entries, sort_keys=True,
                    separators=(',', ':')).encode()).hexdigest(),
                additional_target_entries_unverified=0)))
        self.assertEqual(len(self.verify()['present_artifacts']), 4)
        self.entries.pop()
        self.write_archive()
        with self.assertRaises(CloseoutError): self.verify()

    def test_missing_output_outside_cwd_cannot_be_inferred_from_archive_absence(self):
        self.sources.append('/outside/absent')
        self.rows.append(dict(source='/outside/absent', type='file', status='failed'))
        with self.assertRaises(CloseoutError): self.verify()

    def test_archive_directory_structure_and_file_bytes_must_match(self):
        for position, replacement in [(2, ('app/src', b'not a directory')),
                                      (3, ('app/src/other-empty', None)),
                                      (4, ('app/src/new.txt', b'changed'))]:
            with self.subTest(position=position):
                saved = self.entries[position]
                self.entries[position] = replacement
                self.write_archive()
                with self.assertRaises(CloseoutError): self.verify()
                self.entries[position] = saved

    def test_malformed_archive_is_not_a_missing_output(self):
        self.archive.write_bytes(b'not a gzip archive')
        self.checkpoint['checkpoint_sha256'] = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        with self.assertRaises(CloseoutError): self.verify()

    def test_wrong_isolation_session_or_duplicate_upload_is_rejected(self):
        self.isolation[0]['session_id'] = 'sample-foreign__verifier__trial'
        with self.assertRaises(CloseoutError): self.verify()
        self.isolation[0]['session_id'] = 'sample__verifier__trial'
        self.proofs.append(copy.deepcopy(self.proofs[0]))
        with self.assertRaises(CloseoutError): self.verify()

    def test_archive_hash_and_checkpoint_qualification_are_required(self):
        for key, value in [('checkpoint_sha256', '0'*64), ('capture', 'partial'),
                           ('workspace_in_archive', False), ('native_session_files', []),
                           ('captured_before_grading', False)]:
            with self.subTest(key=key):
                old = self.checkpoint[key]
                self.checkpoint[key] = value
                with self.assertRaises(CloseoutError): self.verify()
                self.checkpoint[key] = old

    def test_duplicate_or_noncanonical_archive_paths_are_rejected(self):
        for name in ('app/src/new.txt', './app/src/new.txt', 'app/src/../src/new.txt',
                     '/app/src/new.txt', 'app//src/new.txt'):
            with self.subTest(name=name):
                self.entries.append((name, b'new\0content\n'))
                self.write_archive()
                with self.assertRaises(CloseoutError): self.verify()
                self.entries.pop()

    def test_links_and_special_entries_in_declared_tree_are_rejected(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.FIFOTYPE):
            with self.subTest(kind=kind):
                link = tarfile.TarInfo('app/src/link')
                link.type, link.linkname = kind, 'app/config'
                self.entries.append((link.name, link))
                self.write_archive()
                with self.assertRaises(CloseoutError): self.verify()
                self.entries.pop()

    def test_source_links_and_wrong_collection_types_are_rejected(self):
        (self.tree / 'new.txt').unlink()
        (self.tree / 'new.txt').symlink_to(self.collected / 'app/config')
        with self.assertRaises(CloseoutError): self.verify()
        (self.tree / 'new.txt').unlink()
        (self.tree / 'new.txt').write_bytes(b'new\0content\n')
        self.rows[1]['type'] = 'file'
        with self.assertRaises(CloseoutError): self.verify()

    def test_target_coverage_is_exact_not_just_a_count(self):
        for rows in [self.rows+self.rows[:1], self.rows[:1],
                     [dict(self.rows[0], service='db'), self.rows[1]]]:
            with self.subTest(rows=rows):
                original, self.rows = self.rows, rows
                with self.assertRaises(CloseoutError): self.verify()
                self.rows = original

    def test_prefix_session_collision_is_not_an_upload_for_this_sample(self):
        self.proofs[0]['session_id'] += '-foreign'
        with self.assertRaises(CloseoutError): self.verify()

    def test_upload_identity_stage_counts_and_bytes_are_bound(self):
        mutations = [('container_id', 'b'*64), ('network_mode', 'bridge'),
                     ('stage', 'after_grading'), ('source_and_verifier_bytes_match', False)]
        for key, value in mutations:
            with self.subTest(key=key):
                saved = copy.deepcopy(self.proofs)
                self.proofs[0][key] = value
                with self.assertRaises(CloseoutError): self.verify()
                self.proofs = saved
        for key, value in [('manifest_sha256', '0'*64), ('file_count', True),
                           ('directory_count', 1), ('additional_target_entries_unverified', 1),
                           ('bytes', 999)]:
            with self.subTest(key=key):
                saved = copy.deepcopy(self.proofs)
                self.proofs[0]['projection'][key] = value
                with self.assertRaises(CloseoutError): self.verify()
                self.proofs = saved

    def test_missing_archive_workspace_does_not_prove_absence(self):
        self.sources.append('/app/absent')
        self.rows.append(dict(source='/app/absent', type='file', status='failed', service=None))
        self.entries = [e for e in self.entries if e[0] != 'app']
        self.write_archive()
        with self.assertRaises(CloseoutError): self.verify()

    def test_overlapping_declarations_and_unqualified_paths_are_rejected(self):
        for target in ('/app/src/new.txt', '/app/../app/absent', 'app/config'):
            with self.subTest(target=target):
                self.sources.append(target)
                with self.assertRaises(CloseoutError): self.verify()
                self.sources.pop()

    def declare_directory_with_trailing_separator(self):
        self.sources[1] = '/app/src/'
        self.rows[1]['source'] = '/app/src/'
        self.proofs[1]['target'] = '/app/src/'

    def test_directory_trailing_separator_keeps_original_evidence_identity(self):
        self.declare_directory_with_trailing_separator()
        result = self.verify()
        self.assertEqual(result['present_artifacts']['/app/src/']['file_count'], 1)
        self.assertEqual(result['present_artifacts']['/app/src/']['directory_count'], 2)
        self.assertNotIn('/app/src', result['present_artifacts'])
        self.assertFalse(result['can_resume_agent'])
        (self.tree / 'new.txt').write_bytes(b'changed')
        with self.assertRaises(CloseoutError): self.verify()

    def test_missing_directory_trailing_separator_still_requires_archive_absence(self):
        self.sources.append('/app/absent/')
        self.rows.append(dict(source='/app/absent/', type='directory', status='missing'))
        self.assertEqual(self.verify()['missing_artifacts'], ['/app/absent/'])
        self.entries.append(('app/absent/child', b'present'))
        self.write_archive()
        with self.assertRaises(CloseoutError): self.verify()

    def test_empty_directory_trailing_separator_is_present_not_missing(self):
        self.declare_directory_with_trailing_separator()
        (self.tree / 'new.txt').unlink()
        (self.tree / 'empty').rmdir()
        self.entries = [e for e in self.entries if not e[0].startswith('app/src/')]
        self.rows[1]['status'] = 'empty'
        self.proofs[1]['projection'].update(file_count=0, directory_count=1, bytes=0,
            manifest_sha256=hashlib.sha256(json.dumps({'': {'type': 'directory'}},
                sort_keys=True, separators=(',', ':')).encode()).hexdigest())
        self.write_archive()
        result = self.verify()
        self.assertEqual(result['missing_artifacts'], [])
        self.assertEqual(result['present_artifacts']['/app/src/']['directory_count'], 1)

    def test_directory_aliases_and_overlaps_are_rejected_before_collection(self):
        self.declare_directory_with_trailing_separator()
        for target in ('/app/src', '/app/src/new.txt'):
            with self.subTest(target=target):
                self.sources.append(target)
                with self.assertRaisesRegex(CloseoutError, '^artifact_declarations_overlap$'):
                    self.verify()
                self.sources.pop()

    def test_only_a_single_directory_separator_is_allowed(self):
        for target in ('/app/src//', '/app//src/', '/app/src/./', '/app/src/../src/', '/', 'app/src/'):
            with self.subTest(target=target):
                self.sources[1] = target
                self.rows[1]['source'] = target
                self.proofs[1]['target'] = target
                with self.assertRaises(CloseoutError): self.verify()
        self.sources[1] = self.rows[1]['source'] = self.proofs[1]['target'] = '/app/src'
        self.sources[0] = self.rows[0]['source'] = self.proofs[0]['target'] = '/app/config/'
        with self.assertRaises(CloseoutError): self.verify()

    def test_directory_separator_does_not_normalize_checkpoint_or_receipt_identity(self):
        self.declare_directory_with_trailing_separator()
        self.checkpoint['declared_artifacts'] = ['/app/config', '/app/src']
        with self.assertRaises(CloseoutError): self.verify()
        self.checkpoint['declared_artifacts'] = self.sources
        for row, key in ((self.rows[1], 'source'), (self.proofs[1], 'target')):
            row[key] = '/app/src'
            with self.assertRaises(CloseoutError): self.verify()
            row[key] = '/app/src/'
        self.checkpoint['cwd'] = '/app/'
        with self.assertRaises(CloseoutError): self.verify()

    def test_directory_separator_does_not_allow_source_links(self):
        self.declare_directory_with_trailing_separator()
        (self.tree / 'new.txt').unlink()
        (self.tree / 'new.txt').symlink_to(self.collected / 'app/config')
        with self.assertRaises(CloseoutError): self.verify()


class SidecarArtifactTests(unittest.TestCase):
    def setUp(self):
        self.fixture = ArtifactCloseoutTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        f = self.fixture
        self.source = '/tmp/service-snapshot'
        self.declared = [*f.sources, dict(source=self.source, service='queue')]
        f.checkpoint['declared_artifacts'] = self.declared
        f.rows.append(dict(source=self.source, type='file', status='ok', service='queue'))
        self.collected = f.collected / self.source.lstrip('/')
        self.collected.parent.mkdir()
        self.collected.write_bytes(b'snapshot')
        record = {'': dict(type='file', bytes=8, sha256=hashlib.sha256(b'snapshot').hexdigest())}
        projection = dict(scope='uploaded_projection', file_count=1, directory_count=0, bytes=8,
                          manifest_sha256=hashlib.sha256(json.dumps(record, sort_keys=True,
                              separators=(',', ':')).encode()).hexdigest(),
                          additional_target_entries_unverified=0)
        f.proofs.append(dict(f.proofs[0], target=self.source, projection=projection))
        self.containers = {'queue': 'b'*64}
        self.sidecar_proofs = [dict(source=self.source, service='queue', container_id='b'*64,
                                    session_id='sample__verifier__trial',
                                    stage='after_collection_before_upload', projection=projection.copy(),
                                    source_and_collected_bytes_match=True)]

    def verify(self):
        f = self.fixture
        return verify_artifacts(f.collected, f.archive, self.declared, f.rows, f.checkpoint,
                                f.proofs, f.isolation, session_id='sample__verifier__trial',
                                sidecar_containers=self.containers, sidecar_proofs=self.sidecar_proofs)

    def test_source_service_file_uses_an_independent_origin_and_verifier_chain(self):
        f = self.fixture
        self.assertEqual(artifact_sources(self.declared, sidecar_services={'queue'}), f.sources)
        # A main-container file at the same absolute path does not supply or
        # change the sidecar artifact.
        f.entries.extend([('tmp', None), ('tmp/service-snapshot', b'main decoy')])
        f.write_archive()
        report = self.verify()
        self.assertEqual(report['sidecar_artifacts'][self.source], 'queue')
        self.assertEqual(report['present_artifacts'][self.source]['bytes'], 8)
        self.assertFalse(report['can_resume_agent'])

    def test_manifest_cannot_substitute_another_service_or_missing_source(self):
        row = self.fixture.rows[-1]
        row['service'] = 'main'
        with self.assertRaisesRegex(CloseoutError, 'artifact_collection_unqualified'):
            self.verify()
        row.update(service='queue', status='missing')
        self.collected.unlink()
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_absence_unverified'):
            self.verify()

    def set_sidecar_absence(self):
        self.fixture.rows[-1]['status'] = 'failed'
        self.fixture.proofs.pop()
        self.collected.unlink()
        proof = self.sidecar_proofs[0]
        del proof['projection'], proof['source_and_collected_bytes_match']
        proof.update(stage='after_upload_before_official_grading',
                     source_absent_before_and_after_collection=True,
                     verifier_artifact_absent=True,
                     verifier_container_id=self.fixture.isolation[0]['container_id'])
        return proof

    def test_missing_sidecar_file_requires_source_and_disconnected_verifier_observations(self):
        self.set_sidecar_absence()
        report = self.verify()
        self.assertEqual(report['missing_artifacts'], [self.source])
        self.assertNotIn(self.source, report['present_artifacts'])
        self.assertFalse(report['can_resume_agent'])

    def test_missing_sidecar_file_rejects_unqualified_absence_evidence(self):
        good = self.set_sidecar_absence().copy()
        for override in (
            dict(source_absent_before_and_after_collection=False),
            dict(verifier_artifact_absent=1),
            dict(verifier_container_id='c'*64),
            dict(stage='after_official_grading'),
            dict(container_id='c'*64),
            dict(session_id='other__verifier__trial'),
        ):
            with self.subTest(override=override):
                self.sidecar_proofs[0] = dict(good, **override)
                with self.assertRaises(CloseoutError):
                    self.verify()
        self.sidecar_proofs[0] = good
        self.collected.write_bytes(b'unexpected delivery')
        with self.assertRaises(CloseoutError):
            self.verify()

    def test_identical_bytes_do_not_substitute_wrong_container_or_trial(self):
        good = self.sidecar_proofs[0].copy()
        for overrides in (dict(container_id='c'*64), dict(service='main'),
                          dict(session_id='old__verifier__trial'),
                          dict(stage='after_official_grading'),
                          dict(source_and_collected_bytes_match=False)):
            with self.subTest(overrides=overrides):
                self.sidecar_proofs[0] = dict(good, **overrides)
                with self.assertRaises(CloseoutError):
                    self.verify()
        self.sidecar_proofs[0] = good
        del good['source_and_collected_bytes_match']
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_proof_mismatch'):
            self.verify()

    def test_require_distinct_origin_attestation_and_matching_projection(self):
        good = self.sidecar_proofs.pop()
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_proof_coverage'):
            self.verify()
        self.sidecar_proofs.append(good)
        self.collected.write_bytes(b'modified')
        with self.assertRaisesRegex(CloseoutError, 'sidecar_artifact_proof_mismatch'):
            self.verify()
        self.collected.write_bytes(b'snapshot')
        good['container_id'] = self.containers['queue'] = self.fixture.isolation[0]['container_id']
        with self.assertRaisesRegex(CloseoutError, 'sidecar_container_unqualified'):
            self.verify()

    def test_sidecar_admission_remains_opt_in_and_file_only(self):
        with self.assertRaises(CloseoutError):
            artifact_sources(self.declared)
        for entry in (dict(source=self.source + '/', service='queue'),
                      dict(source=self.source, service='queue', destination='snapshot'),
                      dict(source=self.source, service='queue', exclude=['*.tmp']),
                      dict(source=self.source, service='other')):
            with self.subTest(entry=entry), self.assertRaises(CloseoutError):
                artifact_sources([entry], sidecar_services={'queue'})


class ArtifactExclusionTests(unittest.TestCase):
    def setUp(self):
        self.fixture = ArtifactCloseoutTests('test_directory_addition_deletion_and_empty_directory_closeout')
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.declared = ['/app/config', {'source': '/app/src', 'exclude': ['node_modules', '*.pyc']}]
        self.fixture.checkpoint['declared_artifacts'] = copy.deepcopy(self.declared)

    def verify(self):
        f = self.fixture
        return verify_artifacts(f.collected, f.archive, self.declared, f.rows,
                                f.checkpoint, f.proofs, f.isolation,
                                session_id='sample__verifier__trial')

    def test_filtered_collection_is_bound_to_full_capture_and_original_rules(self):
        f = self.fixture
        f.entries += [('app/src/node_modules', None),
                      ('app/src/node_modules/pkg.js', b'excluded cache'),
                      ('app/src/new.pyc', b'excluded bytecode')]
        f.write_archive()
        result = self.verify()
        self.assertEqual(result['present_artifacts']['/app/src']['file_count'], 1)
        self.assertFalse(result['can_resume_agent'])
        pin = hashlib.sha256(json.dumps(self.declared, sort_keys=True,
                            separators=(',', ':')).encode()).hexdigest()
        self.assertEqual(result['artifact_selection']['declarations_sha256'], pin)
        self.assertEqual(result['artifact_selection']['exclude_profile'], 'gnu-tar-simple-glob-v1')
        f.checkpoint['declared_artifacts'] = f.sources
        with self.assertRaises(CloseoutError): self.verify()
        f.checkpoint['declared_artifacts'] = copy.deepcopy(self.declared)
        f.checkpoint['declared_artifacts'][1]['exclude'].append('new.txt')
        with self.assertRaises(CloseoutError): self.verify()

    def test_rules_cannot_hide_changed_included_or_unexpected_collected_bytes(self):
        f = self.fixture
        self.verify()
        (f.tree / 'new.txt').write_bytes(b'wrong included bytes')
        with self.assertRaises(CloseoutError): self.verify()
        (f.tree / 'new.txt').write_bytes(b'new\0content\n')
        (f.tree / 'unexpected.pyc').write_bytes(b'should not have been collected')
        with self.assertRaises(CloseoutError): self.verify()

    def test_excluded_links_are_inert_but_duplicate_and_traversing_paths_remain_blocked(self):
        f = self.fixture
        link = tarfile.TarInfo('app/src/node_modules')
        link.type, link.linkname = tarfile.SYMTYPE, '/outside'
        f.entries.append((link.name, link))
        f.write_archive()
        self.assertTrue(self.verify()['artifact_chain_verified'])
        for entry in [('app/src/node_modules', link), ('app/src/node_modules/../hidden', b'x')]:
            with self.subTest(path=entry[0]):
                f.entries.append(entry)
                f.write_archive()
                with self.assertRaises(CloseoutError): self.verify()
                f.entries.pop()

    def test_empty_filtered_directory_stays_present_and_file_excludes_are_ignored(self):
        f = self.fixture
        (f.tree / 'new.txt').unlink()
        (f.tree / 'empty').rmdir()
        entries = {'': {'type': 'directory'}}
        f.rows[1]['status'] = 'empty'
        f.proofs[1]['projection'].update(file_count=0, directory_count=1, bytes=0,
            manifest_sha256=hashlib.sha256(json.dumps(entries, sort_keys=True,
                separators=(',', ':')).encode()).hexdigest())
        self.declared = [{'source': '/app/config', 'exclude': ['*']},
                         {'source': '/app/src', 'exclude': ['*']}]
        f.checkpoint['declared_artifacts'] = copy.deepcopy(self.declared)
        result = self.verify()
        self.assertEqual(result['missing_artifacts'], [])
        self.assertEqual(result['present_artifacts']['/app/config']['bytes'], 6)
        self.assertEqual(result['present_artifacts']['/app/src']['directory_count'], 1)

    def test_unanchored_recursive_globs_and_component_boundaries(self):
        f = self.fixture
        self.declared[1]['exclude'].append('backend/cache?.tmp')
        f.checkpoint['declared_artifacts'] = copy.deepcopy(self.declared)
        f.entries += [('app/src/frontend', None), ('app/src/frontend/node_modules', None),
                      ('app/src/frontend/node_modules/pkg', b'excluded'),
                      ('app/src/backend', None), ('app/src/backend/cache1.tmp', b'excluded'),
                      ('app/src/node_modules-extra', None),
                      ('app/src/node_modules-extra/app.js', b'keep'),
                      ('app/src/module.pyc', b'excluded')]
        for name in ('frontend', 'backend', 'node_modules-extra'):
            (f.tree / name).mkdir()
        (f.tree / 'node_modules-extra/app.js').write_bytes(b'keep')
        expected = {'': {'type': 'directory'}, 'empty': {'type': 'directory'},
            'frontend': {'type': 'directory'}, 'backend': {'type': 'directory'},
            'node_modules-extra': {'type': 'directory'},
            'new.txt': {'type': 'file', 'bytes': 12,
                        'sha256': hashlib.sha256(b'new\0content\n').hexdigest()},
            'node_modules-extra/app.js': {'type': 'file', 'bytes': 4,
                                         'sha256': hashlib.sha256(b'keep').hexdigest()}}
        f.proofs[1]['projection'].update(file_count=2, directory_count=5, bytes=16,
            manifest_sha256=hashlib.sha256(json.dumps(expected, sort_keys=True,
                separators=(',', ':')).encode()).hexdigest())
        f.write_archive()
        self.assertEqual(self.verify()['present_artifacts']['/app/src']['file_count'], 2)

    def test_pre_dispatch_source_validation_rejects_unproven_syntax_and_mappings(self):
        from scripts.terminal_bench.artifact_closeout import artifact_sources
        before = copy.deepcopy(self.declared)
        self.assertEqual(artifact_sources(self.declared), ['/app/config', '/app/src'])
        self.assertEqual(self.declared, before)
        for bad in ({'source': '/app/src', 'exclude': '*.pyc'},
                    {'source': '/app/src', 'exclude': ['']},
                    {'source': '/app/src', 'exclude': ['file[0-9]']},
                    {'source': '/app/src', 'exclude': [r'escaped\*']},
                    {'source': '/app/src', 'exclude': [None]},
                    {'source': '/app/src', 'exclude': None},
                    {'source': '/app/src', 'destination': 'other'},
                    {'source': '/app/src', 'service': 'db'},
                    {'source': '/app/src', 'unknown': True},
                    {'source': '/app/src', 'exclude': ['x'*1025]}):
            with self.subTest(declaration=bad), self.assertRaises(CloseoutError):
                artifact_sources([bad])

    def test_missing_output_cannot_be_inferred_from_excluding_present_bytes(self):
        f = self.fixture
        self.declared.append({'source': '/app/missing', 'exclude': ['*']})
        f.checkpoint['declared_artifacts'] = copy.deepcopy(self.declared)
        f.rows.append(dict(source='/app/missing', type='directory', status='failed', service=None))
        self.assertEqual(self.verify()['missing_artifacts'], ['/app/missing'])
        f.entries.append(('app/missing', None))
        f.write_archive()
        with self.assertRaises(CloseoutError): self.verify()


if __name__ == '__main__':
    unittest.main()
