.PHONY: build clear test test-contract test-race test-pty test-fuzz-smoke test-e2e test-risk test-fault-injection test-fuzz-deep test-e2e-deep test-pty-deep eval-baseline run debug fmt fmt-check lint lint-new docs-check docs-check-ci verify verify-focused verify-merge verify-deep check-boundaries change-plan change-evidence change-evidence-ready iteration-policy-check iteration-metrics iteration-hook-benchmark worktree-audit publication-inventory publication-check-policy publication-scan-expression publication-materialize publication-check-tree publication-manifest publication-safety prepare-publication-tools prepare-govulncheck prepare-gitleaks prepare-cyclonedx-gomod vuln-check secret-check license-check sbom verify-publication verify-publication-tree setup-git-hooks prepare-gofumpt prepare-golangci-lint prepare-golangci-lint-v2 prepare-gotestsum prepare-dlv

SHELL := /bin/bash
.SHELLFLAGS := -o pipefail -c

BUILD_DIR := build
GO ?= go
GOFM := $(shell $(GO) env GOPATH)/bin/gofumpt
GOLINT := $(shell $(GO) env GOPATH)/bin/golangci-lint
GOTEST := $(shell $(GO) env GOPATH)/bin/gotestsum
DLV := $(shell $(GO) env GOPATH)/bin/dlv
GOLINT_V2_DIR := $(abspath $(BUILD_DIR)/tools)
GOLINT_V2 := $(GOLINT_V2_DIR)/golangci-lint-v2
EVAL_OUTPUT_DIR ?= $(BUILD_DIR)/evaluation
EVAL_BINARY ?= $(EVAL_OUTPUT_DIR)/yhc$(if $(filter windows,$(shell $(GO) env GOHOSTOS)),.exe,)
EVAL_REPORT ?= $(EVAL_OUTPUT_DIR)/p43-report.json
EVAL_SCENARIO ?= localized-write-fix/v1
E2E_OUTPUT_DIR ?= $(BUILD_DIR)/e2e
E2E_BINARY ?= $(E2E_OUTPUT_DIR)/yhc$(if $(filter windows,$(shell $(GO) env GOHOSTOS)),.exe,)

GOFUMPT_VERSION ?= v0.7.0
GOLANGCI_LINT_VERSION ?= v1.64.8
GOLANGCI_LINT_V2_VERSION ?= 2.12.2
GOTESTSUM_VERSION ?= v1.13.0
GOVULNCHECK_VERSION := v1.6.0
# v8.30.1 is intentionally not used: its directory scanner can silently miss
# findings. Keep this pin coupled to the canary in secret-check.
GITLEAKS_VERSION := v8.29.1
CYCLONEDX_GOMOD_VERSION := v1.10.0
LINT_NEW_BASE ?= origin/master
TEST_CONTRACT_TIMEOUT ?= 3m
TEST_RACE_TIMEOUT ?= 5m
TEST_PTY_TIMEOUT ?= 3m
TEST_FUZZ_TIME ?= 5s
TEST_FUZZ_TIMEOUT ?= 2m
TEST_E2E_TIMEOUT ?= 10m
TEST_FUZZ_DEEP_TIME ?= 30s
TEST_DEEP_TIMEOUT ?= 10m
ITERATION_BASE ?= origin/master
ITERATION_FORMAT ?= markdown
ITERATION_SLICE_ID ?=
HOOK_BENCHMARK_RUNS ?= 7
WORKTREE_AUDIT_BASE ?= origin/master
WORKTREE_AUDIT_FORMAT ?= text
PUBLICATION_CONFIG ?= quality/publication.yaml
PUBLICATION_ROOT ?=
PUBLICATION_OUTPUT ?=
PUBLICATION_SOURCE_COMMIT ?=
PUBLICATION_MANIFEST_OUTPUT ?= $(PUBLICATION_ROOT)/PUBLICATION_MANIFEST.json
PUBLICATION_TOOLS_DIR := $(abspath $(BUILD_DIR)/tools/publication)
PUBLICATION_REPORT_DIR := $(abspath $(BUILD_DIR)/publication)
GOVULNCHECK := $(PUBLICATION_TOOLS_DIR)/govulncheck
GITLEAKS := $(PUBLICATION_TOOLS_DIR)/gitleaks
CYCLONEDX_GOMOD := $(PUBLICATION_TOOLS_DIR)/cyclonedx-gomod
PUBLICATION_SBOM_GENERATED := $(BUILD_DIR)/publication/sbom.cdx.json
PUBLICATION_LICENSE_REPORT := $(BUILD_DIR)/publication/dependency-licenses.json

