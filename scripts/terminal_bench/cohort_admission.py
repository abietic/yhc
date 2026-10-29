"""Operator-owned shared budget journal for serial benchmark cold dispatch.

The plan is independently anchored authority, never inferred from checkpoints.
One POSIX lock spans admission, execution and settlement. An interrupted writer
leaves its full allocation retained; reopening a ledger cannot release it.
"""

import argparse
import copy
import json
import os
import re
import stat
from contextlib import contextmanager
from decimal import Decimal, localcontext
from pathlib import Path

try:
    import fcntl
except ImportError:
    fcntl = None

from scripts.terminal_bench.native_cost import decimal_amount
from scripts.terminal_bench.service_snapshot import MAX_BYTES, SnapshotError, decode, digest, encode, identity, private_json, require
from scripts.terminal_bench.workspace_io import session_identity
from scripts.terminal_bench.workspace_recovery import usage_history

MAX_EVENTS = 256
MAX_JOURNAL_BYTES = 64 << 20


def amount(value, *, positive=False):
    try:
        result = Decimal(decimal_amount(value))
        require(not positive or result > 0, "cohort_amount_invalid")
        return result
    except (ValueError, ArithmeticError):
        raise SnapshotError("cohort_amount_invalid") from None


def sha(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def history_totals(histories, currency):
    """Validate every row and globally unique invocation/call identity."""
    known = outstanding = retained = Decimal(0)
    complete, segments, calls = True, set(), set()
    for session_id, hist in histories.items():
        require(session_identity(session_id), "cohort_session_invalid")
        complete = bool(usage_history(hist, session_id)["coverage_complete"] and complete)
        for row in hist["segments"]:
            usage = row["usage"]
            ledger, cost = usage["call_ledger"], usage.get("cost")
            require(ledger["segment_id"] not in segments, "cohort_usage_identity_repeated")
            segments.add(ledger["segment_id"])
            for record in ledger["records"]:
                require(record["call_id"] not in calls, "cohort_usage_identity_repeated")
                calls.add(record["call_id"])
            require(isinstance(cost, dict) and cost.get("price_card", {}).get("currency") == currency
                    and isinstance(cost.get("reservation"), dict), "cohort_currency_or_cost_unverified")
            known += Decimal(cost["known_estimated_amount"])
            outstanding += Decimal(cost["reservation"]["outstanding_amount"])
            retained += Decimal(cost["reservation"]["retained_amount"])
    return known, outstanding, retained, complete


def validate_plan(plan, anchor):
    require(isinstance(plan, dict) and set(plan) == {"version", "scope", "cohort_id", "currency", "max_estimated_cost",
            "expires_unix", "sessions", "external_reservations"} and sha(anchor) and digest(plan) == anchor
            and type(plan["version"]) is int and plan["version"] == 1 and plan["scope"] == "benchmark-cohort-admission-v1"
            and identity(plan["cohort_id"]) and isinstance(plan["currency"], str)
            and re.fullmatch(r"[A-Z]{3}", plan["currency"]) and type(plan["expires_unix"]) is int
            and 0 < plan["expires_unix"] < 2**63, "cohort_plan_binding_invalid")
    amount(plan["max_estimated_cost"], positive=True)
    require(isinstance(plan["sessions"], list) and 1 <= len(plan["sessions"]) <= 128
            and isinstance(plan["external_reservations"], list) and len(plan["external_reservations"]) <= 128,
            "cohort_plan_inventory_invalid")
    histories, ceilings, holds = {}, {}, set()
    for row in plan["sessions"]:
        require(isinstance(row, dict) and set(row) == {"session_id", "max_estimated_cost", "usage_history"}
                and session_identity(row["session_id"]) and row["session_id"] not in histories, "cohort_session_invalid")
        ceilings[row["session_id"]] = amount(row["max_estimated_cost"], positive=True)
        histories[row["session_id"]] = copy.deepcopy(row["usage_history"])
    history_totals(histories, plan["currency"])
    for row in plan["external_reservations"]:
        require(isinstance(row, dict) and set(row) == {"reservation_id", "amount", "state"}
                and identity(row["reservation_id"]) and row["reservation_id"] not in holds
                and row["state"] in ("outstanding", "retained"), "cohort_external_reservation_invalid")
        holds.add(row["reservation_id"])
        amount(row["amount"], positive=True)
    return histories, ceilings


class Journal:
    """Pinned private directory, immutable chained events, one exclusive writer."""

    def __init__(self, root, fd):
        self.root, self.fd = root, fd
        self.events, self.bytes = [], 0

    def check_root(self):
        a, b = os.fstat(self.fd), self.root.lstat()
        require(self.root.parent.resolve() == self.root.parent and stat.S_ISDIR(b.st_mode) and a.st_dev == b.st_dev and a.st_ino == b.st_ino
                and b.st_uid == os.geteuid() and stat.S_IMODE(b.st_mode) == 0o700, "cohort_directory_changed")
        if hasattr(self, "lock_identity"):
            lock = os.stat("owner.lock", dir_fd=self.fd, follow_symlinks=False)
            require((lock.st_dev, lock.st_ino) == self.lock_identity and lock.st_nlink == 1
                    and stat.S_IMODE(lock.st_mode) == 0o600, "cohort_lock_changed")

    def read(self, name, mode):
        self.check_root()
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=self.fd)
        with os.fdopen(fd, "rb") as file:
            info = os.fstat(file.fileno())
            require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and info.st_nlink == 1
                    and stat.S_IMODE(info.st_mode) == mode and info.st_size <= MAX_BYTES, "cohort_file_unverified")
            return decode(file.read(MAX_BYTES + 1))

    def write(self, name, value, mode):
        self.check_root()
        raw = encode(value)
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self.fd)
        with os.fdopen(fd, "wb") as file:
            file.write(raw)
            file.flush()
            os.fchmod(file.fileno(), mode)
            os.fsync(file.fileno())
        os.fsync(self.fd)
        self.check_root()

    def load(self, anchor):
        names = set(os.listdir(self.fd))
        expected = {"plan.json", "owner.lock", "head.json"}
        require(expected <= names, "cohort_journal_incomplete")
        names -= expected
        require(len(names) <= MAX_EVENTS and all(re.fullmatch(r"event\.[0-9]{8}\.json", n) for n in names),
                "cohort_journal_unverified")
        head = self.read("head.json", 0o400)
        require(isinstance(head, dict) and set(head) == {"sequence", "sha256"}
                and type(head["sequence"]) is int and head["sequence"] == len(names), "cohort_journal_head_mismatch")
        previous = anchor
        for sequence in range(1, len(names) + 1):
            name = "event.%08d.json" % sequence
            require(name in names, "cohort_journal_gap")
            event = self.read(name, 0o400)
            require(isinstance(event, dict) and set(event) == {"version", "sequence", "previous_sha256", "payload"}
                    and type(event["version"]) is int and event["version"] == 1
                    and type(event["sequence"]) is int and event["sequence"] == sequence
                    and event["previous_sha256"] == previous, "cohort_journal_chain_invalid")
            self.bytes += len(encode(event))
            require(self.bytes <= MAX_JOURNAL_BYTES, "cohort_journal_capacity_exhausted")
            self.events.append(event)
            previous = digest(event)
        require(head["sha256"] == previous, "cohort_journal_head_mismatch")

    def append(self, payload, anchor):
        event = dict(version=1, sequence=len(self.events) + 1,
                     previous_sha256=digest(self.events[-1]) if self.events else anchor, payload=payload)
        size = len(encode(event))
        require(len(self.events) < MAX_EVENTS and self.bytes + size <= MAX_JOURNAL_BYTES, "cohort_journal_capacity_exhausted")
        # Sync a high-water intent before publishing an event. A missing tail,
        # failed write/rename/sync or abandoned temp file blocks future use.
        self.write("head.next", dict(sequence=event["sequence"], sha256=digest(event)), 0o400)
        os.replace("head.next", "head.json", src_dir_fd=self.fd, dst_dir_fd=self.fd)
        os.fsync(self.fd)
        self.write("event.%08d.json" % event["sequence"], event, 0o400)
        self.events.append(event)
        self.bytes += size


