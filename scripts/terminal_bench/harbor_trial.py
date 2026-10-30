"""Pinned Harbor evaluator seam: separate verification without any network.

Use IsolatedVerifierTrial.create and IsolatedDockerEnvironment together. The
agent's network policy is unchanged; unqualified verifier configurations fail
before the native agent starts. This module requires requirements.txt.
"""

import asyncio
import copy
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import tempfile

from harbor.environments.docker.docker import DockerEnvironment
from harbor.models.task.config import NetworkMode, TaskOS, VerifierEnvironmentMode
from harbor.models.task.verifier_mode import resolve_effective_verifier_env_config
from harbor.trial.trial import Trial

from scripts.terminal_bench.trial_policy import (
    PolicyError, verify_compose, verify_container, verify_proof_directory,
)


def _immutable_image(image):
    return (isinstance(image, str) and "://" not in image
            and re.fullmatch(r"(?:[A-Za-z0-9][A-Za-z0-9._:/-]*@)?sha256:[0-9a-f]{64}", image) is not None)


def isolated_task(task):
    """Clone runtime policy, retaining immutable task files and agent semantics."""
    cloned = copy.copy(task)
    cfg = task.config.model_copy(deep=True)
    # Resolve every inherited definition before changing any defaults.
    definitions = [(step, resolve_effective_verifier_env_config(task.config, step))
                   for step in (task.config.steps or [None])]
    targets = cfg.steps or [None]
    for target, (_, original) in zip(targets, definitions):
        if original is None:
            raise PolicyError("shared_verifier_unqualified")
        if original.os != TaskOS.LINUX or not _immutable_image(original.docker_image):
            raise PolicyError("verifier_requires_pinned_linux_image")
        environment = original.model_copy(deep=True)
        environment.network_mode = NetworkMode.NO_NETWORK
        environment.allowed_hosts = []
        verifier = cfg.verifier if target is None else target.verifier
        verifier.environment_mode = VerifierEnvironmentMode.SEPARATE
        verifier.environment = environment
        verifier.network_mode = NetworkMode.NO_NETWORK
        verifier.allowed_hosts = []
    cloned.config = cfg
    return cloned


class IsolatedVerifierTrial(Trial):
    @classmethod
    async def create(cls, config):
        if config.source_trial is not None:
            raise PolicyError("source_trial_recovery_unqualified")
        return await super().create(config)

    @staticmethod
    async def _load_task(config):
        if version("harbor") != "0.22.0":
            raise PolicyError("unqualified_harbor_version")
        if config.verifier.disable or config.environment.import_path is None:
            raise PolicyError("isolated_verifier_adapter_required")
        # Qualify the actual configured class, not its string name.
        from harbor.environments.factory import import_class
        cls = import_class(config.environment.import_path, label="environment")
        if not issubclass(cls, IsolatedDockerEnvironment):
            raise PolicyError("isolated_verifier_adapter_required")
        task, download = await Trial._load_task(config)
        return isolated_task(task), download