SOURCES := $(shell find cmd engine internal server tools -type f -name '*.go')

# Default provider config (override via ENV or make args)
PROV ?= agenticdeepseek
PROV_API_KEY ?= $(ANTHROPIC_AUTH_TOKEN)
PROV_MODEL ?= deepseek-v4-flash

# Release flags
VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/abietic/yhc/internal/buildinfo.Version=$(VERSION)

# Debug flags: disable inlining and optimizations so breakpoints work correctly
GC_FLAGS_DEBUG := all=-N -l
# Delve server port (compatible with VSCode, GoLand, Trae)
DLV_PORT ?= 2345

# ── Build ──────────────────────────────────────────────
build: build/linux-amd64/yhc build/darwin-amd64/yhc build/darwin-arm64/yhc build/windows-amd64/yhc.exe

build/linux-amd64/yhc: $(SOURCES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/yhc/

build/darwin-amd64/yhc: $(SOURCES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)/darwin-amd64
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/yhc/

build/darwin-arm64/yhc: $(SOURCES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)/darwin-arm64
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/yhc/

build/windows-amd64/yhc.exe: $(SOURCES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)/windows-amd64
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/yhc/

# ── Clear ──────────────────────────────────────────────
clear:
	rm -rf $(BUILD_DIR)

# ── Test ───────────────────────────────────────────────
test: prepare-gotestsum
	@mkdir -p $(BUILD_DIR)
	$(GOTEST) --junitfile $(BUILD_DIR)/test-report.xml -- -coverprofile=$(BUILD_DIR)/coverage.out -covermode=atomic -count=1 ./...
	$(GO) -C third_party/acp-go-sdk test ./...
	$(GO) tool cover -html=$(BUILD_DIR)/coverage.out -o $(BUILD_DIR)/coverage.html
	@echo "Coverage report: $(BUILD_DIR)/coverage.html"

# Deterministic contract pack for the runtime loop, durable replay, and ACP
# delivery boundaries. These focused packs supplement rather than replace test.
test-contract:
	$(GO) test ./engine -run '^(TestScenarioMultiTurnConversationWithToolCalls|TestScenarioPTLRecoveryTriggersCompactionThenContinues|TestScenarioInterruptionDuringToolExecutionPropagatesCancellation|TestGoldenAbortDuringStreaming|TestGoldenEventOrdering|TestGoldenTerminalEventIsLast|TestQueuedFollowUpStartsAfterPriorTerminalAndPersistsOnce|TestAutoCompactRestartUsesDurableBoundaryNotOriginalHistory)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)
	$(GO) test ./engine/session -run '^(TestP234aReplaySnapshotMatchesResumeAndIsMutationIsolated|TestP234aReplaySnapshotFailsClosed|TestTranscriptAtomicReplacePreservesOnCrash)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)
	$(GO) test ./server/acp -run '^(TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts|TestP234bLoadDeliveryFailureAbortsWithoutRegistrationOrPersistence|TestP235ACPStdioLoadResumeAndExactActiveReconnect)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)

# Race detector pack for shared-state settlements that have deterministic
# synchronization. Keep this scoped; the ordinary full-suite gate remains test.
test-race:
	$(GO) test -race ./engine -run '^(TestPermissionCoordinatorClassifierWinsUserRaceExactlyOnce|TestP243ClaimPauseRaceHasOnePermanentWinner|TestP234aRestoreStagingCommitAbortRace)$$' -count=1 -timeout=$(TEST_RACE_TIMEOUT)
	$(GO) test -race ./server/acp -run '^(TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts|TestP235ACPStdioLoadResumeAndExactActiveReconnect)$$' -count=1 -timeout=$(TEST_RACE_TIMEOUT)

# Unix PTY smoke proves terminal bytes, process lifecycle, resize, and mode
# restoration. It is not a visual or physical-terminal oracle.
test-pty:
	@if [[ "$$($(GO) env GOOS)" == "windows" ]]; then \
		echo "test-pty is Unix-only"; \
	else \
		$(GO) test ./cmd/yhc/cmd ./internal/tui -run '^(TestP245aPlainGoalWorkflowPTY|TestTUITerminalRestorationPTY|TestTUITerminalShutdownRestoresTermiosAndKillsOwnedShellTreePTY|TestTUIWorkflowPTY)$$' -count=1 -timeout=$(TEST_PTY_TIMEOUT); \
	fi

# Go runs one fuzz target per invocation. Bound each smoke run so it remains a
# discovery aid instead of an unbounded default gate.
test-fuzz-smoke:
	$(GO) test ./engine/commands -run '^$$' -fuzz '^FuzzParseCommandInput$$' -fuzztime=$(TEST_FUZZ_TIME) -timeout=$(TEST_FUZZ_TIMEOUT)
	$(GO) test ./internal/tui -run '^$$' -fuzz '^FuzzWidthProfileClusterAndControlPreservation$$' -fuzztime=$(TEST_FUZZ_TIME) -timeout=$(TEST_FUZZ_TIMEOUT)
	$(GO) test ./internal/tui -run '^$$' -fuzz '^FuzzP272AnnotationRoundTrip$$' -fuzztime=$(TEST_FUZZ_TIME) -timeout=$(TEST_FUZZ_TIMEOUT)

# Hermetic product-correctness pack: real binary plus exact engine, ACP,
# permission-race, and PTY entrypoint oracles. It uses no external provider.
test-e2e: $(E2E_BINARY)
	EINO_E2E_BINARY=$(abspath $(E2E_BINARY)) $(GO) test ./scripts/e2e -count=1 -timeout=$(TEST_E2E_TIMEOUT)
	$(GO) test ./engine -run '^(TestGoldenEventOrdering|TestGoldenTerminalEventIsLast|TestCanonicalProjectGraphQueryTrace)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)
	$(GO) test ./server/acp -run '^(TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts|TestP235ACPStdioLoadResumeAndExactActiveReconnect)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)
	$(GO) test -race ./engine -run '^TestPermissionCoordinatorClassifierWinsUserRaceExactlyOnce$$' -count=1 -timeout=$(TEST_RACE_TIMEOUT)
	$(MAKE) test-pty

$(E2E_BINARY): $(SOURCES) go.mod go.sum
	@mkdir -p $(E2E_OUTPUT_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/yhc/

test-risk: test-contract test-race test-pty

# Opt-in discovery packs stop at the first failing target through verify-deep.
# They diagnose risk; they do not promote merge evidence or repair failures.
test-fault-injection:
	$(GO) test ./engine -run '^(TestScenarioInterruptionDuringToolExecutionPropagatesCancellation|TestP234aRestoreStagingCommitAbortRace|TestQueryMalformedToolArgsYieldsErrorToolResult|TestP138ProjectGraphInterruptResumeExecutesToolExactlyOnce)$$' -count=1 -timeout=$(TEST_DEEP_TIMEOUT)

test-fuzz-deep:
	$(GO) test ./engine/commands -run '^$$' -fuzz '^FuzzParseCommandInput$$' -fuzztime=$(TEST_FUZZ_DEEP_TIME) -timeout=$(TEST_DEEP_TIMEOUT)
	$(GO) test ./internal/tui -run '^$$' -fuzz '^FuzzWidthProfileClusterAndControlPreservation$$' -fuzztime=$(TEST_FUZZ_DEEP_TIME) -timeout=$(TEST_DEEP_TIMEOUT)
	$(GO) test ./internal/tui -run '^$$' -fuzz '^FuzzP272AnnotationRoundTrip$$' -fuzztime=$(TEST_FUZZ_DEEP_TIME) -timeout=$(TEST_DEEP_TIMEOUT)

test-e2e-deep: $(E2E_BINARY)
	EINO_E2E_BINARY=$(abspath $(E2E_BINARY)) $(GO) test ./scripts/e2e -count=3 -timeout=$(TEST_DEEP_TIMEOUT)

test-pty-deep:
	@if [[ "$$($(GO) env GOOS)" == "windows" ]]; then \
		echo "test-pty-deep is Unix-only"; \
	else \
		$(GO) test ./cmd/yhc/cmd ./internal/tui -run '^(TestP245aPlainGoalWorkflowPTY|TestTUITerminalRestorationPTY|TestTUIWorkflowPTY)$$' -count=3 -timeout=$(TEST_DEEP_TIMEOUT); \
	fi

# ── Run ────────────────────────────────────────────────
.PHONY: run
run:
	PROV=$(PROV) PROV_API_KEY=$(PROV_API_KEY) PROV_MODEL=$(PROV_MODEL) $(GO) run ./cmd/yhc/

# ── Debug ───────────────────────────────────────────────
# Launches the agent under delve in headless mode so any IDE can attach.
# Connect from VSCode / GoLand / Trae via "Go Remote" or "Attach to Process"
# targeting localhost:$(DLV_PORT).
#
# Usage:
#   make debug                            # interactive delve shell
#   make debug DLV_PORT=2345              # headless server on port 2345
#   make debug PROV=claude PROV_API_KEY=sk-ant-...  # with provider config
debug: prepare-dlv build/debug/yhc
	PROV=$(PROV) PROV_API_KEY=$(PROV_API_KEY) PROV_MODEL=$(PROV_MODEL) \
		$(DLV) exec ./build/debug/yhc \
			--headless \
			--listen=:$(DLV_PORT) \
			--api-version=2 \
			--accept-multiclient \
			--log

# Build a debug binary (no stripping, no inlining).
build/debug/yhc: $(SOURCES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)/debug
	$(GO) build -gcflags="$(GC_FLAGS_DEBUG)" -o $@ ./cmd/yhc/

# ── Opt-in evaluation ─────────────────────────────────
# This target is intentionally outside verify, required CI, and release builds.
eval-baseline: $(EVAL_BINARY)
	@mkdir -p $(EVAL_OUTPUT_DIR)
	@chmod 700 $(EVAL_OUTPUT_DIR)
	$(GO) run ./scripts/evaluation \
		--binary $(abspath $(EVAL_BINARY)) \
		--scenario $(EVAL_SCENARIO) \
		--report $(abspath $(EVAL_REPORT))

$(EVAL_BINARY): $(SOURCES) go.mod go.sum
	@mkdir -p $(EVAL_OUTPUT_DIR)
	@chmod 700 $(EVAL_OUTPUT_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/yhc/

# ── Fmt ────────────────────────────────────────────────
fmt: prepare-gofumpt
	$(GOFM) -l -w .

fmt-check: prepare-gofumpt
	@files="$$($(GOFM) -l .)"; \
	if [[ -n "$$files" ]]; then \
		echo "The following files need gofumpt:" >&2; \
		echo "$$files" >&2; \
		exit 1; \
	fi

# ── Lint ───────────────────────────────────────────────
lint: prepare-golangci-lint
	$(GOLINT) run ./...

# v2 understands the current Go toolchain. It only rejects findings introduced
# after master until the existing v1 baseline is deliberately remediated.
lint-new: prepare-golangci-lint-v2
	$(GOLINT_V2) run --config .golangci.v2.yml --new-from-merge-base=$(LINT_NEW_BASE) ./...

# ── Required gates ────────────────────────────────────
docs-check:
	$(GO) run ./scripts/docs_check
	$(GO) run ./scripts/migration_queue check
	$(GO) run ./scripts/migration_manifest.go check
	$(GO) run ./scripts/iteration policy-check

docs-check-ci:
	$(GO) run ./scripts/docs_check
	$(GO) run ./scripts/migration_queue check
	$(GO) run ./scripts/migration_manifest.go check-ledger
	$(GO) run ./scripts/iteration policy-check

verify: fmt-check lint test build

# Persist immutable, diff-bound local evidence. Merge verification requires
# current focused evidence for the same digest and begins with a clean-tree gate.
verify-focused:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) verify --level focused

verify-merge:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) verify --level merge

check-boundaries:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) boundaries

