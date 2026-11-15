"""Read closed native artifact chains; never execute or restore their contents.

The caller owns capture provenance, immutable evidence, and verifier isolation
observations. This verifies regular file/directory bytes across that evidence,
not the grader, services, native session semantics, usage, or resumability.
"""
import fnmatch
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import tarfile

from scripts.terminal_bench.artifact_integrity import ArtifactIntegrityError, source_manifest


class CloseoutError(ValueError):
    pass


def _need(condition, reason):
    if not condition:
        raise CloseoutError(reason)


def _path(value, *, directory=False):
    _need(isinstance(value, str) and '\0' not in value, 'artifact_path_unqualified')
    # Match the transport contract; cwd and archive identities stay canonical.
    if directory and value.endswith('/'):
        value = value[:-1]
    path = PurePosixPath(value)
    _need(path.is_absolute() and str(path) == value and path != PurePosixPath('/')
          and '..' not in path.parts, 'artifact_path_unqualified')
    return path


def _below(path, parent):
    return path == parent or path.startswith(parent + '/')


def _declarations(declared):
    _need(isinstance(declared, list) and declared and len(declared) <= 1024,
          'artifact_declarations_unverified')
    rules = {}
    for entry in declared:
        if isinstance(entry, str):
            source, exclude = entry, []
        else:
            _need(isinstance(entry, dict) and set(entry) <= {'source', 'exclude', 'service', 'destination'}
                  and entry.get('service') in (None, 'main')
                  and entry.get('destination') in (None, ''), 'artifact_declarations_unverified')
            source, exclude = entry.get('source'), entry.get('exclude', [])
        _path(source, directory=True)
        _need(source not in rules, 'artifact_declarations_overlap')
        # Harbor's Linux collector uses GNU tar's default unanchored, recursive,
        # case-sensitive excludes. Only this calibrated glob subset is admitted;
        # locale-dependent character classes and escaping remain unverified.
        _need(isinstance(exclude, list) and len(exclude) <= 128
              and all(isinstance(p, str) and 0 < len(p) <= 1024
                      and not any(c in p for c in '\0\r\n[]\\')
                      and not p.endswith('/') for p in exclude),
              'artifact_exclusions_unqualified')
        rules[source] = tuple(re.compile(r'(?:^|/)' + fnmatch.translate(p)[:-2] + r'(?=/|$)')
                              for p in exclude)
    paths = [str(_path(p, directory=True)) for p in rules]
    _need(len(set(paths)) == len(paths) and not any(
        _below(a, b) or _below(b, a) for i, a in enumerate(paths) for b in paths[i+1:]),
        'artifact_declarations_overlap')
    pin = hashlib.sha256(json.dumps(sorted(declared,
        key=lambda e: e if isinstance(e, str) else e['source']),
        sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return rules, pin


def artifact_sources(declared):
    """Validate supported declarations before dispatch; preserve raw checkpoint declarations.

    These paths are for capture, not permission to omit the original structured
    declarations from the checkpoint. Sidecars, destination remaps, and advanced
    tar patterns are deliberately rejected before native or provider execution.
    """
    return list(_declarations(declared)[0])


def _excluded(relative, patterns):
    # tar is invoked with '-C source .'. Match a name or any ancestor from
    # any component boundary; '*' can span '/'. The artifact root itself stays
    # present because Harbor creates the collection directory even for empty tar.
    return any(pattern.search('./' + relative) for pattern in patterns)


def _projection(entries):
    files = [e for e in entries.values() if e['type'] == 'file']
    return dict(scope='uploaded_projection', file_count=len(files),
                directory_count=len(entries)-len(files), bytes=sum(e['bytes'] for e in files),
                manifest_sha256=hashlib.sha256(json.dumps(entries, sort_keys=True,
                    separators=(',', ':')).encode()).hexdigest(),
                additional_target_entries_unverified=0)


def _collected(folder, target):
    path = folder
    _need(path.is_dir() and not path.is_symlink(), 'artifact_source_unqualified')
    for part in _path(target, directory=True).parts[1:]:
        path = path / part
        _need(not path.is_symlink(), 'artifact_source_unqualified')
    return path


def _signature(info):
    return (info.st_dev, info.st_ino, info.st_size, info.st_mode,
            info.st_mtime_ns, info.st_ctime_ns)


def _archive_trees(path, digest, sources, cwd, rules):
    trees = {p: {} for p in sources}
    # Use canonical filesystem roots without rewriting the evidence's raw keys.
    paths = {p: str(_path(p, directory=True)) for p in sources}
    seen, total, workspace = set(), 0, False
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
    with os.fdopen(os.open(path, flags), 'rb') as raw:
        before = os.fstat(raw.fileno())
        _need(stat.S_ISREG(before.st_mode) and before.st_size <= 3 << 30,
              'checkpoint_archive_unqualified')
        actual = hashlib.sha256()
        while chunk := raw.read(1 << 20):
            actual.update(chunk)
        _need(actual.hexdigest() == digest, 'checkpoint_hash_mismatch')
        raw.seek(0)
        with tarfile.open(fileobj=raw, mode='r|gz') as archive:
            for member in archive:
                relative = PurePosixPath(member.name)
                _need(not relative.is_absolute() and str(relative) == member.name
                      and relative.parts and '..' not in relative.parts,
                      'checkpoint_path_unqualified')
                name = '/' + member.name
                _need(name not in seen, 'checkpoint_duplicate_entry')
                seen.add(name)
                total += member.size
                _need(len(seen) <= 150000 and total <= 3 << 30, 'checkpoint_archive_bound')
                if name == cwd:
                    _need(member.isdir(), 'checkpoint_workspace_unqualified')
                    workspace = True
                # Ancestor links cannot attest a declared file or its absence.
                if any(_below(p, name) for p in paths.values()) and name not in paths.values():
                    _need(member.isdir(), 'checkpoint_ancestor_unqualified')
                targets = [p for p in sources if _below(name, paths[p])]
                if not targets:
                    continue
                for target in targets:
                    relative_name = name[len(paths[target]):].lstrip('/')
                    if relative_name and rules[target] and _excluded(relative_name, rules[target]):
                        continue
                    _need(member.isfile() or member.isdir(), 'checkpoint_artifact_unqualified')
                    if member.isdir():
                        entry = dict(type='directory')
                    else:
                        hashed = hashlib.sha256()
                        with archive.extractfile(member) as stream:
                            while chunk := stream.read(65536):
                                hashed.update(chunk)
                        entry = dict(type='file', bytes=member.size, sha256=hashed.hexdigest())
                    trees[target][relative_name] = entry
        _need(_signature(before) == _signature(os.fstat(raw.fileno()))
              == _signature(Path(path).lstat()), 'checkpoint_archive_changed')
    _need(workspace, 'checkpoint_workspace_unverified')
    return trees


def verify_artifacts(folder, archive, declared, manifest, checkpoint, transfers, isolation, *, session_id):
    """Bind an immutable capture, collected trees, and exact upload projections.

    Inputs are closed operator evidence, not solver claims. Missing outputs
    require absence inside a captured workspace. Empty directories are present.
    Main-service paths may be strings or JSON declarations with supported GNU
    tar excludes. Original declarations must also be bound in the checkpoint.
    Additional verifier entries remain blocked even though the lower-level
    transport can report a partial uploaded projection.
    """
    try:
        rules, declaration_pin = _declarations(declared)
        sources = set(rules)
        paths = {p: str(_path(p, directory=True)) for p in sources}
        _need(isinstance(checkpoint, dict) and checkpoint.get('capture') == 'complete'
              and checkpoint.get('captured_before_grading') is True
              and checkpoint.get('workspace_in_archive') is True
              and checkpoint.get('native_session_files'), 'checkpoint_chain_unverified')
        _need(isinstance(checkpoint.get('declared_artifacts'), list)
              and _declarations(checkpoint['declared_artifacts'])[1] == declaration_pin,
              'checkpoint_declarations_changed')
        cwd = str(_path(checkpoint.get('cwd')))
        digest = checkpoint.get('checkpoint_sha256')
        _need(isinstance(digest, str) and re.fullmatch('[0-9a-f]{64}', digest),
              'checkpoint_hash_unverified')
        _need(isinstance(manifest, list) and all(isinstance(m, dict) for m in manifest),
              'artifact_manifest_unverified')
        rows = [m for m in manifest if m.get('source') in sources]
        _need(len(rows) == len(sources) and {m['source'] for m in rows} == sources,
              'artifact_manifest_incomplete')
        expected, missing = {}, []
        folder = Path(folder)
        for row in rows:
            target = row['source']
            _need(row.get('service') in (None, 'main') and row.get('type') in ('file', 'directory'),
                  'artifact_collection_unqualified')
            _path(target, directory=row['type'] == 'directory')
            path = _collected(folder, target)
            if row.get('status') in ('ok', 'empty'):
                entries = source_manifest(path)
                _need(entries['']['type'] == row['type'], 'artifact_collection_type_mismatch')
                _need(row['status'] != 'empty' or entries == {'': {'type': 'directory'}},
                      'artifact_empty_mismatch')
                expected[target] = entries
            else:
                _need(row.get('status') in ('missing', 'failed') and not path.exists()
                      and _below(paths[target], cwd), 'artifact_absence_unverified')
                missing.append(target)
                expected[target] = {}
        trees = _archive_trees(archive, digest, sources, cwd, rules)
        _need(trees == expected, 'checkpoint_and_collected_artifacts_differ')
        _need(isinstance(session_id, str) and session_id and isinstance(isolation, list)
              and isolation and all(isinstance(p, dict) and p.get('network_mode') == 'none'
                  and p.get('session_id') == session_id and isinstance(p.get('container_id'), str)
                  and re.fullmatch('[0-9a-f]{64}', p['container_id']) for p in isolation),
              'artifact_isolation_unverified')
        ids = {p['container_id'] for p in isolation}
        _need(isinstance(transfers, list) and all(isinstance(p, dict) for p in transfers),
              'artifact_transfer_unverified')
        proofs = [p for p in transfers if p.get('session_id') == session_id]
        present = sources - set(missing)
        _need(len(proofs) == len(present) and {p.get('target') for p in proofs} == present,
              'artifact_transfer_coverage')
        for proof in proofs:
            target = proof['target']
            projection = proof.get('projection')
            wanted = _projection(expected[target])
            _need(proof.get('container_id') in ids and proof.get('network_mode') == 'none'
                  and proof.get('stage') == 'after_upload_before_official_grading'
                  and proof.get('source_and_verifier_bytes_match') is True
                  and isinstance(projection, dict)
                  and all(type(projection.get(k)) is type(v) and projection[k] == v
                          for k, v in wanted.items()), 'artifact_transfer_mismatch')
        for target, entries in expected.items():
            path = _collected(folder, target)
            _need((source_manifest(path) == entries) if entries else not path.exists(),
                  'artifact_source_changed')
        result = dict(artifact_chain_verified=True,
                      present_artifacts={p: _projection(expected[p]) for p in sorted(present)},
                      missing_artifacts=sorted(missing), host_executes_model_code=False,
                      can_resume_agent=False)
        if any(isinstance(e, dict) for e in declared):
            result['artifact_selection'] = dict(declarations_sha256=declaration_pin,
                                                exclude_profile='gnu-tar-simple-glob-v1')
        return result
    except CloseoutError:
        raise
    except (OSError, ValueError, TypeError, KeyError, tarfile.TarError, EOFError,
            ArtifactIntegrityError) as error:
        raise CloseoutError('artifact_evidence_unverified') from error
