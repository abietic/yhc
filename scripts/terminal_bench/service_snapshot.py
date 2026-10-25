"""Cooperative service cold restore. This never authorizes an agent/model call.

Participants must implement a trusted, versioned quiescence/state/probe contract.
Opaque container files and legacy capture manifests do not satisfy this protocol.
"""

import hashlib
import json
import os
import stat
import string
import time
import uuid
from pathlib import Path

from scripts.terminal_bench.recovery import same_live_runtime, write_report
from scripts.terminal_bench.service_clock import RecoverableClock

PROTOCOL = "yhc-service-checkpoint-v1"
DIMENSIONS = {"durable", "memory", "rng", "clock", "background", "external"}
MAX_BYTES = 8 << 20
MAX_TOTAL_BYTES = 64 << 20


class SnapshotError(ValueError):
    def __init__(self, code):
        self.code = code if (isinstance(code, str) and 0 < len(code) <= 96
                             and all(c in string.ascii_lowercase + string.digits + "_" for c in code)) else "invalid_service_state"
        super().__init__(self.code)


def require(condition, code):
    if not condition:
        raise SnapshotError(code)


def identity(value):
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value and uuid.UUID(value).int != 0
    except ValueError:
        return False


def digest(value):
    return hashlib.sha256(encode(value)).hexdigest()


def encode(value):
    try:
        raw = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False).encode()
        require(len(raw) <= MAX_BYTES, "state_too_large")
        return raw
    except (TypeError, OverflowError, RecursionError, ValueError) as exc:
        if isinstance(exc, SnapshotError):
            raise
        raise SnapshotError("invalid_state_json") from None


def decode(raw):
    def pairs(values):
        result = {}
        for key, value in values:
            require(key not in result, "duplicate_json_key")
            result[key] = value
        return result
    def number(value):
        require(len(value) <= 20, "json_integer_out_of_bounds")
        return int(value)
    def invalid(_value):
        raise SnapshotError("nonfinite_json_number")
    require(len(raw) <= MAX_BYTES, "state_too_large")
    try:
        return json.loads(raw, object_pairs_hook=pairs, parse_int=number, parse_constant=invalid)
    except (ValueError, UnicodeError, RecursionError) as exc:
        if isinstance(exc, SnapshotError):
            raise
        raise SnapshotError("invalid_state_json") from None


def private_json(path, *, sealed=False):
    """Do not follow a task-created final-component link or read shared inputs."""
    require(os.name == "posix", "private_snapshot_requires_posix")
    path = Path(path).absolute()
    require(path.parent.resolve() == path.parent, "snapshot_parent_symlink")
    parent = path.parent.stat()
    require(parent.st_uid == os.geteuid() and not parent.st_mode & 0o077, "snapshot_parent_not_private")
    if sealed:
        require(stat.S_IMODE(parent.st_mode) == 0o500, "snapshot_directory_not_sealed")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as file:
        info = os.fstat(file.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid()
                and not info.st_mode & 0o077 and info.st_size <= MAX_BYTES, "snapshot_file_not_private")
        if sealed:
            require(stat.S_IMODE(info.st_mode) == 0o400, "snapshot_file_not_sealed")
        return decode(file.read(MAX_BYTES + 1))


