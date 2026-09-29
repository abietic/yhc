"""Run the production YHC agent inside a Harbor task's Linux environment.

Only the agent binary, optional ripgrep and public CA bundle, and task
instruction are uploaded. Host settings, credentials files, repository contents, and
verifier/oracle data are never copied.
"""

import asyncio
import hashlib
import json
import shlex
import shutil
import ssl
import tempfile
import uuid
from pathlib import Path, PurePosixPath

from harbor.agents.installed.base import BaseInstalledAgent, NonZeroAgentExitCodeError
from harbor.agents.installed.base import with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext


def read_result(path: Path) -> dict:
    """Require a versioned stream with exactly one closing terminal record."""
    result = None
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            if not line.strip():
                continue
            record = json.loads(line)
            if (not isinstance(record, dict) or record.get("schema_version") != 1
                    or result is not None):
                raise ValueError("Invalid YHC lifecycle stream or record after result")
            if record.get("type") == "result":
                result = record.get("result")
                if (not isinstance(result, dict)
                        or not isinstance(result.get("status"), str)
                        or type(result.get("exit_code")) is not int):
                    raise ValueError("Invalid YHC terminal result")
            elif record.get("type") != "event" or not isinstance(record.get("event"), dict):
                raise ValueError("Unknown YHC lifecycle record")
    if result is None:
        raise ValueError("YHC lifecycle stream has no terminal result")
    return result


def validated_usage(value: object) -> dict | None:
    """Project numeric invocation usage; partial totals cannot become Harbor totals."""
    fields = ("provider_calls", "known_calls", "unknown_calls", "in_flight",
              "untracked_calls", "prompt_tokens", "completion_tokens", "total_tokens",
              "cached_prompt_tokens", "reasoning_tokens")
    if not isinstance(value, dict) or type(value.get("complete")) is not bool:
        return None
    if any(type(value.get(key)) is not int or value[key] < 0 for key in fields):
        return None
    if (value["cached_prompt_tokens"] > value["prompt_tokens"]
            or value["reasoning_tokens"] > value["completion_tokens"]
            or value["total_tokens"] < value["prompt_tokens"] + value["completion_tokens"]
            or value["provider_calls"] != value["known_calls"] + value["unknown_calls"] + value["in_flight"]):
        return None
    complete = not (value["unknown_calls"] or value["in_flight"] or value["untracked_calls"])
    if value["complete"] != complete:
        return None
    return {**{key: value[key] for key in fields}, "complete": complete}


def inspect_linux_binary(path: Path) -> tuple[str, str]:
    """Identify an uploaded executable before provisioning an environment."""
    with path.open("rb") as binary:
        header = binary.read(20)
        if header[:6] != b"\x7fELF\x02\x01" or len(header) < 20:
            raise ValueError("Uploaded executable must be a Linux 64-bit little-endian ELF binary")
        arch = {62: "x86_64", 183: "aarch64"}.get(int.from_bytes(header[18:20], "little"))
        if arch is None:
            raise ValueError("Unsupported ELF architecture; use linux-amd64 or linux-arm64")
        binary.seek(0)
        return arch, hashlib.file_digest(binary, "sha256").hexdigest()


