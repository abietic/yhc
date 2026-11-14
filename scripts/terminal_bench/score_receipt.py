"""Read closed, explicitly bound binary scoring receipts on a POSIX host.

The original grader may supply CTRF plus reward.txt, or reward.txt and logs.
This reader verifies their bytes and agreement, not the grader's identity,
artifact transfer, execution isolation, funding, or resumability.
"""
import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys


class ScoreError(ValueError):
    pass


def unique(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ScoreError('duplicate_score_key')
        result[key] = value
    return result


def reject_constant(value):
    raise ScoreError('nonfinite_score')


def _signature(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_nlink, info.st_size,
            info.st_mtime_ns, info.st_ctime_ns)


def _read_files(folder, names):
    """Anchor every read to one unchanged directory and reject changed entries."""
    blobs = {}
    try:
        requested = Path(folder).absolute()
        root = requested.resolve(strict=True)
        directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            anchored = _signature(os.fstat(directory))
            entries = {}
            for name in names:
                fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
                with os.fdopen(fd, 'rb') as stream:
                    before = os.fstat(stream.fileno())
                    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
                        raise ScoreError('score_not_regular')
                    raw = stream.read((4 << 20) + 1)
                    if len(raw) > 4 << 20:
                        raise ScoreError('score_file_bound')
                    if _signature(before) != _signature(os.fstat(stream.fileno())):
                        raise ScoreError('score_evidence_changed')
                    entries[name] = _signature(before)
                    blobs[name] = raw
            # A previous file can change while a later log is being read.
            for name, signature in entries.items():
                if _signature(os.stat(name, dir_fd=directory, follow_symlinks=False)) != signature:
                    raise ScoreError('score_evidence_changed')
            if (requested.resolve(strict=True) != root or _signature(root.lstat()) != anchored
                    or _signature(os.fstat(directory)) != anchored):
                raise ScoreError('score_directory_changed')
        finally:
            os.close(directory)
    except ScoreError:
        raise
    except (OSError, TypeError, ValueError) as error:
        raise ScoreError('score_evidence_unreadable') from error
    return blobs


def _log_names(contract, required):
    names = contract.get('expected_log_names', ['test-stdout.txt'] if required else [])
    if (not isinstance(names, list) or (required and not names)
            or any(not isinstance(n, str) or re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,127}', n) is None
                   or n in ('reward.txt', 'ctrf.json') for n in names)
            or len(set(names)) != len(names)):
        raise ScoreError('score_log_contract_unverified')
    return names


def _ctrf_counts(raw, contract, expected):
    names = contract.get('expected_test_names')
    if (not isinstance(names, list) or not names
            or any(not isinstance(n, str) or not n for n in names)
            or len(set(names)) != len(names)):
        raise ScoreError('test_coverage_unverified')
    try:
        data = json.loads(raw, object_pairs_hook=unique, parse_constant=reject_constant)['results']
        tests, summary = data['tests'], data['summary']
    except ScoreError:
        raise
    except (KeyError, TypeError, ValueError, RecursionError) as error:
        raise ScoreError('score_json_invalid') from error
    if (not isinstance(tests, list) or not isinstance(summary, dict) or len(tests) != len(names)
            or any(not isinstance(t, dict) or not isinstance(t.get('name'), str)
                   or t.get('status') not in ('passed', 'failed') for t in tests)
            or sorted(t['name'] for t in tests) != sorted(names)):
        raise ScoreError('test_coverage_unverified')
    counts = Counter(t['status'] for t in tests)
    for key, value in dict(tests=len(names), passed=counts['passed'], failed=counts['failed'],
                           skipped=0, pending=0, other=0).items():
        if type(summary.get(key)) is not int or summary[key] != value:
            raise ScoreError('score_summary_mismatch')
    if expected != int(counts['passed'] == len(names)):
        raise ScoreError('reward_and_tests_mismatch')
    return (dict(total=len(names), passed=counts['passed'], failed=counts['failed']),
            [t['name'] for t in tests if t['status'] == 'failed'])


def verify_score(folder, rewards, contract):
    """Verify only the explicitly selected original score format.

    Reward-only contracts require test-stdout.txt unless expected_log_names
    explicitly names other logs. Counts and failed checks remain unknown.
    Missing or invalid CTRF never falls back to reward-only interpretation.
    Directory aliases are resolved and rechecked; linked score files are not
    followed. Each file is bounded to 4 MiB; changing evidence is rejected.
    """
    if not isinstance(contract, dict):
        raise ScoreError('unsupported_score_contract')
    kind = contract.get('format')
    ctrf = kind == 'ctrf_and_reward_txt' and contract.get('ctrf_expected') is True
    reward_only = kind == 'reward_txt_and_logs' and contract.get('ctrf_expected') is False
    if not (ctrf or reward_only):
        raise ScoreError('unsupported_score_contract')
    if not isinstance(rewards, dict) or set(rewards) != {'reward'}:
        raise ScoreError('reported_reward_unverified')
    expected = rewards['reward']
    if type(expected) not in (int, float) or expected not in (0, 1):
        raise ScoreError('invalid_reward')
    logs = _log_names(contract, required=reward_only)
    blobs = _read_files(folder, ['reward.txt'] + (['ctrf.json'] if ctrf else []) + logs)
    try:
        text = blobs['reward.txt'].decode('utf-8').strip()
    except UnicodeError as error:
        raise ScoreError('reward_text_mismatch') from error
    if text not in ('0', '1', '0.0', '1.0') or float(text) != expected:
        raise ScoreError('reward_text_mismatch')
    counts, failed = _ctrf_counts(blobs['ctrf.json'], contract, expected) if ctrf else (None, None)
    return dict(format=kind, reward=expected, test_counts=counts, failed_checks=failed,
                file_sha256={n: hashlib.sha256(raw).hexdigest() for n, raw in blobs.items()},
                grader_identity_verified_by_this_reader=False, can_resume_agent=False)


def _input_json(path):
    root = Path(path).absolute()
    raw = _read_files(root.parent, [root.name])[root.name]
    try:
        return json.loads(raw, object_pairs_hook=unique, parse_constant=reject_constant)
    except ScoreError:
        raise
    except (ValueError, RecursionError) as error:
        raise ScoreError('score_input_json_invalid') from error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--score-dir', required=True, type=Path)
    parser.add_argument('--reported-rewards', required=True, type=Path)
    parser.add_argument('--contract', required=True, type=Path)
    args = parser.parse_args()
    try:
        result = verify_score(args.score_dir, _input_json(args.reported_rewards), _input_json(args.contract))
    except ScoreError as error:
        parser.error(str(error))
    json.dump(result, sys.stdout, sort_keys=True)
    sys.stdout.write('\n')


if __name__ == '__main__':
    main()