class CohortAdmission:
    def __init__(self, root, plan_sha256):
        self.root, self.anchor = Path(root).absolute(), plan_sha256
        with self._locked() as journal:
            self.plan = journal.read("plan.json", 0o400)
            with localcontext() as ctx:
                ctx.prec = 90
                validate_plan(self.plan, self.anchor)
                journal.load(self.anchor)
                self._replay(journal)

    @classmethod
    def create(cls, root, plan, plan_sha256):
        with localcontext() as ctx:
            ctx.prec = 90
            validate_plan(plan, plan_sha256)
        root = Path(root).absolute()
        require(root.parent.resolve() == root.parent, "cohort_parent_symlink")
        root.mkdir(mode=0o700)  # Explicit new authority only; never reopen/reset.
        fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            journal = Journal(root, fd)
            journal.write("plan.json", plan, 0o400)
            journal.write("owner.lock", {}, 0o600)
            journal.write("head.json", dict(sequence=0, sha256=plan_sha256), 0o400)
            parent = os.open(root.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try: os.fsync(parent)
            finally: os.close(parent)
        finally:
            os.close(fd)
        return cls(root, plan_sha256)

    @contextmanager
    def _locked(self):
        require(fcntl is not None and self.root.parent.resolve() == self.root.parent, "cohort_requires_private_posix_path")
        directory = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        lock = None
        try:
            journal = Journal(self.root, directory)
            journal.check_root()
            # A lost lock is corruption. Never create another lock inode here.
            lock = os.open("owner.lock", os.O_RDWR | os.O_NOFOLLOW, dir_fd=directory)
            a = os.fstat(lock)
            require(stat.S_ISREG(a.st_mode) and a.st_uid == os.geteuid() and a.st_nlink == 1
                    and stat.S_IMODE(a.st_mode) == 0o600, "cohort_lock_unverified")
            try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError: raise SnapshotError("cohort_dispatch_running") from None
            b = os.stat("owner.lock", dir_fd=directory, follow_symlinks=False)
            require(a.st_dev == b.st_dev and a.st_ino == b.st_ino, "cohort_lock_changed")
            journal.lock_identity = (a.st_dev, a.st_ino)
            yield journal
            journal.check_root()
            b = os.stat("owner.lock", dir_fd=directory, follow_symlinks=False)
            require(a.st_dev == b.st_dev and a.st_ino == b.st_ino, "cohort_lock_changed")
        finally:
            if lock is not None: os.close(lock)
            os.close(directory)

    def _load(self, journal):
        require(journal.read("plan.json", 0o400) == self.plan and digest(self.plan) == self.anchor, "cohort_plan_changed")
        journal.load(self.anchor)
        return self._replay(journal)

    def _replay(self, journal):
        from scripts.terminal_bench.cold_dispatch import reconcile_execution
        histories, ceilings = validate_plan(self.plan, self.anchor)
        state = dict(histories=histories, ceilings=ceilings, dispatches={}, sources=set())
        for event in journal.events:
            value = event["payload"]
            require(isinstance(value, dict), "cohort_event_invalid")
            kind = value.get("kind")
            if kind == "reserved":
                require(set(value) == {"kind", "grant", "grant_sha256", "session_id", "source_segment_id", "admitted_unix"}
                        and type(value["admitted_unix"]) is int, "cohort_event_invalid")
                session, source = value["session_id"], value["source_segment_id"]
                require(session in histories, "cohort_session_invalid")
                body = self._body(histories[session])
                require(source == body["paused_invocation"]["record"]["segment_id"], "cohort_source_mismatch")
                self._check(state, body, value["grant"], value["grant_sha256"], value["admitted_unix"])
                state["dispatches"][value["grant"]["dispatch_id"]] = dict(**value, state="reserved")
                state["sources"].add(source)
            else:
                require(kind in ("unknown", "reconciled") and identity(value.get("dispatch_id")), "cohort_event_invalid")
                dispatch = state["dispatches"].get(value["dispatch_id"])
                require(dispatch is not None and dispatch["state"] == "reserved", "cohort_settlement_repeated_or_unreserved")
                if kind == "unknown":
                    require(set(value) == {"kind", "dispatch_id"}, "cohort_event_invalid")
                    dispatch["state"] = "unknown"
                else:
                    require(set(value) == {"kind", "dispatch_id", "execution", "receipt_sha256"} and sha(value["receipt_sha256"]),
                            "cohort_event_invalid")
                    body = self._body(histories[dispatch["session_id"]])
                    result = reconcile_execution(body, dispatch["grant"]["bundle_sha256"], dispatch["grant"],
                                                 dispatch["grant_sha256"], value["execution"])
                    self._apply_result(state, dispatch, result)
        self._snapshot(state)
        return state

    def _body(self, hist):
        return dict(cohort_id=self.plan["cohort_id"], usage_history=hist, paused_invocation=dict(record=dict(
            session_id=hist["session_id"], segment_id=hist["segment_ids"][-1])))

    def _snapshot(self, state, active=None):
        known, outstanding, retained, complete = history_totals(state["histories"], self.plan["currency"])
        for session, hist in state["histories"].items():
            totals = history_totals({session: hist}, self.plan["currency"])
            complete = complete and sum(totals[:3]) <= state["ceilings"][session]
        for hold in self.plan["external_reservations"]:
            if hold["state"] == "outstanding": outstanding += amount(hold["amount"])
            else: retained += amount(hold["amount"])
            complete = False
        for dispatch_id, row in state["dispatches"].items():
            if row["state"] == "settled": continue
            allocated = amount(row["grant"]["limits"]["max_estimated_cost"])
            partial = Decimal(row.get("known_delta", "0"))
            known += partial
            if row["state"] == "reserved" and dispatch_id == active: outstanding += allocated - partial
            else: retained += allocated - partial
            complete = False
        exposure = known + outstanding + retained
        return dict(version=1, scope="benchmark-cohort-accounting", cohort_id=self.plan["cohort_id"],
                    plan_sha256=self.anchor, currency=self.plan["currency"], max_estimated_cost=self.plan["max_estimated_cost"],
                    known_estimated_amount=format(known, ".12f"), outstanding_amount=format(outstanding, ".12f"),
                    retained_amount=format(retained, ".12f"), exposure_amount=format(exposure, ".12f"),
                    remaining_amount=format(max(Decimal(0), amount(self.plan["max_estimated_cost"]) - exposure), ".12f"),
                    blocked=not complete or exposure > amount(self.plan["max_estimated_cost"]),
                    usage_histories=copy.deepcopy(state["histories"]), automatic_paid_resume=False)

    def _check(self, state, body, grant, anchor, now):
        from scripts.terminal_bench.cold_dispatch import validate_grant
        snap = self._snapshot(state)
        require(not snap["blocked"], "cohort_accounting_unverified")
        session = body["usage_history"]["session_id"]
        require(body.get("cohort_id") == self.plan["cohort_id"] and session in state["histories"]
                and body["usage_history"] == state["histories"][session], "cohort_scope_or_history_mismatch")
        source = body["paused_invocation"]["record"]["segment_id"]
        require(body["paused_invocation"]["record"]["session_id"] == session
                and source == body["usage_history"]["segment_ids"][-1], "cohort_source_mismatch")
        validate_grant(grant, anchor, body, grant["bundle_sha256"], grant["attempt_id"], now)
        require(grant["currency"] == self.plan["currency"]
                and amount(grant["max_cumulative_estimated_cost"]) <= state["ceilings"][session], "cohort_currency_or_ceiling_mismatch")
        require(type(now) in (int, float) and 0 <= now and now + grant["limits"]["timeout_sec"] <= self.plan["expires_unix"],
                "cohort_plan_expired")
        require(grant["dispatch_id"] not in state["dispatches"] and source not in state["sources"], "cohort_dispatch_replayed")
        require(Decimal(snap["exposure_amount"]) + amount(grant["limits"]["max_estimated_cost"]) <= amount(self.plan["max_estimated_cost"]),
                "cohort_budget_insufficient")

    def _apply_result(self, state, dispatch, result):
        session = dispatch["session_id"]
        candidate = copy.deepcopy(state["histories"])
        candidate[session] = result["usage_history"]
        history_totals(candidate, self.plan["currency"])
        if result["accounting_complete"]:
            state["histories"][session] = result["usage_history"]
            dispatch["state"] = "settled"
        else:
            dispatch["state"] = "unknown"
            dispatch["known_delta"] = result["usage_history"]["segments"][-1]["usage"]["cost"]["known_estimated_amount"]

    def inspect(self):
        with self._locked() as journal, localcontext() as ctx:
            ctx.prec = 90
            return self._snapshot(self._load(journal))

    @contextmanager
    def admit(self, body, grant, grant_sha256, *, now):
        with self._locked() as journal, localcontext() as ctx:
            ctx.prec = 90
            state = self._load(journal)
            self._check(state, body, grant, grant_sha256, now)
            # Keep room for both the reservation and its largest bounded result.
            require(len(journal.events) <= MAX_EVENTS - 2 and journal.bytes <= MAX_JOURNAL_BYTES - 2 * MAX_BYTES,
                    "cohort_journal_capacity_exhausted")
            allocation = Allocation(self, journal, state)
            try:
                yield allocation
            finally:
                try:
                    if allocation.reserved and not allocation.reconciled:
                        journal.append(dict(kind="unknown", dispatch_id=allocation.dispatch_id), self.anchor)
                finally:
                    allocation.active = False


class Allocation:
    def __init__(self, owner, journal, state):
        self.owner, self.journal, self.state = owner, journal, state
        self.reserved = self.reconciled = False
        self.active = True
        self.dispatch_id = None

    def reserve(self, body, grant, anchor, *, now):
        require(self.active and not self.reserved, "cohort_allocation_repeated_or_closed")
        self.owner._check(self.state, body, grant, anchor, now)
        value = dict(kind="reserved", grant=copy.deepcopy(grant), grant_sha256=anchor,
                     session_id=body["usage_history"]["session_id"],
                     source_segment_id=body["paused_invocation"]["record"]["segment_id"], admitted_unix=int(now))
        self.journal.append(value, self.owner.anchor)  # Full cap is durable before acknowledgment.
        self.dispatch_id, self.reserved = grant["dispatch_id"], True
        self.state["dispatches"][self.dispatch_id] = dict(**value, state="reserved")
        self.state["sources"].add(value["source_segment_id"])
        return dict(cohort_id=self.owner.plan["cohort_id"], plan_sha256=self.owner.anchor,
                    dispatch_id=self.dispatch_id, reserved_amount=grant["limits"]["max_estimated_cost"],
                    currency=self.owner.plan["currency"])

    def snapshot(self):
        require(self.active, "cohort_allocation_closed")
        return self.owner._snapshot(self.state, self.dispatch_id)

    def reconcile(self, result):
        from scripts.terminal_bench.cold_dispatch import reconcile_execution
        require(self.active and self.reserved and not self.reconciled, "cohort_allocation_not_open")
        row = self.state["dispatches"][self.dispatch_id]
        body = self.owner._body(self.state["histories"][row["session_id"]])
        checked = reconcile_execution(body, row["grant"]["bundle_sha256"], row["grant"], row["grant_sha256"], result["execution"])
        require(all(checked[k] == result.get(k) for k in checked), "cohort_result_unverified")
        candidate = copy.deepcopy(self.state)
        self.owner._apply_result(candidate, candidate["dispatches"][self.dispatch_id], checked)
        self.journal.append(dict(kind="reconciled", dispatch_id=self.dispatch_id, execution=checked["execution"],
                                 receipt_sha256=digest(result)), self.owner.anchor)
        self.state, self.reconciled = candidate, True


def main():
    parser = argparse.ArgumentParser(description="Initialize or inspect an explicitly anchored shared cold-dispatch budget")
    parser.add_argument("operation", choices=("init", "inspect"))
    parser.add_argument("--ledger-dir", required=True, type=Path)
    parser.add_argument("--plan-sha256", required=True)
    parser.add_argument("--plan", type=Path)
    args = parser.parse_args()
    try:
        if args.operation == "init":
            require(args.plan is not None, "cohort_plan_required")
            ledger = CohortAdmission.create(args.ledger_dir, private_json(args.plan), args.plan_sha256)
        else:
            ledger = CohortAdmission(args.ledger_dir, args.plan_sha256)
        print(json.dumps(ledger.inspect(), sort_keys=True))
    except (OSError, ValueError, KeyError, TypeError):
        parser.exit(1, "cohort_failed_closed: private_accounting_or_authority_unverified\n")


if __name__ == "__main__":
    main()