class IsolatedDockerEnvironment(DockerEnvironment):
    """Only an explicit all-no-network role enters this restricted topology.

    Rendered Compose is checked before startup; running-container attestation
    finishes inside start(), before Harbor can upload model-produced artifacts.
    The agent's public/allowlist environment still uses Harbor's normal path.
    """

    @staticmethod
    def _requires_egress_control(*, startup_network_policy, phase_network_policies):
        policies = [startup_network_policy, *phase_network_policies]
        if all(policy.network_mode == NetworkMode.NO_NETWORK for policy in policies):
            # Native Docker none needs no networked nftables sidecar.
            return False
        return DockerEnvironment._requires_egress_control(
            startup_network_policy=startup_network_policy,
            phase_network_policies=phase_network_policies)

    @property
    def capabilities(self):
        capabilities = super().capabilities
        if (self.network_policy.network_mode == NetworkMode.NO_NETWORK
                and all(p.network_mode == NetworkMode.NO_NETWORK for p in self._phase_network_policies)):
            return capabilities.model_copy(update={"disable_internet": True, "dynamic_network_policy": False})
        return capabilities

    def __init__(self, *args, verifier_proof_dir=None, **kwargs):
        super().__init__(*args, **kwargs)
        self._isolated = self.network_policy.network_mode == NetworkMode.NO_NETWORK
        self._isolation_overlay_dir = None
        self._isolation_overlay = None
        self._compose_verified = False
        if verifier_proof_dir is None:
            raise PolicyError("host_owned_verifier_proof_required")
        self._proof_directory = Path(verifier_proof_dir)
        verify_proof_directory(self._proof_directory, {"services": {"main": {"volumes": [
            {"type": "bind", "source": str(self.trial_paths.agent_dir.resolve())},
            {"type": "bind", "source": str(self.trial_paths.verifier_dir.resolve())},
            {"type": "bind", "source": str(self.trial_paths.artifacts_dir.resolve())},
            {"type": "bind", "source": str(self.trial_paths.user_agent_dir.resolve())},
        ]}}})
        if not self._isolated:
            return
        if (self._is_windows_container or self._enable_egress_control
                or any(p.network_mode != NetworkMode.NO_NETWORK for p in self._phase_network_policies)):
            raise PolicyError("verifier_network_transition_unqualified")
        image = self.task_env_config.docker_image or ""
        if not _immutable_image(image):
            raise PolicyError("verifier_requires_pinned_linux_image")
        directory = self._proof_directory
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        self._log_dir = str(self.trial_paths.verifier_dir.resolve())
        if directory == Path(self._log_dir) or Path(self._log_dir) in directory.parents:
            raise PolicyError("verifier_proof_exposed_to_container")
        name = hashlib.sha256(self.session_id.encode()).hexdigest() + ".json"
        self.verifier_proof_path = directory / name
        if self.verifier_proof_path.exists() or self.verifier_proof_path.is_symlink():
            raise PolicyError("verifier_proof_already_exists")
        self._isolation_overlay_dir = tempfile.TemporaryDirectory(prefix="yhc-verifier-")
        self._isolation_overlay = Path(self._isolation_overlay_dir.name) / "none.yaml"
        # Reset inherited networks explicitly. Compose's rendered output, not
        # this intended overlay, is the admission fact checked below.
        self._isolation_overlay.write_text(
            "services:\n  main:\n    network_mode: none\n    networks: !reset []\n"
            "    pull_policy: never\n")

    @property
    def _docker_compose_paths(self):
        paths = super()._docker_compose_paths
        if self._isolation_overlay:
            paths.append(self._isolation_overlay)
        return paths

    async def _run_docker_compose_command(self, command, **kwargs):
        if command[0] in ("up", "create", "start", "run"):
            rendered = await super()._run_docker_compose_command(["config", "--format", "json"])
            config = json.loads(rendered.stdout)
            verify_proof_directory(self._proof_directory, config)
            if self._isolated:
                verify_compose(config, image=self.task_env_config.docker_image,
                               log_dir=self._log_dir)
                self._compose_verified = True
        return await super()._run_docker_compose_command(command, **kwargs)

    async def _apply_network_policy(self, network_policy):
        if self._isolated:
            raise PolicyError("verifier_network_transition_unqualified")
        return await super()._apply_network_policy(network_policy)

    async def start(self, force_build):
        if not self._isolated:
            return await super().start(force_build)
        if force_build:
            raise PolicyError("verifier_build_unqualified")
        # Fail before Harbor can pull a missing image or start any container.
        image = self.task_env_config.docker_image
        cached = await self._inspect("image", image)
        content_id = cached.get("Id", "")
        if (cached.get("Os") != "linux" or not isinstance(content_id, str)
                or not re.fullmatch(r"sha256:[0-9a-f]{64}", content_id)
                or image.startswith("sha256:") and content_id != image):
            raise PolicyError("verifier_cached_image_unverified")
        await super().start(force_build=False)
        if not self._compose_verified:
            raise PolicyError("verifier_compose_unverified")
        result = await super()._run_docker_compose_command(["ps", "--quiet", "main"])
        ids = result.stdout.split()
        if len(ids) != 1:
            raise PolicyError("verifier_container_unverified")
        row = await self._inspect("container", ids[0])
        verify_container(row, image=content_id, log_dir=self._log_dir)
        proof = dict(version=1, session_id=self.session_id, container_id=row["Id"],
                     image_id=row["Image"], image_reference=image, network_mode="none", compose_verified=True,
                     stage="before_artifact_upload", can_resume_agent=False)
        with self.verifier_proof_path.open("x") as out:
            json.dump(proof, out, sort_keys=True)
            out.write("\n")
            out.flush()
            os.fsync(out.fileno())
        self.verifier_proof_path.chmod(0o400)

    async def stop(self, delete):
        try:
            return await super().stop(delete)
        finally:
            if delete and self._isolation_overlay_dir:
                self._isolation_overlay_dir.cleanup()

    @staticmethod
    async def _inspect(kind, identity):
        process = await asyncio.create_subprocess_exec(
            "docker", kind, "inspect", identity, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE)
        try:
            out, _ = await asyncio.wait_for(process.communicate(), timeout=30)
        except BaseException:
            if process.returncode is None:
                process.kill()
            await process.communicate()
            raise
        if process.returncode:
            raise PolicyError("verifier_inspect_failed")
        rows = json.loads(out)
        if len(rows) != 1:
            raise PolicyError("verifier_inspect_failed")
        return rows[0]


def collect_verifier_proofs(trial):
    """Read host proofs using Harbor's actual (possibly shortened) session IDs."""
    directory = Path(trial.config.environment.kwargs["verifier_proof_dir"])
    keys = [step.name for step in (trial.task.config.steps or [])] or ["trial"]
    proofs = []
    for key in keys:
        session = trial._separate_verifier_session_id(key)
        path = directory / (hashlib.sha256(session.encode()).hexdigest() + ".json")
        if not path.is_file() or path.is_symlink():
            return []
        try:
            row = json.loads(path.read_text())
        except (OSError, ValueError):
            return []
        if not isinstance(row, dict):
            return []
        if (row.get("session_id") != session or row.get("network_mode") != "none"
                or row.get("compose_verified") is not True or row.get("stage") != "before_artifact_upload"):
            return []
        proofs.append(row)
    return proofs
