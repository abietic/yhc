#!/usr/bin/env python3
"""Validate the project migration skill bundle without loading Codex internals."""

from __future__ import annotations

from pathlib import Path
import re
import sys

import yaml


LIFECYCLE_SKILL = "skill-runtime"
LIFECYCLE_REFERENCE = "$skill-runtime"
ITERATION_WORKFLOW_SKILL = "iteration-workflow"
ITERATION_WORKFLOW_REFERENCE = "$iteration-workflow"
LOCAL_ONLY_EINO_SKILLS = frozenset((
    "eino-agent",
    "eino-component",
    "eino-compose",
    "eino-guide",
))
LOCAL_ONLY_MATT_SKILLS = frozenset((
    "ask-matt",
    "code-review",
    "codebase-design",
    "diagnosing-bugs",
    "domain-modeling",
    "grill-me",
    "grill-with-docs",
    "grilling",
    "handoff",
    "implement",
    "improve-codebase-architecture",
    "prototype",
    "research",
    "resolving-merge-conflicts",
    "setup-matt-pocock-skills",
    "tdd",
    "teach",
    "to-spec",
    "to-tickets",
    "triage",
    "wayfinder",
    "writing-great-skills",
))
LOCAL_ONLY_SUPERPOWERS_SKILLS = frozenset((
    "brainstorming",
    "writing-plans",
))
LOCAL_ONLY_UPSTREAM_SKILLS = LOCAL_ONLY_MATT_SKILLS | LOCAL_ONLY_SUPERPOWERS_SKILLS
LOGGED_SKILLS = frozenset((
    "migration-slice",
    ITERATION_WORKFLOW_SKILL,
    "reference-parity-audit",
    "tui-runtime-change",
    "runtime-depth-change",
    "write-docs",
    "defect-investigation",
    "human-agent-docs",
    "migration-loop",
))
RETIRED_SKILLS = frozenset(("iteration-closeout",))
RETIRED_SKILL_REFERENCES = ("$iteration-closeout",)
ANALYZED_SKILLS = LOGGED_SKILLS | frozenset((LIFECYCLE_SKILL,))
EXPLICIT_ONLY_SKILLS = frozenset(("migration-loop",))
SHORT_DESCRIPTION_MIN = 25
SHORT_DESCRIPTION_MAX = 64
REQUIRED_LOG_MARKERS = ("skill_log.py start", "skill_log.py event", "skill_log.py finish")
REQUIRED_DELEGATION_MARKERS = (
    "skill_log.py delegate-start",
    "skill_log.py delegate-finish",
    "skill_log.py delegate-assess",
)
TELEMETRY_ADMISSION_MARKERS = ("admission", "准入")
FORBIDDEN_SKILL_REFERENCES = ("$kimi-offload",)
FINAL_REPOSITORY_GATE_MARKERS = ("make fmt", "make lint", "make test", "make build")
ITERATION_WORKFLOW_MARKERS = (
    LIFECYCLE_REFERENCE,
    "make change-plan",
    "make verify-focused",
    "make verify-merge",
    "make change-evidence",
    "clean committed tree",
    "blocked",
    "not-applicable",
    "Child completion",
    "parent acceptance",
)
WORKFLOW_CALLER_INVARIANTS = {
    "migration-loop": ("align", "plan", "execute", "MIGRATION_LOOP_SIGNAL"),
    "migration-slice": ("accepted slice", "preserve", "adapt", "combine", "project-native", "compatibility"),
    "reference-parity-audit": ("comparative evidence", "adoption recommendation"),
    "runtime-depth-change": ("ordering", "cancellation", "fallback", "recovery", "lifecycle"),
    "tui-runtime-change": ("reducer", "queue", "projection", "PTY", "terminal"),
    "write-docs": ("source-backed", "ownership", "lifecycle", "examples", "diagrams"),
    "defect-investigation": ("reproduction", "causal", "regression"),
}


def discover_skills(skills_root: Path) -> list[str]:
    return sorted(
        path.parent.name
        for path in skills_root.glob("*/SKILL.md")
        if path.is_file()
    )