def seal_capsule(path, filenames):
    """Persist read-only payload and directory metadata before registering it."""
    path = Path(path)
    for filename in filenames:
        fd = os.open(path / filename, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            os.fchmod(fd, 0o400)
            os.fsync(fd)
        finally:
            os.close(fd)
    directory = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fchmod(directory, 0o500)
        os.fsync(directory)
    finally:
        os.close(directory)
    parent_fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(parent_fd)
    finally:
        os.close(parent_fd)


def validate_contract(contract):
    require(isinstance(contract, dict) and set(contract) == {
        "version", "consistency", "max_downtime_ns", "clock_skew_ns", "services"}, "invalid_service_contract")
    require(type(contract["version"]) is int and contract["version"] == 1
            and contract["consistency"] == "quiescent-closed-world", "unsupported_consistency_contract")
    require(type(contract["max_downtime_ns"]) is int and 0 < contract["max_downtime_ns"] < (1 << 63), "invalid_downtime_limit")
    require(type(contract["clock_skew_ns"]) is int and 0 <= contract["clock_skew_ns"] <= 1_000_000_000, "invalid_clock_skew")
    services = contract["services"]
    require(isinstance(services, dict) and 0 < len(services) <= 32, "invalid_service_set")
    for name, spec in services.items():
        require(isinstance(name, str) and name and len(name) <= 128
                and name[0].isalnum() and name.isascii()
                and all(c.isalnum() or c in "_.-" for c in name), "invalid_service_name")
        require(isinstance(spec, dict) and set(spec) == {
            "schema", "coverage", "clock_policy", "rate_num", "rate_den", "command"}, "invalid_service_spec")
        require(isinstance(spec["schema"], str) and 0 < len(spec["schema"]) <= 128, "invalid_service_schema")
        coverage = spec["coverage"]
        require(isinstance(coverage, dict) and set(coverage) == DIMENSIONS
                and all(value in ("snapshot", "absent") for value in coverage.values()), "unsupported_or_unknown_state")
        # External effects and background execution need a stronger reconciliation
        # protocol. No claim of general distributed transaction/process recovery.
        require(coverage["external"] == coverage["background"] == "absent", "unreconciled_external_or_background_state")
        require(coverage["clock"] == "snapshot", "service_clock_not_recoverable")
        RecoverableClock(spec["clock_policy"], spec["rate_num"], spec["rate_den"])
        argv = spec["command"]
        require(isinstance(argv, list) and 0 < len(argv) <= 32
                and all(isinstance(arg, str) and 0 < len(arg) <= 4096 and "\0" not in arg for arg in argv)
                and argv[0].startswith("/"), "invalid_service_command")
    return contract


class ServiceSnapshots:
    """Host-owned coordinator. Backend RPC runs only inside owned containers."""

    def __init__(self, contract, backend, *, control_dir, wall_ns=time.time_ns,
                 monotonic_ns=time.monotonic_ns, sleep=time.sleep):
        self.contract = decode(encode(validate_contract(contract)))
        self.backend = backend
        self.wall_ns = wall_ns
        self.monotonic_ns = monotonic_ns
        self.sleep = sleep
        self.specs = self.contract["services"]
        self.control_dir = Path(control_dir).absolute()
        require(self.control_dir.resolve() == self.control_dir, "control_directory_symlink")
        info = self.control_dir.stat()
        require(info.st_uid == os.geteuid() and not info.st_mode & 0o077, "control_directory_not_private")

    def _outside_capsule(self, path):
        require(self.control_dir != path and path not in self.control_dir.parents, "control_directory_must_be_outside_capsule")

    def _anchor(self, root_digest):
        require(isinstance(root_digest, str) and len(root_digest) == 64
                and all(c in "0123456789abcdef" for c in root_digest), "trusted_snapshot_digest_required")
        return self.control_dir / ("snapshot." + root_digest + ".json")

    def activation_claim(self, root_digest):
        self._anchor(root_digest)
        return self.control_dir / ("activation." + root_digest + ".json")

    def _inventory(self, cohort, attempt=None):
        value = self.backend.inventory(cohort, attempt)
        require(value.get("inventory_complete") is True and set(value["services"]) == set(self.specs), "incomplete_service_inventory")
        require(len(value["containers"]) == len(self.specs) and same_live_runtime(value, value), "service_not_live_or_replicated")
        return value

    def _call(self, name, op, barrier, **payload):
        request = dict(protocol=PROTOCOL, request_id=str(uuid.uuid4()), operation=op, barrier_id=barrier, **payload)
        reply = self.backend.rpc(name, self.specs[name]["command"], request)
        require(isinstance(reply, dict) and reply.get("protocol") == PROTOCOL
                and reply.get("request_id") == request["request_id"]
                and identity(reply.get("instance_id")) and isinstance(reply.get("body"), dict), "invalid_service_reply")
        require(reply.get("ok") is True, "service_operation_rejected")
        return reply["instance_id"], reply["body"]

    def _capabilities(self, name, barrier, *, pristine=False):
        instance, body = self._call(name, "capabilities", barrier)
        spec = self.specs[name]
        require(all(body.get(key) == spec[key] for key in ("schema", "coverage", "clock_policy", "rate_num", "rate_den")), "service_capability_mismatch")
        require(type(body.get("rate_num")) is int and type(body.get("rate_den")) is int, "invalid_clock_capability_types")
        require(type(body.get("in_flight")) is int and body["in_flight"] == 0, "service_not_drained")
        if pristine:
            require(body.get("pristine") is True, "restore_target_not_pristine")
        else:
            require(body.get("phase") == "running", "capture_source_not_running")
        return instance

    def _quiescent_probe(self, name, barrier, instance, state, clock):
        observed, body = self._call(name, "probe", barrier)
        require(observed == instance and body.get("barrier_id") == barrier
                and body.get("phase") == "quiescent" and type(body.get("in_flight")) is int
                and body["in_flight"] == 0, "service_generation_or_quiescence_changed")
        wanted = {key: digest(value) for key, value in state.items()}
        require(body.get("digests") == wanted, "restored_state_probe_mismatch")
        expected_clock = {key: clock[key] for key in ("policy", "rate_num", "rate_den", "logical_ns")}
        observed_clock = body.get("clock")
        require(isinstance(observed_clock, dict) and observed_clock == expected_clock
                and all(type(observed_clock.get(k)) is int for k in ("rate_num", "rate_den", "logical_ns")), "restored_clock_probe_mismatch")
        return body

    def _clock(self, spec, saved, reference):
        clock = RecoverableClock(spec["clock_policy"], spec["rate_num"], spec["rate_den"])
        clock.freeze()
        clock.restore(saved, reference_wall_ns=reference, max_downtime_ns=self.contract["max_downtime_ns"])
        return clock.snapshot()

    def capture(self, path, cohort):
        require(identity(cohort), "invalid_cohort_identity")
        path = Path(path).absolute()
        self._outside_capsule(path)
        require(path.parent.resolve() == path.parent, "snapshot_parent_symlink")
        parent = path.parent.stat()
        require(parent.st_uid == os.geteuid() and not parent.st_mode & 0o077, "snapshot_parent_not_private")
        before = self._inventory(cohort)
        barrier = str(uuid.uuid4())
        instances = {name: self._capabilities(name, barrier) for name in sorted(self.specs)}
        # Refuse existing outputs and publish the manifest last. A failed capture
        # leaves its partial files and sources quiescent; it is never resumable.
        path.mkdir(mode=0o700)
        start = self.wall_ns()
        records = {}
        for name in sorted(self.specs):
            instance, body = self._call(name, "quiesce", barrier)
            require(instance == instances[name] and body.get("phase") == "quiescent"
                    and type(body.get("in_flight")) is int and body["in_flight"] == 0, "service_quiesce_failed")
        for name in sorted(self.specs):
            instance, body = self._call(name, "export", barrier)
            require(instance == instances[name] and set(body) == {"state", "clock"}, "invalid_export")
            state, clock = body["state"], body["clock"]
            expected = {key for key, value in self.specs[name]["coverage"].items() if value == "snapshot" and key != "clock"}
            require(isinstance(state, dict) and set(state) == expected, "incomplete_state_export")
            # Validate policy, bounds and timestamp without moving this source's clock.
            self._clock(self.specs[name], clock, clock["wall_ns"])
            require(start - self.contract["clock_skew_ns"] <= clock["wall_ns"] <= self.wall_ns() + self.contract["clock_skew_ns"], "source_clock_anchor_unverified")
            self._quiescent_probe(name, barrier, instance, state, clock)
            filename = "state." + name + ".json"
            write_report(path / filename, body)
            records[name] = dict(file=filename, sha256=digest(body), instance_id=instance)
        require(same_live_runtime(before, self._inventory(cohort)), "source_runtime_changed")
        manifest = dict(version=1, protocol=PROTOCOL, status="service_snapshot_complete", cohort_id=cohort,
                        barrier_id=barrier, captured_wall_ns=self.wall_ns(), contract_sha256=digest(self.contract),
                        source=before, services=records, can_resume_agent=False)
        require(sum((path / r["file"]).stat().st_size for r in records.values()) <= MAX_TOTAL_BYTES, "snapshot_total_too_large")
        write_report(path / "manifest.json", manifest)
        # Seal and persist both the payloads and directory metadata before the
        # trusted registry can advertise this capture as complete.
        seal_capsule(path, [r["file"] for r in records.values()] + ["manifest.json"])
        # The externally held digest/control record is the authority. A capsule
        # cannot certify replacement payloads by rewriting its own manifest.
        root_digest = digest(manifest)
        write_report(self._anchor(root_digest), dict(version=1, snapshot_sha256=root_digest,
                                                    cohort_id=cohort, barrier_id=barrier))
        return manifest

    def load(self, path, expected_snapshot_sha256):
        path = Path(path).absolute()
        self._outside_capsule(path)
        anchor = private_json(self._anchor(expected_snapshot_sha256))
        manifest = private_json(path / "manifest.json", sealed=True)
        require(digest(manifest) == expected_snapshot_sha256
                and anchor == dict(version=1, snapshot_sha256=expected_snapshot_sha256,
                                   cohort_id=manifest.get("cohort_id"), barrier_id=manifest.get("barrier_id")), "snapshot_anchor_mismatch")
        require(isinstance(manifest, dict) and type(manifest.get("version")) is int and manifest["version"] == 1
                and manifest.get("protocol") == PROTOCOL and manifest.get("status") == "service_snapshot_complete"
                and identity(manifest.get("cohort_id")) and identity(manifest.get("barrier_id"))
                and manifest.get("contract_sha256") == digest(self.contract), "invalid_or_incompatible_snapshot")
        require(isinstance(manifest.get("services"), dict) and set(manifest["services"]) == set(self.specs), "incomplete_snapshot_services")
        require(type(manifest.get("captured_wall_ns")) is int
                and 0 <= self.wall_ns() - manifest["captured_wall_ns"] <= self.contract["max_downtime_ns"], "snapshot_expired_or_clock_reversed")
        blobs = {}
        total = 0
        for name, record in manifest["services"].items():
            require(record.get("file") == "state." + name + ".json" and identity(record.get("instance_id")), "invalid_snapshot_record")
            filename = path / record["file"]
            total += filename.stat().st_size
            require(total <= MAX_TOTAL_BYTES, "snapshot_total_too_large")
            blob = private_json(filename, sealed=True)
            require(digest(blob) == record.get("sha256"), "snapshot_digest_mismatch")
            require(set(blob) == {"state", "clock"}, "invalid_snapshot_state")
            expected = {k for k, v in self.specs[name]["coverage"].items() if v == "snapshot" and k != "clock"}
            require(isinstance(blob["state"], dict) and set(blob["state"]) == expected, "incomplete_snapshot_state")
            self._clock(self.specs[name], blob["clock"], self.wall_ns())
            blobs[name] = blob
        return manifest, blobs

    def verify_quiescent_source(self, path, expected_snapshot_sha256):
        """Bind another component while every captured service remains frozen."""
        manifest, blobs = self.load(path, expected_snapshot_sha256)
        require(same_live_runtime(manifest["source"], self._inventory(manifest["cohort_id"])), "source_runtime_changed")
        for name, blob in blobs.items():
            self._quiescent_probe(name, manifest["barrier_id"], manifest["services"][name]["instance_id"],
                                  blob["state"], blob["clock"])
        return manifest

    def restore(self, path, attempt, *, expected_snapshot_sha256, activate=False, activation_delay_ns=3_000_000_000):
        require(identity(attempt), "invalid_restore_attempt")
        manifest, blobs = self.load(path, expected_snapshot_sha256)
        claim_path = self.activation_claim(expected_snapshot_sha256)
        require(not activate or not claim_path.exists(), "snapshot_activation_already_claimed")
        cohort, barrier = manifest["cohort_id"], manifest["barrier_id"]
        target = self._inventory(cohort, attempt)
        source_ids = {r["container_id"] for r in manifest["source"]["containers"]}
        require(target["project"] != manifest["source"]["project"]
                and not source_ids.intersection(r["container_id"] for r in target["containers"]), "cold_restore_requires_new_containers")
        require(target.get("daemon_id") and target.get("daemon_id") == manifest["source"].get("daemon_id"), "cross_daemon_restore_unverified")
        source_images = {r["service"]: r["image"] for r in manifest["source"]["containers"]}
        require(all(source_images.get(r["service"]) == r["image"] for r in target["containers"]), "restore_image_mismatch")
        require(not set(manifest["source"].get("owned_volumes", [])).intersection(target.get("owned_volumes", [])), "restore_requires_independent_storage")
        instances = {name: self._capabilities(name, barrier, pristine=True) for name in sorted(self.specs)}
        require(all(instances[name] != manifest["services"][name]["instance_id"] for name in self.specs), "cold_restore_requires_new_processes")
        reference = self.wall_ns()
        expected_clocks = {name: self._clock(self.specs[name], blob["clock"], reference) for name, blob in blobs.items()}
        try:
            for name in sorted(self.specs):
                instance, body = self._call(name, "quiesce", barrier)
                require(instance == instances[name] and body.get("phase") == "quiescent"
                        and type(body.get("in_flight")) is int and body["in_flight"] == 0, "target_quiesce_failed")
            for name in sorted(self.specs):
                instance, _ = self._call(name, "import", barrier, checkpoint=blobs[name], reference_wall_ns=reference,
                                         max_downtime_ns=self.contract["max_downtime_ns"])
                require(instance == instances[name], "target_generation_changed")
            # Nobody is released until every independently queried component agrees.
            for name in sorted(self.specs):
                self._quiescent_probe(name, barrier, instances[name], blobs[name]["state"], expected_clocks[name])
            require(same_live_runtime(target, self._inventory(cohort, attempt)), "target_runtime_changed")
            if activate:
                require(type(activation_delay_ns) is int and 1_000_000_000 <= activation_delay_ns <= 30_000_000_000, "invalid_activation_delay")
                self.backend.assert_source_absent(manifest, target)
                last_wall, wait_start = self.wall_ns(), self.monotonic_ns()
                at = last_wall + activation_delay_ns
                claim = dict(attempt_id=attempt, target=target, snapshot_sha256=digest(manifest), status="claimed")
                write_report(claim_path, claim)
                for name in sorted(self.specs):
                    instance, body = self._call(name, "activate", barrier, at_wall_ns=at)
                    require(instance == instances[name] and body.get("phase") == "scheduled", "activation_not_scheduled")
                while True:
                    now_wall = self.wall_ns()
                    require(now_wall >= last_wall, "activation_wall_clock_reversed")
                    last_wall = now_wall
                    if now_wall >= at + self.contract["clock_skew_ns"]:
                        break
                    elapsed = self.monotonic_ns() - wait_start
                    require(0 <= elapsed <= activation_delay_ns + self.contract["clock_skew_ns"], "activation_wait_timeout")
                    self.sleep(max(0, min(0.05, (at + self.contract["clock_skew_ns"] - now_wall) / 1e9)))
                for name in sorted(self.specs):
                    started = self.wall_ns()
                    instance, body = self._call(name, "probe", barrier)
                    ended = self.wall_ns()
                    require(instance == instances[name] and body.get("phase") == "running"
                            and body.get("barrier_id") == barrier and type(body.get("in_flight")) is int and body["in_flight"] == 0
                            and body.get("digests") == {k: digest(v) for k, v in blobs[name]["state"].items()}, "active_state_probe_mismatch")
                    clock = body.get("clock") or {}
                    spec, frozen = self.specs[name], expected_clocks[name]
                    require(isinstance(clock, dict) and set(clock) == {"policy", "rate_num", "rate_den", "logical_ns"}
                            and type(clock["rate_num"]) is int and type(clock["rate_den"]) is int
                            and clock.get("policy") == spec["clock_policy"]
                            and all(clock.get(key) == spec[key] for key in ("rate_num", "rate_den")), "active_clock_configuration_mismatch")
                    base = frozen["logical_ns"]
                    if spec["clock_policy"] == "logical_elapsed":
                        base += (at - reference) * spec["rate_num"] // spec["rate_den"]
                    low = base + max(0, started - at - self.contract["clock_skew_ns"]) * spec["rate_num"] // spec["rate_den"]
                    high = base + (ended - at + self.contract["clock_skew_ns"]) * spec["rate_num"] // spec["rate_den"] + 2
                    require(type(clock.get("logical_ns")) is int and low <= clock["logical_ns"] <= high, "active_clock_probe_mismatch")
                require(same_live_runtime(target, self._inventory(cohort, attempt)), "target_runtime_changed_after_activation")
        except BaseException:
            # Partial import/release is not rollback. Discard/fence only this
            # admitted fresh target; leave the activation claim consumed.
            try:
                self.backend.quarantine(target)
            except BaseException:
                raise SnapshotError("restore_failed_and_quarantine_unconfirmed") from None
            raise
        return dict(version=1, status="services_active_verified" if activate else "services_restored_quiescent",
                    service_state_verified=True, service_clocks_verified=True, can_resume_agent=False,
                    agent_blockers=["workspace_restore_unverified", "native_session_load_unverified",
                                    "provider_usage_and_new_authorization_require_separate_checks"],
                    snapshot_sha256=digest(manifest), cohort_id=cohort, attempt_id=attempt,
                    target=target, reference_wall_ns=reference, expected_clocks=expected_clocks)