verify-deep:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) deep

# Select quality owners and evidence requirements for the current tracked diff.
# S1 is read-only: these targets do not execute or persist any selected gate.
change-plan:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) plan

change-evidence:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) evidence

change-evidence-ready:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --head HEAD --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) evidence --require-ready

iteration-policy-check:
	$(GO) run ./scripts/iteration policy-check

# Read-only aggregate of retained local gate evidence. It is intentionally
# advisory and is not part of verification, hooks, or CI.
iteration-metrics:
	$(GO) run ./scripts/iteration metrics --format $(ITERATION_FORMAT)

# Explicit advisory fixture benchmark; it does not change hooks, CI, or verification.
iteration-hook-benchmark:
	$(GO) run ./scripts/iteration hook-benchmark --runs $(HOOK_BENCHMARK_RUNS) --format json

# Read-only worktree inventory for preservation and manual cleanup review.
worktree-audit:
	$(GO) run ./scripts/worktree_audit --base $(WORKTREE_AUDIT_BASE) --format $(WORKTREE_AUDIT_FORMAT)

publication-inventory:
	@install -d -m 0700 $(BUILD_DIR)/publication
	$(GO) run ./scripts/publication inventory --config $(PUBLICATION_CONFIG) --output $(BUILD_DIR)/publication/inventory.json

