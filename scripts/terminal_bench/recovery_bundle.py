"""Bind service, workspace, actual native-load and cumulative-usage evidence.

Capture/restore never dispatch. Explicit dispatch needs a separate finite grant.
Old file captures are not upgraded into this versioned cooperative protocol.
"""

import argparse
import base64
import hashlib
import json
import os
import stat
from pathlib import Path

from scripts.terminal_bench.cold_recovery import DockerServices
from scripts.terminal_bench.recovery import same_live_runtime, write_report
from scripts.terminal_bench.service_snapshot import (
    MAX_BYTES, ServiceSnapshots, SnapshotError, digest, identity, private_json, require, seal_capsule,
)
from scripts.terminal_bench.workspace_io import MAX_FILE, validate_entries
from scripts.terminal_bench.workspace_recovery import DockerWorkspace, native_proof, usage_history
from scripts.terminal_bench.paused_recovery import cold_tool_handoff, verify_paused_joint

PROTOCOL = "yhc-cold-recovery-bundle-v1"
PAUSED_PROTOCOL = "yhc-cold-recovery-bundle-v2"


def file_digest(raw):
    return hashlib.sha256(raw).hexdigest()


def user_files(entries):
    # Native administration may commit its own canonical state. User files,
    # including Git state and executable bits, must remain exactly unchanged.
    return [e for e in entries if e["path"] != "." and e["path"] != ".yhc" and not e["path"].startswith(".yhc/")]


