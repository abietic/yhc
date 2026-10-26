"""Explicit one-shot benchmark cold dispatch, outside the agent solver.

The operator supplies an independently anchored finite grant. Component proofs
and historical usage never manufacture that grant or reopen an old writer.
"""

import copy
import ipaddress
import os
import re
import stat
import time
from contextlib import contextmanager
from decimal import Decimal, localcontext
from urllib.parse import urlsplit

try:
    import fcntl
except ImportError:
    fcntl = None

from scripts.terminal_bench.native_cost import decimal_amount, validate_price_card
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.service_snapshot import SnapshotError, digest, identity, require
from scripts.terminal_bench.workspace_recovery import usage_history


def sha(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def validate_gateway_transport(grant):
    """The current keeper protocol has network=none and no host relay.

    This is new-dispatch policy, not historical grant/usage validation. DNS,
    external/host routes and unproved network transitions cannot consume a
    source claim or budget allocation merely to fail when the CLI connects.
    A loopback address alone does not certify a trusted provider is ready.
    """
    route = grant.get("route") if isinstance(grant, dict) else None
    base = route.get("base_url") if isinstance(route, dict) else None
    require(isinstance(base, str) and 0 < len(base) <= 256, "dispatch_gateway_transport_unverified")
    try:
        url = urlsplit(base)
        address = ipaddress.ip_address(url.hostname)
        require(url.scheme in ("http", "https") and url.hostname and "%" not in url.hostname
                and address.is_loopback and getattr(address, "ipv4_mapped", None) is None
                and (url.port is None or 0 < url.port <= 65535), "dispatch_gateway_transport_unverified")
    except ValueError:
        raise SnapshotError("dispatch_gateway_transport_unverified") from None


def validate_grant(value, anchor, body, bundle_digest, attempt, now):
    keys = {"version", "scope", "dispatch_id", "bundle_sha256", "attempt_id", "usage_history_sha256", "expires_unix",
            "currency", "max_cumulative_estimated_cost", "binary_sha256", "route", "tools", "price_card", "limits"}
    require(isinstance(value, dict) and set(value) == keys and digest(value) == anchor
            and type(value["version"]) is int and value["version"] == 1 and value["scope"] == "benchmark-cold-dispatch-v1"
            and identity(value["dispatch_id"]) and value["bundle_sha256"] == bundle_digest
            and value["attempt_id"] == attempt and identity(attempt)
            and value["usage_history_sha256"] == digest(body["usage_history"])
            and sha(value["binary_sha256"]), "dispatch_grant_binding_invalid")
    limits = value["limits"]
    require(isinstance(limits, dict) and set(limits) == {"max_provider_calls", "max_total_tokens", "max_output_tokens_per_call",
            "max_estimated_cost", "timeout_sec"} and all(type(limits[k]) is int for k in limits if k != "max_estimated_cost")
            and 0 < limits["max_provider_calls"] <= 200 and 0 <= limits["max_total_tokens"] < 2**63
            and 0 < limits["max_output_tokens_per_call"] <= 65536 and 0 < limits["timeout_sec"] <= 900
            and type(value["expires_unix"]) is int and value["expires_unix"] >= now + limits["timeout_sec"],
            "dispatch_grant_limits_or_expiry_invalid")
    route, tools = value["route"], value["tools"]
    require(isinstance(route, dict) and set(route) == {"provider", "model", "base_url"}
            and all(isinstance(route[k], str) and 0 < len(route[k]) <= 256 and "\0" not in route[k] for k in route)
            and isinstance(tools, list) and len(tools) <= 128 and len(set(tools)) == len(tools)
            and all(isinstance(t, str) and re.fullmatch(r"[A-Za-z0-9_]+", t) for t in tools), "dispatch_route_invalid")
    url = urlsplit(route["base_url"])
    require(url.scheme in ("http", "https") and url.hostname and not url.username and not url.password
            and not url.query and not url.fragment, "dispatch_route_invalid")
    card = validate_price_card(value["price_card"])
    require(card["window"] == "upper_bound" and card["currency"] == value["currency"]
            and all(r.get("api_model") for r in card["routes"]), "dispatch_price_card_unverified")
    past = body["usage_history"]
    require(usage_history(past, body["paused_invocation"]["record"]["session_id"])["coverage_complete"],
            "dispatch_cumulative_usage_incomplete")
    require(len(past["segments"]) < 17, "dispatch_history_capacity_exhausted")
    try:
        decimal_amount(limits["max_estimated_cost"])
        decimal_amount(value["max_cumulative_estimated_cost"])
        with localcontext() as ctx:
            ctx.prec = 90
            spent = Decimal(0)
            for row in past["segments"]:
                cost = row["usage"]["cost"]
                require(cost["price_card"]["currency"] == value["currency"], "dispatch_currency_conversion_unverified")
                spent += Decimal(cost["known_estimated_amount"])
            fresh, total = Decimal(limits["max_estimated_cost"]), Decimal(value["max_cumulative_estimated_cost"])
            require(fresh > 0 and spent + fresh <= total, "dispatch_cumulative_budget_insufficient")
    except (KeyError, ValueError, ArithmeticError):
        raise SnapshotError("dispatch_cost_unverified") from None
    return dict(historical_estimated_amount=format(spent, ".12f"), new_reserved_limit=str(fresh),
                cumulative_exposure_limit=str(total), currency=value["currency"])


@contextmanager
def dispatch_lock(control, segment):
    require(fcntl is not None and identity(segment), "dispatch_source_identity_or_platform_invalid")
    path = control / ("dispatch." + segment + ".lock")
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and info.st_nlink == 1
                and stat.S_IMODE(info.st_mode) == 0o600, "dispatch_lock_unverified")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise SnapshotError("dispatch_already_claimed_or_running") from None
        claim = control / ("dispatch." + segment + ".json")
        require(not claim.exists() and not claim.is_symlink(), "dispatch_already_claimed_or_running")
        yield claim
    finally:
        os.close(fd)


