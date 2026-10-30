"""Explicit operator CLI for cooperative service snapshots, never model dispatch."""

import argparse
import os
import selectors
import signal
import subprocess
import time
from pathlib import Path

from scripts.terminal_bench.recovery import runtime_inventory, write_report
from scripts.terminal_bench.service_snapshot import (
    MAX_BYTES, ServiceSnapshots, SnapshotError, decode, digest, encode, identity, private_json, require, validate_contract,
)

COHORT_LABEL = "io.yhc.checkpoint-cohort"
ATTEMPT_LABEL = "io.yhc.restore-attempt"
ROLE_LABEL = "io.yhc.checkpoint-role"


def bounded_command(argv, payload=b"", timeout=20):
    """Bound both directions and reap only our own CLI process group on failure."""
    require(os.name == "posix" and len(payload) <= MAX_BYTES, "unsupported_rpc_host_or_input")
    env = {k: v for k, v in os.environ.items() if not any(word in k.upper() for word in (
        "API_KEY", "AUTH", "TOKEN", "SECRET", "PASSWORD", "PRIVATE_KEY", "ACCESS_KEY"))}
    process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               start_new_session=True, env=env)
    outputs = {"stdout": bytearray(), "stderr": bytearray()}
    deadline = time.monotonic() + timeout
    succeeded = False
    try:
        with selectors.DefaultSelector() as selector:
            for stream, kind in ((process.stdout, "stdout"), (process.stderr, "stderr")):
                os.set_blocking(stream.fileno(), False)
                selector.register(stream, selectors.EVENT_READ, kind)
            if payload:
                os.set_blocking(process.stdin.fileno(), False)
                selector.register(process.stdin, selectors.EVENT_WRITE, "stdin")
            else:
                process.stdin.close()
            offset = 0
            while selector.get_map():
                remaining = deadline - time.monotonic()
                require(remaining > 0, "service_rpc_timeout")
                for event, _mask in selector.select(min(remaining, 0.1)):
                    stream, kind = event.fileobj, event.data
                    if kind == "stdin":
                        try:
                            offset += os.write(stream.fileno(), payload[offset:offset + 65536])
                        except BrokenPipeError:
                            raise SnapshotError("service_rpc_input_closed") from None
                        if offset == len(payload):
                            selector.unregister(stream)
                            stream.close()
                        continue
                    block = os.read(stream.fileno(), 65536)
                    if not block:
                        selector.unregister(stream)
                        stream.close()
                    else:
                        outputs[kind].extend(block)
                        limit = MAX_BYTES if kind == "stdout" else 8192
                        require(len(outputs[kind]) <= limit, "service_rpc_output_limit")
        require(process.wait(timeout=max(0.01, deadline - time.monotonic())) == 0, "service_rpc_process_failed")
        succeeded = True
        return bytes(outputs["stdout"])
    except subprocess.TimeoutExpired:
        raise SnapshotError("service_rpc_timeout") from None
    finally:
        if not succeeded:
            # The CLI leader can exit while descendants keep its pipes open.
            # Timeout must still fence the owned group in that case.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        if process.poll() is None:
            process.wait(timeout=5)
        for stream in (process.stdin, process.stdout, process.stderr):
            stream.close()


