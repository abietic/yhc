"""Fixed sample envelopes for a NEW operator-owned benchmark cohort.

Each cap remains assigned to its sample, even after completion. There is no
borrowing, reopening, recovery or paid authorization API. The caller still owns
funding admission, the single-use dispatch claim, price/contract/account/time
checks, native checkpoints and stopping policy. Existing cold-recovery journal
and native YHC accounting are unchanged. No provider or credential code here.
"""

import argparse
import copy
import hashlib
import json
import os
import re
import stat
import threading
import uuid
from dataclasses import dataclass, field
from decimal import Decimal, localcontext
from pathlib import Path

try:
    import fcntl
except ImportError:
    fcntl = None

from scripts.terminal_bench.native_cost import decimal_amount
from scripts.terminal_bench.reservation_bound import canonical, digest, request_bound, token_cost


class BudgetStop(ValueError):
    pass


def require(value, code):
    if not value:
        raise BudgetStop(code)


def amount(value):
    return Decimal(decimal_amount(value))


def receipt_amount(value):
    require(isinstance(value, str) and re.fullmatch(r"[0-9]{1,60}(?:\.[0-9]{1,12})?", value), "receipt_amount_invalid")
    return Decimal(value)


def _assess(plan, anchor):
    keys = {"version", "scope", "cohort_id", "currency", "max_estimated_cost", "max_output_tokens_per_call",
            "max_provider_calls", "price_card", "samples"}
    require(isinstance(plan, dict) and set(plan) == keys and isinstance(anchor, str)
            and re.fullmatch(r"[0-9a-f]{64}", anchor) and digest(plan) == anchor
            and type(plan["version"]) is int and plan["version"] == 1
            and plan["scope"] == "benchmark-fresh-isolated-budget-v1", "fresh_plan_binding_invalid")
    require(isinstance(plan["cohort_id"], str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}", plan["cohort_id"])
            and isinstance(plan["currency"], str) and plan["currency"] == plan["price_card"].get("currency")
            and type(plan["max_provider_calls"]) is int and 1 <= plan["max_provider_calls"] <= 200
            and amount(plan["max_estimated_cost"]) > 0, "fresh_plan_limits_invalid")
    require(isinstance(plan["samples"], list) and 1 <= len(plan["samples"]) <= 128, "fresh_sample_inventory_invalid")
    seen, samples, total = set(), [], Decimal(0)
    for row in plan["samples"]:
        require(isinstance(row, dict) and set(row) == {"sample_id", "protocol", "max_estimated_cost"}
                and isinstance(row["sample_id"], str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}", row["sample_id"])
                and row["sample_id"] not in seen and amount(row["max_estimated_cost"]) > 0, "fresh_sample_invalid")
        seen.add(row["sample_id"])
        key = "max_output_tokens" if row["protocol"] == "responses" else "max_tokens"
        proof = request_bound(dict(model="deepseek-flash", **{key: plan["max_output_tokens_per_call"]}),
                              row["protocol"], plan["price_card"], plan["max_output_tokens_per_call"])
        total += amount(row["max_estimated_cost"])
        samples.append(dict(**row, minimum_full_request_reservation=proof["reserved_amount"],
                            first_request_fundable=amount(row["max_estimated_cost"]) >= amount(proof["reserved_amount"])))
    blocked = [r["sample_id"] for r in samples if not r["first_request_fundable"]]
    reasons = (["sample_caps_exceed_shared_cap"] if total > amount(plan["max_estimated_cost"]) else [])
    if blocked:
        reasons.append("batch_request_reservation_unfunded")
    return dict(status="blocked" if reasons else "ready_offline", plan_sha256=anchor, currency=plan["currency"],
                shared_cap=plan["max_estimated_cost"], committed_sample_caps=format(total, ".12f"), samples=samples,
                blocked_samples=blocked, blocking_reasons=reasons, paid_dispatch_authorized=False,
                real_provider_calls=0, can_resume_agent=False, automatic_paid_resume=False)


def assess_plan(plan, anchor):
    with localcontext() as context:
        context.prec = 90
        try:
            return _assess(plan, anchor)
        except BudgetStop:
            raise
        except (ValueError, KeyError, TypeError, AttributeError, ArithmeticError) as exc:
            raise BudgetStop("fresh_plan_invalid") from exc


@dataclass(frozen=True)
class Reservation:
    call_id: str
    body_sha256: str
    wire_body: bytes = field(repr=False)