publication-check-policy:
	$(GO) run ./scripts/publication check --config $(PUBLICATION_CONFIG)

publication-scan-expression:
	@test -n "$(PUBLICATION_ROOT)" || { echo "PUBLICATION_ROOT is required" >&2; exit 2; }
	$(GO) run ./scripts/publication scan-expression --config $(PUBLICATION_CONFIG) --root $(PUBLICATION_ROOT)

publication-materialize:
	@test -n "$(PUBLICATION_SOURCE_COMMIT)" || { echo "PUBLICATION_SOURCE_COMMIT is required" >&2; exit 2; }
	@test -n "$(PUBLICATION_OUTPUT)" || { echo "PUBLICATION_OUTPUT is required" >&2; exit 2; }
	$(GO) run ./scripts/publication materialize --config $(PUBLICATION_CONFIG) --source-commit $(PUBLICATION_SOURCE_COMMIT) --output $(PUBLICATION_OUTPUT)

publication-check-tree:
	@test -n "$(PUBLICATION_ROOT)" || { echo "PUBLICATION_ROOT is required" >&2; exit 2; }
	$(GO) run ./scripts/publication check-tree --config $(PUBLICATION_CONFIG) --root $(PUBLICATION_ROOT)

publication-manifest:
	@test -n "$(PUBLICATION_ROOT)" || { echo "PUBLICATION_ROOT is required" >&2; exit 2; }
	$(GO) run ./scripts/publication manifest --config $(PUBLICATION_CONFIG) --root $(PUBLICATION_ROOT) --output $(PUBLICATION_MANIFEST_OUTPUT)

