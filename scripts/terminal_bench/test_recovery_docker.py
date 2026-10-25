"""Opt-in, offline two-service lifecycle proof using only a cached image.

YHC_RECOVERY_DOCKER_TEST=1 python -m unittest scripts.terminal_bench.test_recovery_docker
No original benchmark resources, model requests, image pulls, or host task-code
execution. SQLite is recoverable in this fixture; RAM and process clock are not.
"""

import asyncio
import json
import os
import subprocess
import tempfile
import unittest
import uuid
from pathlib import Path
from types import SimpleNamespace

from scripts.terminal_bench.recovery import observe_environment, retained_checkpoint_report, same_live_runtime


SERVICE = r'''
import http.server, json, os, sqlite3, time, uuid
os.makedirs('/state', exist_ok=True)
db = '/state/store.sqlite'
def durable(increment=False):
    with sqlite3.connect(db) as c:
        c.execute('CREATE TABLE IF NOT EXISTS counter (value INTEGER NOT NULL)')
        if not c.execute('SELECT count(*) FROM counter').fetchone()[0]:
            c.execute('INSERT INTO counter VALUES (0)')
        if increment:
            c.execute('UPDATE counter SET value=value+1')
        return c.execute('SELECT value FROM counter').fetchone()[0]
started, epoch, memory, clock_offset = time.monotonic(), uuid.uuid4().hex, 0, 0
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        global memory, clock_offset
        increment = self.path == '/advance'
        if increment:
            memory += 1
            clock_offset += 1000
        body = json.dumps(dict(durable=durable(increment), memory=memory,
                              epoch=epoch, clock_origin=started,
                              clock=time.monotonic()-started+clock_offset)).encode()
        self.send_response(200); self.end_headers(); self.wfile.write(body)
    def log_message(self, *args):
        pass
http.server.HTTPServer(('127.0.0.1', 8080), Handler).serve_forever()
'''


@unittest.skipUnless(os.environ.get("YHC_RECOVERY_DOCKER_TEST") == "1", "explicit offline Docker opt-in")
class DockerRecoveryTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        from harbor.environments.docker.docker import DockerEnvironment
        self.temporary = tempfile.TemporaryDirectory(prefix="yhc-recovery-proof-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.project = "yhc-recovery-proof-" + uuid.uuid4().hex[:12]
        self.image = os.environ.get("YHC_RECOVERY_DOCKER_IMAGE", "yhc-terminal-bench-python:3.13.14-cache")
        self.docker("image", "inspect", self.image)  # Must already be cached.
        common = {"image": self.image, "pull_policy": "never", "network_mode": "none",
                  "mem_limit": "128m", "cpus": 0.25, "read_only": True}
        compose = {"services": {
            "main": {**common, "entrypoint": ["python3", "-c", "import time; time.sleep(600)"]},
            "api": {**common, "read_only": False, "entrypoint": ["python3", "-u", "-c", SERVICE]},
        }}
        self.config = self.root / "compose.json"
        self.config.write_text(json.dumps(compose))
        # Use the pinned Harbor inventory seam with a real Compose command;
        # avoid its trial lifecycle, verifier and any provider configuration.
        self.environment = object.__new__(DockerEnvironment)
        self.environment.session_id = self.project
        async def command(argv, **_kwargs):
            output = await asyncio.to_thread(self.compose, *argv)
            return SimpleNamespace(stdout=output, return_code=0)
        self.environment._run_docker_compose_command = command
        self.addCleanup(self.compose, "down", "--volumes", "--remove-orphans")
        self.compose("up", "--detach", "--pull", "never", "--no-build")
        await self.wait_api()

    def docker(self, *argv):
        return subprocess.run(["docker", *argv], check=True, capture_output=True, text=True, timeout=40).stdout.strip()

    def compose(self, *argv):
        return self.docker("compose", "--project-name", self.project, "-f", str(self.config), *argv)

    def read_api(self, advance=False):
        api = self.compose("ps", "--quiet", "api")
        code = "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080/%s').read().decode())" % ("advance" if advance else "")
        return json.loads(self.docker("exec", api, "python3", "-c", code))

    async def wait_api(self):
        for _ in range(40):
            try:
                return await asyncio.to_thread(self.read_api)
            except subprocess.CalledProcessError:
                await asyncio.sleep(0.1)
        self.fail("fixture API did not become ready")

    async def test_retained_container_and_database_are_not_restored_ram_or_clock(self):
        before = await observe_environment(self.environment)
        self.assertTrue(before["inventory_complete"])
        self.assertTrue(same_live_runtime(before, await observe_environment(self.environment)))
        original = self.read_api(advance=True)
        self.assertGreaterEqual(original["clock"], 1000)
        api = self.compose("ps", "--quiet", "api")
        backup = self.root / "store.sqlite"
        self.docker("cp", api + ":/state/store.sqlite", str(backup))

        self.compose("stop", "api")
        stopped = await observe_environment(self.environment)
        self.assertFalse(same_live_runtime(before, stopped))
        self.compose("start", "api")
        await self.wait_api()
        restarted = self.read_api()
        self.assertEqual(restarted["durable"], 1)
        self.assertEqual(restarted["memory"], 0)
        self.assertNotEqual(restarted["epoch"], original["epoch"])
        self.assertNotEqual(restarted["clock_origin"], original["clock_origin"])
        self.assertLess(restarted["clock"], original["clock"])
        self.assertFalse(same_live_runtime(before, await observe_environment(self.environment)))

        # Actually reconstruct a different service container and restore just
        # its known file-backed state. Independent probes distinguish the state
        # that survived/restored from the in-memory counter and clock generation.
        self.compose("rm", "--stop", "--force", "api")
        self.compose("up", "--detach", "--pull", "never", "--no-build", "api")
        await self.wait_api()
        self.assertEqual(self.read_api()["durable"], 0)
        fresh_api = self.compose("ps", "--quiet", "api")
        self.assertNotEqual(api, fresh_api)
        self.docker("cp", str(backup), fresh_api + ":/state/store.sqlite")
        restored = self.read_api()
        self.assertEqual(restored["durable"], 1)
        self.assertEqual(restored["memory"], 0)
        self.assertNotEqual(restored["epoch"], original["epoch"])
        self.assertNotEqual(restored["clock_origin"], original["clock_origin"])
        self.assertLess(restored["clock"], original["clock"])
        report = retained_checkpoint_report({"capture": "complete", "workspace_in_archive": True,
                                             "native_session_files": [{"path": "session"}]},
                                            archive_verified=True)
        self.assertFalse(report["can_resume"])
