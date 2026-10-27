"""Owned offline fixture: independent business API and cooperative control API."""

import hashlib
import http.client
import http.server
import json
import os
import random
import sqlite3
import sys
import time
import uuid

# This path belongs to the immutable fixture image, never a task work directory.
sys.path.insert(0, "/opt/checkpoint")
from service_clock import RecoverableClock

PROTOCOL = "yhc-service-checkpoint-v1"


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False).encode()


def tuple_state(value):
    return tuple(tuple_state(v) for v in value) if isinstance(value, list) else value


class Service:
    def __init__(self):
        self.instance = str(uuid.uuid4())
        self.policy = os.environ["CLOCK_POLICY"]
        self.clock = RecoverableClock(self.policy, 3, 2)
        self.memory = 0
        self.rng = random.Random(2718)
        self.activity, self.imports = 0, 0
        self.phase, self.barrier = "running", None
        self.fault = os.environ.get("RESTORE_FAULT", "")
        os.makedirs("/state", exist_ok=True)
        self.db = sqlite3.connect("/state/store.sqlite")
        self.db.execute("CREATE TABLE IF NOT EXISTS counter (value INTEGER NOT NULL)")
        if not self.db.execute("SELECT count(*) FROM counter").fetchone()[0]:
            self.db.execute("INSERT INTO counter VALUES (0)")
        self.db.commit()

    def durable(self):
        return self.db.execute("SELECT value FROM counter").fetchone()[0]

    def state(self):
        return dict(memory=self.memory, durable=self.durable(), rng=json.loads(json.dumps(self.rng.getstate())))

    def current_phase(self):
        if self.phase == "scheduled" and self.clock.status() == "running":
            return "running"
        return self.phase

    def business(self, operation):
        phase = self.current_phase()
        if operation != "state" and phase != "running":
            raise ValueError("service_not_active")
        if operation == "mutate":
            self.memory += 7
            self.db.execute("UPDATE counter SET value=value+11")
            self.db.commit()
            self.rng.random()
            self.activity += 1
        if operation == "next":
            self.activity += 1
            return dict(value=self.rng.random())
        peek = random.Random()
        peek.setstate(self.rng.getstate())
        return dict(memory=self.memory, durable=self.durable(), next_random=peek.random(),
                    logical_ns=self.clock.value_ns(), phase=phase, instance=self.instance)

    def control(self, request):
        assert request["protocol"] == PROTOCOL
        op, barrier = request["operation"], request["barrier_id"]
        if op == "capabilities":
            return dict(schema="counter-v1", coverage=dict(durable="snapshot", memory="snapshot", rng="snapshot",
                        clock="snapshot", background="absent", external="absent"), clock_policy=self.policy,
                        rate_num=3, rate_den=2, in_flight=0, phase=self.current_phase(),
                        pristine=self.activity == self.imports == self.memory == self.durable() == 0)
        if op == "quiesce":
            # A released target may become the source of a fresh capture. An
            # active quiescent barrier still cannot be replaced or re-imported.
            assert self.barrier in (None, barrier) or self.current_phase() == "running"
            self.clock.freeze()
            self.barrier, self.phase = barrier, "quiescent"
            return dict(phase=self.phase, in_flight=0)
        assert barrier == self.barrier
        if op == "export":
            assert self.phase == "quiescent"
            return dict(state=self.state(), clock=self.clock.snapshot())
        if op == "import":
            assert self.phase == "quiescent" and self.imports == 0 and self.activity == 0
            state, clock = request["checkpoint"]["state"], request["checkpoint"]["clock"]
            if self.fault != "memory":
                self.memory = state["memory"]
            self.db.execute("UPDATE counter SET value=?", (state["durable"],))
            self.db.commit()
            if self.fault != "rng":
                self.rng.setstate(tuple_state(state["rng"]))
            if self.fault != "clock":
                self.clock.restore(clock, reference_wall_ns=request["reference_wall_ns"], max_downtime_ns=request["max_downtime_ns"])
            self.imports += 1
            return dict(phase=self.phase)
        if op == "activate":
            assert self.imports == 1
            if self.fault == "activate":
                raise ValueError("fixture_activation_failure")
            self.clock.schedule_resume(request["at_wall_ns"])
            self.phase = "scheduled"
            return dict(phase=self.phase)
        if op == "probe":
            result = dict(phase=self.current_phase(), barrier_id=self.barrier, in_flight=0,
                        digests={key: hashlib.sha256(canonical(value)).hexdigest() for key, value in self.state().items()},
                        clock=dict(policy=self.policy, rate_num=3, rate_den=2, logical_ns=self.clock.value_ns()))
            if self.fault == "active_clock" and self.current_phase() == "running":
                result["clock"]["policy"] = "incorrect-policy"
            return result
        raise ValueError("unsupported_control_operation")


def client(path, value):
    connection = http.client.HTTPConnection("localhost", 8088, timeout=10)
    connection.request("POST", path, canonical(value), {"Content-Type": "application/json"})
    response = connection.getresponse()
    data = response.read(8 << 20)
    connection.close()
    if response.status != 200:
        raise ValueError("fixture_request_failed")
    return data


def main():
    if sys.argv[1] != "server":
        if sys.argv[1] == "rpc":
            sys.stdout.buffer.write(client("/control", json.load(sys.stdin)))
        else:
            sys.stdout.buffer.write(client("/business", {"operation": sys.argv[2]}))
        return
    service = Service()
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            try:
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                if self.path == "/control":
                    try:
                        result = dict(protocol=PROTOCOL, request_id=request["request_id"], instance_id=service.instance,
                                      ok=True, body=service.control(request))
                    except Exception:
                        result = dict(protocol=PROTOCOL, request_id=request.get("request_id"), instance_id=service.instance,
                                      ok=False, body={"error": "control_operation_failed"})
                else:
                    result = service.business(request["operation"])
                raw = canonical(result)
                self.send_response(200)
                self.end_headers()
                self.wfile.write(raw)
            except Exception:
                self.send_response(409)
                self.end_headers()
        def log_message(self, *_args):
            pass
    http.server.HTTPServer(("localhost", 8088), Handler).serve_forever()


if __name__ == "__main__":
    main()
