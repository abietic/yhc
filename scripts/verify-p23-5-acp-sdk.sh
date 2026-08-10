#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
harness_root=$(mktemp -d /tmp/eino-p23-5-sdk.XXXXXX)

cleanup() {
	case "$harness_root" in
	/tmp/eino-p23-5-sdk.*)
		rm -rf -- "$harness_root"
		;;
	esac
}
trap cleanup EXIT HUP INT TERM

npm install \
	--prefix "$harness_root/sdk" \
	--silent \
	--no-audit \
	--no-fund \
	@agentclientprotocol/sdk@1.3.0 \
	zod@4

(
	cd "$repo_root"
	go build -o "$harness_root/yhc" ./cmd/yhc
)

P23_ACP_SDK_ENTRY="$harness_root/sdk/node_modules/@agentclientprotocol/sdk/dist/acp.js" \
	P23_AGENT_BINARY="$harness_root/yhc" \
	node "$repo_root/server/acp/testdata/p23_5_typescript_sdk_harness.mjs"
