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
import subprocess
import sys
import tempfile
import threading
import uuid
from pathlib import Path

PROMPT = "continue the owned checkpoint in one new turn"
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


def verify_input(body, now=None, platform_name=None, tool_round=0):
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
    require(len(projected) == (11 if tool_round == 2 else 8)
            and all(item[:2] == ["message", "user"] for item in projected[:2]), "unexpected_runtime_prefix")
    now = now or datetime.datetime.now(datetime.timezone.utc)
    deadline = re.search(r"\ndeadline_utc=(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z) ", projected[0][2])
    template = BUDGET_TEMPLATE.replace("provider_call_limit=1", "provider_call_limit=2") if tool_round else BUDGET_TEMPLATE
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
    if tool_round == 2:
        expected.extend([["message", "assistant", " "],
                         ["call", "continued-write", "Write", dict(file_path="/workspace/continued.txt", content="continued once\n")],
                         ["result", "continued-write", "Wrote 15 bytes to /workspace/continued.txt"]])
    require(projected == expected, "native_history_or_new_turn_changed")
    return hashlib.sha256(json.dumps(items, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def run(session_id, scenario, card):
    require(str(uuid.UUID(session_id)) == session_id, "invalid_fixture_session")
    require(scenario in ("known", "unknown", "denied", "unbound", "tool"), "invalid_fixture_scenario")
    root = Path.cwd()
    require(str(root) == "/workspace" and Path("/opt/yhc/yhc").is_file(), "container_only_fixture")
    requests, errors, inputs, admissions = [], [], [], []
    checkpoint = root / ".yhc/usage-checkpoints" / ("tool-" + uuid.uuid4().hex)
    if scenario == "tool":
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
                verify_input(body, tool_round=len(requests) if scenario == "tool" else 0)
                require(len(requests) <= (2 if scenario == "tool" else 1), "unexpected_retry_or_auxiliary_call")
                if scenario == "tool":
                    view = inspect_checkpoint()
                    snapshot = view["snapshot"]
                    require(view["phase"] == "admitted" and not view["admission_closed"]
                            and snapshot["provider_calls"] == len(requests) and snapshot["in_flight"] == 1
                            and snapshot["known_calls"] == len(requests) - 1
                            and snapshot["cost"]["reservation"]["outstanding_amount"] != "0.000000000000", "provider_entered_without_durable_admission")
                    admissions.append(view)
            except Exception as error:
                errors.append(str(error)[:180])
                self.send_error(400)
                return
            if scenario == "unknown":
                self.send_response(503)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"error":{"type":"overloaded","message":"owned unknown-usage fixture"}}')
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
            if scenario == "tool" and len(requests) == 1:
                item = dict(type="function_call", id="continued-item", call_id="continued-write", name="Write", status="completed",
                            arguments=json.dumps(dict(file_path="/workspace/continued.txt", content="continued once\n")))
                events[0] = dict(type="response.output_item.done", sequence_number=0, output_index=0, item=item)
                events[1]["response"]["output"] = [item]
            for event in events:
                self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())

    server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="yhc-offline-handoff-") as temporary:
            price = Path(temporary) / "prices.json"
            price.write_text(json.dumps(card))
            env = dict(PATH="/usr/local/bin:/usr/bin:/bin", HOME=temporary, TZ="UTC",
                       XDG_CONFIG_HOME=temporary + "/config", XDG_DATA_HOME=temporary + "/data",
                       XDG_CACHE_HOME=temporary + "/cache", YHC_SESSION_CATALOG=str(root / ".yhc/session-roots.json"))
            args = ["/opt/yhc/yhc", "exec", PROMPT, "--resume", session_id, "--output-format", "json",
                    "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", "http://127.0.0.1:" + str(server.server_port),
                    "--api-key", "fixture", "--permission-mode", "acceptEdits", "--tools", "Write", "--max-turns", "1",
                    "--timeout", "20s", "--max-provider-calls", "1", "--price-card", str(price),
                    "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "16",
                    "--max-estimated-cost", "0.01" if scenario == "denied" else "5"]
            if scenario == "tool":
                args[args.index("--max-turns") + 1] = "2"
                args[args.index("--max-provider-calls") + 1] = "2"
                args.extend(["--usage-checkpoint-dir", str(checkpoint)])
            completed = subprocess.run(args, cwd=root, env=env, capture_output=True, timeout=25)
            require(len(completed.stdout) <= 1 << 20, "oversized_cli_result")
            result = json.loads(completed.stdout)
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
    require(not thread.is_alive(), "fixture_provider_did_not_stop")
    return dict(version=1, scope="offline_public_exec_resume", scenario=scenario, result=result,
                durable_admissions=admissions, durable_checkpoint=inspect_checkpoint() if scenario == "tool" else None,
                exit_code=completed.returncode, observed_provider_requests=len(requests), request_input_sha256=requests,
                provider="synthetic_loopback", provider_protocol_errors=errors,
                failed_fixture_inputs=inputs if errors else [], real_provider_requests=0, can_resume_agent=False)


def main():
    try:
        require(len(sys.argv) == 4, "fixture_arguments")
        value = run(sys.argv[1], sys.argv[2], json.loads(sys.argv[3]))
        print(json.dumps(value, separators=(",", ":")))
    except Exception as error:
        print("offline native handoff fixture failed: " + type(error).__name__ + ":" + str(error)[:240], file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
