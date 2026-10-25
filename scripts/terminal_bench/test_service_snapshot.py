"""State/clock restoration oracles, including faulty importers and release failures."""

import copy
import json
import os
import random
import shutil
import sys
import tempfile
import time
import unittest
import uuid
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.cold_recovery import bounded_command
from scripts.terminal_bench.service_clock import RecoverableClock
from scripts.terminal_bench.service_snapshot import (
    PROTOCOL, ServiceSnapshots, SnapshotError, decode, digest, encode,
)


def contract():
    def spec(policy):
        return dict(schema="counter-v1", command=["/opt/control"], clock_policy=policy, rate_num=3, rate_den=2,
                    coverage=dict(durable="snapshot", memory="snapshot", rng="snapshot", clock="snapshot",
                                  background="absent", external="absent"))
    return dict(version=1, consistency="quiescent-closed-world", max_downtime_ns=600_000_000_000,
                clock_skew_ns=10_000_000, services=dict(main=spec("logical_pause"), api=spec("logical_elapsed")))


def tuples(value):
    return tuple(tuples(v) for v in value) if isinstance(value, list) else value


class Time:
    def __init__(self):
        self.wall = 1_000_000_000_000_000
        self.mono = 0
    def advance(self, ns):
        self.wall += ns
        self.mono += ns
    def sleep(self, seconds):
        self.advance(max(1, int(seconds * 1e9)))


class App:
    def __init__(self, spec, timer):
        self.instance = str(uuid.uuid4())
        self.spec, self.timer = spec, timer
        self.clock = RecoverableClock(spec["clock_policy"], spec["rate_num"], spec["rate_den"],
                                      wall_ns=lambda: timer.wall, monotonic_ns=lambda: timer.mono)
        self.memory = 0
        self.durable = 0
        self.rng = random.Random(7)
        self.phase, self.barrier, self.activity, self.imports = "running", None, 0, 0
    def mutate(self):
        self.memory += 4
        self.durable += 9
        self.rng.random()
        self.activity += 1
    def state(self):
        return dict(memory=self.memory, durable=self.durable, rng=json.loads(json.dumps(self.rng.getstate())))
    def clock_probe(self):
        return dict(policy=self.clock.policy, rate_num=self.clock.rate_num,
                    rate_den=self.clock.rate_den, logical_ns=self.clock.value_ns())


class FakeBackend:
    def __init__(self, plan, timer, *, target=False, fault=None):
        self.plan, self.timer, self.target, self.fault = plan, timer, target, fault
        self.project = "target" if target else "source"
        self.volumes = ["target-store" if target else "source-store"]
        self.apps = {name: App(spec, timer) for name, spec in plan["services"].items()}
        self.calls, self.stopped, self.source_removed = [], False, False
        self.rows = [dict(service=name, container_id=("cdef" if target else "abcd")[i] * 64,
                          image="sha256:" + "f" * 64, pid=i + 1, started_at="initial", restart_count=0,
                          running=True, paused=False, restarting=False, health_status="not_configured")
                     for i, name in enumerate(sorted(self.apps))]
    def inventory(self, cohort, attempt=None):
        if self.fault == "restart" and any(op == "import" for _, op in self.calls):
            self.rows[0]["restart_count"] = 1
        return copy.deepcopy(dict(project=self.project, services=sorted(self.apps), containers=self.rows,
                                  inventory_complete=True, daemon_id="fixture-daemon", owned_volumes=self.volumes,
                                  cohort_id=cohort, restore_attempt_id=attempt))
    def rpc(self, name, command, req):
        op = req["operation"]
        self.calls.append((name, op))
        a = self.apps[name]
        if op == "capabilities":
            body = {k: a.spec[k] for k in ("schema", "coverage", "clock_policy", "rate_num", "rate_den")}
            body.update(phase=a.phase, in_flight=0, pristine=a.activity == a.imports == 0)
        elif op == "quiesce":
            a.clock.freeze()
            a.barrier, a.phase = req["barrier_id"], "quiescent"
            body = dict(phase=a.phase, in_flight=0)
        elif op == "export":
            body = dict(state=a.state(), clock=a.clock.snapshot())
        elif op == "import":
            state = req["checkpoint"]["state"]
            if self.fault != "memory":
                a.memory = state["memory"]
            a.durable = state["durable"]
            if self.fault != "rng":
                a.rng.setstate(tuples(state["rng"]))
            if self.fault != "clock":
                a.clock.restore(req["checkpoint"]["clock"], reference_wall_ns=req["reference_wall_ns"],
                                max_downtime_ns=req["max_downtime_ns"])
            a.imports += 1
            body = dict(phase=a.phase)
        elif op == "probe":
            if self.fault == "process":
                a.instance = str(uuid.uuid4())
            phase = a.phase
            if phase == "scheduled" and a.clock.status() == "running":
                phase = "running"
            body = dict(phase=phase, barrier_id=a.barrier, in_flight=0,
                        digests={k: digest(v) for k, v in a.state().items()}, clock=a.clock_probe())
            if phase == "running" and self.fault == "active_policy":
                body["clock"]["policy"] = "incorrect-policy"
            if phase == "running" and self.fault == "active_rate":
                body["clock"]["rate_num"] += 1
            if phase == "running" and self.fault == "active_missing":
                body["clock"].pop("rate_den")
        elif op == "activate":
            if self.fault == "partial_activate" and name == "main":
                raise SnapshotError("fixture_activation_failed")
            a.clock.schedule_resume(req["at_wall_ns"])
            a.phase = "scheduled"
            body = dict(phase=a.phase)
        else:
            raise AssertionError(op)
        return dict(protocol=PROTOCOL, request_id=req["request_id"], ok=True, instance_id=a.instance, body=body)
    def assert_source_absent(self, manifest, target):
        if not self.source_removed:
            raise SnapshotError("source_still_exists")
    def quarantine(self, target):
        self.stopped = True