class RecoveryBundle:
    def __init__(self, services, workspace):
        self.services, self.workspace = services, workspace
        self.control = services.control_dir

    def _record(self, prefix, root_digest):
        self.services._anchor(root_digest)  # Shared strict digest syntax.
        return self.control / (prefix + "." + root_digest + ".json")

    def _paused(self, pause, proof, history):
        record = pause["record"]
        checkpoint = self.workspace.rpc("usage_probe", checkpoint_path=record["checkpoint_path"], segment_id=record["segment_id"])
        joint = verify_paused_joint(record, pause["record_sha256"], proof, checkpoint, history, self.workspace.cwd)
        return dict(record=record, record_sha256=pause["record_sha256"], checkpoint=checkpoint, joint_proof=joint)

    def capture(self, path, service_path, service_digest, session_id, history, *, pause_record=None, pause_sha256=None):
        path = Path(path).absolute()
        self.services._outside_capsule(path)
        require(path.parent.resolve() == path.parent and path.parent.stat().st_uid == os.geteuid()
                and not path.parent.stat().st_mode & 0o077, "bundle_parent_not_private")
        service = self.services.verify_quiescent_source(service_path, service_digest)
        before = self.workspace.inventory()
        require(before["cohort_id"] == service["cohort_id"]
                and before["daemon_id"] == service["source"]["daemon_id"], "bundle_source_cohort_mismatch")
        accounting = usage_history(history, session_id)
        pre_native = validate_entries(self.workspace.rpc("inventory"))
        native_path = ".yhc/transcripts/" + session_id + ".jsonl"
        require(any(e["path"] == native_path and e["kind"] == "file" for e in pre_native), "canonical_native_transcript_not_captured")
        proof = self.workspace.rpc("native_probe", session_id=session_id)
        pause = None
        if pause_record is not None or pause_sha256 is not None:
            require(isinstance(pause_record, dict) and pause_record.get("session_id") == session_id
                    and digest(pause_record) == pause_sha256, "paused_record_anchor_or_identity_mismatch")
            pause = self._paused(dict(record=pause_record, record_sha256=pause_sha256), proof, history)
        else:
            native_proof(proof, session_id, before["cwd"])
        entries = validate_entries(self.workspace.rpc("inventory"))
        require(any(e["path"] == native_path and e["kind"] == "file" for e in entries), "canonical_native_transcript_not_captured")
        if pause:
            checkpoint_path = pause["record"]["checkpoint_path"]
            require(all(any(e["path"] == checkpoint_path + "/" + filename and e["kind"] == "file" for e in entries)
                        for filename in ("owner.json", "checkpoint.json")), "paused_checkpoint_not_captured")
        require(user_files(entries) == user_files(pre_native), "source_native_load_changed_user_workspace")
        path.mkdir(mode=0o700)
        payloads = []
        for entry in entries:
            if entry["kind"] != "file":
                continue
            response = self.workspace.rpc("read", entry=entry)
            raw = base64.b64decode(response["data"], validate=True)
            require(len(raw) == entry["size"] and file_digest(raw) == entry["sha256"], "workspace_capture_digest_mismatch")
            filename = "blob." + str(len(payloads)).zfill(4)
            fd = os.open(path / filename, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "wb") as file:
                file.write(raw)
                file.flush()
                os.fsync(file.fileno())
            payloads.append(dict(file=filename, path=entry["path"], sha256=entry["sha256"], size=entry["size"]))
        require(self.workspace.rpc("inventory") == entries and self.workspace.inventory() == before,
                "workspace_changed_during_capture")
        self.services.verify_quiescent_source(service_path, service_digest)
        manifest = dict(version=1, protocol=PROTOCOL, cohort_id=service["cohort_id"], barrier_id=service["barrier_id"],
                        service_snapshot_sha256=service_digest, source_workspace=before, entries=entries, payloads=payloads,
                        native_proof=proof, usage_history=history, usage_coverage_complete=accounting["coverage_complete"],
                        can_resume_agent=False)
        if pause:
            manifest.update(version=2, protocol=PAUSED_PROTOCOL, paused_invocation=pause)
        write_report(path / "manifest.json", manifest)
        require((path / "manifest.json").stat().st_size <= MAX_BYTES, "bundle_manifest_too_large")
        seal_capsule(path, [p["file"] for p in payloads] + ["manifest.json"])
        root_digest = digest(manifest)
        write_report(self._record("bundle", root_digest), dict(version=1, bundle_sha256=root_digest,
                     service_snapshot_sha256=service_digest, cohort_id=service["cohort_id"], barrier_id=service["barrier_id"]))
        return dict(bundle_sha256=root_digest, cohort_id=service["cohort_id"], can_resume_agent=False,
                    usage_coverage_complete=accounting["coverage_complete"])

    def load(self, path, expected_digest, service_path):
        path = Path(path).absolute()
        self.services._outside_capsule(path)
        anchor = private_json(self._record("bundle", expected_digest))
        body = private_json(path / "manifest.json", sealed=True)
        require(digest(body) == expected_digest and isinstance(body, dict)
                and ((body.get("version") == 1 and body.get("protocol") == PROTOCOL and "paused_invocation" not in body)
                     or (body.get("version") == 2 and body.get("protocol") == PAUSED_PROTOCOL and "paused_invocation" in body)),
                "bundle_anchor_mismatch")
        require(anchor == dict(version=1, bundle_sha256=expected_digest, service_snapshot_sha256=body["service_snapshot_sha256"],
                               cohort_id=body["cohort_id"], barrier_id=body["barrier_id"]), "bundle_capture_record_mismatch")
        service, _ = self.services.load(service_path, body["service_snapshot_sha256"])
        require((body["cohort_id"], body["barrier_id"]) == (service["cohort_id"], service["barrier_id"]), "bundle_service_epoch_mismatch")
        source = body["source_workspace"]
        require(source["cohort_id"] == body["cohort_id"] and source["daemon_id"] == service["source"]["daemon_id"],
                "bundle_workspace_epoch_mismatch")
        session_id = body["native_proof"]["session_id"]
        if "paused_invocation" in body:
            pause = body["paused_invocation"]
            require(verify_paused_joint(pause["record"], pause["record_sha256"], body["native_proof"], pause["checkpoint"],
                                       body["usage_history"], source["cwd"]) == pause["joint_proof"], "paused_joint_proof_mismatch")
        else:
            native_proof(body["native_proof"], session_id, source["cwd"])
        accounting = usage_history(body["usage_history"], session_id)
        entries = validate_entries(body["entries"])
        files = [e for e in entries if e["kind"] == "file"]
        require(isinstance(body["payloads"], list) and len(files) == len(body["payloads"]), "bundle_payload_missing")
        blobs = {}
        for index, (entry, record) in enumerate(zip(files, body["payloads"])):
            expected = dict(file="blob." + str(index).zfill(4), path=entry["path"], sha256=entry["sha256"], size=entry["size"])
            require(record == expected, "bundle_payload_binding_mismatch")
            fd = os.open(path / record["file"], os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, "rb") as file:
                info = os.fstat(file.fileno())
                require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o400
                        and info.st_size == record["size"], "bundle_payload_not_sealed")
                raw = file.read(MAX_FILE + 1)
            require(len(raw) == record["size"] and file_digest(raw) == record["sha256"], "bundle_payload_corrupt")
            blobs[record["path"]] = raw
        return body, blobs, accounting

    def restore(self, path, expected_digest, service_path, attempt):
        require(identity(attempt), "invalid_restore_attempt")
        body, blobs, accounting = self.load(path, expected_digest, service_path)
        require(accounting["coverage_complete"] is True, "cumulative_usage_incomplete_no_restore")
        claim = self._record("bundle-restore", expected_digest)
        require(not claim.exists(), "bundle_restore_already_claimed")
        target = self.workspace.inventory()
        source = body["source_workspace"]
        require(self.workspace.attempt == attempt and target["attempt_id"] == attempt
                and target["cohort_id"] == body["cohort_id"] and target["container_id"] != source["container_id"]
                and target["volume"] != source["volume"] and target["daemon_id"] == source["daemon_id"]
                and all(target[k] == source[k] for k in ("cwd", "image", "user")), "workspace_target_not_fresh_or_equivalent")
        empty = validate_entries(self.workspace.rpc("inventory"))
        require(len(empty) == 1 and empty[0]["path"] == ".", "workspace_target_not_empty")
        self.workspace.assert_source_absent(source)
        service_target = self.services._inventory(body["cohort_id"], attempt)
        require(service_target["daemon_id"] == target["daemon_id"], "restore_daemon_mismatch")
        write_report(claim, dict(version=1, bundle_sha256=expected_digest, attempt_id=attempt, workspace=target, services=service_target))
        try:
            for entry in sorted((e for e in body["entries"] if e["kind"] == "directory" and e["path"] != "."),
                                key=lambda e: len(e["path"])):
                self.workspace.rpc("mkdir", path=entry["path"])
            for entry in body["entries"]:
                if entry["kind"] == "file":
                    self.workspace.rpc("write", entry=entry, data=base64.b64encode(blobs[entry["path"]]).decode())
            restored = self.workspace.rpc("finish", entries=body["entries"])
            require(restored == body["entries"], "workspace_restore_probe_mismatch")
            session_id = body["native_proof"]["session_id"]
            loaded = self.workspace.rpc("native_probe", session_id=session_id)
            restored_pause = None
            if "paused_invocation" in body:
                restored_pause = self._paused(body["paused_invocation"], loaded, body["usage_history"])
                require(restored_pause == body["paused_invocation"], "paused_joint_proof_mismatch")
            else:
                require(native_proof(loaded, session_id, target["cwd"]) == native_proof(body["native_proof"], session_id, source["cwd"]),
                        "native_loaded_state_mismatch")
            require(loaded["process_nonce"] != body["native_proof"]["process_nonce"], "native_loader_process_reused")
            post_native = self.workspace.rpc("inventory")
            require(user_files(post_native) == user_files(restored), "native_restore_changed_user_workspace")
            # Service activation is last. Every file/native/financial check has
            # already passed, and no Agent or provider is started by it.
            services = self.services.restore(service_path, attempt, expected_snapshot_sha256=body["service_snapshot_sha256"], activate=True)
            require(self.workspace.inventory() == target and self.workspace.rpc("inventory") == post_native,
                    "workspace_changed_before_proof_commit")
            receipt = dict(version=body["version"], protocol=body["protocol"], status="restored_components_verified", bundle_sha256=expected_digest,
                           service_snapshot_sha256=body["service_snapshot_sha256"], cohort_id=body["cohort_id"],
                           barrier_id=body["barrier_id"], attempt_id=attempt, workspace_runtime=target,
                           workspace_restored=True, restored_tree_sha256=digest(restored), post_native_tree_sha256=digest(post_native),
                           native_session_loaded=True, native_proof=loaded, cumulative_usage_restored=True, accounting=accounting,
                           services=services, can_resume_agent=False,
                           blocking_reasons=["native_runtime_dispatch_not_integrated", "new_spending_authorization_required"])
            if restored_pause:
                receipt["paused_joint_proof"] = restored_pause["joint_proof"]
                receipt["blocking_reasons"].insert(1, "tool_runtime_state_not_integrated")
            write_report(self._record("bundle-receipt", expected_digest), receipt)
            return receipt
        except BaseException:
            errors = []
            for backend, inventory in ((self.workspace, target), (self.services.backend, service_target)):
                try:
                    backend.quarantine(inventory)
                except BaseException:
                    errors.append(True)
            require(not errors, "bundle_restore_failed_quarantine_unconfirmed")
            raise

    def prepare_dispatch(self, path, expected_digest, service_path):
        """Reobserve all joined components before a separately authorized launch."""
        body, _, accounting = self.load(path, expected_digest, service_path)
        require(body["protocol"] == PAUSED_PROTOCOL and accounting["coverage_complete"] is True,
                "dispatch_requires_verified_paused_bundle")
        receipt = private_json(self._record("bundle-receipt", expected_digest))
        claim = private_json(self._record("bundle-restore", expected_digest))
        require(receipt.get("status") == "restored_components_verified" and receipt.get("version") == 2
                and receipt.get("bundle_sha256") == expected_digest
                and receipt.get("service_snapshot_sha256") == body["service_snapshot_sha256"]
                and receipt.get("cohort_id") == body["cohort_id"] and receipt.get("barrier_id") == body["barrier_id"]
                and receipt.get("accounting") == accounting and receipt.get("cumulative_usage_restored") is True
                and receipt.get("workspace_restored") is True and receipt.get("native_session_loaded") is True,
                "dispatch_restore_receipt_mismatch")
        target = self.workspace.inventory()
        claimed_services, active_services = claim.get("services"), receipt["services"]["target"]
        require(target == receipt["workspace_runtime"] and target["attempt_id"] == receipt["attempt_id"]
                and target["cohort_id"] == body["cohort_id"]
                and claim == dict(version=1, bundle_sha256=expected_digest, attempt_id=receipt["attempt_id"],
                                  workspace=target, services=claimed_services)
                and same_live_runtime(claimed_services, active_services)
                and all(claimed_services.get(k) == active_services.get(k)
                        for k in ("daemon_id", "owned_volumes", "cohort_id", "restore_attempt_id")),
                "dispatch_workspace_generation_changed")
        self.workspace.assert_source_absent(body["source_workspace"])
        self.services.verify_active_restore(service_path, body["service_snapshot_sha256"], receipt["services"])
        before = validate_entries(self.workspace.rpc("inventory"))
        require(digest(before) == receipt["post_native_tree_sha256"], "dispatch_workspace_changed")
        pause = body["paused_invocation"]
        native = self.workspace.rpc("native_probe", session_id=pause["record"]["session_id"])
        checkpoint = self.workspace.rpc("usage_probe", checkpoint_path=pause["record"]["checkpoint_path"],
                                        segment_id=pause["record"]["segment_id"])
        handoff = cold_tool_handoff(pause["record"], pause["record_sha256"], native, checkpoint, body["usage_history"], target["cwd"])
        require(verify_paused_joint(pause["record"], pause["record_sha256"], native, checkpoint, body["usage_history"], target["cwd"])
                == receipt["paused_joint_proof"], "dispatch_joint_proof_mismatch")
        after = validate_entries(self.workspace.rpc("inventory"))
        require(user_files(before) == user_files(after) and self.workspace.inventory() == target, "dispatch_workspace_changed_during_probe")
        self.services.verify_active_restore(service_path, body["service_snapshot_sha256"], receipt["services"])
        require(self.workspace.rpc("inventory") == after, "dispatch_workspace_changed_during_probe")
        return body, handoff, dict(workspace=target, tree_sha256=digest(after), restore_receipt_sha256=digest(receipt))


