#!/usr/bin/env bash
set -euo pipefail

readonly repository_root="$(git rev-parse --show-toplevel)"
readonly fixture_parent="$(mktemp -d)"
readonly fixture_root="$fixture_parent/repository"
readonly fixture_bin="$fixture_parent/bin"
readonly journal_file="$fixture_parent/go-argv.log"
readonly stdout_file="$fixture_parent/stdout"
readonly stderr_file="$fixture_parent/stderr"
readonly sha_a="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
readonly sha_b="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
readonly zero_sha="0000000000000000000000000000000000000000"
cleanup_fixture() {
	local cleanup_status=$?
	trap - EXIT
	rm -rf "$fixture_parent"
	exit "$cleanup_status"
}
trap cleanup_fixture EXIT

mkdir -p "$fixture_root/.githooks" "$fixture_bin"
cp "$repository_root/.githooks/pre-push" "$fixture_root/.githooks/pre-push"
git -C "$fixture_root" init -q

cat >"$fixture_bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${EINO_TEST_JOURNAL:?}"
exit "${EINO_TEST_GO_EXIT:-0}"
EOF
chmod +x "$fixture_bin/go"

case_status=0
invoke_hook() {
	local input="$1"
	local master_allow="$2"
	local stale_allow="$3"
	local go_exit="$4"
	: >"$journal_file"
	: >"$stdout_file"
	: >"$stderr_file"
	set +e
	printf '%b' "$input" |
		(
			cd "$fixture_root"
			env \
				PATH="$fixture_bin:$PATH" \
				EINO_TEST_JOURNAL="$journal_file" \
				EINO_TEST_GO_EXIT="$go_exit" \
				EINO_ALLOW_MASTER_PUSH="$master_allow" \
				EINO_ALLOW_STALE_EVIDENCE="$stale_allow" \
				EINO_ITERATION_BASE="origin/master" \
				bash .githooks/pre-push
		) >"$stdout_file" 2>"$stderr_file"
	case_status=$?
	set -e
}

expect_status() {
	local want="$1"
	local label="$2"
	if [[ "$case_status" -ne "$want" ]]; then
		echo "$label status=$case_status want=$want stderr=$(<"$stderr_file")" >&2
		exit 1
	fi
}

invoke_hook "refs/heads/master $sha_a refs/heads/master $zero_sha\n" 0 0 0
expect_status 1 "protected master"
if [[ -s "$journal_file" ]]; then
	echo "protected master reached evidence lookup" >&2
	exit 1
fi

invoke_hook "refs/heads/feature $sha_a refs/heads/feature $zero_sha\n" 0 0 0
expect_status 0 "ready feature"
grep -q -- "--head $sha_a evidence --require-ready" "$journal_file"

for failure in missing stale blocked; do
	invoke_hook "refs/heads/feature $sha_a refs/heads/feature $zero_sha\n" 0 0 1
	expect_status 1 "$failure evidence"
done

invoke_hook "refs/heads/feature $zero_sha refs/heads/feature $sha_a\n" 0 0 0
expect_status 0 "deletion"
if [[ -s "$journal_file" ]]; then
	echo "deletion reached evidence lookup" >&2
	exit 1
fi

invoke_hook "refs/heads/one $sha_a refs/heads/one $zero_sha\nrefs/heads/two $sha_b refs/heads/two $zero_sha\n" 0 0 0
expect_status 0 "two feature refs"
if [[ "$(wc -l <"$journal_file" | tr -d ' ')" -ne 2 ]]; then
	echo "two feature refs did not check both commits" >&2
	exit 1
fi
grep -q -- "--head $sha_a evidence --require-ready" "$journal_file"
grep -q -- "--head $sha_b evidence --require-ready" "$journal_file"

invoke_hook "refs/heads/master $sha_a refs/heads/master $zero_sha\n" 1 0 1
expect_status 1 "master-only bypass"
if [[ ! -s "$journal_file" ]]; then
	echo "master bypass incorrectly skipped evidence" >&2
	exit 1
fi

invoke_hook "refs/heads/feature $sha_a refs/heads/feature $zero_sha\n" 0 1 1
expect_status 0 "stale-evidence bypass"
if [[ -s "$journal_file" ]] || [[ "$(grep -c 'WARNING: bypassing committed iteration evidence' "$stderr_file")" -ne 1 ]]; then
	echo "stale-evidence bypass was not isolated or visible" >&2
	exit 1
fi

invoke_hook "refs/heads/master $sha_a refs/heads/master $zero_sha\n" 0 1 0
expect_status 1 "stale bypass cannot bypass master"
if [[ -s "$journal_file" ]]; then
	echo "stale bypass reached evidence after protected-master failure" >&2
	exit 1
fi

invoke_hook "refs/heads/feature not-a-sha refs/heads/feature $zero_sha\n" 0 0 0
expect_status 1 "malformed sha"
if [[ -s "$journal_file" ]]; then
	echo "malformed sha reached evidence lookup" >&2
	exit 1
fi

invoke_hook "refs/heads/feature not-a-sha refs/heads/feature $zero_sha" 0 0 0
expect_status 1 "unterminated malformed sha"
if [[ -s "$journal_file" ]]; then
	echo "unterminated malformed sha reached evidence lookup" >&2
	exit 1
fi