# Publication security tools are installed under ignored build state and are
# accepted only when Go's embedded module metadata matches the exact pin.
prepare-publication-tools: prepare-govulncheck prepare-gitleaks prepare-cyclonedx-gomod

prepare-govulncheck:
	@install -d -m 0700 $(PUBLICATION_TOOLS_DIR)
	@if ! $(GO) version -m $(GOVULNCHECK) 2>/dev/null | awk '$$1 == "mod" && $$2 == "golang.org/x/vuln" && $$3 == "$(GOVULNCHECK_VERSION)" { found = 1 } END { exit !found }'; then \
		GOBIN=$(PUBLICATION_TOOLS_DIR) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION); \
	fi

prepare-gitleaks:
	@install -d -m 0700 $(PUBLICATION_TOOLS_DIR)
	@if ! $(GO) version -m $(GITLEAKS) 2>/dev/null | awk '$$1 == "mod" && $$2 == "github.com/zricethezav/gitleaks/v8" && $$3 == "$(GITLEAKS_VERSION)" { found = 1 } END { exit !found }'; then \
		GOBIN=$(PUBLICATION_TOOLS_DIR) $(GO) install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION); \
	fi

prepare-cyclonedx-gomod:
	@install -d -m 0700 $(PUBLICATION_TOOLS_DIR)
	@if ! $(GO) version -m $(CYCLONEDX_GOMOD) 2>/dev/null | awk '$$1 == "mod" && $$2 == "github.com/CycloneDX/cyclonedx-gomod" && $$3 == "$(CYCLONEDX_GOMOD_VERSION)" { found = 1 } END { exit !found }'; then \
		GOBIN=$(PUBLICATION_TOOLS_DIR) $(GO) install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_GOMOD_VERSION); \
	fi

