"""Trusted container-only file transport; never executes workspace programs.

V1 preserves bytes, modes and mtime for one bounded, single-owner tree.
Links, special files, xattrs and mixed owners are unsupported, not approximated.
"""

import base64
import hashlib
import json
import os
import selectors
import signal
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path, PurePosixPath

PROTOCOL = "yhc-workspace-files-v1"
MAX_FILE = 4 << 20
MAX_TOTAL = 64 << 20
MAX_ENTRIES = 4096
MAX_JSON = 8 << 20


def require(value):
    if not value:
        raise ValueError("unsupported_workspace_state")


def root_path(value):
    require(isinstance(value, str) and len(value) <= 512 and value.startswith("/") and value != "/")
    path = Path(value)
    require(path.as_posix() == value and path.resolve() == path and path.is_dir())
    return path


def child(root, name):
    require(isinstance(name, str) and len(name) <= 2048 and "\0" not in name)
    parsed = PurePosixPath(name)
    require(name == "." or (not parsed.is_absolute() and parsed.as_posix() == name
                            and all(part not in (".", "..") for part in parsed.parts)))
    path = root if name == "." else root / name
    require(path.parent.resolve() == path.parent)
    return path


def metadata(root, path):
    require(hasattr(os, "listxattr"))  # Audited Linux container API, fail closed elsewhere.
    value = path.lstat()
    require(value.st_uid == os.geteuid() and value.st_gid == os.getegid()
            and not stat.S_IMODE(value.st_mode) & 0o7000
            and not os.listxattr(path, follow_symlinks=False))
    require(stat.S_ISDIR(value.st_mode) or (stat.S_ISREG(value.st_mode) and value.st_nlink == 1))
    result = dict(path="." if path == root else path.relative_to(root).as_posix(),
                  kind="directory" if stat.S_ISDIR(value.st_mode) else "file",
                  mode=stat.S_IMODE(value.st_mode), mtime_ns=value.st_mtime_ns)
    require(0 <= result["mtime_ns"] < 1 << 63)
    if result["kind"] == "file":
        require(value.st_size <= MAX_FILE)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as file:
            raw = file.read(MAX_FILE + 1)
            after = os.fstat(file.fileno())
        require(len(raw) == value.st_size and after.st_ino == value.st_ino
                and after.st_mtime_ns == value.st_mtime_ns and after.st_size == value.st_size)
        result.update(size=len(raw), sha256=hashlib.sha256(raw).hexdigest())
    return result


def inventory(root):
    root = root_path(str(root))
    result = [metadata(root, root)]
    for directory, dirs, files in os.walk(root, followlinks=False):
        for name in sorted(dirs + files):
            result.append(metadata(root, Path(directory) / name))
            require(len(result) <= MAX_ENTRIES)
    require(sum(item.get("size", 0) for item in result) <= MAX_TOTAL)
    return sorted(result, key=lambda item: item["path"])


def validate_entries(entries):
    require(isinstance(entries, list) and 0 < len(entries) <= MAX_ENTRIES)
    seen = {}
    total = 0
    for item in entries:
        require(isinstance(item, dict) and item.get("kind") in ("file", "directory"))
        keys = {"path", "kind", "mode", "mtime_ns"}
        if item["kind"] == "file":
            keys |= {"size", "sha256"}
        require(set(item) == keys and isinstance(item.get("path"), str))
        name = item["path"]
        parsed = PurePosixPath(name)
        require(name not in seen and (name == "." or (name and not parsed.is_absolute()
                and parsed.as_posix() == name and ".." not in parsed.parts)) and "\0" not in name)
        require(type(item["mode"]) is int and 0 <= item["mode"] <= 0o777
                and type(item["mtime_ns"]) is int and 0 <= item["mtime_ns"] < 1 << 63)
        if item["kind"] == "file":
            require(type(item["size"]) is int and 0 <= item["size"] <= MAX_FILE
                    and isinstance(item["sha256"], str) and len(item["sha256"]) == 64
                    and all(c in "0123456789abcdef" for c in item["sha256"]))
            total += item["size"]
        seen[name] = item
    require(total <= MAX_TOTAL and seen.get(".", {}).get("kind") == "directory")
    for name in seen:
        if name != ".":
            require(seen.get(str(PurePosixPath(name).parent), {}).get("kind") == "directory")
    return entries


def control_identity():
    digest = hashlib.sha256()
    with open("/opt/yhc/yhc", "rb") as binary:
        for block in iter(lambda: binary.read(65536), b""):
            digest.update(block)
    return dict(binary_sha256=digest.hexdigest())


