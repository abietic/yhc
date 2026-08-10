package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	enginediagnostics "github.com/abietic/yhc/engine/diagnostics"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/transcript"
)

const diagnosticConfigReadLimit = 1 << 20

// DiagnosticsSnapshot returns the single source-derived diagnostic read model
// used by every supported command entrypoint. It never estimates tokens,
// exposes credential values, or performs a provider network request.
func (e *QueryEngine) DiagnosticsSnapshot(
	ctx context.Context,
) (enginediagnostics.Snapshot, error) {
	if e == nil {
		return enginediagnostics.Snapshot{}, errors.New("diagnostics require a query engine")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return enginediagnostics.Snapshot{}, err
	}

	e.mu.Lock()
	sessionID := e.config.SessionID
	modelName := e.config.Model
	cwd := e.config.CWD
	fallbackModel := e.config.FallbackModel
	resolver := e.config.ModelResolver
	recorder := e.transcript
	clock := e.config.Clock
	messages := append([]*schema.Message(nil), e.messages...)
	e.mu.Unlock()
	permissionMode := e.PermissionMode()
	toolCount := len(e.GetToolNames())
	if clock == nil {
		clock = time.Now
	}
	observedAt := clock().UTC()
	known := func(source string) enginediagnostics.FieldMeta {
		return enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateKnown,
			Source:     source,
			ObservedAt: observedAt,
		}
	}
	unavailable := func(source, detail string) enginediagnostics.FieldMeta {
		return enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateUnavailable,
			Source:     source,
			ObservedAt: observedAt,
			Detail:     detail,
		}
	}

	snapshot := enginediagnostics.Snapshot{ObservedAt: observedAt}
	snapshot.Status.SessionID = stringField(sessionID, known("engine.session"), "session identity is empty")
	snapshot.Status.CWD = stringField(cwd, known("engine.cwd"), "working directory is empty")
	snapshot.Status.MessageCount = intField(len(messages), known("engine.active-messages"))
	snapshot.Status.ToolCount = intField(toolCount, known("engine.tool-registry"))
	snapshot.Context.TotalMessages = intField(len(messages), known("engine.active-messages"))
	populateContextContributors(&snapshot.Context, messages, observedAt)
	snapshot.Context.TransientContributors = enginediagnostics.StringField{
		FieldMeta: unavailable(
			"model-call-assembly",
			"system prompt, tool schemas, prefetch content, and provider normalization are assembled per request and are not persisted as attributable token fields",
		),
	}

	resolved, resolveErr := resolveDiagnosticRoute(resolver, modelName)
	if resolveErr == nil {
		providerField := enginediagnostics.StringField{
			FieldMeta: known(diagnosticSource(resolved.Sources.Provider)),
			Value:     string(resolved.Provider),
		}
		modelField := enginediagnostics.StringField{
			FieldMeta: known(diagnosticSource(resolved.Sources.Model)),
			Value:     resolved.Model,
		}
		snapshot.Status.Provider = providerField
		snapshot.Status.Model = modelField
		snapshot.Context.Model = modelField
		snapshot.Config.Provider = providerField
		snapshot.Config.Model = modelField
		snapshot.Config.CredentialConfigured = enginediagnostics.BoolField{
			FieldMeta: known(diagnosticSource(resolved.Sources.APIKey)),
			Value:     resolved.CredentialConfigured || strings.TrimSpace(resolved.APIKey) != "",
		}
		endpoint, endpointOK := redactDiagnosticEndpoint(resolved.BaseURL)
		if endpointOK {
			snapshot.Config.Endpoint = enginediagnostics.StringField{
				FieldMeta: known(diagnosticSource(resolved.Sources.BaseURL)),
				Value:     endpoint,
			}
		} else {
			snapshot.Config.Endpoint = enginediagnostics.StringField{
				FieldMeta: unavailable(
					diagnosticSource(resolved.Sources.BaseURL),
					"the effective endpoint is empty or cannot be rendered without exposing sensitive components",
				),
			}
		}
	} else {
		detail := "the active provider route could not be resolved from the injected runtime"
		providerMeta := unavailable("provider-resolver", detail)
		modelMeta := known("engine.model")
		if strings.TrimSpace(modelName) == "" {
			modelMeta = unavailable("engine.model", "no active model is configured")
		}
		modelField := enginediagnostics.StringField{FieldMeta: modelMeta, Value: modelName}
		snapshot.Status.Provider = enginediagnostics.StringField{FieldMeta: providerMeta}
		snapshot.Status.Model = modelField
		snapshot.Context.Model = modelField
		snapshot.Config.Provider = enginediagnostics.StringField{FieldMeta: providerMeta}
		snapshot.Config.Model = modelField
		snapshot.Config.CredentialConfigured = enginediagnostics.BoolField{FieldMeta: providerMeta}
		snapshot.Config.Endpoint = enginediagnostics.StringField{FieldMeta: providerMeta}
	}

	if window, ok := modelcaps.KnownContextWindow(modelName); ok {
		snapshot.Context.ContextWindowTokens = intField(window, known("model-capability-table"))
	} else {
		snapshot.Context.ContextWindowTokens = enginediagnostics.IntField{
			FieldMeta: unavailable(
				"model-capability-table",
				"the active model has no authoritative context-window entry or explicit context suffix",
			),
		}
	}

	loadResult, transcriptMeta, transcriptDetail := loadDiagnosticTranscript(recorder, observedAt)
	snapshot.Status.Transcript = enginediagnostics.StringField{
		FieldMeta: transcriptMeta,
		Value:     transcriptDetail,
	}
	populateUsageDiagnostics(&snapshot, loadResult, transcriptMeta, messages, observedAt)

	if snapshot.Context.CurrentInputTokens.State == enginediagnostics.StateKnown ||
		snapshot.Context.CurrentInputTokens.State == enginediagnostics.StateStale {
		if snapshot.Context.ContextWindowTokens.State == enginediagnostics.StateKnown &&
			snapshot.Context.ContextWindowTokens.Value > 0 {
			meta := snapshot.Context.CurrentInputTokens.FieldMeta
			meta.Source = "provider-response-meta + model-capability-table"
			snapshot.Context.UsagePercent = intField(
				snapshot.Context.CurrentInputTokens.Value*100/snapshot.Context.ContextWindowTokens.Value,
				meta,
			)
		} else {
			snapshot.Context.UsagePercent = enginediagnostics.IntField{
				FieldMeta: unavailable(
					"context-diagnostics",
					"usage percent requires both current provider input tokens and an authoritative context window",
				),
			}
		}
	} else {
		snapshot.Context.UsagePercent = enginediagnostics.IntField{
			FieldMeta: unavailable(
				"provider-response-meta",
				"the latest persisted model response has no authoritative input-token metadata",
			),
		}
	}
	snapshot.Status.ContextPercent = snapshot.Context.UsagePercent
	snapshot.Status.UsageCoverage = snapshot.Usage.Coverage

	snapshot.Config.FallbackModel = stringField(
		fallbackModel,
		known("engine.fallback-model"),
		"no fallback model is configured",
	)
	snapshot.Config.PermissionMode = enginediagnostics.StringField{
		FieldMeta: known("engine.permission-mode"),
		Value:     string(permissionMode),
	}
	snapshot.Config.Precedence = enginediagnostics.StringField{
		FieldMeta: known("provider-resolution-policy"),
		Value:     "effective values are resolved field-by-field; each field reports its winning source",
	}

	checks, err := e.buildDoctorChecks(
		ctx,
		observedAt,
		cwd,
		toolCount,
		permissionMode,
		resolved,
		resolveErr,
		transcriptMeta,
		recorder != nil,
	)
	if err != nil {
		return enginediagnostics.Snapshot{}, err
	}
	snapshot.Doctor.Checks = checks
	return snapshot, nil
}