vuln-check: prepare-govulncheck
	@install -d -m 0700 $(PUBLICATION_REPORT_DIR)
	$(GOVULNCHECK) -json ./... > $(PUBLICATION_REPORT_DIR)/govulncheck.json

# The canary makes a scanner regression fail closed before any candidate tree
# is inspected. Its synthetic token is assembled from short pieces so the
# repository itself never stores a provider-token-shaped value.
secret-check: prepare-gitleaks
	@test -n "$(PUBLICATION_ROOT)" || { echo "PUBLICATION_ROOT is required" >&2; exit 2; }
	@install -d -m 0700 $(PUBLICATION_REPORT_DIR)
	@canary_dir="$$(mktemp -d /tmp/yhc-gitleaks-canary.XXXXXX)"; \
		cleanup() { case "$$canary_dir" in /tmp/yhc-gitleaks-canary.*) rm -rf -- "$$canary_dir" ;; esac; }; \
		trap cleanup EXIT; \
		printf '%s%s%s%s\n' 'gh' 'p_' 'A1b2C3d4E5f6G7h8I9' 'j0K1l2M3n4O5p6Q7r8' > "$$canary_dir/canary.txt"; \
		set +e; \
		$(GITLEAKS) dir --no-banner --no-color --redact=100 --exit-code 23 --report-format json --report-path "$$canary_dir/report.json" "$$canary_dir" >/dev/null 2>&1; \
		canary_status=$$?; \
		set -e; \
		if [[ $$canary_status -ne 23 ]]; then echo "gitleaks canary was not detected" >&2; exit 1; fi
	@task_scan_root="$(PUBLICATION_ROOT)"; \
		task_tree_parent=""; \
		cleanup() { if [[ -n "$$task_tree_parent" ]]; then case "$$task_tree_parent" in /tmp/yhc-gitleaks-tree.*) rm -rf -- "$$task_tree_parent" ;; esac; fi; }; \
		trap cleanup EXIT; \
		if [[ -e "$$task_scan_root/.git" ]]; then \
			if [[ "$$task_scan_root" != "." ]]; then echo "Git source secret-check requires PUBLICATION_ROOT=." >&2; exit 2; fi; \
			task_tree_parent="$$(mktemp -d /tmp/yhc-gitleaks-tree.XXXXXX)"; \
			task_source_commit="$$(git rev-parse HEAD)"; \
			$(GO) run ./scripts/publication materialize --config $(PUBLICATION_CONFIG) --source-commit "$$task_source_commit" --output "$$task_tree_parent/tree"; \
			task_scan_root="$$task_tree_parent/tree"; \
		fi; \
		$(GITLEAKS) dir --no-banner --no-color --redact=100 --exit-code 23 --report-format json --report-path $(PUBLICATION_REPORT_DIR)/gitleaks.json "$$task_scan_root"

sbom: prepare-cyclonedx-gomod
	@install -d -m 0700 $(PUBLICATION_REPORT_DIR)
	@set -e; \
		task_tree_parent="$$(mktemp -d /tmp/yhc-sbom-tree.XXXXXX)"; \
		cleanup() { case "$$task_tree_parent" in /tmp/yhc-sbom-tree.*) rm -rf -- "$$task_tree_parent" ;; esac; }; \
		trap cleanup EXIT; \
		task_source_commit="$$(git rev-parse HEAD)"; \
		$(GO) run ./scripts/publication materialize --config $(PUBLICATION_CONFIG) --source-commit "$$task_source_commit" --output "$$task_tree_parent/tree"; \
		cd "$$task_tree_parent/tree"; \
		GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(CYCLONEDX_GOMOD) mod -licenses -test -json -noserial -notimestamp -output $(abspath $(PUBLICATION_SBOM_GENERATED)) .

license-check: sbom
	$(GO) run ./scripts/publication licenses --config $(PUBLICATION_CONFIG) --root . --sbom $(abspath $(PUBLICATION_SBOM_GENERATED)) --output $(PUBLICATION_LICENSE_REPORT)

# CI runs each public-release safety check on every change. These commands are
# intentionally separate so a later prerequisite cannot hide a skipped gate.
publication-safety:
	$(MAKE) publication-check-policy
	$(MAKE) publication-scan-expression PUBLICATION_ROOT=.
	$(MAKE) vuln-check
	$(MAKE) license-check
	$(MAKE) secret-check PUBLICATION_ROOT=.