@unittest.skipUnless(os.name == "posix", "private snapshot permissions")
class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.path = self.root / "snapshot"
        self.plan, self.timer = contract(), Time()
        self.control = self.root / "control"
        self.control.mkdir(mode=0o700)
        self.cohort, self.attempt = str(uuid.uuid4()), str(uuid.uuid4())
        self.source = FakeBackend(self.plan, self.timer)
        for app in self.source.apps.values():
            app.mutate()
        self.timer.advance(2_000_000_000)
        self.manifest = self.coordinator(self.source).capture(self.path, self.cohort)
        self.anchor = digest(self.manifest)
        self.timer.advance(10_000_000_000)
    def coordinator(self, backend):
        return ServiceSnapshots(self.plan, backend, control_dir=self.control, wall_ns=lambda: self.timer.wall,
                                monotonic_ns=lambda: self.timer.mono, sleep=self.timer.sleep)

    def test_new_process_state_rng_and_both_clock_policies_really_restore(self):
        target = FakeBackend(self.plan, self.timer, target=True)
        target.source_removed = True
        result = self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor, activate=True)
        self.assertEqual(result["status"], "services_active_verified")
        self.assertFalse(result["can_resume_agent"])
        for name in self.plan["services"]:
            old, new = self.source.apps[name], target.apps[name]
            self.assertNotEqual(old.instance, new.instance)
            self.assertEqual(old.state(), new.state())
            self.assertEqual(old.rng.random(), new.rng.random())
            if name == "api":
                self.assertGreater(new.clock.value_ns(), old.clock.value_ns() + 10_000_000_000)
            else:
                self.assertLess(new.clock.value_ns() - old.clock.value_ns(), 100_000_000)
        self.assertTrue(self.coordinator(target).activation_claim(self.anchor).is_file())
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o500)
        copied = self.root / "copied-capsule"
        shutil.copytree(self.path, copied)
        another = FakeBackend(self.plan, self.timer, target=True)
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            self.coordinator(another).restore(copied, str(uuid.uuid4()), expected_snapshot_sha256=self.anchor, activate=True)
        self.assertEqual(another.calls, [])

    def test_omitted_ram_rng_or_clock_and_process_restart_block_all_release(self):
        for fault in ("memory", "rng", "clock", "process", "restart"):
            with self.subTest(fault=fault):
                target = FakeBackend(self.plan, self.timer, target=True, fault=fault)
                target.source_removed = True
                with self.assertRaises(SnapshotError):
                    self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor, activate=True)
                self.assertTrue(target.stopped)
                self.assertFalse(any(op == "activate" for _, op in target.calls))

    def test_quiescent_restore_is_not_agent_resume_or_automatic_activation(self):
        target = FakeBackend(self.plan, self.timer, target=True)
        result = self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor)
        self.assertEqual(result["status"], "services_restored_quiescent")
        self.assertFalse(result["can_resume_agent"])
        self.assertTrue(all(a.phase == "quiescent" for a in target.apps.values()))
        self.assertFalse(self.coordinator(target).activation_claim(self.anchor).exists())

    def test_corrupt_snapshot_or_unsupported_state_is_rejected_before_mutation(self):
        filename = self.path / "state.api.json"
        self.path.chmod(0o700)
        filename.chmod(0o600)
        saved = json.loads(filename.read_text())
        saved["state"]["memory"] = 999
        filename.write_text(json.dumps(saved))
        filename.chmod(0o400)
        self.path.chmod(0o500)
        target = FakeBackend(self.plan, self.timer, target=True)
        with self.assertRaisesRegex(SnapshotError, "digest"):
            self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor)
        self.assertEqual(target.calls, [])
        self.plan["services"]["api"]["coverage"]["external"] = "unsupported"
        with self.assertRaisesRegex(SnapshotError, "unsupported"):
            self.coordinator(target)

    def test_unsealed_capsule_or_payload_is_rejected_before_target_access(self):
        for unsealed in (self.path, self.path / "state.api.json", self.path / "manifest.json"):
            with self.subTest(path=unsealed.name):
                unsealed.chmod(0o700 if unsealed == self.path else 0o600)
                target = FakeBackend(self.plan, self.timer, target=True)
                with self.assertRaisesRegex(SnapshotError, "not_sealed"):
                    self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor)
                self.assertEqual(target.calls, [])
                unsealed.chmod(0o500 if unsealed == self.path else 0o400)

    def test_interrupted_sealing_does_not_publish_trusted_capture_record(self):
        path = self.root / "interrupted"
        source = FakeBackend(self.plan, self.timer)
        coordinator = self.coordinator(source)
        with mock.patch("scripts.terminal_bench.service_snapshot.os.fchmod", side_effect=OSError("fixture_seal_failure")):
            with self.assertRaises(OSError):
                coordinator.capture(path, str(uuid.uuid4()))
        manifest = json.loads((path / "manifest.json").read_text())
        self.assertFalse(coordinator._anchor(digest(manifest)).exists())
        self.assertTrue(all(app.phase == "quiescent" for app in source.apps.values()))

    def test_source_still_exists_and_incomplete_coverage_never_activate(self):
        target = FakeBackend(self.plan, self.timer, target=True)
        with self.assertRaisesRegex(SnapshotError, "source_still_exists"):
            self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor, activate=True)
        self.assertTrue(target.stopped)
        self.assertFalse(any(op == "activate" for _, op in target.calls))

    def test_partial_activation_consumes_claim_and_quarantines_without_retry(self):
        target = FakeBackend(self.plan, self.timer, target=True, fault="partial_activate")
        target.source_removed = True
        with self.assertRaisesRegex(SnapshotError, "fixture_activation_failed"):
            self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor, activate=True)
        self.assertTrue(target.stopped)
        self.assertTrue(self.coordinator(target).activation_claim(self.anchor).is_file())
        another = FakeBackend(self.plan, self.timer, target=True)
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            self.coordinator(another).restore(self.path, str(uuid.uuid4()), expected_snapshot_sha256=self.anchor, activate=True)
        self.assertEqual(another.calls, [])

    def clock_failure_during_activation(self, *, backwards):
        target = FakeBackend(self.plan, self.timer, target=True)
        target.source_removed = True
        coordinator = self.coordinator(target)
        iterations = 0
        def sleep(seconds):
            nonlocal iterations
            iterations += 1
            if iterations > 100:
                raise AssertionError("activation wait did not enforce a clock deadline")
            self.timer.mono += max(1, int(seconds * 1e9))
            if backwards:
                self.timer.wall -= 1
        coordinator.sleep = sleep
        code = "activation_wall_clock_reversed" if backwards else "activation_wait_timeout"
        with self.assertRaisesRegex(SnapshotError, code):
            coordinator.restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor, activate=True)
        self.assertTrue(target.stopped)
        self.assertTrue(coordinator.activation_claim(self.anchor).is_file())

    def test_wall_clock_reversal_during_activation_quarantines_all_services(self):
        self.clock_failure_during_activation(backwards=True)

    def test_stalled_wall_clock_during_activation_has_monotonic_deadline(self):
        self.clock_failure_during_activation(backwards=False)

    def test_expired_snapshot_and_reused_target_are_rejected(self):
        self.timer.advance(self.plan["max_downtime_ns"] + 1)
        with self.assertRaisesRegex(SnapshotError, "expired"):
            self.coordinator(FakeBackend(self.plan, self.timer, target=True)).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor)

    def test_old_containers_images_shared_storage_or_wrong_daemon_block_before_import(self):
        for fault in ("old", "image", "storage", "daemon"):
            with self.subTest(fault=fault):
                target = FakeBackend(self.plan, self.timer, target=fault != "old")
                if fault == "image":
                    target.rows[0]["image"] = "sha256:" + "e" * 64
                if fault == "storage":
                    target.volumes = ["source-store"]
                if fault == "daemon":
                    original = target.inventory
                    target.inventory = lambda *a, **k: {**original(*a, **k), "daemon_id": "different-daemon"}
                with self.assertRaises(SnapshotError):
                    self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor)
                self.assertEqual(target.calls, [])

    def test_payload_and_manifest_rewrite_cannot_replace_the_trusted_capture(self):
        self.path.chmod(0o700)
        payload_path, manifest_path = self.path / "state.api.json", self.path / "manifest.json"
        payload = json.loads(payload_path.read_text())
        manifest = json.loads(manifest_path.read_text())
        payload["state"]["memory"] = 4321
        manifest["services"]["api"]["sha256"] = digest(payload)
        for filename, value in ((payload_path, payload), (manifest_path, manifest)):
            filename.chmod(0o600)
            filename.write_text(json.dumps(value))
            filename.chmod(0o400)
        self.path.chmod(0o500)
        target = FakeBackend(self.plan, self.timer, target=True)
        with self.assertRaisesRegex(SnapshotError, "anchor_mismatch"):
            self.coordinator(target).restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor)
        self.assertEqual(target.calls, [])

    def test_changed_or_missing_active_clock_configuration_quarantines_and_consumes_claim(self):
        for fault in ("active_policy", "active_rate", "active_missing"):
            with self.subTest(fault=fault):
                # Each capture/activation is single use; use a new control root
                # with the trusted capture record for this isolated negative case.
                control = self.root / fault
                control.mkdir(mode=0o700)
                record = self.control / ("snapshot." + self.anchor + ".json")
                from scripts.terminal_bench.recovery import write_report
                write_report(control / record.name, json.loads(record.read_text()))
                target = FakeBackend(self.plan, self.timer, target=True, fault=fault)
                target.source_removed = True
                coordinator = ServiceSnapshots(self.plan, target, control_dir=control,
                                               wall_ns=lambda: self.timer.wall, sleep=self.timer.sleep)
                with self.assertRaisesRegex(SnapshotError, "active_clock_configuration"):
                    coordinator.restore(self.path, self.attempt, expected_snapshot_sha256=self.anchor, activate=True)
                self.assertTrue(target.stopped)
                self.assertTrue(coordinator.activation_claim(self.anchor).exists())


