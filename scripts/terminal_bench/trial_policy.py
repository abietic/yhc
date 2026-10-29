"""Fail-closed evaluator policy; independent of Harbor and provider SDKs."""

from dataclasses import dataclass
from decimal import Decimal, InvalidOperation
import math
from pathlib import Path


class PolicyError(ValueError):
    """The evaluator's isolation contract could not be established."""


@dataclass(frozen=True)
class TrialOutcome:
    ledger_status: str
    reason: str
    # Evaluation closure is never joint runtime recovery authority.
    can_resume_agent: bool = False


def classify_trial(*, accounting, usage_complete, native_completed,
                   checkpoint_complete, deadline_reason, result,
                   isolation_verified, error_type=None, budget_denied=False):
    """Classify host-owned receipts, never model statements about budget.

    ``budget_denied`` must come from an actual admission denial or the native
    usage ledger. A timeout, missing reward or exception is not such evidence.
    Missing/unclosed accounting always keeps reservations and blocks dispatch.
    """
    try:
        outstanding = Decimal(accounting["outstanding"])
        accounting_closed = (accounting["unknown_usage"] is False
                             and accounting.get("accounting_error") is False
                             and outstanding.is_finite() and outstanding == 0)
    except (KeyError, TypeError, ValueError, InvalidOperation):
        accounting_closed = False
    if not accounting_closed or usage_complete is not True:
        return TrialOutcome("unknown_usage", "usage_unverified")
    if checkpoint_complete is not True:
        return TrialOutcome("failed", "checkpoint_unverified")
    if deadline_reason == "deadline_exceeded":
        return TrialOutcome("failed", "agent_deadline")
    if error_type:
        return TrialOutcome("failed", "trial_failed")
    if result is not None and not isinstance(result, dict):
        return TrialOutcome("failed", "trial_unverified")
    exception = (result or {}).get("exception_info")
    if exception and (native_completed is True
                      or exception.get("exception_type") != "NonZeroAgentExitCodeError"):
        return TrialOutcome("failed", "grading_failed" if native_completed is True else "trial_failed")
    if isolation_verified is not True:
        return TrialOutcome("failed", "verifier_isolation_unverified")
    rewards = ((result or {}).get("verifier_result") or {}).get("rewards")
    if (not isinstance(rewards, dict) or not rewards or any(
            isinstance(value, bool) or not isinstance(value, (int, float))
            or not math.isfinite(value) for value in rewards.values())):
        return TrialOutcome("failed", "grading_unverified")
    # Harbor retains the first native exception and can hide a later grading
    # exception. Budget evidence alone must never replace a missing grade.
    if native_completed is not True:
        if budget_denied is True:
            return TrialOutcome("budget_paused", "budget_admission_denied")
        return TrialOutcome("failed", "native_unverified")
    if deadline_reason != "native_completed":
        return TrialOutcome("failed", "native_unverified")
    return TrialOutcome("completed", "evaluated")


def _need(condition, reason):
    if not condition:
        raise PolicyError(reason)


def verify_proof_directory(directory, config):
    """The proof must be outside every container-visible host bind source."""
    directory = Path(directory)
    _need(directory.is_absolute() and not directory.is_symlink()
          and directory.resolve() == directory, "host_owned_verifier_proof_required")
    sources = []
    for service in (config.get("services") or {}).values():
        for mount in service.get("volumes") or []:
            _need(isinstance(mount, dict), "unrendered_mount_unqualified")
            if mount.get("type") == "bind":
                _need(bool(mount.get("source")), "unrendered_mount_unqualified")
                sources.append(Path(mount["source"]).resolve())
    for volume in (config.get("volumes") or {}).values():
        volume = volume or {}
        # External/custom drivers may hide host binds; qualify them separately.
        _need(not volume.get("external") and volume.get("driver", "local") == "local",
              "proof_volume_driver_unqualified")
        options = volume.get("driver_opts") or {}
        if options:
            _need(options.get("type") == "none" and "bind" in options.get("o", "").split(",")
                  and bool(options.get("device")), "proof_volume_driver_unqualified")
            sources.append(Path(options["device"]).resolve())
    _need(all(directory != source and source not in directory.parents for source in sources),
          "verifier_proof_exposed_to_container")


def _mounts(mounts, *, log_dir, compose):
    # Separate verifiers upload copies of artifacts. Only their own log bind is
    # admitted; sockets, workspace mounts, volumes_from and sidecars are not.
    _need(isinstance(mounts, list) and len(mounts) == 1, "verifier_mounts_unverified")
    row = mounts[0]
    source_key, target_key, type_key = (("source", "target", "type") if compose
                                      else ("Source", "Destination", "Type"))
    _need(isinstance(row, dict) and row.get(type_key) == "bind"
          and row.get(source_key) == str(Path(log_dir).resolve())
          and row.get(target_key) == "/logs/verifier", "verifier_mounts_unverified")


def verify_compose(config, *, image, log_dir):
    """Check Docker's rendered Compose, before any container can start."""
    services = config.get("services") or {}
    _need(set(services) == {"main"}, "verifier_topology_unverified")
    row = services["main"]
    _need(row.get("network_mode") == "none" and not row.get("networks"),
          "verifier_network_not_none")
    _need(row.get("image") == image and row.get("pull_policy") == "never"
          and not row.get("build"), "verifier_image_unverified")
    for key in ("privileged", "cap_add", "devices", "volumes_from", "pid", "ipc",
                "uts", "use_api_socket", "provider", "post_start", "pre_stop",
                "develop", "external_links", "ports"):
        _need(not row.get(key), "verifier_privilege_unverified")
    _mounts(row.get("volumes"), log_dir=log_dir, compose=True)


def verify_container(row, *, image, log_dir):
    """Check the running container, before Harbor uploads model artifacts."""
    host = row.get("HostConfig") or {}
    _need(host.get("NetworkMode") == "none"
          and set((row.get("NetworkSettings") or {}).get("Networks") or {}) <= {"none"},
          "verifier_network_not_none")
    _need(row.get("Image") == image and (row.get("State") or {}).get("Running") is True
          and bool(row.get("Id")), "verifier_runtime_unverified")
    _need(not host.get("Privileged") and not host.get("CapAdd") and not host.get("Devices")
          and not host.get("VolumesFrom") and not host.get("PidMode")
          and host.get("IpcMode", "private") in ("", "private")
          and not host.get("UTSMode"), "verifier_privilege_unverified")
    _mounts(row.get("Mounts"), log_dir=log_dir, compose=False)