def validate_retired_references(owner: str, text: str) -> list[str]:
    errors: list[str] = []
    for reference in RETIRED_SKILL_REFERENCES:
        if reference in text:
            errors.append(
                f"{owner} contains retired reference {reference!r}; use "
                f"{ITERATION_WORKFLOW_REFERENCE}"
            )
    return errors


def validate_iteration_routing(name: str, text: str) -> list[str]:
    errors = validate_retired_references(name, text)
    folded = text.casefold()
    if name in RETIRED_SKILLS:
        errors.append(f"{name} is a retired skill; use {ITERATION_WORKFLOW_REFERENCE}")
    if name in WORKFLOW_CALLER_INVARIANTS:
        if ITERATION_WORKFLOW_REFERENCE not in text:
            errors.append(
                f"{name} must hand off shared mechanics to {ITERATION_WORKFLOW_REFERENCE}"
            )
        for sentinel in WORKFLOW_CALLER_INVARIANTS[name]:
            if sentinel.casefold() not in folded:
                errors.append(f"{name} lost domain invariant {sentinel!r}")

    if name != ITERATION_WORKFLOW_SKILL and all(
        marker in text for marker in FINAL_REPOSITORY_GATE_MARKERS
    ):
        errors.append(
            f"{name} contains a raw repository gate list instead of routing to "
            f"{ITERATION_WORKFLOW_REFERENCE}"
        )
    return errors