func stringField(value string, meta enginediagnostics.FieldMeta, emptyDetail string) enginediagnostics.StringField {
	if strings.TrimSpace(value) == "" && meta.State == enginediagnostics.StateKnown {
		meta.State = enginediagnostics.StateUnavailable
		meta.Detail = emptyDetail
	}
	return enginediagnostics.StringField{FieldMeta: meta, Value: value}
}

func intField(value int, meta enginediagnostics.FieldMeta) enginediagnostics.IntField {
	return enginediagnostics.IntField{FieldMeta: meta, Value: value}
}

func diagnosticSource(source string) string {
	if strings.TrimSpace(source) == "" {
		return "provider-resolver"
	}
	return source
}

func resolveDiagnosticRoute(resolver ModelResolver, modelName string) (provider.ResolvedConfig, error) {
	if resolver == nil {
		return provider.ResolvedConfig{}, errors.New("provider resolver unavailable")
	}
	if strings.TrimSpace(modelName) == "" {
		return provider.ResolvedConfig{}, errors.New("active model unavailable")
	}
	resolved, err := resolver.ResolveModel(modelName)
	if err != nil {
		return provider.ResolvedConfig{}, err
	}
	if strings.TrimSpace(string(resolved.Provider)) == "" || strings.TrimSpace(resolved.Model) == "" {
		return provider.ResolvedConfig{}, errors.New("provider resolver returned an incomplete route")
	}
	return resolved, nil
}

