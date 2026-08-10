package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	enginediagnostics "github.com/abietic/yhc/engine/diagnostics"
)

type diagnosticsEngine interface {
	DiagnosticsSnapshot(context.Context) (enginediagnostics.Snapshot, error)
}

func executeStatus(ctx *CommandContext, _ string) (*CommandResult, error) {
	snapshot, err := commandDiagnosticsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &CommandResult{Output: renderDiagnosticStatus(snapshot)}, nil
}

func executeContextCommand(ctx *CommandContext, _ string) (*CommandResult, error) {
	snapshot, err := commandDiagnosticsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &CommandResult{Output: renderDiagnosticContext(snapshot)}, nil
}

func executeUsage(ctx *CommandContext, _ string) (*CommandResult, error) {
	snapshot, err := commandDiagnosticsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &CommandResult{Output: renderDiagnosticUsage(snapshot)}, nil
}

func executeConfig(ctx *CommandContext, _ string) (*CommandResult, error) {
	snapshot, err := commandDiagnosticsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &CommandResult{Output: renderDiagnosticConfig(snapshot)}, nil
}

func executeDoctor(ctx *CommandContext, _ string) (*CommandResult, error) {
	snapshot, err := commandDiagnosticsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &CommandResult{Output: renderDiagnosticDoctor(snapshot)}, nil
}

func commandDiagnosticsSnapshot(ctx *CommandContext) (enginediagnostics.Snapshot, error) {
	if ctx == nil {
		return enginediagnostics.Snapshot{}, fmt.Errorf("diagnostics require command context")
	}
	engine, ok := ctx.Engine.(diagnosticsEngine)
	if !ok || engine == nil {
		return enginediagnostics.Snapshot{}, fmt.Errorf("diagnostics service is unavailable")
	}
	commandCtx := ctx.Context
	if commandCtx == nil {
		commandCtx = context.Background()
	}
	return engine.DiagnosticsSnapshot(commandCtx)
}

func renderDiagnosticStatus(snapshot enginediagnostics.Snapshot) string {
	var output strings.Builder
	output.WriteString("Session Status\n")
	output.WriteString("==============\n")
	fmt.Fprintf(&output, "Observed: %s\n\n", diagnosticTime(snapshot.ObservedAt))
	writeStringDiagnostic(&output, "Session", snapshot.Status.SessionID)
	writeStringDiagnostic(&output, "Provider", snapshot.Status.Provider)
	writeStringDiagnostic(&output, "Model", snapshot.Status.Model)
	writeStringDiagnostic(&output, "CWD", snapshot.Status.CWD)
	writeIntDiagnostic(&output, "Messages", snapshot.Status.MessageCount)
	writeIntDiagnostic(&output, "Tools", snapshot.Status.ToolCount)
	writeStringDiagnostic(&output, "Transcript", snapshot.Status.Transcript)
	writeStringDiagnostic(&output, "Usage coverage", snapshot.Status.UsageCoverage)
	writeIntDiagnostic(&output, "Persisted tokens", snapshot.Usage.TotalTokens)
	writePercentDiagnostic(&output, "Current context", snapshot.Status.ContextPercent)
	output.WriteString("\nDetails: /context  /usage  /config  /doctor")
	return output.String()
}

func renderDiagnosticContext(snapshot enginediagnostics.Snapshot) string {
	value := snapshot.Context
	var output strings.Builder
	output.WriteString("Context Diagnostics\n")
	output.WriteString("===================\n")
	fmt.Fprintf(&output, "Observed: %s\n\n", diagnosticTime(snapshot.ObservedAt))
	writeStringDiagnostic(&output, "Model", value.Model)
	writeIntDiagnostic(&output, "Context window tokens", value.ContextWindowTokens)
	writeIntDiagnostic(&output, "Current input tokens", value.CurrentInputTokens)
	writePercentDiagnostic(&output, "Current usage", value.UsagePercent)
	output.WriteString("\nPersisted active contributors\n")
	writeIntDiagnostic(&output, "Total messages", value.TotalMessages)
	writeIntDiagnostic(&output, "User messages", value.UserMessages)
	writeIntDiagnostic(&output, "Assistant messages", value.AssistantMessages)
	writeIntDiagnostic(&output, "Tool results", value.ToolMessages)
	writeIntDiagnostic(&output, "System messages", value.SystemMessages)
	writeIntDiagnostic(&output, "Tool calls", value.ToolCalls)
	writeIntDiagnostic(&output, "Compaction boundaries", value.CompactionBoundaries)
	writeStringDiagnostic(&output, "Transient contributors", value.TransientContributors)
	return output.String()
}

