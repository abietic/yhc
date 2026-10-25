"""Observe live continuity; never equate retained files with restored services.

Docker inventory is a deliberately version-bound Harbor 0.22 seam. All Docker
operations here are read-only. This module never starts, restores or deletes an
environment, executes task code on the host, or authorizes a provider request.
"""

import argparse
import asyncio
import hashlib
import importlib.metadata
import json
import os
import re
import subprocess
import stat
from datetime import datetime, timezone
from pathlib import Path


class RecoveryBoundaryError(ValueError):
    """The next invocation cannot be proved to use the original live runtime."""


def _now():
    return datetime.now(timezone.utc).isoformat()


def _name(value):
    return isinstance(value, str) and re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", value) is not None


def runtime_inventory(project, services, inspected):
    """Project a complete Compose inventory into bounded, non-secret evidence."""
    if (not _name(project) or not isinstance(services, list) or not services
            or len(services) > 128 or any(not _name(s) for s in services)
            or len(set(services)) != len(services)
            or not isinstance(inspected, list) or not inspected or len(inspected) > 256):
        raise RecoveryBoundaryError("invalid_runtime_inventory")
    rows = []
    for raw in inspected:
        labels = raw.get("Config", {}).get("Labels") or {}
        service = labels.get("com.docker.compose.service")
        state = raw.get("State") or {}
        health = state.get("Health", {}).get("Status", "not_configured")
        if health not in ("not_configured", "healthy", "starting", "unhealthy"):
            health = "unknown"
        container_id, image = raw.get("Id"), raw.get("Image")
        if (labels.get("com.docker.compose.project") != project or service not in services
                or not isinstance(container_id, str) or not re.fullmatch(r"[0-9a-f]{64}", container_id)
                or not isinstance(image, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", image)
                or type(state.get("Pid")) is not int or state["Pid"] < 0
                or type(raw.get("RestartCount")) is not int or raw["RestartCount"] < 0
                or not isinstance(state.get("StartedAt"), str)
                or len(state["StartedAt"]) > 64):
            raise RecoveryBoundaryError("runtime_identity_or_ownership_mismatch")
        rows.append({"service": service, "container_id": container_id, "image": image,
                     "started_at": state["StartedAt"], "pid": state["Pid"],
                     "restart_count": raw["RestartCount"],
                     "running": state.get("Running") is True,
                     "paused": state.get("Paused") is True,
                     "restarting": state.get("Restarting") is True,
                     "health_status": health})
    if len({r["container_id"] for r in rows}) != len(rows):
        raise RecoveryBoundaryError("duplicate_runtime_identity")
    if set(services) != {r["service"] for r in rows}:
        raise RecoveryBoundaryError("declared_service_missing")
    return {"version": 1, "observed_at": _now(), "inventory_complete": True,
            "inventory_scope": "compose_declared_services",
            "project": project, "services": sorted(services),
            "containers": sorted(rows, key=lambda r: (r["service"], r["container_id"]))}


def same_live_runtime(before, after):
    """A point-in-time generation check, not a service-memory/clock checkpoint."""
    if not isinstance(before, dict) or not isinstance(after, dict):
        return False
    if before.get("inventory_complete") is not True or after.get("inventory_complete") is not True:
        return False
    if any(before.get(k) != after.get(k) for k in ("project", "services", "containers")):
        return False
    rows = after.get("containers")
    return (isinstance(rows, list) and bool(rows)
            and all(r.get("running") is True and r.get("pid", 0) > 0
                    and not r.get("paused") and not r.get("restarting")
                    and r.get("health_status") in ("not_configured", "healthy") for r in rows))


def _docker_inspect(ids):
    if not ids or len(ids) > 256 or any(not re.fullmatch(r"[0-9a-f]{12,64}", i) for i in ids):
        raise RecoveryBoundaryError("invalid_container_handles")
    completed = subprocess.run(["docker", "inspect", *ids], check=True, capture_output=True,
                               text=True, timeout=30)
    # Never persist Docker's Config.Env, mount sources, arbitrary labels, or logs.
    return json.loads(completed.stdout)


async def observe_environment(environment):
    """Return unavailable on unsupported backends; never guess sidecar coverage."""
    try:
        from harbor.environments.docker.docker import DockerEnvironment, _sanitize_docker_compose_project_name
        if importlib.metadata.version("harbor") != "0.22.0" or not isinstance(environment, DockerEnvironment):
            return {"version": 1, "observed_at": _now(), "inventory_complete": False,
                    "reason": "unsupported_environment_inventory"}
        async def observe():
            services = await environment._run_docker_compose_command(
                ["config", "--services"], check=True, timeout_sec=5)
            handles = await environment._run_docker_compose_command(
                ["ps", "--all", "--quiet"], check=True, timeout_sec=5)
            inspected = await asyncio.to_thread(_docker_inspect, (handles.stdout or "").split())
            return runtime_inventory(_sanitize_docker_compose_project_name(environment.session_id),
                                     (services.stdout or "").split(), inspected)
        return await asyncio.wait_for(observe(), timeout=5)
    except Exception:
        # The reason is finite. Exceptions may contain task paths or Docker env.
        return {"version": 1, "observed_at": _now(), "inventory_complete": False,
                "reason": "runtime_inventory_unavailable"}


def continuation_report(before, after, *, session_identified, usage_complete, grant_available):
    continuous = same_live_runtime(before, after)
    reasons = []
    if not continuous:
        reasons.append("live_runtime_continuity_unverified")
    if not session_identified:
        reasons.append("native_session_unidentified")
    if not usage_complete:
        reasons.append("usage_incomplete_or_unknown")
    if not grant_available:
        reasons.append("no_remaining_continuation_grant")
    return {"version": 1, "observed_at": _now(),
            "scope": "same_live_environment_only", "live_runtime_continuity": continuous,
            "warm_continuation_eligible_at_observation": not reasons,
            "cold_resume_supported": False, "blocking_reasons": reasons,
            "cold_resume_blockers": ["workspace_restore_not_verified", "native_session_load_not_verified",
                                     "service_memory_not_checkpointed", "clock_continuity_not_verified",
                                     "external_side_effects_not_reconciled"],
            "runtime_before": before, "runtime_after": after}


def retained_checkpoint_report(manifest, *, archive_verified=False, current=None):
    """Assess old/new capture manifests without upgrading their restoration claim.

    A legacy capture='complete' means that capture finished. Even a matching
    archive digest does not prove a native session loads or sidecar RAM restores.
    """
    handles = manifest.get("environment_handles") or []
    if not isinstance(handles, list) or len(handles) > 256:
        raise RecoveryBoundaryError("invalid_checkpoint_handles")
    services = []
    for handle in handles:
        service = handle.get("service")
        if not _name(service):
            raise RecoveryBoundaryError("invalid_checkpoint_service")
        live = next((r for r in (current or {}).get("containers", [])
                     if r["container_id"] == handle.get("id") and r["service"] == service), None)
        services.append({"service": service,
                         "process_memory": "lost_on_stop" if live and not live["running"] else "not_checkpointed",
                         "clock": "not_checkpointed", "cold_restore": "unsupported"})
    return {"version": 1, "observed_at": _now(), "capture_status": manifest.get("capture", "unknown"),
            "archive_digest_verified": archive_verified, "can_resume": False,
            "cold_resume_supported": False,
            "native_session": "retained_not_load_verified" if archive_verified and manifest.get("native_session_files") else "unverified",
            "workspace": "archived_not_restore_verified" if archive_verified and manifest.get("workspace_in_archive") is True else "unverified",
            "service_inventory_complete": False, "services": services, "current_runtime": current,
            "blocking_reasons": ["no_verified_cold_restore_entrypoint", "native_session_load_not_verified",
                                 "complete_service_state_and_clock_not_restored",
                                 "usage_and_new_dispatch_authorization_require_separate_checks"]}


def write_report(path, report):
    """Require a private host-owned parent; caller must exclude task mounts."""
    if os.name != "posix":
        raise RecoveryBoundaryError("private_report_output_requires_posix_host")
    path = Path(path).absolute()
    parent = path.parent
    if parent.resolve() != parent:
        raise RecoveryBoundaryError("report_parent_must_not_traverse_symlinks")
    info = parent.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise RecoveryBoundaryError("report_parent_must_be_private_and_host_owned")
    # Refuse links and existing files: evidence must not be silently overwritten.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as file:
        json.dump(report, file, indent=2, sort_keys=True)
        file.write("\n")


def main():
    parser = argparse.ArgumentParser(description="Assess retained checkpoint evidence; never resumes execution")
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--inspect-docker", action="store_true", help="Read only the manifest's owned Docker handles")
    args = parser.parse_args()
    if args.manifest.stat().st_size > 8 << 20:
        raise RecoveryBoundaryError("checkpoint_manifest_too_large")
    manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    archive = args.manifest.parent / "native-checkpoint.tar.gz"
    verified = False
    if archive.is_file() and manifest.get("checkpoint_sha256"):
        digest = hashlib.sha256()
        with archive.open("rb") as file:
            for block in iter(lambda: file.read(1024 * 1024), b""):
                digest.update(block)
        verified = digest.hexdigest() == manifest["checkpoint_sha256"]
    current = None
    if args.inspect_docker:
        handles = manifest.get("environment_handles") or []
        projects = {r.get("project") for r in handles}
        if len(projects) != 1:
            raise RecoveryBoundaryError("checkpoint_project_ambiguous")
        # Handles are captured identities, not proof of complete service coverage.
        current = runtime_inventory(projects.pop(), sorted({r["service"] for r in handles}),
                                    _docker_inspect([r["id"] for r in handles]))
        current["inventory_complete"] = False
    write_report(args.output, retained_checkpoint_report(manifest, archive_verified=verified, current=current))


if __name__ == "__main__":
    main()