verify-publication:
	@git diff --quiet && git diff --cached --quiet && test -z "$$(git status --porcelain --untracked-files=all)" || { echo "verify-publication requires a clean source tree" >&2; exit 1; }
	$(MAKE) publication-inventory
	$(MAKE) publication-check-policy
	$(MAKE) publication-scan-expression PUBLICATION_ROOT=.
	$(MAKE) vuln-check
	$(MAKE) secret-check PUBLICATION_ROOT=.
	$(MAKE) license-check

verify-publication-tree:
	@test ! -e .git || { echo "verify-publication-tree requires a tree without .git" >&2; exit 1; }
	$(MAKE) publication-check-tree PUBLICATION_ROOT=.
	$(MAKE) publication-scan-expression PUBLICATION_ROOT=.
	$(MAKE) secret-check PUBLICATION_ROOT=.
	$(MAKE) vuln-check
	$(MAKE) license-check

# ── Repository setup ──────────────────────────────────
setup-git-hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks enabled from .githooks"

# ── Prepare tools ──────────────────────────────────────
prepare-gofumpt:
	@command -v $(GOFM) >/dev/null 2>&1 || $(GO) install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)

prepare-golangci-lint:
	@$(GOLINT) version 2>/dev/null | grep -q "version $(GOLANGCI_LINT_VERSION)" || GOTOOLCHAIN=local $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

prepare-golangci-lint-v2:
	@mkdir -p $(GOLINT_V2_DIR)
	@if ! $(GOLINT_V2) version 2>/dev/null | grep -q "version $(GOLANGCI_LINT_V2_VERSION)"; then \
		GOBIN=$(GOLINT_V2_DIR) GOTOOLCHAIN=local $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_V2_VERSION); \
		mv $(GOLINT_V2_DIR)/golangci-lint $(GOLINT_V2); \
	fi

prepare-gotestsum:
	@$(GO) version -m $(GOTEST) 2>/dev/null | grep -q "gotest.tools/gotestsum $(GOTESTSUM_VERSION)" || $(GO) install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

prepare-dlv:
	@command -v $(DLV) >/dev/null 2>&1 || $(GO) install github.com/go-delve/delve/cmd/dlv@latest

# ── Public canonical cutover recovery ──────────────────
.PHONY: cutover-recovery-capture cutover-recovery-verify

CUTOVER_PRIVATE_ROOT ?=
CUTOVER_PUBLIC_ROOT ?=
CUTOVER_ARCHIVE_ROOT ?=
CUTOVER_INPUT ?=
CUTOVER_MANIFEST ?=
CUTOVER_PHASE ?= pre-move

cutover-recovery-capture:
	@test -n "$(CUTOVER_PRIVATE_ROOT)" || { echo "CUTOVER_PRIVATE_ROOT is required" >&2; exit 2; }
	@test -n "$(CUTOVER_PUBLIC_ROOT)" || { echo "CUTOVER_PUBLIC_ROOT is required" >&2; exit 2; }
	@test -n "$(CUTOVER_ARCHIVE_ROOT)" || { echo "CUTOVER_ARCHIVE_ROOT is required" >&2; exit 2; }
	@test -n "$(CUTOVER_INPUT)" || { echo "CUTOVER_INPUT is required" >&2; exit 2; }
	@test -n "$(CUTOVER_MANIFEST)" || { echo "CUTOVER_MANIFEST is required" >&2; exit 2; }
	$(GO) run ./scripts/cutover_recovery capture --private-root "$(CUTOVER_PRIVATE_ROOT)" --public-root "$(CUTOVER_PUBLIC_ROOT)" --archive-root "$(CUTOVER_ARCHIVE_ROOT)" --input "$(CUTOVER_INPUT)" --output "$(CUTOVER_MANIFEST)"

cutover-recovery-verify:
	@test -n "$(CUTOVER_MANIFEST)" || { echo "CUTOVER_MANIFEST is required" >&2; exit 2; }
	@test -n "$(CUTOVER_PHASE)" || { echo "CUTOVER_PHASE is required" >&2; exit 2; }
	$(GO) run ./scripts/cutover_recovery verify --manifest "$(CUTOVER_MANIFEST)" --phase "$(CUTOVER_PHASE)"