func renderDiagnosticUsage(snapshot enginediagnostics.Snapshot) string {
	value := snapshot.Usage
	var output strings.Builder
	output.WriteString("Persisted Provider Usage\n")
	output.WriteString("========================\n")
	fmt.Fprintf(&output, "Observed: %s\n\n", diagnosticTime(snapshot.ObservedAt))
	writeStringDiagnostic(&output, "Coverage", value.Coverage)
	writeIntDiagnostic(&output, "Input tokens", value.PromptTokens)
	writeIntDiagnostic(&output, "Output tokens", value.CompletionTokens)
	writeIntDiagnostic(&output, "Total tokens", value.TotalTokens)
	writeIntDiagnostic(&output, "Responses with metadata", value.ResponsesWithMetadata)
	writeIntDiagnostic(&output, "Responses without metadata", value.ResponsesWithoutMetadata)
	output.WriteString("\nMoney is omitted: no authoritative provider/model billing catalog is attached to this runtime.")
	return output.String()
}

func renderDiagnosticConfig(snapshot enginediagnostics.Snapshot) string {
	value := snapshot.Config
	var output strings.Builder
	output.WriteString("Effective Configuration\n")
	output.WriteString("=======================\n")
	fmt.Fprintf(&output, "Observed: %s\n\n", diagnosticTime(snapshot.ObservedAt))
	writeStringDiagnostic(&output, "Provider", value.Provider)
	writeStringDiagnostic(&output, "Model", value.Model)
	credential := "not configured"
	if value.CredentialConfigured.Value {
		credential = "configured"
	}
	writeDiagnosticLine(&output, "Credential", credential, value.CredentialConfigured.FieldMeta)
	writeStringDiagnostic(&output, "Endpoint", value.Endpoint)
	writeStringDiagnostic(&output, "Fallback model", value.FallbackModel)
	writeStringDiagnostic(&output, "Permission mode", value.PermissionMode)
	writeStringDiagnostic(&output, "Precedence", value.Precedence)
	output.WriteString("\nCredential values, suffixes, URL userinfo, paths, queries, and fragments are never rendered.")
	return output.String()
}

// RenderDiagnosticConfig projects the shared redacted configuration snapshot
// for non-slash entrypoints.
func RenderDiagnosticConfig(snapshot enginediagnostics.Snapshot) string {
	return renderDiagnosticConfig(snapshot)
}

func renderDiagnosticDoctor(snapshot enginediagnostics.Snapshot) string {
	var output strings.Builder
	output.WriteString("Doctor\n")
	output.WriteString("======\n")
	fmt.Fprintf(&output, "Observed: %s\n\n", diagnosticTime(snapshot.ObservedAt))
	for _, check := range snapshot.Doctor.Checks {
		fmt.Fprintf(
			&output,
			"[%s] %s: %s %s\n",
			check.Outcome,
			check.ID,
			check.Summary,
			formatDiagnosticMeta(check.FieldMeta),
		)
		if check.Remediation != "" {
			fmt.Fprintf(&output, "      remediation: %s\n", check.Remediation)
		}
	}
	return strings.TrimRight(output.String(), "\n")
}

// RenderDiagnosticDoctor projects the shared stable-ID doctor snapshot for
// non-slash entrypoints.
func RenderDiagnosticDoctor(snapshot enginediagnostics.Snapshot) string {
	return renderDiagnosticDoctor(snapshot)
}

func writeStringDiagnostic(output *strings.Builder, label string, field enginediagnostics.StringField) {
	value := field.Value
	if strings.TrimSpace(value) == "" {
		value = string(field.State)
	}
	writeDiagnosticLine(output, label, value, field.FieldMeta)
}

func writeIntDiagnostic(output *strings.Builder, label string, field enginediagnostics.IntField) {
	value := fmt.Sprintf("%d", field.Value)
	if field.State != enginediagnostics.StateKnown && field.State != enginediagnostics.StateStale {
		value = string(field.State)
	}
	writeDiagnosticLine(output, label, value, field.FieldMeta)
}

func writePercentDiagnostic(output *strings.Builder, label string, field enginediagnostics.IntField) {
	value := fmt.Sprintf("%d%%", field.Value)
	if field.State != enginediagnostics.StateKnown && field.State != enginediagnostics.StateStale {
		value = string(field.State)
	}
	writeDiagnosticLine(output, label, value, field.FieldMeta)
}

func writeDiagnosticLine(
	output *strings.Builder,
	label, value string,
	meta enginediagnostics.FieldMeta,
) {
	fmt.Fprintf(output, "%-28s %s %s\n", label+":", value, formatDiagnosticMeta(meta))
}

func formatDiagnosticMeta(meta enginediagnostics.FieldMeta) string {
	state := meta.State
	if state == "" {
		state = enginediagnostics.StateUnavailable
	}
	parts := []string{string(state)}
	if meta.Source != "" {
		parts = append(parts, "source="+meta.Source)
	}
	if !meta.ObservedAt.IsZero() {
		parts = append(parts, "observed="+diagnosticTime(meta.ObservedAt))
	}
	formatted := "[" + strings.Join(parts, "; ") + "]"
	if meta.Detail != "" {
		formatted += " - " + meta.Detail
	}
	return formatted
}

func diagnosticTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}
