#!/usr/bin/env bash
set -euo pipefail

readonly repository_root="$(git rev-parse --show-toplevel)"
exec go -C "$repository_root" run ./scripts/iteration hook "$@"