func redactDiagnosticEndpoint(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "configured (value redacted)", true
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), true
}

func populateContextContributors(
	contextSnapshot *enginediagnostics.ContextSnapshot,
	messages []*schema.Message,
	observedAt time.Time,
) {
	if contextSnapshot == nil {
		return
	}
	meta := enginediagnostics.FieldMeta{
		State:      enginediagnostics.StateKnown,
		Source:     "engine.active-messages",
		ObservedAt: observedAt,
	}
	var userMessages, assistantMessages, toolMessages, systemMessages, toolCalls int
	for _, message := range messages {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.User:
			userMessages++
		case schema.Assistant:
			assistantMessages++
			toolCalls += len(message.ToolCalls)
		case schema.Tool:
			toolMessages++
		case schema.System:
			systemMessages++
		}
	}
	contextSnapshot.UserMessages = intField(userMessages, meta)
	contextSnapshot.AssistantMessages = intField(assistantMessages, meta)
	contextSnapshot.ToolMessages = intField(toolMessages, meta)
	contextSnapshot.SystemMessages = intField(systemMessages, meta)
	contextSnapshot.ToolCalls = intField(toolCalls, meta)
}

func loadDiagnosticTranscript(
	recorder *transcript.Recorder,
	observedAt time.Time,
) (*transcript.LoadResult, enginediagnostics.FieldMeta, string) {
	if recorder == nil || recorder.Path() == "" {
		return nil, enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateUnavailable,
			Source:     "engine.transcript",
			ObservedAt: observedAt,
			Detail:     "no transcript recorder is configured",
		}, "unavailable"
	}
	path := recorder.Path()
	info, statErr := os.Stat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateUnavailable,
			Source:     "transcript-file",
			ObservedAt: observedAt,
			Detail:     "the transcript file cannot be inspected",
		}, "unreadable"
	}
	result, err := recorder.LoadFull()
	if err != nil {
		return nil, enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateUnavailable,
			Source:     "transcript-jsonl",
			ObservedAt: observedAt,
			Detail:     "the transcript could not be loaded",
		}, "unreadable"
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return result, enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateKnown,
			Source:     "transcript-file",
			ObservedAt: observedAt,
			Detail:     "the session has not created a transcript file yet",
		}, "not-created"
	}
	meta := enginediagnostics.FieldMeta{
		State:      enginediagnostics.StateKnown,
		Source:     "transcript-jsonl",
		ObservedAt: info.ModTime().UTC(),
	}
	if len(result.Corruptions) > 0 {
		meta.State = enginediagnostics.StateStale
		meta.Detail = fmt.Sprintf("%d corrupt transcript record(s) were skipped", len(result.Corruptions))
		return result, meta, "partially-readable"
	}
	return result, meta, "readable"
}

