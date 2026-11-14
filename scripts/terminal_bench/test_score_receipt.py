import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from scripts.terminal_bench.score_receipt import ScoreError, verify_score


class ScoreReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name).resolve()
        self.folder = self.base / 'verifier'
        self.folder.mkdir()
        self.reward_contract = {'format': 'reward_txt_and_logs', 'ctrf_expected': False}
        self.ctrf_contract = {'format': 'ctrf_and_reward_txt', 'ctrf_expected': True,
                              'expected_test_names': ['alpha', 'beta']}

    def reward(self, value='0\n', log=b'Independent grader finished.\n'):
        (self.folder / 'reward.txt').write_text(value)
        (self.folder / 'test-stdout.txt').write_bytes(log)

    def ctrf(self, passed=True):
        self.reward('1\n' if passed else '0\n')
        data = {'results': {'summary': {'tests': 2, 'passed': 2 if passed else 1,
                'failed': 0 if passed else 1, 'skipped': 0, 'pending': 0, 'other': 0},
                'tests': [{'name': 'alpha', 'status': 'passed'},
                          {'name': 'beta', 'status': 'passed' if passed else 'failed'}]}}
        (self.folder / 'ctrf.json').write_text(json.dumps(data))
        return data

    def test_reward_only_preserves_unknown_counts_for_success_and_failure(self):
        for value in (0, 1):
            with self.subTest(reward=value):
                self.reward(str(value) + '\n')
                receipt = verify_score(self.folder, {'reward': value}, self.reward_contract)
                self.assertEqual(receipt['reward'], value)
                self.assertIsNone(receipt['test_counts'])
                self.assertIsNone(receipt['failed_checks'])
                self.assertFalse(receipt['grader_identity_verified_by_this_reader'])
                self.assertFalse(receipt['can_resume_agent'])
                self.assertEqual(receipt['file_sha256'], {
                    name: hashlib.sha256((self.folder / name).read_bytes()).hexdigest()
                    for name in ('reward.txt', 'test-stdout.txt')})
                self.assertFalse((self.folder / 'ctrf.json').exists())

    def test_reward_only_accepts_float_notation_and_binary_log_bytes(self):
        self.reward(' 1.0\n', log=b'\xff\x00')
        self.assertEqual(verify_score(self.folder, {'reward': 1.0}, self.reward_contract)['reward'], 1)

    def test_explicit_original_format_required(self):
        self.reward()
        for contract in ({}, {'format': 'unknown'},
                         {'format': 'reward_txt_and_logs', 'ctrf_expected': True},
                         {'format': 'ctrf_and_reward_txt', 'ctrf_expected': False}):
            with self.subTest(contract=contract), self.assertRaises(ScoreError):
                verify_score(self.folder, {'reward': 0}, contract)

    def test_missing_logs_cannot_be_accepted_as_complete_reward_only_evidence(self):
        self.reward()
        (self.folder / 'test-stdout.txt').unlink()
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_explicit_log_names_are_bound_and_not_optional(self):
        self.reward()
        (self.folder / 'grader.log').write_bytes(b'')
        contract = dict(self.reward_contract, expected_log_names=['grader.log'])
        result = verify_score(self.folder, {'reward': 0}, contract)
        self.assertEqual(set(result['file_sha256']), {'reward.txt', 'grader.log'})
        for names in ([], ['../grader.log'], ['/grader.log'], ['grader.log', 'grader.log'],
                      ['reward.txt'], ['ctrf.json'], 'grader.log', [False]):
            with self.subTest(names=names), self.assertRaises(ScoreError):
                verify_score(self.folder, {'reward': 0}, dict(contract, expected_log_names=names))

    def test_reported_reward_is_a_single_finite_binary_number(self):
        self.reward()
        for reported in ({}, {'reward': False}, {'reward': '0'}, {'reward': 0.5},
                         {'reward': float('nan')}, {'reward': float('inf')},
                         {'reward': 0, 'other': 1}, None):
            with self.subTest(reported=reported), self.assertRaises(ScoreError):
                verify_score(self.folder, reported, self.reward_contract)

    def test_reward_file_must_match_report_and_have_exact_numeric_syntax(self):
        for text in ('1\n', 'False', 'NaN', 'Infinity', '0 extra', '0 0', '', '0e0'):
            with self.subTest(text=text):
                self.reward(text)
                with self.assertRaises(ScoreError):
                    verify_score(self.folder, {'reward': 0}, self.reward_contract)
        (self.folder / 'reward.txt').write_bytes(b'\xff')
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_ctrf_success_and_failure_keep_test_counts_and_failure_names(self):
        for passed in (False, True):
            with self.subTest(passed=passed):
                self.ctrf(passed)
                result = verify_score(self.folder, {'reward': int(passed)}, self.ctrf_contract)
                self.assertEqual(result['test_counts'], {'total': 2, 'passed': 2 if passed else 1,
                                                        'failed': 0 if passed else 1})
                self.assertEqual(result['failed_checks'], [] if passed else ['beta'])

    def test_ctrf_cannot_be_downgraded_when_missing_or_conflicting(self):
        self.ctrf()
        (self.folder / 'ctrf.json').unlink()
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 1}, self.ctrf_contract)
        self.ctrf(False)
        self.reward('1\n')
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 1}, self.ctrf_contract)

    def test_ctrf_requires_original_complete_unique_names_and_exact_summary(self):
        changes = [lambda d: d['results']['tests'].pop(),
                   lambda d: d['results']['tests'][1].update(name='alpha'),
                   lambda d: d['results']['tests'][1].update(name='invented'),
                   lambda d: d['results']['tests'][1].update(status='skipped'),
                   lambda d: d['results']['summary'].update(tests=True),
                   lambda d: d['results']['summary'].update(passed=1),
                   lambda d: d.update(results=[])]
        for change in changes:
            data = self.ctrf()
            change(data)
            (self.folder / 'ctrf.json').write_text(json.dumps(data))
            with self.assertRaises(ScoreError):
                verify_score(self.folder, {'reward': 1}, self.ctrf_contract)
        self.ctrf()
        for names in ([], ['alpha', 'alpha'], ['alpha', False], 'alpha'):
            with self.subTest(names=names), self.assertRaises(ScoreError):
                verify_score(self.folder, {'reward': 1}, dict(self.ctrf_contract, expected_test_names=names))

    def test_duplicate_json_keys_and_nonfinite_or_malformed_ctrf_are_rejected(self):
        self.ctrf()
        for raw in ('{"results":{},"results":{}}', '{"results":{"summary":{"tests":NaN}}}',
                    '[]', '{}', '{'):
            with self.subTest(raw=raw):
                (self.folder / 'ctrf.json').write_text(raw)
                with self.assertRaises(ScoreError):
                    verify_score(self.folder, {'reward': 1}, self.ctrf_contract)

    def test_links_and_nonregular_score_files_are_rejected(self):
        self.reward()
        outside = self.base / 'outside'
        outside.write_text('0\n')
        for name in ('reward.txt', 'test-stdout.txt'):
            with self.subTest(name=name):
                self.reward()
                (self.folder / name).unlink()
                (self.folder / name).symlink_to(outside)
                with self.assertRaises(ScoreError):
                    verify_score(self.folder, {'reward': 0}, self.reward_contract)
                (self.folder / name).unlink()
        self.reward()
        (self.folder / 'reward.txt').unlink()
        os.mkfifo(self.folder / 'reward.txt')
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_bounded_evidence_does_not_silently_truncate_logs(self):
        self.reward()
        with (self.folder / 'test-stdout.txt').open('wb') as out:
            out.truncate((4 << 20) + 1)
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_directory_alias_or_replacement_cannot_redirect_evidence(self):
        self.reward()
        alias = self.base / 'alias'
        alias.symlink_to(self.folder, target_is_directory=True)
        self.assertEqual(verify_score(alias, {'reward': 0}, self.reward_contract)['reward'], 0)
        original_open = os.open
        def replace(name, flags, *args, **kwargs):
            if name == 'test-stdout.txt':
                self.folder.rename(self.base / 'retained')
                self.folder.mkdir()
                self.reward('1\n')
            return original_open(name, flags, *args, **kwargs)
        with mock.patch('scripts.terminal_bench.score_receipt.os.open', side_effect=replace):
            with self.assertRaises(ScoreError):
                verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_directory_alias_retarget_during_read_is_rejected(self):
        self.reward()
        alias = self.base / 'alias'
        alias.symlink_to(self.folder, target_is_directory=True)
        other = self.base / 'other'
        other.mkdir()
        original_open = os.open
        def retarget(name, flags, *args, **kwargs):
            if name == 'test-stdout.txt':
                alias.unlink()
                alias.symlink_to(other, target_is_directory=True)
            return original_open(name, flags, *args, **kwargs)
        with mock.patch('scripts.terminal_bench.score_receipt.os.open', side_effect=retarget):
            with self.assertRaises(ScoreError):
                verify_score(alias, {'reward': 0}, self.reward_contract)

    def test_already_read_reward_mutation_is_detected_before_accepting_logs(self):
        self.reward()
        original_open = os.open
        def mutate(name, flags, *args, **kwargs):
            if name == 'test-stdout.txt':
                path = self.folder / 'reward.txt'
                path.write_text('1\n')
                os.utime(path, ns=(0, 0))
            return original_open(name, flags, *args, **kwargs)
        with mock.patch('scripts.terminal_bench.score_receipt.os.open', side_effect=mutate):
            with self.assertRaises(ScoreError):
                verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_cli_reads_bound_inputs_and_returns_unknown_counts_without_ctrf(self):
        self.reward()
        report, contract = self.base / 'reported.json', self.base / 'contract.json'
        report.write_text(json.dumps({'reward': 0}))
        contract.write_text(json.dumps(self.reward_contract))
        command = [sys.executable, '-m', 'scripts.terminal_bench.score_receipt',
                   '--score-dir', str(self.folder), '--reported-rewards', str(report),
                   '--contract', str(contract)]
        result = subprocess.run(command, cwd=Path(__file__).resolve().parents[2],
                                text=True, capture_output=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        receipt = json.loads(result.stdout)
        self.assertEqual(receipt['reward'], 0)
        self.assertIsNone(receipt['test_counts'])
        self.assertFalse(receipt['can_resume_agent'])
        report.write_text('{"reward":0,"reward":1}')
        invalid = subprocess.run(command, cwd=Path(__file__).resolve().parents[2],
                                 text=True, capture_output=True, timeout=15)
        self.assertEqual(invalid.returncode, 2)
        self.assertEqual(invalid.stdout, '')

    def test_hardlinked_evidence_is_not_a_closed_private_receipt(self):
        self.reward()
        os.link(self.folder / 'reward.txt', self.base / 'linked-reward')
        with self.assertRaises(ScoreError):
            verify_score(self.folder, {'reward': 0}, self.reward_contract)

    def test_cli_accepts_raw_temporary_and_aliased_parent_paths(self):
        self.reward()
        (self.base / 'reported.json').write_text(json.dumps({'reward': 0}))
        (self.base / 'contract.json').write_text(json.dumps(self.reward_contract))
        alias = self.base / 'parent-alias'
        alias.symlink_to(self.base, target_is_directory=True)
        # macOS tempfile paths normally contain /var -> /private/var.
        for parent in (Path(self.temporary.name), alias):
            with self.subTest(parent=str(parent)):
                result = subprocess.run([
                    sys.executable, '-m', 'scripts.terminal_bench.score_receipt',
                    '--score-dir', str(parent / 'verifier'),
                    '--reported-rewards', str(parent / 'reported.json'),
                    '--contract', str(parent / 'contract.json')],
                    cwd=Path(__file__).resolve().parents[2], text=True, capture_output=True, timeout=15)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIsNone(json.loads(result.stdout)['test_counts'])


if __name__ == '__main__':
    unittest.main()
