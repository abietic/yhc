"""Managed local client lifetime contracts; trusted POSIX fixtures only."""
import asyncio
import os
import sys
import unittest
from unittest.mock import patch

from harbor.environments.docker.docker import DockerEnvironment
from scripts.terminal_bench.harbor_trial import IsolatedDockerEnvironment
from scripts.terminal_bench.trial_policy import PolicyError


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