func populateUsageDiagnostics(
	snapshot *enginediagnostics.Snapshot,
	loadResult *transcript.LoadResult,
	transcriptMeta enginediagnostics.FieldMeta,
	liveMessages []*schema.Message,
	observedAt time.Time,
) {
	if snapshot == nil {
		return
	}
	if loadResult == nil {
		meta := enginediagnostics.FieldMeta{
			State:      enginediagnostics.StateUnavailable,
			Source:     "transcript-response-meta",
			ObservedAt: observedAt,
			Detail:     "persisted provider usage cannot be loaded",
		}
		setUsageFields(&snapshot.Usage, transcript.UsageSummary{}, meta, "unavailable")
		snapshot.Context.CurrentInputTokens = enginediagnostics.IntField{FieldMeta: meta}
		snapshot.Context.CompactionBoundaries = enginediagnostics.IntField{FieldMeta: meta}
		return
	}
	usage := loadResult.Usage
	state := enginediagnostics.StateKnown
	detail := "all persisted model responses exposed provider usage metadata"
	coverage := "complete"
	missing := usage.ResponsesWithoutMetadata + usage.LegacyBoundariesWithoutUsage
	if usage.UnsupportedSnapshotVersion != 0 {
		state = enginediagnostics.StateStale
		detail = fmt.Sprintf(
			"a cumulative usage snapshot uses unsupported version %d",
			usage.UnsupportedSnapshotVersion,
		)
		coverage = "partial: unsupported usage snapshot"
	}
	if transcriptMeta.State == enginediagnostics.StateStale {
		state = enginediagnostics.StateStale
		detail = transcriptMeta.Detail
		coverage = "partial: transcript corruption"
	}
	if missing > 0 {
		state = enginediagnostics.StateStale
		detail = fmt.Sprintf(
			"%d persisted response or legacy boundary record(s) lack attributable provider usage metadata",
			missing,
		)
		coverage = "partial"
	}
	if usage.ResponsesWithMetadata == 0 && usage.UnsupportedSnapshotVersion != 0 {
		state = enginediagnostics.StateUnavailable
		detail = fmt.Sprintf(
			"persisted cumulative usage uses unsupported snapshot version %d",
			usage.UnsupportedSnapshotVersion,
		)
		coverage = "unavailable: unsupported usage snapshot"
	} else if usage.ResponsesWithMetadata == 0 && missing > 0 {
		state = enginediagnostics.StateUnavailable
		detail = "persisted model responses do not contain provider usage metadata"
		coverage = "unavailable"
	}
	if usage.UnsupportedSnapshotVersion == 0 &&
		usage.ResponsesWithMetadata == 0 && missing == 0 && hasUsageRelevantLiveMessage(liveMessages) {
		state = enginediagnostics.StateUnavailable
		detail = "the active conversation contains model responses but no persisted provider usage metadata"
		coverage = "unavailable"
	}
	if usage.UnsupportedSnapshotVersion == 0 &&
		usage.ResponsesWithMetadata == 0 && missing == 0 && !hasUsageRelevantLiveMessage(liveMessages) {
		detail = "no model response has been persisted; zero is authoritative for this empty usage ledger"
		coverage = "empty"
	}
	meta := enginediagnostics.FieldMeta{
		State:      state,
		Source:     "transcript-response-meta",
		ObservedAt: transcriptMeta.ObservedAt,
		Detail:     detail,
	}
	setUsageFields(&snapshot.Usage, usage, meta, coverage)
	snapshot.Context.CompactionBoundaries = intField(
		countLifecycleBoundaries(loadResult, transcript.LifecycleCompact),
		enginediagnostics.FieldMeta{
			State:      transcriptMeta.State,
			Source:     "transcript-lifecycle",
			ObservedAt: transcriptMeta.ObservedAt,
			Detail:     transcriptMeta.Detail,
		},
	)
	if usage.CurrentContextUsageKnown {
		snapshot.Context.CurrentInputTokens = intField(usage.CurrentContextPromptTokens, meta)
	} else {
		snapshot.Context.CurrentInputTokens = enginediagnostics.IntField{
			FieldMeta: enginediagnostics.FieldMeta{
				State:      enginediagnostics.StateUnavailable,
				Source:     "transcript-response-meta",
				ObservedAt: transcriptMeta.ObservedAt,
				Detail:     "the latest persisted model response has no provider input-token metadata",
			},
		}
	}
}

func setUsageFields(
	usageSnapshot *enginediagnostics.UsageSnapshot,
	usage transcript.UsageSummary,
	meta enginediagnostics.FieldMeta,
	coverage string,
) {
	usageSnapshot.PromptTokens = intField(usage.PromptTokens, meta)
	usageSnapshot.CompletionTokens = intField(usage.CompletionTokens, meta)
	usageSnapshot.TotalTokens = intField(usage.TotalTokens, meta)
	usageSnapshot.ResponsesWithMetadata = intField(usage.ResponsesWithMetadata, meta)
	usageSnapshot.ResponsesWithoutMetadata = intField(
		usage.ResponsesWithoutMetadata+usage.LegacyBoundariesWithoutUsage,
		meta,
	)
	usageSnapshot.Coverage = enginediagnostics.StringField{FieldMeta: meta, Value: coverage}
}

