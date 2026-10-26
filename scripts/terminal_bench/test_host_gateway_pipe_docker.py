"""Opt-in cold dispatch through a host fake provider and a network-none pipe.

The bridge is injected only after production cohort admission. It is test
instrumentation, not an enabled operator transport or permission to spend.
"""

import hashlib
import http.server
import json
import os
import socket
import subprocess
import threading
import time
import unittest
import uuid
from decimal import Decimal
from pathlib import Path

from scripts.terminal_bench import test_recovery_bundle_docker as native_fixture
from scripts.terminal_bench.cold_dispatch import ColdBenchmarkLauncher
from scripts.terminal_bench.cohort_admission import CohortAdmission
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.service_snapshot import SnapshotError, digest
from scripts.terminal_bench.test_cold_dispatch import grant_for
from scripts.terminal_bench.test_cohort_admission import cohort_plan
from scripts.terminal_bench.test_recovery_bundle import history
from scripts.terminal_bench.testdata.host_gateway_pipe import Frames, PipeFailure, forward
from scripts.terminal_bench.testdata.native_handoff import OUTPUT, verify_input


@unittest.skipUnless(os.environ.get("YHC_HOST_GATEWAY_PIPE_DOCKER_TEST") == "1"
                     and os.environ.get("YHC_NATIVE_RECOVERY_DOCKER_TEST") == "1", "explicit native and host-pipe Docker opt-in")
