#!/usr/bin/env bash
set -euo pipefail

readonly source_root="$(git rev-parse --show-toplevel)"
readonly fixture_parent="$(mktemp -d)"
readonly fixture_root="$fixture_parent/repository"
cleanup_fixture() {
	local cleanup_status=$?
	trap - EXIT
	rm -rf "$fixture_parent"
	exit "$cleanup_status"
}
trap cleanup_fixture EXIT

mkdir -p "$fixture_root"
git -C "$source_root" archive HEAD | tar -x -C "$fixture_root"
mkdir -p "$fixture_root/.codex/hooks"
cp "$source_root/.codex/hooks.json" "$fixture_root/.codex/hooks.json"
cp "$source_root/.codex/hooks/iteration.sh" "$fixture_root/.codex/hooks/iteration.sh"

git -C "$fixture_root" init -q
git -C "$fixture_root" config user.name "Iteration Hook Test"
git -C "$fixture_root" config user.email "iteration-hook@example.invalid"
git -C "$fixture_root" add .
git -C "$fixture_root" commit -qm "test fixture"
git -C "$fixture_root" update-ref refs/remotes/origin/master HEAD

run_hook() {
	local command="$1"
	local event="$2"
	local suffix="${3:-}"
	local stdout_file="$fixture_parent/stdout"
	local stderr_file="$fixture_parent/stderr"
	local cwd="$fixture_root/scripts"
	printf '{"session_id":"adapter-session","cwd":"%s","hook_event_name":"%s"%s}\n' \
		"$cwd" "$event" "$suffix" |
		(cd "$cwd" && bash "$fixture_root/.codex/hooks/iteration.sh" "$command") \
		>"$stdout_file" 2>"$stderr_file"
}

run_hook session-start SessionStart ',"source":"startup"'
if [[ -s "$fixture_parent/stdout" ]]; then
	echo "clean SessionStart unexpectedly emitted context" >&2
	exit 1
fi

printf '\n' >>"$fixture_root/Makefile"
for event_spec in \
	"post-tool-use PostToolUse" \
	"subagent-start SubagentStart" \
	"subagent-stop SubagentStop"; do
	read -r command event <<<"$event_spec"
	suffix=""
	if [[ "$command" == subagent-* ]]; then
		suffix=',"agent_id":"adapter-child","agent_type":"worker"'
	fi
	run_hook "$command" "$event" "$suffix"
	if [[ -s "$fixture_parent/stdout" ]]; then
		echo "$event unexpectedly emitted stdout" >&2
		exit 1
	fi
done

run_hook stop Stop
if ! grep -q '"decision": "block"' "$fixture_parent/stdout"; then
	echo "Stop did not emit the one-shot stale-evidence decision" >&2
	exit 1
fi
run_hook stop Stop
if [[ -s "$fixture_parent/stdout" ]]; then
	echo "second Stop repeated the one-shot decision" >&2
	exit 1
fi
run_hook session-end SessionEnd
if [[ -s "$fixture_parent/stdout" ]]; then
	echo "SessionEnd unexpectedly emitted stdout" >&2
	exit 1
fi

python3 -m json.tool "$fixture_root/.codex/hooks.json" >/dev/null
(cd "$fixture_root" && go run ./scripts/iteration plan >/dev/null)
mv "$fixture_root/.codex/hooks.json" "$fixture_root/.codex/hooks.disabled.json"
(cd "$fixture_root" && go run ./scripts/iteration plan >/dev/null)
