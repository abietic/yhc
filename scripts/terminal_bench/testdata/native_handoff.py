"""Owned container-only fixture for the public exec --resume entrypoint.

The provider is a deterministic loopback HTTP server. The container has no
network, credentials, package installation or task-controlled executable.
This is test evidence, never a production continuation or spending grant.
"""

import datetime
import hashlib
import http.server
import json
import os
import platform
import re
import signal
import subprocess
import sys
import tempfile
import threading
import uuid
from pathlib import Path

PROMPT = "continue the owned checkpoint in one new turn"
COLD_PROMPT = "finish the owned paused file task"
OUTPUT = "offline continuation complete"
BUDGET_TEMPLATE = (
    "<run-budget>\nRuntime-enforced limits for this invocation, shared with children and auxiliary calls. "
    "These are total limits, not remaining allowances; earlier calls may have consumed them.\n"
    "provider_call_limit=1\ndeadline_utc={deadline} (enforced deadline)\n"
    "Plan targeted verification and a final response within the remaining budget. Near the limit, avoid broad new exploration "
    "or delegation. Report unfinished work and unverified claims explicitly; budget pressure is not evidence of success.\n</run-budget>"
)
CONTEXT_TEMPLATE = (
    "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n"
    "# currentDate\nToday's date is {date}.\n# cwd\nCurrent working directory: /workspace\n"
    "# platform\nPlatform: {platform}.\n\nIMPORTANT: this context may or may not be relevant to your tasks. "
    "You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n"
)


def require(value, reason):
    if not value:
        raise ValueError(reason)


def text_parts(content):
    if isinstance(content, list):
        require(all(isinstance(part, dict) and part.get("type") in ("input_text", "output_text")
                    and isinstance(part.get("text"), str) for part in content), "unexpected_message_parts")
        content = "".join(part["text"] for part in content)
    require(isinstance(content, str), "invalid_message_text")
    return content