class HostPipeColdDockerTests(native_fixture.NativeColdDockerTests):
    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        parent = cls.image
        cls.image = "yhc-host-pipe-proof:" + uuid.uuid4().hex[:12]
        helper = cls.docker("create", "--pull", "never", "--network", "none", "--entrypoint", "/bin/sleep", parent, "infinity")
        try:
            cls.docker("cp", str(Path(__file__).parent / "testdata/host_gateway_pipe.py"), helper + ":/opt/checkpoint/host_gateway_pipe.py")
            cls.docker("commit", helper, cls.image)
        finally: cls.docker("rm", "--force", helper)
        cls.addClassCleanup(cls.docker, "image", "rm", cls.image)

    def check_host_pipe(self, scenario):
        manifest, observed, _ = self.capture_joint(paused=True, environment_key=True, generated_session=True)
        bundle = self.target_bundle()
        restored = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        grant = grant_for(manifest, self.bundle_digest, self.attempt, int(time.time()))
        grant["binary_sha256"] = hashlib.sha256(Path(os.environ["YHC_NATIVE_RECOVERY_BINARY"]).read_bytes()).hexdigest()
        # Keep the exact source URL and opaque credential for native route binding.
        # The container port is inherited from capture, never selected by a task.
        grant["route"]["base_url"] = "http://127.0.0.1:" + str(self.paused_execution["provider_port"])
        plan = cohort_plan([manifest["usage_history"], history(str(uuid.uuid4()))], cohort=manifest["cohort_id"],
                           cap="0.1" if scenario == "budget" else "6")
        plan["expires_unix"] = int(time.time()) + 2000
        cohort = CohortAdmission.create(self.control / "host-pipe-cohort", plan, digest(plan))
        before = cohort.inspect()
        state = dict(requests=0, errors=[], admissions=[], bridge_results=[], bridge_errors=[], launches=0, cleanup=[])
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *args): pass
            def do_POST(self):
                stage = "request_route"
                try:
                    fixture.assertEqual(self.path, "/responses")
                    fixture.assertEqual(self.headers.get("Authorization"), "Bearer fixture")
                    length = int(self.headers["Content-Length"])
                    fixture.assertTrue(0 < length <= 4 << 20)
                    body = json.loads(self.rfile.read(length))
                    state["requests"] += 1
                    count = state["requests"]
                    fixture.assertLessEqual(count, 2 if scenario == "known" else 1)
                    stage = "native_history"
                    verify_input(body, provider_call_limit=2, cold_round=count,
                                 cold_prompt="Continue the saved task using the verified restored state. Do not repeat completed side effects.")
                    checkpoint = state["launch"]["checkpoint_path"]
                    stage = "durable_admission"
                    program = ("import json,os,sys;from pathlib import Path;"
                               "p=Path(sys.argv[1]);s=json.loads((p/'owner.json').read_bytes())['segment_id'];"
                               "os.execv('/opt/yhc/yhc',['yhc','usage','inspect','--checkpoint-dir',str(p),'--segment-id',s])")
                    view = json.loads(fixture.docker("exec", "--workdir", "/workspace", bundle.workspace.container,
                                      "/usr/local/bin/python3", "-I", "-c", program, checkpoint))
                    snapshot = view["snapshot"]
                    fixture.assertEqual(view["phase"], "admitted")
                    fixture.assertFalse(view["admission_closed"])
                    fixture.assertEqual(snapshot["provider_calls"], count)
                    fixture.assertEqual(snapshot["in_flight"], 1)
                    fixture.assertEqual(snapshot["known_calls"], count - 1)
                    fixture.assertNotEqual(snapshot["cost"]["reservation"]["outstanding_amount"], "0.000000000000")
                    stage = "cohort_claim"
                    claim = json.loads((fixture.control / ("dispatch." + manifest["paused_invocation"]["record"]["segment_id"] + ".json")).read_bytes())
                    fixture.assertEqual(Decimal(claim["cohort_admission"]["reserved_amount"]), Decimal("5"))
                    state["admissions"].append(view)
                except Exception as error:
                    state["errors"].append(stage + ":" + type(error).__name__)
                    self.send_error(400); self.close_connection = True
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                if scenario == "reset":
                    events = [dict(type="response.output_text.delta", sequence_number=0, item_id="offline-final",
                                   output_index=0, content_index=0, delta="owned partial response")]
                else:
                    item = dict(type="message", id="offline-final", role="assistant", status="completed",
                                content=[dict(type="output_text", text=OUTPUT, annotations=[])])
                    event = dict(type="response.output_text.delta", sequence_number=0, item_id="offline-final",
                                 output_index=0, content_index=0, delta=OUTPUT)
                    if state["requests"] == 1:
                        item = dict(type="function_call", id="cold-edit-item", call_id="cold-edit", name="Edit", status="completed",
                                    arguments=json.dumps(dict(file_path="/workspace/continued.txt", old_string="continued once", new_string="edited after cold restore")))
                        event = dict(type="response.output_item.done", sequence_number=0, output_index=0, item=item)
                    events = [event, dict(type="response.completed", sequence_number=1, response=dict(id="offline-response", object="response",
                              status="completed", model="deepseek-v4-flash", output=[item], usage=dict(input_tokens=10, output_tokens=4, total_tokens=14,
                              input_tokens_details=dict(cached_tokens=6), output_tokens_details=dict(reasoning_tokens=2))))]
                for event in events:
                    raw = ("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode()
                    self.wfile.write(format(len(raw), "x").encode() + b"\r\n" + raw + b"\r\n"); self.wfile.flush()
                if scenario == "reset": self.connection.shutdown(socket.SHUT_RDWR)
                else: self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
                self.close_connection = True

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        server_thread = threading.Thread(target=server.serve_forever, daemon=True)
        server_thread.start()
        def close_server():
            server.shutdown(); server.server_close(); server_thread.join(3)
            self.assertFalse(server_thread.is_alive())
        self.addCleanup(close_server)
        original = bundle.workspace.rpc
        calls = []

        def rpc(operation, **body):
            calls.append(operation)
            if operation != "cold_dispatch": return original(operation, **body)
            state["launch"] = body["launch"]; state["launches"] += 1
            nonce = str(uuid.uuid4())
            command = ["/bin/true"] if scenario == "unready" else ["/usr/local/bin/python3", "-I", "/opt/checkpoint/host_gateway_pipe.py", nonce]
            process = subprocess.Popen(["docker", "exec", "--interactive", bundle.workspace.container, *command],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            thread = None
            try:
                channel = Frames(process.stdout.fileno(), process.stdin.fileno(), nonce)
                channel.send("start", port=self.paused_execution["provider_port"], requests=2 if scenario == "known" else 1)
                ready, ready_ok = threading.Event(), []
                class ObservedChannel:
                    def receive(self):
                        value = channel.receive()
                        if value == dict(kind="ready", port=self_port): ready_ok.append(True); ready.set()
                        return value
                    send = channel.send
                    remaining = channel.remaining
                self_port = self.paused_execution["provider_port"]
                def pump():
                    try:
                        state["bridge_results"].append(forward(ObservedChannel(), "127.0.0.1", server.server_port,
                            self_port, 2 if scenario == "known" else 1))
                    except PipeFailure as error: state["bridge_errors"].append(str(error))
                    finally: ready.set()
                thread = threading.Thread(target=pump, daemon=True); thread.start()
                self.assertTrue(ready.wait(5) and ready_ok, "owned bridge did not become ready")
                result = original(operation, **body)
                process.wait(timeout=5); thread.join(3)
                self.assertFalse(thread.is_alive(), "pipe exchange did not terminate")
                self.assertEqual(process.returncode, 0 if scenario == "known" else 1)
                self.assertEqual(bool(state["bridge_errors"]), scenario != "known")
                return result
            finally:
                try:
                    if process.poll() is None: process.kill()
                    process.wait(timeout=5)
                finally:
                    if thread is not None: thread.join(3)
                    process.stdin.close(); process.stdout.close()
                    if thread is not None and thread.is_alive(): thread.join(1)
                    alive = thread is not None and thread.is_alive()
                    state["cleanup"].append(dict(reaped=process.poll() is not None, stdin_closed=process.stdin.closed,
                                                 stdout_closed=process.stdout.closed, thread_alive=alive))
                    self.assertFalse(alive, "fixture pipe thread leaked")

        bundle.workspace.rpc = rpc
        launcher = ColdBenchmarkLauncher(bundle, cohort=cohort)
        if scenario == "budget":
            with self.assertRaisesRegex(SnapshotError, "cohort_budget_insufficient"):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(calls, [])
            self.assertEqual(cohort.inspect(), before)
            self.assertEqual(state["launches"], 0)
            self.assertEqual(state["requests"], 0)
            self.assertFalse(any(self.control.glob("dispatch.*.json")))
            result = None
        elif scenario == "unready":
            with self.assertRaises((PipeFailure, AssertionError)):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(state["requests"], 0)
            self.assertEqual(state["launches"], 1)
            self.assertEqual(state["cleanup"], [dict(reaped=True, stdin_closed=True, stdout_closed=True, thread_alive=False)])
            self.assertTrue(cohort.inspect()["blocked"])
            self.assertEqual(cohort.inspect()["retained_amount"], "5.000000000000")
            stopped = json.loads(self.docker("inspect", bundle.workspace.container))[0]["State"]
            self.assertFalse(stopped["Running"] or stopped["Restarting"])
            self.assertEqual(stopped["Pid"], 0)
            volume = restored["workspace_runtime"]["volume"]
            text = self.docker("run", "--rm", "--pull", "never", "--read-only", "--network", "none", "--cap-drop", "ALL",
                "--mount", "type=volume,src=" + volume + ",dst=/proof,readonly", "--entrypoint", "/bin/cat", self.image, "/proof/continued.txt")
            self.assertEqual(text, "continued once")
            self.assertTrue((self.control / ("dispatch." + manifest["paused_invocation"]["record"]["segment_id"] + ".json")).is_file())
            evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
            if evidence:
                write_report(Path(evidence) / "host-pipe-unready.json", dict(verification_complete=True,
                    scope="test_only_host_pipe", observed=state, cohort=cohort.inspect(), workspace_stopped=True,
                    original_progress_retained=True, real_provider_requests=0, production_transport_enabled=False))
            return
        else:
            result = launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(state["errors"], [])
            self.assertEqual(state["requests"], 2 if scenario == "known" else 1)
            self.assertEqual(result["status"], "completed" if scenario == "known" else "stopped")
            self.assertEqual(result["accounting_complete"], scenario == "known")
            self.assertEqual(result["usage_history"]["segments"][:-1], manifest["usage_history"]["segments"])
            self.assertEqual(cohort.inspect()["blocked"], scenario != "known")
            self.assertEqual(cohort.inspect()["retained_amount"], "0.000000000000" if scenario == "known" else "5.000000000000")
            self.assertEqual(cohort.inspect(), CohortAdmission(cohort.root, digest(plan)).inspect())
            if scenario == "known":
                self.assertTrue(state["bridge_results"][0]["transport_complete"])
                self.assertFalse(state["bridge_results"][0]["provider_usage_verified"])
            else:
                checkpoint = result["execution"]["checkpoint"]
                self.assertTrue(checkpoint["admission_closed"])
                self.assertEqual(checkpoint["phase"], "sealed")
                self.assertEqual(checkpoint["snapshot"]["unknown_calls"], 1)
                self.assertFalse(checkpoint["accounting_complete"])
            with self.assertRaisesRegex(SnapshotError, "already_claimed"):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(state["launches"], 1)
        text = self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt")
        self.assertEqual(text, "edited after cold restore" if scenario == "known" else "continued once")
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        for name, original_state in observed.items():
            actual = json.loads(self.docker("exec", bundle.services.backend.handles[name],
                "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original_state["memory"], original_state["durable"], original_state["next_random"]))
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / ("host-pipe-" + scenario + ".json"), dict(verification_complete=True, scope="test_only_host_pipe",
                session_id=self.session, result=result, cohort=cohort.inspect(), observed=state, original_restore=restored,
                network_profile="none", services_preserved_after_dispatch=True, real_provider_requests=0, production_transport_enabled=False))

    def test_host_pipe_known_cold_dispatch(self): self.check_host_pipe("known")
    def test_host_pipe_reset_retains_closed_unknown_usage(self): self.check_host_pipe("reset")
    def test_host_pipe_budget_denial_precedes_bridge(self): self.check_host_pipe("budget")
    def test_host_pipe_startup_eof_reaps_transport_and_retains_claim(self): self.check_host_pipe("unready")


def load_tests(loader, tests, pattern):
    # Reuse factory methods without rerunning the inherited native-only matrix.
    return unittest.TestSuite(HostPipeColdDockerTests(name) for name in (
        "test_host_pipe_known_cold_dispatch", "test_host_pipe_reset_retains_closed_unknown_usage", "test_host_pipe_budget_denial_precedes_bridge",
        "test_host_pipe_startup_eof_reaps_transport_and_retains_claim"))
