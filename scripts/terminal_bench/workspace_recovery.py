"""Owned idle-workspace transport and cumulative invocation history.

No task command, archive member, executable or model result runs on the host.
The container image holds both the file helper and provider-free YHC binary.
"""

import re
import uuid
from pathlib import PurePosixPath

from scripts.terminal_bench.cold_recovery import bounded_command
from scripts.terminal_bench.service_snapshot import decode, encode, identity, require
from scripts.terminal_bench.usage import sum_usage, validated_usage
from scripts.terminal_bench.workspace_io import PROTOCOL

WORKSPACE_COHORT = "io.yhc.workspace-cohort"
WORKSPACE_ROLE = "io.yhc.workspace-role"
ATTEMPT = "io.yhc.restore-attempt"


def usage_history(value, session_id):
    require(isinstance(value, dict) and set(value) == {"version", "session_id", "segment_ids", "segments"}
            and type(value["version"]) is int and value["version"] == 1
            and value["session_id"] == session_id, "usage_history_identity_mismatch")
    rows = value["segments"]
    require(isinstance(rows, list) and 0 < len(rows) <= 17, "usage_history_missing_segments")
    expected_ids = value["segment_ids"]
    require(isinstance(expected_ids, list) and len(expected_ids) == len(rows)
            and all(identity(value) for value in expected_ids) and len(set(expected_ids)) == len(expected_ids), "usage_history_missing_segments")
    usages, call_ids = [], set()
    for ordinal, row in enumerate(rows, 1):
        require(isinstance(row, dict) and set(row) == {"ordinal", "session_id", "usage"}
                and type(row["ordinal"]) is int and row["ordinal"] == ordinal
                and row["session_id"] == session_id, "usage_segment_gap_or_identity_mismatch")
        usage = validated_usage(row["usage"])
        require(usage is not None, "invalid_usage_history")
        require("call_ledger" in usage and "call_ledgers" not in usage
                and usage["call_ledger"]["segment_id"] == expected_ids[ordinal - 1], "usage_invocation_ledger_missing_or_reordered")
        ledgers = [usage["call_ledger"]] if "call_ledger" in usage else usage.get("call_ledgers", [])
        for ledger in ledgers:
            for record in ledger["records"]:
                require(record["call_id"] not in call_ids, "usage_call_identity_repeated")
                call_ids.add(record["call_id"])
        usages.append(usage)
    total = sum_usage(usages)
    require(total is not None, "usage_invocation_identity_repeated_or_unreconciled")
    complete = total["complete"] and total.get("call_ledger_complete") is True and total.get("cost_complete") is True
    costs = [total["cost"]] if "cost" in total else [segment["cost"] for segment in total.get("cost_segments", [])]
    complete = bool(complete and costs and all(
        isinstance(cost.get("reservation"), dict)
        and cost["reservation"].get("outstanding_amount") == cost["reservation"].get("retained_amount") == "0.000000000000"
        and cost["reservation"].get("bound_violations") == 0 for cost in costs))
    # These are historical estimates in their original settlement currencies.
    # The control plane does not reset them or infer another spending allowance.
    return dict(totals=total, coverage_complete=complete, new_spending_authorized=False)


def native_proof(value, session_id, cwd):
    require(isinstance(value, dict) and type(value.get("version")) is int and value["version"] == 1
            and value.get("scope") == "canonical_administration_restore"
            and value.get("session_id") == session_id and value.get("cwd") == cwd
            and isinstance(value.get("thread_id"), str) and bool(value["thread_id"])
            and type(value.get("message_count")) is int and value["message_count"] > 0
            and re.fullmatch(r"[0-9a-f]{64}", value.get("messages_sha256", "")) is not None
            and identity(value.get("process_nonce")) and value.get("can_resume_agent") is False,
            "invalid_native_restore_proof")
    require(value.get("validation_complete") is True and value.get("blocking_reasons") == [],
            "native_restore_state_or_usage_incomplete")
    usage = value.get("native_usage")
    require(isinstance(usage, dict) and type(usage.get("version")) is int and usage["version"] == 1,
            "native_usage_unverified")
    for key in ("prompt_tokens", "completion_tokens", "total_tokens", "responses_with_metadata",
                "responses_without_metadata", "last_prompt_tokens", "last_completion_tokens", "last_total_tokens",
                "current_context_prompt_tokens"):
        require(type(usage.get(key)) is int and usage[key] >= 0, "native_usage_unverified")
    require(not any(usage.get(key, 0) for key in ("unsupported_snapshot_version", "responses_without_metadata",
                                               "legacy_boundaries_without_usage"))
            and usage.get("current_context_usage_known") is True and usage.get("last_response_had_usage_metadata") is True
            and usage["total_tokens"] >= usage["prompt_tokens"] + usage["completion_tokens"], "native_usage_unverified")
    # The nonce is evidence of a fresh loader process, never part of semantic
    # equality. Native transcript accounting and invocation accounting have
    # different coverage; do not force their token totals to be equal.
    return {key: value[key] for key in ("version", "scope", "session_id", "thread_id", "cwd",
                                       "message_count", "messages_sha256", "native_usage")}