class FreshBudget:
    @classmethod
    def create(cls, root, plan, anchor):
        return cls(root, plan, anchor)

    def __init__(self, root, plan, anchor):
        plan = copy.deepcopy(plan)
        report = assess_plan(plan, anchor)
        require(not report["blocking_reasons"], report["blocking_reasons"][0] if report["blocking_reasons"] else "")
        require(fcntl is not None, "budget_requires_posix")
        # Creation is intentionally exclusive. A prior writer's files never
        # become a new live budget, even when every recorded call was known.
        self.root = Path(root).absolute()
        require(self.root.parent.resolve() == self.root.parent, "budget_parent_symlink")
        self.root.mkdir(mode=0o700)
        self._mutex, self._failed, self._fd, self._lock_fd = threading.RLock(), False, None, None
        self._plan, self.plan_sha256, self._disk_sha = copy.deepcopy(plan), anchor, None
        self._state = dict(version=1, plan_sha256=anchor, currency=plan["currency"],
                           samples={s["sample_id"]: dict(status="pending") for s in plan["samples"]},
                           calls=[], unknown_usage=False)
        try:
            self._fd = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            self._identity = (os.fstat(self._fd).st_dev, os.fstat(self._fd).st_ino)
            self._lock_fd = os.open("owner.lock", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self._fd)
            fcntl.flock(self._lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self._lock_identity = (os.fstat(self._lock_fd).st_dev, os.fstat(self._lock_fd).st_ino)
            self._persist()
            parent = os.open(self.root.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                os.fsync(parent)
            finally:
                os.close(parent)
        except BaseException:
            self.close()
            raise

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def close(self):
        with self._mutex:
            for name in ("_lock_fd", "_fd"):
                fd = getattr(self, name)
                if fd is not None:
                    os.close(fd)
                    setattr(self, name, None)

    def _check_storage(self):
        require(self._fd is not None, "accounting_unavailable")
        opened, current = os.fstat(self._fd), self.root.lstat()
        require(self.root.parent.resolve() == self.root.parent
                and stat.S_ISDIR(current.st_mode) and (current.st_dev, current.st_ino) == self._identity
                and (opened.st_dev, opened.st_ino) == self._identity and current.st_uid == os.geteuid()
                and stat.S_IMODE(current.st_mode) == 0o700, "budget_directory_changed")
        lock = os.stat("owner.lock", dir_fd=self._fd, follow_symlinks=False)
        require(stat.S_ISREG(lock.st_mode) and (lock.st_dev, lock.st_ino) == self._lock_identity
                and lock.st_nlink == 1 and lock.st_uid == os.geteuid() and stat.S_IMODE(lock.st_mode) == 0o600,
                "budget_owner_changed")
        if self._disk_sha is not None:
            fd = os.open("ledger.json", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=self._fd)
            with os.fdopen(fd, "rb") as f:
                st = os.fstat(f.fileno())
                require(stat.S_ISREG(st.st_mode) and st.st_uid == os.geteuid() and st.st_nlink == 1
                        and stat.S_IMODE(st.st_mode) == 0o600 and st.st_size <= 32 << 20, "budget_ledger_changed")
                require(hashlib.sha256(f.read()).hexdigest() == self._disk_sha, "budget_ledger_changed")

    def _persist(self):
        try:
            self._check_storage()
            raw = canonical(self._state)
            require(len(raw) <= 32 << 20, "budget_ledger_capacity")
            name = "ledger." + uuid.uuid4().hex + ".tmp"
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self._fd)
            with os.fdopen(fd, "wb") as f:
                f.write(raw); f.flush(); os.fsync(f.fileno())
            os.replace(name, "ledger.json", src_dir_fd=self._fd, dst_dir_fd=self._fd)
            os.fsync(self._fd)
            self._disk_sha = hashlib.sha256(raw).hexdigest()
            self._check_storage()
        except BaseException:
            self._failed = True
            raise

    def _available(self):
        require(not self._failed and self._fd is not None, "accounting_unavailable")
        try:
            self._check_storage()
            require(digest(self._plan) == self.plan_sha256, "fresh_plan_changed")
        except BaseException:
            self._failed = True
            raise

    def _healthy(self):
        self._available()
        require(not self._state["unknown_usage"], "unknown_usage")

    def _sample(self, sample):
        rows = [s for s in self._plan["samples"] if s["sample_id"] == sample]
        require(len(rows) == 1, "sample_not_authorized")
        return rows[0]

    def _exposure(self, sample=None):
        return sum((receipt_amount(r["known_cost"] if r["state"] == "known" else r["liability"])
                    for r in self._state["calls"] if sample is None or r["sample"] == sample), Decimal(0))

    def claim_sample(self, sample):
        with self._mutex:
            self._healthy(); self._sample(sample)
            require(self._state["samples"][sample]["status"] == "pending", "sample_already_started")
            require(not any(s["status"] == "running" for s in self._state["samples"].values()), "previous_sample_not_closed")
            self._state["samples"][sample]["status"] = "running"
            self._persist()

    def reserve(self, sample, body):
        with self._mutex, localcontext() as context:
            context.prec = 90
            self._healthy(); spec = self._sample(sample)
            require(self._state["samples"][sample]["status"] == "running", "sample_not_running")
            require(sum(r["sample"] == sample for r in self._state["calls"]) < self._plan["max_provider_calls"], "sample_call_limit")
            raw = canonical(body)
            bound = request_bound(json.loads(raw), spec["protocol"], self._plan["price_card"], self._plan["max_output_tokens_per_call"])
            reserve = amount(bound["reserved_amount"])
            require(self._exposure(sample) + reserve <= amount(spec["max_estimated_cost"]), "sample_budget_exhausted")
            require(self._exposure() + reserve <= amount(self._plan["max_estimated_cost"]), "cohort_budget_exhausted")
            call_id = str(uuid.uuid4())
            self._state["calls"].append(dict(id=call_id, sample=sample, state="reserved", bound=bound,
                                             liability=bound["reserved_amount"], known_cost=None))
            self._persist()  # The caller can only open upstream after this returns.
            return Reservation(call_id, bound["body_sha256"], raw)

    def _open_call(self, call_id):
        rows = [r for r in self._state["calls"] if r["id"] == call_id]
        require(len(rows) == 1 and rows[0]["state"] == "reserved", "duplicate_or_unknown_settlement")
        return rows[0]

    def mark_unknown(self, call_id):
        with self._mutex:
            self._available()
            row = self._open_call(call_id)
            row["state"] = "unknown"; self._state["unknown_usage"] = True
            self._persist()

    def settle(self, call_id, *, input_tokens, cached_input_tokens, output_tokens):
        with self._mutex:
            self._available()
            row = self._open_call(call_id)
            valid = all(type(n) is int and 0 <= n <= 2**64 - 1 for n in (input_tokens, cached_input_tokens, output_tokens))
            valid = valid and cached_input_tokens <= input_tokens
            cost = token_cost(self._plan["price_card"], input_tokens, cached_input_tokens, output_tokens) if valid else None
            if (not valid or input_tokens > row["bound"]["input_tokens"] or output_tokens > row["bound"]["output_tokens"]
                    or receipt_amount(cost) > receipt_amount(row["liability"])):
                if cost is not None:
                    row["liability"] = str(max(receipt_amount(row["liability"]), receipt_amount(cost)))
                self.mark_unknown(call_id)
                raise BudgetStop("provider_usage_bound_unverified")
            previous = copy.deepcopy(row)
            row.update(state="known", known_cost=cost, input_tokens=input_tokens,
                       cached_input_tokens=cached_input_tokens, output_tokens=output_tokens)
            try:
                self._persist()
            except BaseException:
                # Publication may have failed before or after rename. Neither
                # uncertain disk state permits releasing the durable reserve.
                # The mutex hides this provisional known state from readers.
                row.clear(); row.update(previous)
                row["state"] = "unknown"
                self._state["unknown_usage"] = True
                raise

    def finish_sample(self, sample, status):
        with self._mutex:
            self._available()
            self._sample(sample)
            require(status in ("completed", "failed", "budget_paused", "unknown_usage"), "invalid_sample_status")
            require(self._state["samples"][sample]["status"] == "running", "sample_not_running")
            require(not any(r["sample"] == sample and r["state"] == "reserved" for r in self._state["calls"]), "outstanding_request")
            require(not self._state["unknown_usage"] or status == "unknown_usage", "unknown_usage")
            self._state["samples"][sample]["status"] = status
            self._persist()

    def snapshot(self):
        with self._mutex, localcontext() as context:
            context.prec = 90
            if not self._failed and self._fd is not None:
                self._available()
            pending = [r for r in self._state["calls"] if r["state"] == "reserved"]
            retained = [r for r in self._state["calls"] if r["state"] == "unknown"]
            if self._fd is None:
                retained += pending; pending = []
            return dict(currency=self._plan["currency"], plan_sha256=self.plan_sha256, accounting_error=self._failed,
                        known_cost=format(sum((receipt_amount(r["known_cost"]) for r in self._state["calls"] if r["state"] == "known"), Decimal(0)), ".12f"),
                        outstanding=format(sum((receipt_amount(r["liability"]) for r in pending), Decimal(0)), ".12f"),
                        retained=format(sum((receipt_amount(r["liability"]) for r in retained), Decimal(0)), ".12f"),
                        unknown_usage=self._state["unknown_usage"] or bool(retained), exposure=format(self._exposure(), ".12f"),
                        samples=copy.deepcopy(self._state["samples"]), can_resume_agent=False, automatic_paid_resume=False)


def main():
    parser = argparse.ArgumentParser(description="Assess isolated NEW sample budgets offline; never dispatch or resume")
    parser.add_argument("--plan", required=True, type=Path)
    parser.add_argument("--plan-sha256", required=True)
    args = parser.parse_args()
    try:
        raw = args.plan.read_bytes()
        require(len(raw) <= 65536, "fresh_plan_size")
        result = assess_plan(json.loads(raw), args.plan_sha256)
    except (ValueError, OSError) as exc:
        parser.exit(2, "fresh_budget_failed_closed: " + type(exc).__name__ + "\n")
    print(json.dumps(result, sort_keys=True))
    return 2 if result["blocking_reasons"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
