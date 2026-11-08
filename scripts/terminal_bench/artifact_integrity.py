"""Verify uploaded file bytes through Docker archives, without guest tooling.

The caller owns container identity, isolation, and upload ordering. Verification
covers the source projection, preserving Harbor's merge/exclude semantics; it
does not prove an entire workspace, filesystem metadata, or resumability.
"""

import asyncio
import hashlib
import json
import math
import os
from pathlib import Path, PurePosixPath
import re
import stat
import tarfile
import tempfile


class ArtifactIntegrityError(ValueError):
    pass


def _identity(value):
    return (value.st_dev, value.st_ino, value.st_mode, value.st_size,
            value.st_mtime_ns, value.st_ctime_ns)


def source_manifest(source):
    """Describe only regular files/directories; never follow source links."""
    entries = {}

    def visit(path, relative):
        before = path.lstat()
        if stat.S_ISDIR(before.st_mode):
            entries[relative] = {"type": "directory"}
            for child in sorted(path.iterdir()):
                visit(child, str(PurePosixPath(relative) / child.name))
        elif stat.S_ISREG(before.st_mode):
            flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
            with os.fdopen(os.open(path, flags), "rb") as stream:
                if _identity(os.fstat(stream.fileno())) != _identity(before):
                    raise ArtifactIntegrityError("artifact_source_changed")
                digest, size = hashlib.sha256(), 0
                while chunk := stream.read(65536):
                    size += len(chunk)
                    if size > before.st_size:
                        raise ArtifactIntegrityError("artifact_source_changed")
                    digest.update(chunk)
                if size != before.st_size or _identity(os.fstat(stream.fileno())) != _identity(before):
                    raise ArtifactIntegrityError("artifact_source_changed")
            entries[relative] = {"type": "file", "bytes": size, "sha256": digest.hexdigest()}
        else:
            raise ArtifactIntegrityError("artifact_source_type_unqualified")
        if _identity(path.lstat()) != _identity(before):
            raise ArtifactIntegrityError("artifact_source_changed")

    try:
        visit(Path(source), "")
    except OSError as error:
        raise ArtifactIntegrityError("artifact_source_unavailable") from error
    return entries


def _target_path(target):
    if not isinstance(target, str) or "\0" in target:
        raise ArtifactIntegrityError("artifact_target_unqualified")
    path = PurePosixPath(target)
    if not path.is_absolute() or path == PurePosixPath("/") or ".." in path.parts or str(path) != target:
        raise ArtifactIntegrityError("artifact_target_unqualified")
    return path


def verify_archive(expected, target, stream):
    """Compare a Docker archive to the uploaded projection without extracting it."""
    root = _target_path(target).name
    seen, matched = set(), set()
    additional = 0
    try:
        with tarfile.open(fileobj=stream, mode="r:") as archive:
            for member in archive:
                path = PurePosixPath(member.name)
                if (path.is_absolute() or ".." in path.parts or not path.parts
                        or path.parts[0] != root or not (member.isfile() or member.isdir())):
                    raise ArtifactIntegrityError("artifact_archive_unqualified")
                relative = "/".join(path.parts[1:])
                if relative in seen:
                    raise ArtifactIntegrityError("artifact_archive_duplicate")
                seen.add(relative)
                entry = expected.get(relative)
                if entry is None:
                    additional += 1
                    continue
                if entry["type"] == "directory":
                    if not member.isdir():
                        raise ArtifactIntegrityError("artifact_bytes_mismatch")
                else:
                    if not member.isfile() or member.size != entry["bytes"]:
                        raise ArtifactIntegrityError("artifact_bytes_mismatch")
                    digest = hashlib.sha256()
                    with archive.extractfile(member) as content:
                        while chunk := content.read(65536):
                            digest.update(chunk)
                    if digest.hexdigest() != entry["sha256"]:
                        raise ArtifactIntegrityError("artifact_bytes_mismatch")
                matched.add(relative)
    except (tarfile.TarError, OSError, EOFError) as error:
        raise ArtifactIntegrityError("artifact_archive_unqualified") from error
    if matched != set(expected):
        raise ArtifactIntegrityError("artifact_bytes_mismatch")
    files = [entry for entry in expected.values() if entry["type"] == "file"]
    return {"scope": "uploaded_projection", "file_count": len(files),
            "directory_count": len(expected) - len(files),
            "bytes": sum(entry["bytes"] for entry in files),
            "manifest_sha256": hashlib.sha256(json.dumps(expected, sort_keys=True,
                separators=(",", ":")).encode()).hexdigest(),
            "additional_target_entries_unverified": additional}


async def attest_docker_artifact(source, target, container_id, *, timeout=60):
    """Read a bounded raw Docker archive; never execute artifact or guest code."""
    _target_path(target)
    if not isinstance(container_id, str) or re.fullmatch(r"[0-9a-f]{64}", container_id) is None:
        raise ArtifactIntegrityError("artifact_container_unqualified")
    if not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0:
        raise ArtifactIntegrityError("artifact_timeout_unqualified")
    expected = await asyncio.to_thread(source_manifest, source)
    size = sum(entry.get("bytes", 0) for entry in expected.values())
    # Bound extra image contents and archive metadata as well as expected bytes.
    limit = max(64 << 20, size * 2 + len(expected) * 65536 + (1 << 20))
    with tempfile.TemporaryFile() as archive:
        try:
            process = await asyncio.create_subprocess_exec(
                "docker", "cp", container_id + ":" + target, "-",
                stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.DEVNULL)
        except OSError as error:
            raise ArtifactIntegrityError("artifact_archive_transport_unavailable") from error

        async def copy_output():
            total = 0
            while chunk := await process.stdout.read(65536):
                total += len(chunk)
                if total > limit:
                    raise ArtifactIntegrityError("artifact_archive_size_limit")
                archive.write(chunk)

        tasks = [asyncio.create_task(copy_output()), asyncio.create_task(process.wait())]
        combined = asyncio.gather(*tasks)
        try:
            await asyncio.wait_for(combined, timeout=timeout)
            if process.returncode:
                raise ArtifactIntegrityError("artifact_archive_transport_failed")
        except BaseException as error:
            if process.returncode is None:
                process.kill()
            for task in tasks:
                task.cancel()
            await asyncio.gather(combined, *tasks, return_exceptions=True)
            await process.communicate()
            if isinstance(error, asyncio.TimeoutError):
                raise ArtifactIntegrityError("artifact_archive_timeout") from error
            raise
        archive.seek(0)
        return verify_archive(expected, target, archive)