class YHCAgent(BaseInstalledAgent):
    """Harbor 0.22 installed agent, sharing the TUI's QueryEngine traversal."""

    def __init__(self, logs_dir: Path, model_name: str | None = None,
                 binary_path: str = "build/linux-amd64/yhc", provider: str | None = None,
                 max_turns: int = 0, ripgrep_path: str | None = None,
                 ca_bundle_path: str | None = None, max_provider_calls: int = 0,
                 max_total_tokens: int = 0, **kwargs):
        super().__init__(logs_dir=logs_dir, model_name=model_name, **kwargs)
        if not model_name or not model_name.strip():
            raise ValueError("YHC requires --model provider/model (or --ak provider=...)")
        if type(max_turns) is not int or max_turns < 0:
            raise ValueError("max_turns must be a nonnegative integer; 0 is unlimited")
        for name, value in (("max_provider_calls", max_provider_calls),
                            ("max_total_tokens", max_total_tokens)):
            if type(value) is not int or not 0 <= value <= 2**63 - 1:
                raise ValueError(f"{name} must be a nonnegative int64; 0 disables")
        self.max_provider_calls = max_provider_calls
        self.max_total_tokens = max_total_tokens
        self.binary_path = Path(binary_path).expanduser().resolve(strict=True)
        self.arch, self.binary_sha256 = inspect_linux_binary(self.binary_path)
        self.ripgrep_path = Path(ripgrep_path).expanduser().resolve(strict=True) if ripgrep_path else None
        self.ripgrep_sha256 = None
        if self.ripgrep_path:
            rg_arch, self.ripgrep_sha256 = inspect_linux_binary(self.ripgrep_path)
            if rg_arch != self.arch:
                raise ValueError("ripgrep_path architecture must match the YHC binary")
        self.ca_bundle_path = Path(ca_bundle_path).expanduser().resolve(strict=True) if ca_bundle_path else None
        self.ca_bundle_sha256 = None
        if self.ca_bundle_path:
            bundle = self.ca_bundle_path.read_bytes()
            if b"PRIVATE KEY" in bundle or b"-----BEGIN CERTIFICATE-----" not in bundle:
                raise ValueError("ca_bundle_path must contain public PEM certificates only")
            try:
                trust = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
                trust.load_verify_locations(cadata=bundle.decode("ascii"))
            except (UnicodeError, ssl.SSLError) as exc:
                raise ValueError("ca_bundle_path is not a valid public PEM certificate bundle") from exc
            if not trust.cert_store_stats()["x509"]:
                raise ValueError("ca_bundle_path contains no certificates")
            self.ca_bundle_sha256 = hashlib.sha256(bundle).hexdigest()
        self.provider = provider
        self.max_turns = max_turns
        self.remote_dir = f"/installed-agent/yhc-{uuid.uuid4().hex}"
        self.remote_binary = f"{self.remote_dir}/yhc"
        self.remote_prompt = f"{self.remote_dir}/instruction.txt"
        self.remote_pid = str(self.environment_logs_dir / "yhc.pid")
        self.state_dir_provisioned = False
        self.execution_env()  # Fail before provisioning when credentials are absent.

    @staticmethod
    def name() -> str:
        return "yhc"

    def version(self) -> str:
        # Identify the actual uploaded artifact, including uncommitted local builds.
        return f"sha256:{self.binary_sha256}"

    def execution_env(self) -> dict[str, str]:
        key = self._get_env("YHC_BENCH_API_KEY", "PROV_API_KEY")
        if not key:
            raise ValueError("Set YHC_BENCH_API_KEY (or PROV_API_KEY) before running Harbor")
        env = {"PROV_API_KEY": key}
        base_url = self._get_env("YHC_BENCH_BASE_URL", "PROV_BASE_URL")
        if base_url:
            env["PROV_BASE_URL"] = base_url
        if self.ca_bundle_path:
            env["SSL_CERT_FILE"] = self.remote_dir + "/ca-certificates.crt"
        return env

    def execution_command(self) -> str:
        argv = [self.remote_binary, "exec", "-", "--output-format", "jsonl",
                "--model", self.model_name, "--max-turns", str(self.max_turns),
                "-y", "--sandbox", "danger-full-access"]
        if self.max_provider_calls:
            argv += ["--max-provider-calls", str(self.max_provider_calls)]
        if self.max_total_tokens:
            argv += ["--max-total-tokens", str(self.max_total_tokens)]
        if self.provider:
            argv += ["--provider", self.provider]
        path_setup = f'export PATH={shlex.quote(self.remote_dir)}:"$PATH"; ' if self.ripgrep_path else ""
        # The shell becomes YHC via exec, so $$ identifies only this invocation.
        # Restrict the PID file without changing the agent's inherited umask.
        pid_setup = f"(umask 077; printf '%s\\n' \"$$\" > {shlex.quote(self.remote_pid)}) && "
        return (f"{path_setup}{pid_setup}exec {shlex.join(argv)} < {shlex.quote(self.remote_prompt)}"
                f" > {shlex.quote(str(self.environment_logs_dir / 'yhc.jsonl'))}"
                f" 2> {shlex.quote(str(self.environment_logs_dir / 'yhc.stderr.log'))}")

    async def install(self, environment: BaseEnvironment) -> None:
        arch = await self.exec_as_agent(environment, command="uname -m")
        if (arch.stdout or "").strip() != self.arch:
            raise ValueError(f"YHC binary architecture {self.arch} does not match task environment")
        if not self.ripgrep_path:
            await self.check_ripgrep(environment, "rg")
        await self.exec_as_root(environment, command=f"mkdir -m 755 {shlex.quote(self.remote_dir)}")
        await environment.upload_file(self.binary_path, self.remote_binary)
        await self.exec_as_root(environment, command=f"chmod 755 {shlex.quote(self.remote_binary)}")
        if self.ripgrep_path:
            remote_rg = self.remote_dir + "/rg"
            await environment.upload_file(self.ripgrep_path, remote_rg)
            await self.exec_as_root(environment, command=f"chmod 755 {shlex.quote(remote_rg)}")
            await self.check_ripgrep(environment, remote_rg)
        if self.ca_bundle_path:
            remote_ca = self.remote_dir + "/ca-certificates.crt"
            await environment.upload_file(self.ca_bundle_path, remote_ca)
            await self.exec_as_root(environment, command=f"chmod 644 {shlex.quote(remote_ca)}")
        # Keep the task's cwd, HOME, user, and installed software intact.
        await self.exec_as_agent(environment, command=f"mkdir -p {shlex.quote(str(self.environment_logs_dir))}")
        await self.exec_as_agent(environment, command=f"{shlex.quote(self.remote_binary)} version")
        await self.prepare_project_state(environment)

    async def prepare_project_state(self, environment: BaseEnvironment) -> None:
        # YHC persists its session and WorkBoard under cwd/.yhc. Some tasks
        # expose writable source subdirectories inside a root-owned cwd.
        # Provision only a missing private state directory, never re-own an
        # existing path or relax permissions on task files or their parent.
        probe = await environment.exec(command=(
            "if [ -L .yhc ]; then exit 1; fi; "
            "if [ -e .yhc ]; then test -d .yhc && test -w .yhc && test -x .yhc; "
            "else test -w . && test -x .; fi"), timeout_sec=15)
        if probe.return_code == 0:
            return
        identity = await self.exec_as_agent(environment, command=(
            "if [ -e .yhc ] || [ -L .yhc ]; then "
            "echo 'YHC state path already exists and is not a writable directory' >&2; exit 1; fi; "
            "pwd -P && id -u && id -g"))
        fields = (identity.stdout or "").splitlines()
        if (len(fields) != 3 or not PurePosixPath(fields[0]).is_absolute()
                or "\x00" in fields[0]
                or not all(value.isascii() and value.isdecimal() for value in fields[1:])):
            raise ValueError("Cannot identify task cwd and user for YHC state provisioning")
        cwd, uid, gid = fields
        state_dir = shlex.quote(str(PurePosixPath(cwd) / ".yhc"))
        # mkdir without -p must succeed before chown. Existing paths, including
        # links created after the probe, therefore fail without ownership changes.
        await self.exec_as_root(environment, command=(
            f"mkdir -m 700 -- {state_dir} && chown -h -- {uid}:{gid} {state_dir}"))
        await self.exec_as_agent(environment, command=(
            f"test ! -L {state_dir} && test -d {state_dir} && "
            f"test -w {state_dir} && test -x {state_dir}"))
        self.state_dir_provisioned = True

    async def check_ripgrep(self, environment: BaseEnvironment, executable: str) -> None:
        # exec_as_agent raises on nonzero status before a missing-dependency
        # diagnostic can be attached. Probe directly as the same default user.
        probe = await environment.exec(command=f"{shlex.quote(executable)} --version", timeout_sec=15)
        if probe.return_code != 0:
            raise ValueError("YHC Grep requires executable ripgrep; provide a matching Linux ripgrep_path")

    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment,
                  context: AgentContext) -> None:
        with tempfile.TemporaryDirectory(prefix="yhc-instruction-") as directory:
            prompt = Path(directory) / "instruction.txt"
            prompt.write_text(instruction, encoding="utf-8")
            await environment.upload_file(prompt, self.remote_prompt)
        await self.exec_as_root(environment, command=f"chmod 644 {shlex.quote(self.remote_prompt)}")
        # Keep context empty until logs are available: after timeout/cancel,
        # Harbor collects partial logs and invokes the post-run hook only for
        # an empty context. A provisional "running" status would suppress it.
        # No pipeline or shell interpolation of prompts/credentials. The exec
        # return code remains YHC's. Harbor owns the task timeout and teardown.
        try:
            execution = await environment.exec(command=self.execution_command(), env=self.execution_env())
        except asyncio.CancelledError:
            # Cancelling Docker exec's client does not signal its container child.
            # Give YHC a bounded chance to cancel descendants and write its final
            # usage before Harbor collects logs and tears down the environment.
            # Never turn the original timeout/cancellation into a successful run.
            try:
                await asyncio.wait_for(self.interrupt_execution(environment), timeout=6)
            except (Exception, asyncio.CancelledError):
                self.logger.warning("YHC cancellation cleanup did not complete; usage may be partial")
            raise
        self.logs_dir.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="yhc-result-") as directory:
            local_result = Path(directory) / "yhc.jsonl"
            await environment.download_file(str(self.environment_logs_dir / "yhc.jsonl"), local_result)
            shutil.copyfile(local_result, self.logs_dir / "yhc.jsonl")
        self.populate_context_post_run(context)
        if execution.return_code != 0:
            raise NonZeroAgentExitCodeError(
                f"YHC exited with code {execution.return_code}; inspect yhc.jsonl and yhc.stderr.log")
        result = read_result(self.logs_dir / "yhc.jsonl")
        if result["exit_code"] != 0 or result["status"] != "completed":
            raise ValueError("YHC process exit and terminal result disagree")

    async def interrupt_execution(self, environment: BaseEnvironment) -> None:
        # Refuse missing/invalid PID files and processes using another executable.
        # No host process or arbitrary task process may be selected for signalling.
        command = (
            f"read -r yhc_pid < {shlex.quote(self.remote_pid)} || exit 0; "
            "case \"$yhc_pid\" in ''|*[!0-9]*) exit 0;; esac; "
            "[ \"$yhc_pid\" -gt 1 ] || exit 0; "
            f"[ \"/proc/$yhc_pid/exe\" -ef {shlex.quote(self.remote_binary)} ] || exit 0; "
            "kill -INT \"$yhc_pid\" 2>/dev/null || exit 0; "
            f"while [ \"/proc/$yhc_pid/exe\" -ef {shlex.quote(self.remote_binary)} ]; "
            "do sleep 0.1; done"
        )
        await environment.exec(command=command, timeout_sec=5)

    def populate_context_post_run(self, context: AgentContext) -> None:
        context.n_input_tokens = None
        context.n_output_tokens = None
        context.n_cache_tokens = None
        metadata = {"binary_sha256": self.binary_sha256}
        if self.ripgrep_sha256:
            metadata["ripgrep_sha256"] = self.ripgrep_sha256
        if self.ca_bundle_sha256:
            metadata["ca_bundle_sha256"] = self.ca_bundle_sha256
        if self.state_dir_provisioned:
            metadata["state_dir_provisioned"] = True
        try:
            result = read_result(self.logs_dir / "yhc.jsonl")
            usage = validated_usage(result.get("usage"))
            if usage is not None:
                metadata["usage"] = usage
                if usage["complete"]:
                    context.n_input_tokens = usage["prompt_tokens"]
                    context.n_output_tokens = usage["completion_tokens"]
                    context.n_cache_tokens = usage["cached_prompt_tokens"]
            metadata.update({key: result[key] for key in (
                "status", "exit_code", "terminal_reason", "session_id") if key in result})
        except (OSError, ValueError):
            metadata["status"] = "incomplete_stream"
        context.metadata = {**(context.metadata or {}), "yhc": metadata}
        # Incomplete usage remains metadata-only; never infer a currency cost.