class ColdBenchmarkLauncher:
    def __init__(self, bundle, *, cohort=None, wall=time.time):
        self.bundle, self.cohort, self.wall = bundle, cohort, wall

    def dispatch(self, path, bundle_digest, service_path, grant, grant_digest, gateway_token):
        require(self.cohort is not None, "dispatch_shared_cohort_admission_required")
        body, _, _ = self.bundle.load(path, bundle_digest, service_path)
        require("paused_invocation" in body, "dispatch_requires_verified_paused_bundle")
        validate_gateway_transport(grant)
        segment = body["paused_invocation"]["record"]["segment_id"]
        with dispatch_lock(self.bundle.control, segment) as claim:
            # The consumed check precedes every native probe. A losing launch
            # cannot change a winner's canonical session while it is running.
            with self.cohort.admit(body, grant, grant_digest, now=self.wall()) as allocation:
                budget = validate_grant(grant, grant_digest, body, bundle_digest, self.bundle.workspace.attempt, self.wall())
                require(isinstance(gateway_token, str) and 0 < len(gateway_token) <= 512
                        and gateway_token.isascii() and not any(c.isspace() for c in gateway_token), "dispatch_gateway_token_missing")
                body, handoff, joint = self.bundle.prepare_dispatch(path, bundle_digest, service_path)
                validate_grant(grant, grant_digest, body, bundle_digest, joint["workspace"]["attempt_id"], self.wall())
                actual = self.bundle.workspace.rpc("control_probe")
                require(actual == dict(binary_sha256=grant["binary_sha256"]), "dispatch_binary_changed")
                admission = allocation.reserve(body, grant, grant_digest, now=self.wall())
                checkpoint = ".yhc/usage-checkpoints/cold-" + grant["dispatch_id"]
                launch = dict(version=1, handoff=handoff, grant=grant, checkpoint_path=checkpoint,
                              expected_tree_sha256=joint["tree_sha256"], expected_workspace=joint["workspace"])
                write_report(claim, dict(version=1, scope="benchmark-cold-dispatch-claim", source_segment_id=segment,
                             bundle_sha256=bundle_digest, grant_sha256=grant_digest, joint=joint, budget=budget,
                             checkpoint_path=checkpoint, usage_history=body["usage_history"], cohort_admission=admission, status="claimed"))
                # Any exception, crash or ambiguous transport leaves this claim and
                # its full newly allocated exposure retained. No automatic retry.
                try:
                    execution = self.bundle.workspace.launch_cold(launch, gateway_token)
                except BaseException:
                    try:
                        self.bundle.workspace.quarantine(joint["workspace"])
                    except BaseException:
                        raise SnapshotError("dispatch_failed_quarantine_unconfirmed") from None
                    raise
                require(isinstance(execution, dict) and type(execution.get("exit_code")) is int,
                        "dispatch_execution_unverified")
                write_report(self.bundle.control / ("dispatch-observation." + segment + ".json"), execution)
                result = reconcile_execution(body, bundle_digest, grant, grant_digest, execution)
                result["cohort_admission"] = admission
                write_report(self.bundle.control / ("dispatch-receipt." + segment + ".json"), result)
                allocation.reconcile(result)
                return result