class DockerWorkspace:
    """One passive keeper with owned local storage; no running Agent to capture."""

    def __init__(self, container_id, cwd, cohort, attempt=None):
        require(isinstance(container_id, str) and re.fullmatch(r"[0-9a-f]{64}", container_id), "invalid_workspace_handle")
        require(isinstance(cwd, str) and len(cwd) <= 512 and cwd.startswith("/") and cwd != "/"
                and PurePosixPath(cwd).as_posix() == cwd and ".." not in PurePosixPath(cwd).parts, "invalid_workspace_root")
        require(identity(cohort) and (attempt is None or identity(attempt)), "invalid_workspace_epoch")
        self.container, self.cwd, self.cohort, self.attempt = container_id, cwd, cohort, attempt
        self.context = bounded_command(["docker", "context", "show"]).decode().strip()
        require(bool(self.context) and len(self.context) <= 128, "invalid_docker_context")
        self.daemon = self.docker("info", "--format", "{{.ID}}").decode().strip()
        require(bool(self.daemon) and len(self.daemon) <= 256, "unidentified_docker_daemon")

    def docker(self, *args):
        return bounded_command(["docker", "--context", self.context, *args])

    def inventory(self):
        row = decode(self.docker("inspect", self.container))[0]
        labels = row["Config"].get("Labels") or {}
        profile, state = row["HostConfig"], row["State"]
        require(labels.get(WORKSPACE_COHORT) == self.cohort and labels.get(WORKSPACE_ROLE) == "idle-native-workspace"
                and (self.attempt is None or labels.get(ATTEMPT) == self.attempt), "workspace_owner_mismatch")
        require(row["Config"].get("Entrypoint") == ["/bin/sleep"] and row["Config"].get("Cmd") == ["infinity"]
                and profile.get("ReadonlyRootfs") is True and not profile.get("Privileged")
                and "ALL" in (profile.get("CapDrop") or []) and profile.get("NetworkMode") == "none"
                and not profile.get("PortBindings") and profile.get("PidMode", "") == ""
                and profile.get("IpcMode", "") in ("", "private"), "workspace_not_isolated_passive_keeper")
        require(state.get("Running") is True and not state.get("Paused") and not state.get("Restarting")
                and (state.get("Health") or {}).get("Status", "healthy") == "healthy", "workspace_keeper_not_live")
        top = [line.strip() for line in self.docker("top", self.container, "-eo", "pid").decode().splitlines() if line.strip()]
        require(top == ["PID", str(state["Pid"])], "workspace_has_uncheckpointed_processes")
        mounts = row.get("Mounts") or []
        for mount in mounts:
            destination = mount.get("Destination", "")
            require(destination and all(not (binary == destination or binary.startswith(destination.rstrip("/") + "/"))
                    for binary in ("/opt/checkpoint/workspace_io.py", "/opt/yhc/yhc", "/usr/local/bin/python3")),
                    "workspace_control_executable_not_immutable")
        volumes = [m for m in mounts if m.get("Type") == "volume"]
        require(len(volumes) == 1 and volumes[0].get("Destination") == self.cwd
                and all(m.get("Type") in ("volume", "tmpfs") for m in mounts), "workspace_mount_coverage_incomplete")
        volume = volumes[0]["Name"]
        info = decode(self.docker("volume", "inspect", volume))[0]
        require(info.get("Driver") == "local" and not info.get("Options")
                and (info.get("Labels") or {}).get(WORKSPACE_COHORT) == self.cohort, "workspace_volume_not_owned_local_storage")
        consumers = self.docker("ps", "--all", "--quiet", "--no-trunc", "--filter", "volume=" + volume).decode().split()
        require(consumers == [self.container], "workspace_volume_shared")
        user = row["Config"].get("User") or "0:0"
        require(re.fullmatch(r"[0-9]+(?::[0-9]+)?", user) is not None, "workspace_user_unresolved")
        return dict(container_id=self.container, image=row["Image"], daemon_id=self.daemon, volume=volume,
                    cwd=self.cwd, user=user, pid=state["Pid"], started_at=state["StartedAt"], restart_count=row["RestartCount"],
                    cohort_id=self.cohort, attempt_id=self.attempt)

    def rpc(self, operation, **body):
        request = dict(protocol=PROTOCOL, request_id=str(uuid.uuid4()), root=self.cwd, operation=operation, **body)
        reply = decode(bounded_command(["docker", "--context", self.context, "exec", "--interactive", "--", self.container,
                                        "/usr/local/bin/python3", "-I", "/opt/checkpoint/workspace_io.py"], encode(request)))
        require(isinstance(reply, dict) and reply.get("ok") is True and reply.get("protocol") == PROTOCOL
                and reply.get("request_id") == request["request_id"], "workspace_transport_failed")
        return reply["body"]

    def assert_source_absent(self, source):
        ids = self.docker("ps", "--all", "--quiet", "--no-trunc").decode().split()
        require(source["container_id"] not in ids, "workspace_source_container_still_exists")

    def quarantine(self, expected):
        row = decode(self.docker("inspect", self.container))[0]
        labels = row["Config"].get("Labels") or {}
        require(self.attempt is not None and expected["container_id"] == self.container
                and labels.get(WORKSPACE_COHORT) == self.cohort and labels.get(ATTEMPT) == self.attempt,
                "workspace_quarantine_scope_unverified")
        self.docker("stop", "--time", "1", "--", self.container)
