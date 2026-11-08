"""Opt-in archive verification against a cached Linux image without Python."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
import uuid

from scripts.terminal_bench.artifact_integrity import ArtifactIntegrityError, attest_docker_artifact


@unittest.skipUnless(os.environ.get("YHC_ARTIFACT_INTEGRITY_DOCKER_TEST") == "1",
                     "explicit cached Python-free image opt-in")
class ArtifactIntegrityDockerTests(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def docker(cls, *args, check=True):
        result = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=60)
        if check and result.returncode:
            raise AssertionError("owned Docker fixture operation failed")
        return result

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        image = os.environ["YHC_ARTIFACT_INTEGRITY_TEST_IMAGE"]
        if not re.fullmatch(r"(?:[A-Za-z0-9][A-Za-z0-9._:/-]*@)?sha256:[0-9a-f]{64}", image):
            raise AssertionError("an exact cached image is required")
        row, = json.loads(cls.docker("image", "inspect", image).stdout)
        if row["Os"] != "linux":
            raise AssertionError("a Linux image is required")
        cls.owner = uuid.uuid4().hex
        cls.cid = cls.docker("create", "--pull=never", "--platform", "linux/" + row["Architecture"],
            "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
            "--memory", "256m", "--cpus", "1", "--pids-limit", "64",
            "--label", "yhc.owner=" + cls.owner, "--entrypoint", "/bin/sleep", image, "infinity").stdout.strip()
        cls.addClassCleanup(cls.remove)
        cls.docker("start", cls.cid)
        actual, = json.loads(cls.docker("inspect", cls.cid).stdout)
        assert actual["Image"] == row["Id"] and actual["Mounts"] == []
        assert actual["HostConfig"]["NetworkMode"] == "none"
        assert actual["HostConfig"]["CapDrop"] == ["ALL"]
        probe = cls.docker("exec", cls.cid, "/bin/sh", "-c", "command -v python3", check=False)
        assert probe.returncode != 0, "this regression requires a Python-free verifier"

    @classmethod
    def remove(cls):
        actual, = json.loads(cls.docker("inspect", cls.cid).stdout)
        assert actual["Config"]["Labels"]["yhc.owner"] == cls.owner
        cls.docker("rm", "--force", cls.cid)

    async def asyncSetUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.source = Path(temporary.name) / "artifact"
        self.source.mkdir()
        (self.source / "empty").mkdir()
        (self.source / "quote';space\n雪").write_bytes(b"\0payload\xff\n")
        self.target = "/tmp/artifact-" + uuid.uuid4().hex
        self.docker("cp", str(self.source), self.cid + ":" + self.target)

    async def test_directory_and_file_without_guest_python_or_ownership_changes(self):
        target_file = self.target + "/quote';space\n雪"
        before = self.docker("exec", self.cid, "stat", "-c", "%u:%g:%a", target_file).stdout
        directory = await attest_docker_artifact(self.source, self.target, self.cid)
        file = await attest_docker_artifact(self.source / "quote';space\n雪", target_file, self.cid)
        self.assertEqual((directory["file_count"], directory["directory_count"]), (1, 2))
        self.assertEqual(file["bytes"], 10)
        after = self.docker("exec", self.cid, "stat", "-c", "%u:%g:%a", target_file).stdout
        self.assertEqual(before, after)

    async def test_mismatch_and_link_fail_closed(self):
        (self.source / "quote';space\n雪").write_bytes(b"changed")
        with self.assertRaisesRegex(ArtifactIntegrityError, "bytes_mismatch"):
            await attest_docker_artifact(self.source, self.target, self.cid)
        (self.source / "quote';space\n雪").write_bytes(b"\0payload\xff\n")
        # Docker Desktop preserves the host UID on copied directories. Install
        # this controlled link through the daemon, retaining cap-drop=ALL.
        link = self.source.parent / "guest-link"
        link.symlink_to("/unqualified-guest-target")
        self.docker("cp", str(link), self.cid + ":" + self.target + "/unqualified-link")
        with self.assertRaisesRegex(ArtifactIntegrityError, "archive_unqualified"):
            await attest_docker_artifact(self.source, self.target, self.cid)


if __name__ == "__main__":
    unittest.main()