class DockerServices:
    """Opt-in immutable service images, owned storage and isolated networking.

    The normalized Compose definition and service contract are private host
    inputs. We inspect/exec already provisioned containers; never pull or build
    images, evaluate Compose includes, start a task or execute its code on host.
    """

    def __init__(self, project, definition, contract):
        require(isinstance(project, str) and project and project.isascii()
                and len(project) <= 128 and project[0].isalnum()
                and all(c.isalnum() or c in "_-" for c in project), "invalid_project")
        self.project = project
        self.contract = decode(encode(validate_contract(contract)))
        self.context = bounded_command(["docker", "context", "show"]).decode().strip()
        require(self.context and len(self.context) <= 128, "invalid_docker_context")
        self.daemon_id = self.docker("info", "--format", "{{.ID}}").decode().strip()
        require(self.daemon_id and len(self.daemon_id) <= 256, "unidentified_docker_daemon")
        config = private_json(definition)
        require(isinstance(config, dict) and isinstance(config.get("services"), dict)
                and set(config["services"]) == set(contract["services"]), "definition_service_set_mismatch")
        self.services = sorted(config["services"])
        self.handles = {}
        self.attempt = None

    def docker(self, *argv):
        return bounded_command(["docker", "--context", self.context, *argv])

    def _ids(self, label):
        ids = self.docker("ps", "--all", "--quiet", "--filter", "label=" + label).decode().split()
        require(len(ids) <= 64 and all(len(i) in (12, 64) and all(c in "0123456789abcdef" for c in i) for i in ids), "invalid_docker_handles")
        return ids

    def inventory(self, cohort, attempt=None):
        require(identity(cohort) and (attempt is None or identity(attempt)), "invalid_runtime_binding")
        ids = self._ids("com.docker.compose.project=" + self.project)
        require(bool(ids), "no_owned_service_containers")
        rows = decode(self.docker("inspect", *ids))
        value = runtime_inventory(self.project, self.services, rows)
        volumes = set()
        for row in rows:
            labels = row.get("Config", {}).get("Labels") or {}
            require(labels.get(COHORT_LABEL) == cohort and (attempt is None or labels.get(ATTEMPT_LABEL) == attempt), "service_cohort_or_attempt_mismatch")
            require(labels.get(ROLE_LABEL) == "owned-service", "agent_or_unclassified_container_not_supported")
            profile = row.get("HostConfig") or {}
            require(profile.get("ReadonlyRootfs") is True and not profile.get("Privileged")
                    and not profile.get("PortBindings") and "ALL" in (profile.get("CapDrop") or [])
                    and profile.get("PidMode", "") == "" and profile.get("IpcMode", "") in ("", "private"), "unsupported_service_security_profile")
            require(profile.get("NetworkMode") != "host", "external_service_network")
            for network in (row.get("NetworkSettings", {}).get("Networks") or {}).values():
                network_id = network.get("NetworkID")
                if profile.get("NetworkMode") == "none":
                    continue
                inspected = decode(self.docker("network", "inspect", network_id))[0]
                require(inspected.get("Internal") is True
                        and (inspected.get("Labels") or {}).get("com.docker.compose.project") == self.project, "external_service_network")
            for mount in row.get("Mounts") or []:
                require(mount.get("Type") in ("volume", "tmpfs"), "host_bind_state_unsupported")
                destination = mount.get("Destination", "")
                for arg in self.contract["services"][labels["com.docker.compose.service"]]["command"]:
                    if arg.startswith("/"):
                        require(not (arg == destination or arg.startswith(destination.rstrip("/") + "/")), "checkpoint_hook_not_immutable")
                if mount.get("Type") == "volume":
                    name = mount.get("Name")
                    require(isinstance(name, str) and name and name not in volumes, "shared_service_volume_unsupported")
                    volume = decode(self.docker("volume", "inspect", name))[0]
                    require((volume.get("Labels") or {}).get("com.docker.compose.project") == self.project, "service_volume_not_owned")
                    volumes.add(name)
        self.handles = {r["service"]: r["container_id"] for r in value["containers"]}
        self.attempt = attempt
        value["owned_volumes"] = sorted(volumes)
        value["daemon_id"] = self.daemon_id
        return value

    def rpc(self, service, command, request):
        require(service in self.handles, "service_inventory_required_before_rpc")
        # Explicit '--' and argv prevent hooks/arguments from becoming host shell
        # syntax or Docker options. Only JSON is sent on stdin.
        raw = bounded_command(["docker", "--context", self.context, "exec", "--interactive", "--", self.handles[service], *command], encode(request))
        return decode(raw)

    def assert_source_absent(self, manifest, target):
        ids = self._ids(COHORT_LABEL + "=" + manifest["cohort_id"])
        rows = decode(self.docker("inspect", *ids)) if ids else []
        require({r["Id"] for r in rows} == {r["container_id"] for r in target["containers"]}, "source_or_other_cohort_instance_still_exists")

    def quarantine(self, target):
        require(self.attempt is not None and target["project"] == self.project, "quarantine_scope_unverified")
        ids = [r["container_id"] for r in target["containers"]]
        rows = decode(self.docker("inspect", *ids))
        require(all((r.get("Config", {}).get("Labels") or {}).get(ATTEMPT_LABEL) == self.attempt
                    and (r.get("Config", {}).get("Labels") or {}).get("com.docker.compose.project") == self.project for r in rows), "quarantine_owner_changed")
        self.docker("stop", "--time", "1", "--", *ids)


def main():
    parser = argparse.ArgumentParser(description="Cooperative service state/clock cold recovery; no agent/model dispatch")
    parser.add_argument("operation", choices=("capture", "restore"))
    parser.add_argument("--contract", type=Path, required=True)
    parser.add_argument("--definition", type=Path, required=True, help="Private normalized Compose JSON; never executed")
    parser.add_argument("--project", required=True)
    parser.add_argument("--snapshot", type=Path, required=True)
    parser.add_argument("--control-dir", type=Path, required=True, help="Trusted private registry outside the capsule")
    parser.add_argument("--snapshot-sha256", help="Capture digest retained by the operator, not read from the capsule")
    parser.add_argument("--cohort-id")
    parser.add_argument("--attempt-id")
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--activate", action="store_true")
    args = parser.parse_args()
    try:
        contract = private_json(args.contract)
        backend = DockerServices(args.project, args.definition, contract)
        coordinator = ServiceSnapshots(contract, backend, control_dir=args.control_dir)
        if args.operation == "capture":
            require(not args.activate and args.receipt is None, "invalid_capture_options")
            manifest = coordinator.capture(args.snapshot, args.cohort_id)
            print(encode({"status": "service_snapshot_complete", "snapshot_sha256": digest(manifest), "can_resume_agent": False}).decode())
        else:
            require(args.receipt is not None, "restore_receipt_required")
            # Refuse a bad output destination before any service mutation.
            require(not args.receipt.exists() and args.receipt.parent.resolve() == args.receipt.parent.absolute(), "invalid_receipt_path")
            info = args.receipt.parent.stat()
            require(info.st_uid == os.geteuid() and not info.st_mode & 0o077, "receipt_parent_not_private")
            write_report(args.receipt, dict(status="restore_in_progress", can_resume_agent=False))
            try:
                result = coordinator.restore(args.snapshot, args.attempt_id,
                                             expected_snapshot_sha256=args.snapshot_sha256, activate=args.activate)
                pending = args.receipt.with_name(args.receipt.name + ".complete")
                write_report(pending, result)
                os.replace(pending, args.receipt)
                directory = os.open(args.receipt.parent, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
            except BaseException:
                # Retain an explicit incomplete receipt and any activation claim.
                # No subsequent process may treat it as a successful release.
                raise
            print(encode({"status": result["status"], "can_resume_agent": False}).decode())
    except SnapshotError as exc:
        parser.exit(1, "cold_service_recovery_failed_closed: " + exc.code + "\n")
    except (ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError):
        # State and hook stderr may contain private application data. Fail closed
        # without echoing them; Python API exceptions retain finite local codes.
        parser.exit(1, "cold_service_recovery_failed_closed; inspect private configuration and service state\n")


if __name__ == "__main__":
    main()