def main():
    parser = argparse.ArgumentParser(description="Operator-only cold restore and explicitly granted one-shot dispatch")
    parser.add_argument("operation", choices=("capture", "restore", "dispatch"))
    parser.add_argument("--service-contract", type=Path, required=True)
    parser.add_argument("--service-definition", type=Path, required=True)
    parser.add_argument("--service-project", required=True)
    parser.add_argument("--service-snapshot", type=Path, required=True)
    parser.add_argument("--service-sha256")
    parser.add_argument("--workspace-container", required=True)
    parser.add_argument("--cwd", required=True)
    parser.add_argument("--cohort-id", required=True)
    parser.add_argument("--attempt-id")
    parser.add_argument("--session-id")
    parser.add_argument("--usage-history", type=Path)
    parser.add_argument("--pause-record", type=Path)
    parser.add_argument("--pause-sha256")
    parser.add_argument("--snapshot", type=Path, required=True)
    parser.add_argument("--bundle-sha256")
    parser.add_argument("--control-dir", type=Path, required=True)
    parser.add_argument("--dispatch-grant", type=Path)
    parser.add_argument("--dispatch-grant-sha256")
    args = parser.parse_args()
    try:
        contract = private_json(args.service_contract)
        services = ServiceSnapshots(contract, DockerServices(args.service_project, args.service_definition, contract), control_dir=args.control_dir)
        workspace = DockerWorkspace(args.workspace_container, args.cwd, args.cohort_id, args.attempt_id)
        bundle = RecoveryBundle(services, workspace)
        if args.operation == "capture":
            require(args.usage_history is not None and args.session_id is not None, "capture_native_inputs_required")
            result = bundle.capture(args.snapshot, args.service_snapshot, args.service_sha256, args.session_id, private_json(args.usage_history),
                                    pause_record=private_json(args.pause_record) if args.pause_record else None, pause_sha256=args.pause_sha256)
        elif args.operation == "restore":
            result = bundle.restore(args.snapshot, args.bundle_sha256, args.service_snapshot, args.attempt_id)
        else:
            from scripts.terminal_bench.cold_dispatch import ColdBenchmarkLauncher
            require(args.dispatch_grant is not None and args.dispatch_grant_sha256, "explicit_dispatch_grant_required")
            result = ColdBenchmarkLauncher(bundle).dispatch(args.snapshot, args.bundle_sha256, args.service_snapshot,
                private_json(args.dispatch_grant), args.dispatch_grant_sha256, os.environ.get("YHC_BENCH_GATEWAY_TOKEN"))
            print(json.dumps({k: result[k] for k in ("status", "source_segment_id", "accounting_complete", "automatic_paid_resume")}))
            if result["status"] != "completed": parser.exit(1)
            return
        print(json.dumps({k: result[k] for k in ("cohort_id", "can_resume_agent", "bundle_sha256")}
                         | {"status": result.get("status", "bundle_captured")}))
    except SnapshotError as exc:
        parser.exit(1, "cold_bundle_failed_closed: " + exc.code + "\n")
    except Exception:
        parser.exit(1, "cold_bundle_failed_closed: private_state_or_transport_invalid\n")


if __name__ == "__main__":
    main()