func hasUsageRelevantLiveMessage(messages []*schema.Message) bool {
	for _, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		if message.Extra != nil {
			if isMeta, _ := message.Extra["is_meta"].(bool); isMeta {
				continue
			}
			if isAPIError, _ := message.Extra["api_error"].(bool); isAPIError {
				continue
			}
		}
		return true
	}
	return false
}

func countLifecycleBoundaries(result *transcript.LoadResult, kind transcript.LifecycleBoundaryKind) int {
	if result == nil {
		return 0
	}
	count := 0
	for _, boundary := range result.LifecycleBoundaries {
		if boundary.Kind == kind {
			count++
		}
	}
	return count
}

func (e *QueryEngine) buildDoctorChecks(
	ctx context.Context,
	observedAt time.Time,
	cwd string,
	toolCount int,
	permissionMode permission.Mode,
	resolved provider.ResolvedConfig,
	resolveErr error,
	transcriptMeta enginediagnostics.FieldMeta,
	transcriptConfigured bool,
) ([]enginediagnostics.DoctorCheck, error) {
	checks := []enginediagnostics.DoctorCheck{
		{
			ID:      "runtime.engine",
			Outcome: enginediagnostics.CheckPass,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateKnown, Source: "query-engine", ObservedAt: observedAt,
			},
			Summary: "QueryEngine diagnostics service is available",
		},
	}
	if resolveErr != nil {
		checks = append(checks, enginediagnostics.DoctorCheck{
			ID:      "provider.route",
			Outcome: enginediagnostics.CheckFail,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateUnavailable, Source: "provider-resolver", ObservedAt: observedAt,
				Detail: "the effective provider route could not be resolved",
			},
			Summary:     "Provider route is unavailable",
			Remediation: "configure a supported provider/model and restart the runtime",
		})
		checks = append(checks, enginediagnostics.DoctorCheck{
			ID:      "provider.credential",
			Outcome: enginediagnostics.CheckSkipped,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateUnavailable, Source: "provider-resolver", ObservedAt: observedAt,
				Detail: "credential state cannot be attributed until the effective provider route resolves",
			},
			Summary:     "Provider credential state was not inspected",
			Remediation: "resolve the provider route before checking credential configuration",
		})
	} else {
		checks = append(checks, enginediagnostics.DoctorCheck{
			ID:      "provider.route",
			Outcome: enginediagnostics.CheckPass,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateKnown, Source: "provider-resolver", ObservedAt: observedAt,
			},
			Summary: fmt.Sprintf("Provider route resolves to %s/%s", resolved.Provider, resolved.Model),
		})
		authOutcome := enginediagnostics.CheckPass
		authSummary := "Provider authentication is configured"
		authRemediation := ""
		if !resolved.CredentialConfigured && strings.TrimSpace(resolved.APIKey) == "" {
			authOutcome = enginediagnostics.CheckWarn
			authSummary = "Provider authentication is not configured"
			authRemediation = "configure the selected provider through flags, environment, or the credential store"
		}
		checks = append(checks, enginediagnostics.DoctorCheck{
			ID:      "provider.credential",
			Outcome: authOutcome,
			FieldMeta: enginediagnostics.FieldMeta{
				State:      enginediagnostics.StateKnown,
				Source:     diagnosticSource(resolved.Sources.APIKey),
				ObservedAt: observedAt,
			},
			Summary: authSummary, Remediation: authRemediation,
		})
	}

	transcriptOutcome := enginediagnostics.CheckPass
	transcriptSummary := "Transcript inspection completed"
	transcriptRemediation := remediationForTranscript(transcriptMeta.State)
	if !transcriptConfigured {
		transcriptOutcome = enginediagnostics.CheckSkipped
		transcriptSummary = "No active session transcript exists in the inspection host"
		transcriptRemediation = ""
	} else if transcriptMeta.State == enginediagnostics.StateStale {
		transcriptOutcome = enginediagnostics.CheckWarn
	} else if transcriptMeta.State == enginediagnostics.StateUnavailable {
		transcriptOutcome = enginediagnostics.CheckFail
	}
	checks = append(checks, enginediagnostics.DoctorCheck{
		ID:          "session.transcript",
		Outcome:     transcriptOutcome,
		FieldMeta:   transcriptMeta,
		Summary:     transcriptSummary,
		Remediation: transcriptRemediation,
	})

	configPaths := []struct {
		id    string
		label string
		path  string
	}{
		{id: "config.user", label: "user settings", path: userSettingsPath()},
		{id: "config.project", label: "project settings", path: filepath.Join(cwd, ".claude", "settings.json")},
		{id: "config.local", label: "local settings", path: filepath.Join(cwd, ".claude", "settings.local.json")},
	}
	for _, item := range configPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		checks = append(checks, inspectDiagnosticConfigFile(item.id, item.label, item.path, observedAt))
	}

	toolOutcome := enginediagnostics.CheckPass
	toolSummary := fmt.Sprintf("%d runtime tool(s) are registered", toolCount)
	if toolCount == 0 {
		toolOutcome = enginediagnostics.CheckWarn
		toolSummary = "No runtime tools are registered"
	}
	checks = append(checks,
		enginediagnostics.DoctorCheck{
			ID:      "runtime.tools",
			Outcome: toolOutcome,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateKnown, Source: "engine.tool-registry", ObservedAt: observedAt,
			},
			Summary: toolSummary,
		},
		enginediagnostics.DoctorCheck{
			ID:      "runtime.permission-mode",
			Outcome: enginediagnostics.CheckPass,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateKnown, Source: "engine.permission-mode", ObservedAt: observedAt,
			},
			Summary: fmt.Sprintf("Permission mode is %s", permissionMode),
		},
		enginediagnostics.DoctorCheck{
			ID:      "provider.connectivity",
			Outcome: enginediagnostics.CheckSkipped,
			FieldMeta: enginediagnostics.FieldMeta{
				State: enginediagnostics.StateUnavailable, Source: "read-only-diagnostics", ObservedAt: observedAt,
				Detail: "connectivity is not probed by a read-only slash command",
			},
			Summary:     "Provider connectivity was not tested",
			Remediation: "use startup provider preflight when a network/authentication probe is required",
		},
	)
	return checks, nil
}

func userSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func inspectDiagnosticConfigFile(
	id, label, path string,
	observedAt time.Time,
) enginediagnostics.DoctorCheck {
	base := enginediagnostics.DoctorCheck{
		ID: id,
		FieldMeta: enginediagnostics.FieldMeta{
			State: enginediagnostics.StateKnown, Source: "filesystem", ObservedAt: observedAt,
		},
	}
	if strings.TrimSpace(path) == "" {
		base.Outcome = enginediagnostics.CheckSkipped
		base.FieldMeta.State = enginediagnostics.StateUnavailable
		base.Summary = label + " path is unavailable"
		return base
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		base.Outcome = enginediagnostics.CheckSkipped
		base.Summary = label + " file is not present"
		return base
	}
	if err != nil {
		base.Outcome = enginediagnostics.CheckFail
		base.Summary = label + " file is not readable"
		base.Remediation = "repair file ownership or permissions"
		return base
	}
	defer file.Close() //nolint:errcheck
	reader := bufio.NewReader(io.LimitReader(file, diagnosticConfigReadLimit+1))
	data, err := io.ReadAll(reader)
	if err != nil {
		base.Outcome = enginediagnostics.CheckFail
		base.Summary = label + " file could not be read"
		return base
	}
	if len(data) > diagnosticConfigReadLimit {
		base.Outcome = enginediagnostics.CheckFail
		base.Summary = label + " file exceeds the 1 MiB diagnostic limit"
		base.Remediation = "reduce the settings file to a bounded JSON document"
		return base
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		base.Outcome = enginediagnostics.CheckFail
		base.Summary = label + " file contains invalid JSON"
		base.Remediation = "repair the JSON before restarting the runtime"
		return base
	}
	base.Outcome = enginediagnostics.CheckPass
	base.Summary = label + " file contains valid JSON"
	return base
}

func remediationForTranscript(state enginediagnostics.FieldState) string {
	switch state {
	case enginediagnostics.StateStale:
		return "inspect the transcript corruption diagnostics before relying on partial usage"
	case enginediagnostics.StateUnavailable:
		return "repair transcript storage access and retry"
	default:
		return ""
	}
}