def bounded_exec(args, root, env, payload, timeout):
    """Cap both pipes while running, including after the leader exits."""
    require(len(payload) <= MAX_JSON and 0 < timeout <= 905)
    process = subprocess.Popen(args, cwd=root, env=env, stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    output = {"stdout": bytearray(), "stderr": bytearray()}
    deadline, succeeded = time.monotonic() + timeout, False
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
                require(remaining > 0)
                for event, _mask in selector.select(min(remaining, 0.1)):
                    stream, kind = event.fileobj, event.data
                    if kind == "stdin":
                        offset += os.write(stream.fileno(), payload[offset:offset + 65536])
                        if offset == len(payload):
                            selector.unregister(stream); stream.close()
                        continue
                    block = os.read(stream.fileno(), 65536)
                    if not block:
                        selector.unregister(stream); stream.close()
                    else:
                        output[kind].extend(block)
                        require(len(output[kind]) <= (MAX_JSON if kind == "stdout" else 8192))
        code = process.wait(timeout=max(0.01, deadline - time.monotonic()))
        succeeded = True
        return code, bytes(output["stdout"])
    finally:
        if not succeeded:
            try: os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError: pass
        if process.poll() is None: process.wait(timeout=5)
        for stream in (process.stdin, process.stdout, process.stderr): stream.close()


def cold_dispatch(root, launch, token):
    """Only the immutable YHC binary runs; task paths never select an executable."""
    require(isinstance(launch, dict) and set(launch) == {"version", "handoff", "grant", "checkpoint_path", "expected_tree_sha256", "expected_workspace"}
            and launch["version"] == 1 and type(launch["version"]) is int)
    grant, handoff = launch["grant"], launch["handoff"]
    require(control_identity()["binary_sha256"] == grant["binary_sha256"]
            and hashlib.sha256(json.dumps(inventory(root), sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
            == launch["expected_tree_sha256"])
    require(isinstance(token, str) and 0 < len(token) <= 512 and token.isascii() and not any(c.isspace() for c in token))
    require(launch["checkpoint_path"] == ".yhc/usage-checkpoints/cold-" + grant["dispatch_id"])
    checkpoint = child(root, launch["checkpoint_path"])
    require(not checkpoint.exists() and not checkpoint.is_symlink())
    limits, route = grant["limits"], grant["route"]
    require(type(limits["timeout_sec"]) is int and 0 < limits["timeout_sec"] <= 900
            and type(grant["expires_unix"]) is int and grant["expires_unix"] >= time.time() + limits["timeout_sec"])
    with tempfile.TemporaryDirectory(prefix="yhc-cold-control-") as temporary:
        private = Path(temporary)
        handoff_bytes = json.dumps(handoff, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
        for name, raw in (("handoff.json", handoff_bytes), ("price.json", json.dumps(grant["price_card"]).encode())):
            fd = os.open(private / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "wb") as f:
                f.write(raw); f.flush(); os.fsync(f.fileno())
        args = ["/opt/yhc/yhc", "exec", "-", "--resume", handoff["native_proof"]["session_id"],
                "--output-format", "json", "--capture-read-guards", "--cold-tool-handoff", str(private / "handoff.json"),
                "--cold-tool-handoff-sha256", hashlib.sha256(handoff_bytes).hexdigest(),
                "--usage-checkpoint-dir", str(checkpoint), "--price-card", str(private / "price.json"),
                "--reserve-cost-before-dispatch", "--timeout", str(limits["timeout_sec"]) + "s",
                "--provider", route["provider"], "--model", route["model"], "--base-url", route["base_url"],
                "--permission-mode", "acceptEdits"]
        for key in ("max_provider_calls", "max_total_tokens", "max_estimated_cost", "max_output_tokens_per_call"):
            args.extend(["--" + key.replace("_", "-"), str(limits[key])])
        if grant["tools"]:
            args.extend(["--tools", ",".join(grant["tools"])])
        env = dict(PATH="/usr/local/bin:/usr/bin:/bin", HOME=temporary, TZ="UTC", PROV_API_KEY=token,
                   XDG_CONFIG_HOME=temporary + "/config", XDG_DATA_HOME=temporary + "/data",
                   XDG_CACHE_HOME=temporary + "/cache", YHC_SESSION_CATALOG=str(root / ".yhc/session-roots.json"))
        code, raw = bounded_exec(args, root, env,
            b"Continue the saved task using the verified restored state. Do not repeat completed side effects.", limits["timeout_sec"] + 5)
        try: terminal = json.loads(raw)
        except (ValueError, UnicodeError): terminal = None
        owner = json.loads((checkpoint / "owner.json").read_bytes())
        inspected = operate(dict(protocol=PROTOCOL, root=str(root), operation="usage_probe",
                                 checkpoint_path=launch["checkpoint_path"], segment_id=owner["segment_id"]))
        return dict(exit_code=code, terminal=terminal, checkpoint=inspected)


def operate(request):
    require(isinstance(request, dict) and request.get("protocol") == PROTOCOL)
    root = root_path(request["root"])
    op = request["operation"]
    if op == "control_probe":
        return control_identity()
    if op == "cold_dispatch":
        return cold_dispatch(root, request["launch"], request["gateway_token"])
    if op == "inventory":
        return inventory(root)
    if op == "read":
        item = request["entry"]
        path = child(root, item["path"])
        require(item["kind"] == "file" and metadata(root, path) == item)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as file:
            raw = file.read(MAX_FILE + 1)
        require(len(raw) == item["size"] and hashlib.sha256(raw).hexdigest() == item["sha256"])
        return dict(data=base64.b64encode(raw).decode())
    if op == "mkdir":
        name = request["path"]
        require(name != ".")
        child(root, name).mkdir(mode=0o700)
        return {}
    if op == "write":
        item = request["entry"]
        require(item["kind"] == "file" and type(item["size"]) is int and 0 <= item["size"] <= MAX_FILE)
        raw = base64.b64decode(request["data"], validate=True)
        require(len(raw) == item["size"] and hashlib.sha256(raw).hexdigest() == item["sha256"])
        fd = os.open(child(root, item["path"]), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as file:
            file.write(raw)
            file.flush()
            os.fchmod(file.fileno(), item["mode"])
            os.utime(file.fileno(), ns=(item["mtime_ns"], item["mtime_ns"]))
            os.fsync(file.fileno())
        return {}
    if op == "finish":
        entries = validate_entries(request["entries"])
        for item in sorted((v for v in entries if v["kind"] == "directory"), key=lambda v: len(v["path"]), reverse=True):
            path = child(root, item["path"])
            fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                os.fchmod(fd, item["mode"])
                os.utime(fd, ns=(item["mtime_ns"], item["mtime_ns"]))
                os.fsync(fd)
            finally:
                os.close(fd)
        return inventory(root)
    if op == "native_probe":
        session = request["session_id"]
        require(isinstance(session, str) and 0 < len(session) <= 128 and session[0].isalnum()
                and all(c.isascii() and (c.isalnum() or c in "_.-") for c in session))
        # The audited binary is in the immutable image, never in the restored
        # workspace. This administration entrypoint constructs no provider.
        env = dict(PATH="/usr/local/bin:/usr/bin:/bin", HOME="/tmp/yhc-proof-home",
                   YHC_SESSION_CATALOG=str(root / ".yhc" / "session-roots.json"))
        result = subprocess.run(["/opt/yhc/yhc", "sessions", "resume", session,
                                 "--recovery-proof", "--output-format", "json"],
                                cwd=root, env=env, capture_output=True, timeout=20, check=True)
        require(len(result.stdout) <= MAX_JSON)
        value = json.loads(result.stdout)
        require(value.get("status") == "completed" and value.get("operation") == "sessions.resume")
        return value["result"]["recovery_proof"]
    if op == "usage_probe":
        relative = request["checkpoint_path"]
        require(isinstance(relative, str) and relative.startswith(".yhc/usage-checkpoints/"))
        directory = child(root, relative)
        require(directory.is_dir() and directory.resolve() == directory)
        segment = request["segment_id"]
        require(isinstance(segment, str) and len(segment) == 36)
        result = subprocess.run(["/opt/yhc/yhc", "usage", "inspect", "--checkpoint-dir", str(directory),
                                 "--segment-id", segment], cwd=root,
                                env=dict(PATH="/usr/local/bin:/usr/bin:/bin", HOME="/tmp/yhc-proof-home"),
                                capture_output=True, timeout=20, check=True)
        require(len(result.stdout) <= MAX_JSON)
        return json.loads(result.stdout)
    raise ValueError("unsupported_workspace_operation")


def main():
    try:
        raw = sys.stdin.buffer.read(MAX_JSON + 1)
        require(len(raw) <= MAX_JSON)
        request = json.loads(raw)
        body = operate(request)
        result = dict(ok=True, protocol=PROTOCOL, request_id=request["request_id"], body=body)
    except Exception:
        result = dict(ok=False, protocol=PROTOCOL, error="workspace_io_failed")
    encoded = json.dumps(result, separators=(",", ":"), allow_nan=False).encode()
    require(len(encoded) <= MAX_JSON)
    sys.stdout.buffer.write(encoded)


if __name__ == "__main__":
    main()
