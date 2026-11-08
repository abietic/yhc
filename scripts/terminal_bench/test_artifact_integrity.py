"""Independent byte/type oracles and real subprocess cleanup; no provider."""

import asyncio
import gc
import hashlib
import io
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.terminal_bench.artifact_integrity import (
    ArtifactIntegrityError, attest_docker_artifact, source_manifest, verify_archive,
)


def archive(entries):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w") as output:
        for name, value in entries:
            member = tarfile.TarInfo(name)
            if value is None:
                member.type = tarfile.DIRTYPE
                output.addfile(member)
            elif isinstance(value, tuple):
                member.type, member.linkname = value
                output.addfile(member)
            else:
                member.size = len(value)
                output.addfile(member, io.BytesIO(value))
    data.seek(0)
    return data


class ArtifactIntegrityTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.file = self.root / "source.bin"
        self.file.write_bytes(b"\0test\xff\n")

    def test_file_can_have_a_different_target_name(self):
        expected = source_manifest(self.file)
        self.assertEqual(expected, {"": {"type": "file", "bytes": 7,
            "sha256": hashlib.sha256(b"\0test\xff\n").hexdigest()}})
        report = verify_archive(expected, "/app/different.bin",
                                archive([("different.bin", self.file.read_bytes())]))
        self.assertEqual((report["file_count"], report["bytes"]), (1, 7))

    def test_directory_including_empty_directory_and_hostile_names(self):
        source = self.root / "source"
        source.mkdir()
        (source / "empty").mkdir()
        (source / "space;quote'\n雪").write_bytes(b"payload")
        expected = source_manifest(source)
        report = verify_archive(expected, "/app/src", archive([
            ("src", None), ("src/empty", None), ("src/space;quote'\n雪", b"payload")]))
        self.assertEqual(report["directory_count"], 2)
        self.assertEqual(report["file_count"], 1)

    def test_existing_target_files_are_outside_uploaded_projection(self):
        report = verify_archive(source_manifest(self.file), "/app/file", archive([
            ("file", self.file.read_bytes())]))
        self.assertEqual(report["additional_target_entries_unverified"], 0)
        source = self.root / "source"
        source.mkdir()
        (source / "new").write_bytes(b"new")
        report = verify_archive(source_manifest(source), "/app/src", archive([
            ("src", None), ("src/new", b"new"), ("src/excluded-existing", b"untouched")]))
        self.assertEqual(report["additional_target_entries_unverified"], 1)
        self.assertEqual(report["bytes"], 3)

    def test_mismatch_missing_type_and_duplicate_fail_closed(self):
        expected = source_manifest(self.file)
        cases = [
            [("file", b"bad")],
            [("file", b"\0test\xfe\n")],
            [],
            [("file", None)],
            [("file", self.file.read_bytes()), ("file", self.file.read_bytes())],
        ]
        for entries in cases:
            with self.subTest(entries=entries), self.assertRaises(ArtifactIntegrityError):
                verify_archive(expected, "/app/file", archive(entries))

    def test_archive_cannot_traverse_link_or_change_root(self):
        for name, value in [
            ("/file", b"x"), ("file/../other", b"x"), ("other", b"x"),
            ("file", (tarfile.SYMTYPE, "/etc/passwd")),
            ("file", (tarfile.LNKTYPE, "other")),
            ("file", (tarfile.FIFOTYPE, "")),
        ]:
            with self.subTest(name=name, value=value), self.assertRaises(ArtifactIntegrityError):
                verify_archive(source_manifest(self.file), "/app/file", archive([(name, value)]))

    def test_malformed_archive_and_source_link_are_rejected(self):
        with self.assertRaisesRegex(ArtifactIntegrityError, "archive_unqualified"):
            verify_archive(source_manifest(self.file), "/app/file", io.BytesIO(b"not a tar"))
        link = self.root / "link"
        link.symlink_to(self.file)
        with self.assertRaisesRegex(ArtifactIntegrityError, "source_type_unqualified"):
            source_manifest(link)
        folder = self.root / "folder"
        folder.mkdir()
        (folder / "escape").symlink_to(self.file)
        with self.assertRaisesRegex(ArtifactIntegrityError, "source_type_unqualified"):
            source_manifest(folder)


class ArtifactTransportTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.file = self.root / "source"
        self.file.write_bytes(b"payload")
        self.tar = self.root / "archive.tar"
        self.tar.write_bytes(archive([("file", b"payload")]).getvalue())
        self.real_spawn = asyncio.create_subprocess_exec
        self.created = asyncio.Event()
        self.children = []
        self.commands = []
        self.body = "import pathlib,sys;sys.stdout.buffer.write(pathlib.Path(sys.argv[1]).read_bytes())"

        async def spawn(*args, **kwargs):
            self.commands.append(args)
            child = await self.real_spawn(sys.executable, "-c", self.body, str(self.tar), **kwargs)
            self.children.append(child)
            self.created.set()
            return child

        replacement = patch("scripts.terminal_bench.artifact_integrity.asyncio.create_subprocess_exec", spawn)
        replacement.start()
        self.addCleanup(replacement.stop)

    async def test_archive_is_read_without_any_guest_exec(self):
        result = await attest_docker_artifact(self.file, "/app/file", "a" * 64)
        self.assertEqual(result["bytes"], 7)
        self.assertEqual(self.commands, [("docker", "cp", "a" * 64 + ":/app/file", "-")])
        self.assertEqual(self.children[0].returncode, 0)

    async def test_transport_failure_is_not_a_byte_mismatch(self):
        self.body = "raise SystemExit(13)"
        with self.assertRaisesRegex(ArtifactIntegrityError, "transport_failed"):
            await attest_docker_artifact(self.file, "/app/file", "a" * 64)
        self.assertEqual(self.children[0].returncode, 13)

    async def test_timeout_terminates_and_reaps_only_the_transport(self):
        self.body = "import threading;threading.Event().wait()"
        with self.assertRaisesRegex(ArtifactIntegrityError, "archive_timeout"):
            await attest_docker_artifact(self.file, "/app/file", "a" * 64, timeout=0.1)
        self.assertIsNotNone(self.children[0].returncode)

    async def test_cancellation_reaps_the_started_transport(self):
        self.body = "import threading;threading.Event().wait()"
        pending = asyncio.create_task(attest_docker_artifact(self.file, "/app/file", "a" * 64))
        await self.created.wait()
        pending.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await pending
        self.assertIsNotNone(self.children[0].returncode)

    async def test_size_limit_terminates_and_reaps_transport(self):
        self.body = "import sys;sys.stdout.buffer.write(b'x' * ((64 << 20) + 1));sys.stdout.flush()"
        with self.assertRaisesRegex(ArtifactIntegrityError, "archive_size_limit"):
            await attest_docker_artifact(self.file, "/app/file", "a" * 64)
        self.assertIsNotNone(self.children[0].returncode)

    async def test_unqualified_target_or_identity_never_starts_docker(self):
        for target, identity in [("/", "a" * 64), ("relative", "a" * 64),
                                 ("/app/../file", "a" * 64), ("/app/file", "short-id")]:
            with self.subTest(target=target, identity=identity), self.assertRaises(ArtifactIntegrityError):
                await attest_docker_artifact(self.file, target, identity)
        self.assertEqual(self.commands, [])


class ArtifactCancellationDiagnosticsTests(unittest.TestCase):
    def test_cancellation_has_no_unretrieved_async_exception(self):
        diagnostics = []

        async def exercise():
            loop = asyncio.get_running_loop()
            loop.set_exception_handler(lambda _loop, context: diagnostics.append(context["message"]))
            with tempfile.TemporaryDirectory() as directory:
                source = Path(directory) / "source"
                source.write_bytes(b"payload")
                real_spawn = asyncio.create_subprocess_exec
                created = asyncio.Event()
                children = []

                async def spawn(*_args, **kwargs):
                    child = await real_spawn(sys.executable, "-c",
                        "import threading;threading.Event().wait()", **kwargs)
                    children.append(child)
                    created.set()
                    return child

                with patch("scripts.terminal_bench.artifact_integrity.asyncio.create_subprocess_exec", spawn):
                    pending = asyncio.create_task(attest_docker_artifact(source, "/app/file", "a" * 64))
                    await created.wait()
                    pending.cancel()
                    try:
                        await pending
                    except asyncio.CancelledError:
                        pass
                    self.assertIsNotNone(children[0].returncode)
                    del pending
                    await asyncio.sleep(0)
                    gc.collect()

        asyncio.run(exercise())
        gc.collect()
        self.assertEqual(diagnostics, [])


if __name__ == "__main__":
    unittest.main()
