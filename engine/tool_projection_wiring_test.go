package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestCanonicalProjectionWiringPublishesStartBeforePermissionAndFinalInputBeforeDispatch(
	t *testing.T,
) {
	registry := p232ProjectionTestRegistry()
	var events []QueryEvent
	startVisibleAtPermission := false
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		ToolRegistry:      registry,
		PermissionMode:    permission.ModeAuto,
		CommandEntrypoint: commands.EntrypointHeadless,
		PermissionPrompt: func(
			_ context.Context,
			request PermissionPromptRequest,
		) PermissionInteractionResult {
			if request.ToolUseID != "rewrite-call" {
				t.Fatalf("permission request = %#v", request)
			}
			startVisibleAtPermission = canonicalProjectionKindCount(
				events,
				CanonicalProjectionToolStart,
			) == 1
			if canonicalProjectionKindCount(
				events,
				CanonicalProjectionToolInput,
			) != 0 {
				t.Fatal("effective input was projected before permission settled")
			}
			return PermissionInteractionResult{
				Decision: PermissionAllowSession,
				UpdatedInput: map[string]any{
					"value": "final",
				},
			}
		},
	})
	t.Cleanup(engine.Close)

	executedInput := ""
	outcome := executeToolCall(
		t.Context(),
		QueryParams{
			ToolRegistry: registry,
			CanUseTool:   engine.wrappedCanUseTool,
			ToolExecutor: func(
				_ context.Context,
				_ string,
				jsonInput string,
			) (string, error) {
				if canonicalProjectionKindCount(
					events,
					CanonicalProjectionToolInput,
				) != 1 {
					t.Fatal("effective input was not projected before dispatch")
				}
				executedInput = jsonInput
				return "ok", nil
			},
		},
		nil,
		&ToolUseContext{Options: &ToolUseOptions{
			PermissionMode: permission.ModeAuto,
		}},
		&schema.ToolCall{
			ID: "rewrite-call",
			Function: schema.FunctionCall{
				Name:      "PolicyAction",
				Arguments: `{"value":"initial"}`,
			},
		},
		func(event QueryEvent) {
			events = append(events, event)
		},
	)
	if outcome == nil || outcome.Result == nil ||
		messageIsError(outcome.Result) {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !startVisibleAtPermission {
		t.Fatal("permission request became visible before canonical tool start")
	}
	if executedInput != `{"value":"final"}` {
		t.Fatalf("dispatch input = %q", executedInput)
	}
	input := canonicalProjectionByKind(
		t,
		events,
		CanonicalProjectionToolInput,
	)
	if string(input.Tool.EffectiveInput) != `{"value":"final"}` {
		t.Fatalf("projected effective input = %s", input.Tool.EffectiveInput)
	}
}

func TestCanonicalProjectionWiringProjectsProgressAndNormalizedTerminal(
	t *testing.T,
) {
	for _, test := range []struct {
		name         string
		execute      func(context.Context) (string, error)
		wantOutcome  CanonicalToolOutcome
		wantOutput   string
		wantProgress []string
	}{
		{
			name: "completed with replacement snapshots",
			execute: func(ctx context.Context) (string, error) {
				tools.EmitProgress(ctx, tools.ToolProgressEvent{
					Content: "first complete snapshot",
				})
				tools.EmitProgress(ctx, tools.ToolProgressEvent{
					Content: "second complete snapshot",
				})
				return "complete output", nil
			},
			wantOutcome: CanonicalToolOutcomeCompleted,
			wantOutput:  "complete output",
			wantProgress: []string{
				"first complete snapshot",
				"second complete snapshot",
			},
		},
		{
			name: "failed execution",
			execute: func(context.Context) (string, error) {
				return "", errors.New("dispatch failed")
			},
			wantOutcome: CanonicalToolOutcomeFailed,
			wantOutput:  "dispatch failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := p232ProjectionTestRegistry()
			var events []QueryEvent
			_, err := runCanonicalToolRound(
				t.Context(),
				canonicalToolRoundInput{
					params: QueryParams{
						ToolRegistry: registry,
						ToolExecutor: func(
							ctx context.Context,
							_ string,
							_ string,
						) (string, error) {
							return test.execute(ctx)
						},
					},
					toolCalls: []*schema.ToolCall{{
						ID: "round-call",
						Function: schema.FunctionCall{
							Name:      "PolicyAction",
							Arguments: `{"value":"input"}`,
						},
					}},
					yield: func(event QueryEvent) {
						events = append(events, event)
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			projections := canonicalToolProjections(events)
			if len(projections) != 3+len(test.wantProgress) {
				t.Fatalf("canonical projections = %#v", projections)
			}
			if projections[0].Kind != CanonicalProjectionToolStart ||
				projections[1].Kind != CanonicalProjectionToolInput {
				t.Fatalf("projection prefix = %#v", projections[:2])
			}
			for index, want := range test.wantProgress {
				projection := projections[index+2]
				if projection.Kind != CanonicalProjectionToolProgress ||
					projection.Tool.Content != want {
					t.Fatalf(
						"progress projection %d = %#v, want %q",
						index,
						projection,
						want,
					)
				}
			}
			terminal := projections[len(projections)-1]
			if terminal.Kind != CanonicalProjectionToolTerminal ||
				terminal.Tool.Outcome != test.wantOutcome {
				t.Fatalf("terminal projection = %#v", terminal)
			}
			var output string
			if err := json.Unmarshal(terminal.Tool.RawOutput, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, test.wantOutput) {
				t.Fatalf("terminal raw output = %q", output)
			}
		})
	}
}

func p232ProjectionTestRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "PolicyAction",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"value": {
						Type:     schema.String,
						Required: true,
					},
				},
			),
		},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginBuiltin,
			ActionKind: tools.ToolActionRuntimeState,
		},
	})
	return registry
}

func canonicalProjectionKindCount(
	events []QueryEvent,
	kind CanonicalProjectionKind,
) int {
	count := 0
	for _, projection := range canonicalToolProjections(events) {
		if projection.Kind == kind {
			count++
		}
	}
	return count
}

func canonicalProjectionByKind(
	t *testing.T,
	events []QueryEvent,
	kind CanonicalProjectionKind,
) *CanonicalProjectionEvent {
	t.Helper()
	for _, projection := range canonicalToolProjections(events) {
		if projection.Kind == kind {
			return projection
		}
	}
	t.Fatalf("canonical projection %q not found", kind)
	return nil
}

func canonicalToolProjections(
	events []QueryEvent,
) []*CanonicalProjectionEvent {
	projections := make([]*CanonicalProjectionEvent, 0)
	for _, event := range events {
		if event.Type == EventCanonicalProjection &&
			event.CanonicalProjection != nil &&
			event.CanonicalProjection.Tool != nil {
			projections = append(projections, event.CanonicalProjection)
		}
	}
	return projections
}

func messageIsError(message *schema.Message) bool {
	if message == nil {
		return false
	}
	isError, _ := message.Extra["is_error"].(bool)
	return isError
}