def validate(root: Path) -> list[str]:
    errors: list[str] = []
    skills_root = root / ".agents" / "skills"
    skills = discover_skills(skills_root)
    if not skills:
        return [f"no project skills found under {skills_root.relative_to(root)}"]
    if LOGGED_SKILLS.intersection(skills):
        lifecycle_file = skills_root / LIFECYCLE_SKILL / "SKILL.md"
        if not lifecycle_file.is_file():
            errors.append(
                f"missing shared lifecycle skill {lifecycle_file.relative_to(root)}"
            )
        else:
            lifecycle_text = lifecycle_file.read_text(encoding="utf-8")
            for marker in REQUIRED_LOG_MARKERS + REQUIRED_DELEGATION_MARKERS:
                if marker not in lifecycle_text:
                    errors.append(
                        f"missing shared lifecycle marker {marker!r} in "
                        f"{lifecycle_file.relative_to(root)}"
                    )
        workflow_file = skills_root / ITERATION_WORKFLOW_SKILL / "SKILL.md"
        if not workflow_file.is_file():
            errors.append(
                f"missing shared iteration workflow {workflow_file.relative_to(root)}"
            )
        else:
            workflow_text = workflow_file.read_text(encoding="utf-8")
            for marker in ITERATION_WORKFLOW_MARKERS:
                if marker not in workflow_text:
                    errors.append(
                        f"missing iteration workflow marker {marker!r} in "
                        f"{workflow_file.relative_to(root)}"
                    )
    for name in skills:
        skill_file = skills_root / name / "SKILL.md"
        agent_file = skills_root / name / "agents" / "openai.yaml"
        if not skill_file.is_file():
            errors.append(f"missing {skill_file.relative_to(root)}")
            continue
        text = skill_file.read_text(encoding="utf-8")
        match = re.match(r"^---\n(.*?)\n---\n", text, re.DOTALL)
        if not match:
            errors.append(f"invalid frontmatter: {skill_file.relative_to(root)}")
        else:
            try:
                metadata = yaml.safe_load(match.group(1))
            except yaml.YAMLError as exc:
                errors.append(
                    f"invalid frontmatter YAML in "
                    f"{skill_file.relative_to(root)}: {exc}"
                )
                metadata = {}
            if not isinstance(metadata, dict) or metadata.get("name") != name:
                errors.append(f"wrong name in {skill_file.relative_to(root)}")
            if (
                name not in LOCAL_ONLY_UPSTREAM_SKILLS
                and isinstance(metadata, dict)
                and set(metadata) != {"name", "description"}
            ):
                errors.append(
                    f"frontmatter must contain only name and description in "
                    f"{skill_file.relative_to(root)}"
                )
            if (
                not isinstance(metadata, dict)
                or not isinstance(metadata.get("description"), str)
                or not metadata["description"].strip()
            ):
                errors.append(f"missing description in {skill_file.relative_to(root)}")
        if name in LOGGED_SKILLS:
            if LIFECYCLE_REFERENCE not in text:
                errors.append(
                    f"missing shared lifecycle reference {LIFECYCLE_REFERENCE!r} in "
                    f"{skill_file.relative_to(root)}"
                )
            if not any(
                marker in text.casefold() for marker in TELEMETRY_ADMISSION_MARKERS
            ):
                errors.append(
                    f"missing telemetry admission rule in "
                    f"{skill_file.relative_to(root)}"
                )
        for reference in FORBIDDEN_SKILL_REFERENCES:
            if reference in text:
                errors.append(
                    f"removed skill reference {reference!r} in {skill_file.relative_to(root)}"
                )
        for routing_error in validate_iteration_routing(name, text):
            errors.append(f"{routing_error} in {skill_file.relative_to(root)}")
        if not agent_file.is_file():
            if name not in LOCAL_ONLY_SUPERPOWERS_SKILLS:
                errors.append(f"missing {agent_file.relative_to(root)}")
        else:
            agent_text = agent_file.read_text(encoding="utf-8")
            errors.extend(
                validate_retired_references(
                    str(agent_file.relative_to(root)), agent_text
                )
            )
            try:
                metadata = yaml.safe_load(agent_text)
            except yaml.YAMLError as exc:
                errors.append(f"invalid YAML in {agent_file.relative_to(root)}: {exc}")
            else:
                interface = metadata.get("interface", {}) if isinstance(metadata, dict) else {}
                required_interface_fields = ("display_name", "short_description")
                if name not in LOCAL_ONLY_UPSTREAM_SKILLS:
                    required_interface_fields += ("default_prompt",)
                for field in required_interface_fields:
                    if not isinstance(interface.get(field), str) or not interface[field].strip():
                        errors.append(
                            f"missing interface.{field} in "
                            f"{agent_file.relative_to(root)}"
                        )
                display_name = interface.get("display_name", "")
                if (
                    name not in LOCAL_ONLY_EINO_SKILLS
                    and isinstance(display_name, str)
                    and display_name.strip().casefold().startswith("eino ")
                ):
                    errors.append(
                        f"interface.display_name must not use the legacy Eino prefix in "
                        f"{agent_file.relative_to(root)}"
                    )
                if name in ANALYZED_SKILLS:
                    short_description = interface.get("short_description", "")
                    if isinstance(short_description, str) and not (
                        SHORT_DESCRIPTION_MIN
                        <= len(short_description.strip())
                        <= SHORT_DESCRIPTION_MAX
                    ):
                        errors.append(
                            f"interface.short_description must be "
                            f"{SHORT_DESCRIPTION_MIN}-{SHORT_DESCRIPTION_MAX} characters in "
                            f"{agent_file.relative_to(root)}"
                        )
                    default_prompt = interface.get("default_prompt", "")
                    if isinstance(default_prompt, str) and f"${name}" not in default_prompt:
                        errors.append(
                            f"interface.default_prompt must mention ${name} in "
                            f"{agent_file.relative_to(root)}"
                        )
                if name in EXPLICIT_ONLY_SKILLS:
                    policy = metadata.get("policy", {}) if isinstance(metadata, dict) else {}
                    if policy.get("allow_implicit_invocation") is not False:
                        errors.append(
                            f"policy.allow_implicit_invocation must be false in "
                            f"{agent_file.relative_to(root)}"
                        )
        for line_number, line in enumerate(text.splitlines(), start=1):
            if line.rstrip() != line:
                errors.append(
                    f"trailing whitespace in "
                    f"{skill_file.relative_to(root)}:{line_number}"
                )
        if not text.endswith("\n"):
            errors.append(f"missing final newline in {skill_file.relative_to(root)}")
    return errors


def main() -> int:
    root = Path(__file__).resolve().parents[2]
    errors = validate(root)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"validated {len(discover_skills(root / '.agents' / 'skills'))} project skills")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
