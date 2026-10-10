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
import time
import uuid
from pathlib import Path, PurePosixPath

from harbor.agents.installed.base import BaseInstalledAgent, NonZeroAgentExitCodeError
from harbor.agents.installed.base import with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

from scripts.terminal_bench.usage import sum_usage, validated_usage


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
                if result["status"] == "completed" and (
                        result["exit_code"] != 0 or result.get("error") is not None
                        or result.get("terminal_reason") not in (
                            None, "completed", "stop_hook_prevented", "hook_stopped")):
                    # Reason remains optional for command-only/older v1 results;
                    # an explicit cancellation, error, or unknown reason is not
                    # allowed to masquerade as a successful closure.
                    raise ValueError("Completed YHC result contradicts terminal fields")
            elif record.get("type") != "event" or not isinstance(record.get("event"), dict):
                raise ValueError("Unknown YHC lifecycle record")
    if result is None:
        raise ValueError("YHC lifecycle stream has no terminal result")
    return result


def parse_continuation_budgets(value: object) -> list[dict]:
    """Each grant is explicit and finite; there is no automatic unlimited top-up."""
    if value is None:
        return []
    if isinstance(value, str):
        try:
            value = json.loads(value)
        except ValueError as exc:
            raise ValueError("continuation_budgets must be a JSON list") from exc
    if not isinstance(value, list) or len(value) > 16:
        raise ValueError("continuation_budgets must be a list of at most 16 grants")
    grants = []
    keys = {"max_provider_calls", "max_total_tokens"}
    for grant in value:
        if not isinstance(grant, dict) or set(grant) - keys:
            raise ValueError("Each continuation grant only accepts max_provider_calls and max_total_tokens")
        grant = {key: grant.get(key, 0) for key in sorted(keys)}
        if any(type(limit) is not int or not 0 <= limit <= 2**63 - 1 for limit in grant.values()) or not any(grant.values()):
            raise ValueError("Each continuation grant must have a positive finite int64 limit")
        grants.append(grant)
    return grants


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
                 max_total_tokens: int = 0, execution_timeout_sec: int = 0,
                 continuation_budgets: list | str | None = None,
                 verification_turns: int = 0, verification_repairs: int = 0,
                 verification_coverage_review: bool = False, **kwargs):
        super().__init__(logs_dir=logs_dir, model_name=model_name, **kwargs)
        if not model_name or not model_name.strip():
            raise ValueError("YHC requires --model provider/model (or --ak provider=...)")
        if type(max_turns) is not int or max_turns < 0:
            raise ValueError("max_turns must be a nonnegative integer; 0 is unlimited")
        for name, value in (("max_provider_calls", max_provider_calls),
                            ("max_total_tokens", max_total_tokens)):
            if type(value) is not int or not 0 <= value <= 2**63 - 1:
                raise ValueError(f"{name} must be a nonnegative int64; 0 disables")
        if type(execution_timeout_sec) is not int or not 0 <= execution_timeout_sec <= 9223372036:
            raise ValueError("execution_timeout_sec must be nonnegative and fit Go time.Duration")
        self.execution_timeout_sec = execution_timeout_sec
        self.max_provider_calls = max_provider_calls
        self.max_total_tokens = max_total_tokens
        self.continuation_budgets = parse_continuation_budgets(continuation_budgets)
        if self.continuation_budgets and not (max_provider_calls or max_total_tokens):
            raise ValueError("continuation_budgets requires a finite initial usage limit")
        for name, value, upper in (("verification_turns", verification_turns, 32),
                                   ("verification_repairs", verification_repairs, 3)):
            if type(value) is not int or not 0 <= value <= upper:
                raise ValueError(f"{name} must be an integer in 0..{upper}")
        if not verification_turns and verification_repairs:
            raise ValueError("verification_repairs requires verification_turns")
        if type(verification_coverage_review) is not bool or (
                verification_coverage_review and not verification_turns):
            raise ValueError("verification_coverage_review must be a bool and requires verification_turns")
        if verification_turns and (not max_provider_calls or any(
                not grant["max_provider_calls"] for grant in self.continuation_budgets)):
            raise ValueError("verification requires a finite provider-call limit in every segment")
        self.verification_turns = verification_turns
        self.verification_repairs = verification_repairs
        self.verification_coverage_review = verification_coverage_review
        self._segment_session = None
        self._segment_timeout = None
        self._segment_limits = None
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
        if self._segment_session:
            argv += ["--resume", self._segment_session]
            if self.verification_turns:
                argv += ["--resume-verification"]
        if self.execution_timeout_sec:
            argv += ["--timeout", self._segment_timeout or f"{self.execution_timeout_sec}s"]
        limits = self._segment_limits or {"max_provider_calls": self.max_provider_calls,
                                          "max_total_tokens": self.max_total_tokens}
        for name, value in limits.items():
            if value:
                argv += ["--" + name.replace("_", "-"), str(value)]
        if self.verification_turns:
            argv += ["--verification-turns", str(self.verification_turns),
                     "--verification-repairs", str(self.verification_repairs)]
        if self.verification_coverage_review:
            argv += ["--verification-coverage-review"]
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
        self._segment_session = None
        self._segment_timeout = None
        self._segment_limits = None
        await self.upload_instruction(instruction, environment)
        deadline = time.monotonic() + self.execution_timeout_sec if self.execution_timeout_sec else None
        segment = 0
        while True:
            if deadline is not None:
                remaining_ms = int((deadline - time.monotonic()) * 1000)
                if remaining_ms <= 0:
                    raise asyncio.TimeoutError("YHC invocation deadline exhausted before continuation")
                self._segment_timeout = f"{remaining_ms}ms"
            # Keep context empty between segments, so Harbor's post-run hook can
            # harvest a final cancelled stream after bounded process cleanup.
            try:
                execution = await environment.exec(command=self.execution_command(), env=self.execution_env())
            except asyncio.CancelledError:
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
            result = read_result(self.logs_dir / "yhc.jsonl")
            usage = validated_usage(result.get("usage"))
            session_id = result.get("session_id")
            error = result.get("error")
            if self._segment_session and session_id != self._segment_session:
                self.populate_context_post_run(context)
                raise ValueError("YHC continuation changed session identity")
            can_continue = (
                execution.return_code == result["exit_code"] == 1
                and result["status"] == "failed"
                and result.get("terminal_reason") == "run_budget_exceeded"
                and isinstance(error, dict) and error.get("code") == "run_budget_exceeded"
                and usage is not None and usage["complete"]
                and isinstance(session_id, str) and bool(session_id.strip())
                and segment < len(self.continuation_budgets)
            )
            if not can_continue:
                self.populate_context_post_run(context)
                if execution.return_code != 0:
                    raise NonZeroAgentExitCodeError(
                        f"YHC exited with code {execution.return_code}; inspect yhc.jsonl and segment logs")
                if result["exit_code"] != 0 or result["status"] != "completed":
                    raise ValueError("YHC process exit and terminal result disagree")
                return
            # Archive before starting the next process: yhc.jsonl always contains
            # the latest segment, and previous usage/history is never overwritten.
            archive = f"yhc.segment-{segment:04d}"
            shutil.copyfile(self.logs_dir / "yhc.jsonl", self.logs_dir / f"{archive}.jsonl")
            source = shlex.quote(str(self.environment_logs_dir / "yhc.jsonl"))
            target = shlex.quote(str(self.environment_logs_dir / f"{archive}.jsonl"))
            stderr = shlex.quote(str(self.environment_logs_dir / "yhc.stderr.log"))
            saved_stderr = shlex.quote(str(self.environment_logs_dir / f"{archive}.stderr.log"))
            # Mounted logs may already contain the local copy; mv replaces it
            # with the identical source. Task workspace and .yhc stay in place.
            await self.exec_as_agent(environment, command=(
                f"mv -- {source} {target} && "
                f"if [ -f {stderr} ]; then mv -- {stderr} {saved_stderr}; fi"))
            (self.logs_dir / "yhc.jsonl").unlink(missing_ok=True)
            self._segment_session = session_id
            self._segment_limits = self.continuation_budgets[segment]
            segment += 1
            await self.upload_instruction(
                "Continue the original task using the existing session and workspace. "
                "The previous invocation stopped at its usage budget. A new finite allowance "
                "is now authorized. Inspect saved progress, retain completed work, and finish "
                "remaining steps; do not repeat completed side effects.", environment)

    async def upload_instruction(self, instruction: str, environment: BaseEnvironment) -> None:
        with tempfile.TemporaryDirectory(prefix="yhc-instruction-") as directory:
            prompt = Path(directory) / "instruction.txt"
            prompt.write_text(instruction, encoding="utf-8")
            await environment.upload_file(prompt, self.remote_prompt)
        await self.exec_as_root(environment, command=f"chmod 644 {shlex.quote(self.remote_prompt)}")

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
            archives = sorted(self.logs_dir.glob("yhc.segment-*.jsonl"))
            segments = []
            for path in [*archives, self.logs_dir / "yhc.jsonl"]:
                try:
                    saved = read_result(path)
                    segments.append({"log": path.name, "usage": validated_usage(saved.get("usage")),
                                     "session_id": saved.get("session_id"),
                                     "terminal_reason": saved.get("terminal_reason")})
                except (OSError, ValueError):
                    segments.append({"log": path.name, "usage": None})
            usage = sum_usage([segment["usage"] for segment in segments])
            if archives:
                metadata["continuation"] = {"segments": len(segments), "history": segments,
                                            "authorized_grants": self.continuation_budgets}
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
            # Preserve settled prior segments even if the latest process was killed.
            archives = sorted(self.logs_dir.glob("yhc.segment-*.jsonl"))
            if archives:
                history = []
                for path in archives:
                    try:
                        saved = read_result(path)
                        history.append({"log": path.name, "usage": validated_usage(saved.get("usage"))})
                    except (OSError, ValueError):
                        history.append({"log": path.name, "usage": None})
                metadata["continuation"] = {"segments": len(archives) + 1, "history": history,
                                            "usage_complete": False}
        context.metadata = {**(context.metadata or {}), "yhc": metadata}
        # Incomplete usage remains metadata-only; never infer a currency cost.