def verify_input(body, now=None, platform_name=None, tool_round=0, provider_call_limit=None, cold_round=0, cold_prompt=COLD_PROMPT):
    items = body.get("input")
    require(isinstance(items, list), "missing_input")
    projected = []
    for item in items:
        require(isinstance(item, dict), "invalid_input_item")
        kind, role = item.get("type", "message"), item.get("role")
        # Only leading system/developer context is outside this projection.
        if kind == "message" and role in ("system", "developer"):
            require(not projected, "system_context_inside_native_history")
            continue
        if kind == "function_call":
            projected.append(["call", item.get("call_id"), item.get("name"), json.loads(item["arguments"])])
        elif kind == "function_call_output":
            projected.append(["result", item.get("call_id"), text_parts(item.get("output"))])
        else:
            require(kind == "message" and role in ("user", "assistant"), "unexpected_input_item")
            projected.append(["message", role, text_parts(item.get("content"))])
    if cold_round == 2:
        memory = projected[-1]
        prefix = "[system] [Session Memory — context from previous interactions:]\n\n"
        require(memory[:2] == ["message", "user"] and memory[2].startswith(prefix)
                and sorted(memory[2][len(prefix):].splitlines()) == sorted([
                    "- [] File modified: /workspace/continued.txt (via Write)",
                    "- [] File modified: effects.count (via Write)"]), "unexpected_restored_memory_context")
        projected = projected[:-1]
    require(len(projected) == (13 + 3 * (cold_round - 1) if cold_round else 11 if tool_round == 2 else 8)
            and all(item[:2] == ["message", "user"] for item in projected[:2]), "unexpected_runtime_prefix")
    now = now or datetime.datetime.now(datetime.timezone.utc)
    deadline = re.search(r"\ndeadline_utc=(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z) ", projected[0][2])
    limit = provider_call_limit if provider_call_limit is not None else 2 if tool_round else 1
    template = BUDGET_TEMPLATE.replace("provider_call_limit=1", "provider_call_limit=" + str(limit))
    require(deadline is not None and projected[0][2] == template.format(deadline=deadline[1]), "unexpected_budget_context")
    parsed = datetime.datetime.strptime(deadline[1][:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=datetime.timezone.utc)
    if "." in deadline[1]:
        parsed += datetime.timedelta(microseconds=int((deadline[1][20:-1] + "000000")[:6]))
    require(0 < (parsed - now).total_seconds() <= 21, "unexpected_budget_deadline")
    architecture = {"aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine(), platform.machine())
    platform_name = platform_name or "linux/" + architecture
    require(projected[1][2] == CONTEXT_TEMPLATE.format(date=now.date().isoformat(), platform=platform_name), "unexpected_runtime_context")
    # The bridge emits one blank assistant text part before the historical
    # tool call. Require that exact position rather than dropping blank items.
    require(projected[3] == ["message", "assistant", " "], "unexpected_tool_assistant_placeholder")
    projected = projected[2:3] + projected[4:]
    expected = [["message", "user", "owned checkpoint fixture prompt"],
                ["call", "fixture-write", "Write", dict(file_path="effects.count", content="1\n")],
                ["result", "fixture-write", "committed once"],
                ["message", "assistant", "checkpoint complete"], ["message", "user", PROMPT]]
    if tool_round == 2 or cold_round:
        expected.extend([["message", "assistant", " "],
                         ["call", "continued-write", "Write", dict(file_path="/workspace/continued.txt", content="continued once\n")],
                         ["result", "continued-write", "Wrote 15 bytes to /workspace/continued.txt"]])
    if cold_round:
        expected.extend([["message", "assistant", "Model error: provider usage accounting failed closed: run usage budget exceeded"],
                         ["message", "user", cold_prompt]])
        if cold_round == 2:
            expected.extend([["message", "assistant", " "],
                             ["call", "cold-edit", "Edit", dict(file_path="/workspace/continued.txt", old_string="continued once", new_string="edited after cold restore")],
                             ["result", "cold-edit", "Replaced 1 occurrence in /workspace/continued.txt"]])
    require(projected == expected, "native_history_or_new_turn_changed")
    return hashlib.sha256(json.dumps(items, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def run(session_id, scenario, card, handoff=None):
    require(str(uuid.UUID(session_id)) == session_id, "invalid_fixture_session")
    require(scenario in ("known", "unknown", "denied", "unbound", "tool", "pause_tool", "crash_before_response", "crash_after_tool",
                        "cold_edit", "cold_race", "cold_unknown", "cold_denied", "cold_changed", "cold_checkpoint_open", "cold_repeated", "cold_crash", "cold_route_changed",
                        "launcher_edit", "launcher_unknown", "pause_tool_env"), "invalid_fixture_scenario")
    environment_key = scenario == "pause_tool_env"
    if environment_key: scenario = "pause_tool"
    root = Path.cwd()
    require(str(root) == "/workspace" and Path("/opt/yhc/yhc").is_file(), "container_only_fixture")
    requests, errors, inputs, admissions = [], [], [], []
    race_probes = []
    external = scenario.startswith("launcher_")
    cold = scenario.startswith("cold_") or external
    require(not cold or isinstance(handoff, dict), "cold_handoff_required")
    fixture_port = 0
    if cold:
        require(set(handoff) == ({"handoff", "fixture_port", "checkpoint_path"} if external else {"handoff", "fixture_port"}) and type(handoff["fixture_port"]) is int
                and 0 < handoff["fixture_port"] < 65536, "cold_fixture_route_required")
        external_checkpoint = handoff.get("checkpoint_path")
        fixture_port, handoff = handoff["fixture_port"], handoff["handoff"]
        if scenario == "cold_route_changed":
            fixture_port = fixture_port + 1 if fixture_port < 65535 else fixture_port - 1
    crash = scenario.startswith("crash_") or scenario == "cold_crash"
    two_rounds = scenario in ("tool", "pause_tool", "crash_after_tool") or cold
    persist_usage = scenario in ("tool", "pause_tool") or crash or cold
    process_ready = threading.Event()
    checkpoint = root / ".yhc/usage-checkpoints" / ("tool-" + uuid.uuid4().hex)
    if external:
        require(isinstance(external_checkpoint, str) and external_checkpoint.startswith(".yhc/usage-checkpoints/cold-")
                and ".." not in Path(external_checkpoint).parts, "external_checkpoint_invalid")
        checkpoint = root / external_checkpoint
    if persist_usage:
        checkpoint.parent.mkdir(mode=0o700, exist_ok=True)

    def inspect_checkpoint():
        owner = json.loads((checkpoint / "owner.json").read_text())
        inspected = subprocess.run(["/opt/yhc/yhc", "usage", "inspect", "--checkpoint-dir", str(checkpoint),
                                    "--segment-id", owner["segment_id"]], capture_output=True, timeout=5, check=True)
        require(len(inspected.stdout) <= 1 << 20, "oversized_accounting_view")
        value = json.loads(inspected.stdout)
        require(value["can_resume_agent"] is False and value["new_spending_authorized"] is False, "accounting_granted_authority")
        return value

    class Handler(http.server.BaseHTTPRequestHandler):
        def setup(self):
            super().setup()
            self.connection.settimeout(5)

        def log_message(self, *args):
            pass

        def do_POST(self):
            try:
                require(self.path == "/responses" and self.headers.get("Authorization") == "Bearer fixture", "unexpected_provider_route")
                size = int(self.headers["Content-Length"])
                require(0 < size <= 4 << 20, "oversized_fixture_request")
                body = json.loads(self.rfile.read(size))
                requests.append(hashlib.sha256(json.dumps(body.get("input"), sort_keys=True, separators=(",", ":")).encode()).hexdigest())
                inputs.append(body.get("input"))
                verify_input(body, tool_round=len(requests) if two_rounds and not cold else 0,
                             provider_call_limit=1 if scenario == "pause_tool" else 2 if cold else None,
                             cold_round=len(requests) if cold else 0,
                             cold_prompt="Continue the saved task using the verified restored state. Do not repeat completed side effects." if external else COLD_PROMPT)
                require(len(requests) <= (2 if two_rounds else 1), "unexpected_retry_or_auxiliary_call")
                if persist_usage:
                    view = inspect_checkpoint()
                    snapshot = view["snapshot"]
                    require(view["phase"] == "admitted" and not view["admission_closed"]
                            and snapshot["provider_calls"] == len(requests) and snapshot["in_flight"] == 1
                            and snapshot["known_calls"] == len(requests) - 1
                            and snapshot["cost"]["reservation"]["outstanding_amount"] != "0.000000000000", "provider_entered_without_durable_admission")
                    admissions.append(view)
                if scenario == "cold_race" and len(requests) == 1:
                    transcript = root / ".yhc/transcripts" / (session_id + ".jsonl")
                    claim = root / ".yhc/cold-dispatch-claims" / (handoff["checkpoint"]["segment_id"] + ".json")
                    before_native, before_claim = transcript.read_bytes(), claim.read_bytes()
                    competing = list(args)
                    competing[competing.index("--usage-checkpoint-dir") + 1] = str(checkpoint.parent / ("competing-" + uuid.uuid4().hex))
                    loser = subprocess.run(competing, cwd=root, env=env, capture_output=True, timeout=10, check=False)
                    require(len(loser.stdout) <= 1 << 20, "oversized_competing_result")
                    value = json.loads(loser.stdout)
                    require(loser.returncode != 0 and value.get("error", {}).get("code") == "cold_tool_handoff_invalid"
                            and transcript.read_bytes() == before_native and claim.read_bytes() == before_claim,
                            "competing_launch_mutated_winner_native_or_claim")
                    race_probes.append(dict(loser_exit_code=loser.returncode, loser_result=value,
                                            winner_transcript_unchanged=True, winner_claim_unchanged=True))
                if scenario in ("crash_before_response", "cold_crash") or scenario == "crash_after_tool" and len(requests) == 2:
                    require(process_ready.wait(5), "fixture_process_not_ready")
                    process.kill()
                    return
            except Exception as error:
                errors.append(str(error)[:180])
                self.send_error(400)
                return
            if scenario in ("unknown", "cold_unknown", "launcher_unknown"):
                self.send_response(503)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"error":{"type":"overloaded","message":"owned unknown-usage fixture"}}')
                if external: threading.Thread(target=server.shutdown, daemon=True).start()
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            item = dict(type="message", id="offline-final", role="assistant", status="completed",
                        content=[dict(type="output_text", text=OUTPUT, annotations=[])])
            events = [dict(type="response.output_text.delta", sequence_number=0, item_id="offline-final",
                           output_index=0, content_index=0, delta=OUTPUT),
                      dict(type="response.completed", sequence_number=1, response=dict(id="offline-response", object="response",
                           status="completed", model="deepseek-v4-flash", output=[item],
                           usage=dict(input_tokens=10, output_tokens=4, total_tokens=14,
                                      input_tokens_details=dict(cached_tokens=6), output_tokens_details=dict(reasoning_tokens=2))))]
            if two_rounds and len(requests) == 1:
                item = dict(type="function_call", id="continued-item", call_id="continued-write", name="Write", status="completed",
                            arguments=json.dumps(dict(file_path="/workspace/continued.txt", content="continued once\n")))
                events[0] = dict(type="response.output_item.done", sequence_number=0, output_index=0, item=item)
                events[1]["response"]["output"] = [item]
                if cold:
                    item = dict(type="function_call", id="cold-edit-item", call_id="cold-edit", name="Edit", status="completed",
                                arguments=json.dumps(dict(file_path="/workspace/continued.txt", old_string="continued once", new_string="edited after cold restore")))
                    events[0]["item"] = item
                    events[1]["response"]["output"] = [item]
            for event in events:
                self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())
            if external and len(requests) == 2:
                threading.Thread(target=server.shutdown, daemon=True).start()

    server = http.server.HTTPServer(("127.0.0.1", fixture_port), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        if external:
            signal.signal(signal.SIGTERM, lambda *_args: threading.Thread(target=server.shutdown, daemon=True).start())
            print(json.dumps(dict(ready=True, provider_port=server.server_port, pid=os.getpid())), flush=True)
            thread.join(timeout=30)
            require(not thread.is_alive(), "external_fixture_timed_out")
            return dict(observed_provider_requests=len(requests), provider_protocol_errors=errors,
                        durable_admissions=admissions, real_provider_requests=0)
        with tempfile.TemporaryDirectory(prefix="yhc-offline-handoff-") as temporary:
            price = Path(temporary) / "prices.json"
            price.write_text(json.dumps(card))
            env = dict(PATH="/usr/local/bin:/usr/bin:/bin", HOME=temporary, TZ="UTC",
                       XDG_CONFIG_HOME=temporary + "/config", XDG_DATA_HOME=temporary + "/data",
                       XDG_CACHE_HOME=temporary + "/cache", YHC_SESSION_CATALOG=str(root / ".yhc/session-roots.json"))
            args = ["/opt/yhc/yhc", "exec", COLD_PROMPT if cold else PROMPT, "--resume", session_id, "--output-format", "json",
                    "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", "http://127.0.0.1:" + str(server.server_port),
                    "--api-key", "fixture", "--permission-mode", "acceptEdits", "--tools", "Write", "--max-turns", "1",
                    "--timeout", "20s", "--max-provider-calls", "1", "--price-card", str(price),
                    "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "16",
                    "--max-estimated-cost", "0.01" if scenario in ("denied", "cold_denied") else "5"]
            if two_rounds:
                args[args.index("--max-turns") + 1] = "2"
                args[args.index("--max-provider-calls") + 1] = "1" if scenario == "pause_tool" else "2"
            if environment_key:
                index = args.index("--api-key")
                del args[index:index + 2]
                env["PROV_API_KEY"] = "fixture"
            if persist_usage:
                args.extend(["--usage-checkpoint-dir", str(checkpoint)])
            if scenario == "pause_tool" or cold:
                args.append("--capture-read-guards")
            if cold:
                data = json.dumps(handoff, separators=(",", ":")).encode()
                handoff_file = Path(temporary) / "cold-handoff.json"
                handoff_file.write_bytes(data)
                handoff_file.chmod(0o600)
                args.extend(["--cold-tool-handoff", str(handoff_file), "--cold-tool-handoff-sha256", hashlib.sha256(data).hexdigest()])
                args[args.index("--tools") + 1] = "Read,Write,Edit"
                if scenario == "cold_changed":
                    (root / "continued.txt").write_text("externally changed\n")
                if scenario == "cold_checkpoint_open":
                    path = root / handoff["checkpoint_path"] / "checkpoint.json"
                    envelope = json.loads(path.read_text())
                    envelope["data"]["admission_closed"], envelope["data"]["phase"] = False, "settled"
                    raw = json.dumps(envelope["data"], separators=(",", ":")).encode()
                    envelope["sha256"] = hashlib.sha256(raw).hexdigest()
                    path.write_text(json.dumps(envelope, separators=(",", ":")))
            process = subprocess.Popen(args, cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            process_ready.set()
            try:
                stdout, unused_stderr = process.communicate(timeout=25)
            except BaseException:
                process.kill()
                process.communicate(timeout=5)
                raise
            require(len(stdout) <= 1 << 20, "oversized_cli_result")
            result = None if crash else json.loads(stdout)
            require(not crash or process.returncode == -signal.SIGKILL, "fixture_did_not_kill_cli")
            native = None
            if crash or scenario == "pause_tool":
                probe = subprocess.run(["/opt/yhc/yhc", "sessions", "resume", session_id, "--recovery-proof", "--output-format", "json"],
                                       cwd=root, env=env, capture_output=True, timeout=20, check=True)
                require(len(probe.stdout) <= 1 << 20, "oversized_native_proof")
                native = json.loads(probe.stdout)["result"]["recovery_proof"]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
    require(not thread.is_alive(), "fixture_provider_did_not_stop")
    return dict(version=1, scope="offline_public_exec_resume", scenario=scenario, result=result,
                durable_admissions=admissions, durable_checkpoint=inspect_checkpoint() if persist_usage else None,
                concurrent_launch_probes=race_probes,
                checkpoint_path=checkpoint.relative_to(root).as_posix() if persist_usage else None,
                native_after_pause=native if scenario == "pause_tool" else None,
                native_after_crash=native, killed_by_fixture=crash,
                exit_code=process.returncode, observed_provider_requests=len(requests), request_input_sha256=requests,
                provider="synthetic_loopback", provider_protocol_errors=errors,
                provider_port=server.server_port,
                failed_fixture_inputs=inputs if errors else [], real_provider_requests=0, can_resume_agent=False)


def main():
    try:
        require(len(sys.argv) in (4, 5), "fixture_arguments")
        value = run(sys.argv[1], sys.argv[2], json.loads(sys.argv[3]), json.loads(sys.argv[4]) if len(sys.argv) == 5 else None)
        print(json.dumps(value, separators=(",", ":")))
    except Exception as error:
        print("offline native handoff fixture failed: " + type(error).__name__ + ":" + str(error)[:240], file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