class ProtocolTests(unittest.TestCase):
    def test_duplicate_keys_nonfinite_and_oversized_integer_are_not_silently_decoded(self):
        for raw in (b'{"a":1,"a":2}', b'{"a":NaN}', b'{"a":123456789012345678901}'):
            with self.assertRaises(SnapshotError):
                decode(raw)

    @unittest.skipUnless(os.name == "posix", "POSIX bounded subprocess")
    def test_rpc_bounds_and_timeout_reap_the_owned_host_child(self):
        with self.assertRaisesRegex(SnapshotError, "output_limit"):
            bounded_command([sys.executable, "-c", "import sys; sys.stderr.write('x'*9000)"])
        with self.assertRaisesRegex(SnapshotError, "timeout"):
            bounded_command([sys.executable, "-c", "import time; time.sleep(10)"], timeout=0.1)
        output = bounded_command([sys.executable, "-c", "import sys; sys.stdout.buffer.write(sys.stdin.buffer.read())"], encode({"ok": True}))
        self.assertEqual(decode(output), {"ok": True})

    @unittest.skipUnless(os.name == "posix", "POSIX owned process group")
    def test_rpc_timeout_stops_descendant_after_cli_parent_already_exited(self):
        with tempfile.TemporaryDirectory() as root:
            marker = Path(root) / "orphan-continued"
            program = (
                "import os,sys,time; from pathlib import Path\n"
                "if os.fork(): os._exit(0)\n"
                "time.sleep(1); Path(sys.argv[1]).write_text('continued')\n"
            )
            with self.assertRaisesRegex(SnapshotError, "timeout"):
                bounded_command([sys.executable, "-c", program, str(marker)], timeout=0.3)
            time.sleep(1.1)
            self.assertFalse(marker.exists(), "owned RPC descendant continued after timeout")


if __name__ == "__main__":
    unittest.main()