def reconcile_execution(body, bundle_digest, grant, grant_digest, execution):
    """Reconcile actual terminal/checkpoint evidence; shared by launch and ledger replay."""
    require(isinstance(execution, dict) and type(execution.get("exit_code")) is int, "dispatch_execution_unverified")
    segment = body["paused_invocation"]["record"]["segment_id"]
    history = copy.deepcopy(body["usage_history"])
    terminal, checkpoint_view = execution.get("terminal"), execution.get("checkpoint")
    require(isinstance(checkpoint_view, dict), "dispatch_checkpoint_missing_claim_retained")
    usage = copy.deepcopy(checkpoint_view["snapshot"])
    usage["checkpoint"] = dict(version=1, sequence=checkpoint_view["sequence"], failed=False)
    if terminal is not None:
        require(isinstance(terminal, dict) and terminal.get("session_id") == history["session_id"]
                and terminal.get("exit_code") == execution["exit_code"], "dispatch_terminal_mismatch")
        receipt = copy.deepcopy(terminal.get("usage"))
        require(isinstance(receipt, dict), "dispatch_terminal_usage_missing")
        expected = copy.deepcopy(usage)
        require(type(receipt.get("elapsed_ms")) is int and type(expected.get("elapsed_ms")) is int
                and receipt["elapsed_ms"] >= expected["elapsed_ms"], "dispatch_elapsed_unverified")
        for item in (receipt, expected):
            item.pop("elapsed_ms", None)
            for route in item.get("routes", []):
                if route.get("effort") == "": route["effort"] = "unknown"
        require(receipt == expected, "dispatch_terminal_checkpoint_mismatch")
    new_segment = usage["call_ledger"]["segment_id"]
    require(new_segment == checkpoint_view["segment_id"] and new_segment not in history["segment_ids"],
            "dispatch_usage_segment_replayed")
    history["segment_ids"].append(new_segment)
    history["segments"].append(dict(ordinal=len(history["segments"]) + 1, session_id=history["session_id"], usage=usage))
    accounting = usage_history(history, history["session_id"])
    cost = usage["cost"]
    require(cost["price_card"] == grant["price_card"] and cost["limit_mode"] == "pre_dispatch_reservation",
            "dispatch_cost_binding_changed")
    actual_limits = usage.get("limits") or {}
    wanted = grant["limits"]
    require(all(actual_limits.get(k, 0) == wanted[k] for k in
                ("max_provider_calls", "max_total_tokens", "max_output_tokens_per_call"))
            and actual_limits.get("reserve_cost_before_dispatch") is True
            and Decimal(actual_limits.get("max_estimated_cost", "0")) == Decimal(wanted["max_estimated_cost"]),
            "dispatch_limits_changed")
    with localcontext() as ctx:
        ctx.prec = 90
        reservation = cost["reservation"]
        exposure = sum(Decimal(v) for v in (cost["known_estimated_amount"], reservation["retained_amount"], reservation["outstanding_amount"]))
        require(exposure <= Decimal(grant["limits"]["max_estimated_cost"])
                and Decimal(reservation["peak_exposure_amount"]) <= Decimal(grant["limits"]["max_estimated_cost"]),
                "dispatch_cost_exceeds_grant")
    complete = bool(terminal is not None and accounting["coverage_complete"] and checkpoint_view.get("admission_closed") is True
                    and checkpoint_view.get("phase") == "sealed" and checkpoint_view.get("accounting_complete") is True)
    if terminal is None: accounting["coverage_complete"] = False
    result = dict(version=1, scope="benchmark-cold-dispatch", bundle_sha256=bundle_digest,
                  source_segment_id=segment, grant_sha256=grant_digest, execution=execution,
                  usage_history=history, accounting=accounting, accounting_complete=complete,
                  automatic_paid_resume=False, status="completed" if complete and execution["exit_code"] == 0
                  and terminal and terminal.get("status") == "completed" else "stopped")
    return result
