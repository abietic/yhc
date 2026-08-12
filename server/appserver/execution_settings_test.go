package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
)

type executionSettingsEngine struct {
	*fakeSessionEngine
	inventory                                provider.RuntimeInventorySnapshot
	model                                    string
	effort                                   string
	mode                                     permission.Mode
	modelChanges, effortChanges, modeChanges int
	capabilitySet                            bool
	capability                               bool
	capabilityErr                            error
	block                                    *engine.ModelDispatchBlock
	modelErr                                 error
	permissionErr                            error
}

func (e *executionSettingsEngine) ModelInventory() provider.RuntimeInventorySnapshot {
	return e.inventory
}
func (e *executionSettingsEngine) GetModelName() string    { return e.model }
func (e *executionSettingsEngine) ReasoningEffort() string { return e.effort }
func (e *executionSettingsEngine) ReasoningEffortCapability(context.Context) (bool, string, error) {
	if !e.capabilitySet {
		return true, "", nil
	}
	return e.capability, "", e.capabilityErr
}

func (e *executionSettingsEngine) ChangeModel(_ context.Context, value string) (engine.ModelControlState, error) {
	e.modelChanges++
	if e.modelErr != nil {
		return engine.ModelControlState{}, e.modelErr
	}
	e.model = value
	return engine.ModelControlState{}, nil
}

func (e *executionSettingsEngine) ChangeReasoningEffort(_ context.Context, value string) (string, error) {
	e.effortChanges++
	e.effort = value
	return value, nil
}
func (e *executionSettingsEngine) PermissionMode() permission.Mode { return e.mode }
func (e *executionSettingsEngine) SetPermissionModeConfirmed(value permission.Mode, _ bool) error {
	e.modeChanges++
	if e.permissionErr != nil {
		return e.permissionErr
	}
	e.mode = value
	return nil
}

func (e *executionSettingsEngine) ModelDispatchBlockSnapshot() *engine.ModelDispatchBlock {
	return e.block
}

func TestExecutionSettingsGetAndPatch(t *testing.T) {
	var created *executionSettingsEngine
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		created = &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), model: "safe", mode: permission.ModeDefault, block: &engine.ModelDispatchBlock{Code: engine.ModelDispatchBlockInvalidBinding, Selector: "safe", Remediation: "select a model"}, inventory: provider.RuntimeInventorySnapshot{Entries: []provider.RuntimeInventoryEntry{{Selector: "safe", DisplayName: "Safe", Provider: "agenticopenai", APIModel: "gpt-safe", Metadata: modelMetadata([]string{"low", "high", "HIGH"})}}}}
		return created, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	createdResponse := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, createdResponse, &summary)
	_ = createdResponse.Body.Close()
	url := httpServer.URL + "/v1/sessions/" + summary.ID + "/execution-settings"
	var got ExecutionSettingsResponse
	getResponse := getBearer(t, url, "test-token")
	decodeResponse(t, getResponse, &got)
	_ = getResponse.Body.Close()
	if got.Model != "safe" || len(got.Models) != 1 || got.Models[0].Selector != "safe" || !got.ReasoningEffortSupported || !equalStrings(got.ReasoningEffortOptions, []string{"default", "low", "high"}) || !equalStrings(got.PermissionModeOptions, []string{"default", "plan", "acceptEdits", "dontAsk", "auto"}) || got.DispatchBlock == nil || got.DispatchBlock.Code != engine.ModelDispatchBlockInvalidBinding {
		t.Fatalf("projection = %+v", got)
	}
	updated := doJSON(t, url, "test-token", http.MethodPatch, UpdateExecutionSettingsRequest{PermissionMode: stringPointer("plan")})
	decodeResponse(t, updated, &got)
	_ = updated.Body.Close()
	if created.modeChanges != 1 || created.modelChanges != 0 || got.PermissionMode != "plan" {
		t.Fatalf("mutation = %+v", created)
	}
}

func TestExecutionSettingsRejectsInvalidMutation(t *testing.T) {
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		return &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), mode: permission.ModeDefault}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	created := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	_ = created.Body.Close()
	response := doJSON(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token", http.MethodPatch, UpdateExecutionSettingsRequest{PermissionMode: stringPointer("bypassPermissions")})
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestExecutionSettingsReportsBypassButDoesNotOfferIt(t *testing.T) {
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		return &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), mode: permission.ModeBypassPermissions}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	created := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	_ = created.Body.Close()
	var got ExecutionSettingsResponse
	getResponse := getBearer(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token")
	decodeResponse(t, getResponse, &got)
	_ = getResponse.Body.Close()
	if got.PermissionMode != string(permission.ModeBypassPermissions) || containsExecutionOption(got.PermissionModeOptions, string(permission.ModeBypassPermissions)) || containsExecutionOption(got.PermissionModeOptions, string(permission.ModeBubble)) {
		t.Fatalf("permission projection = %+v", got)
	}
}

func TestExecutionSettingsRejectsCardinalityUnknownAndActiveTurn(t *testing.T) {
	var created *executionSettingsEngine
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		created = &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, true), mode: permission.ModeDefault}
		return created, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	createdResponse := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, createdResponse, &summary)
	_ = createdResponse.Body.Close()
	url := httpServer.URL + "/v1/sessions/" + summary.ID + "/execution-settings"
	for _, body := range []string{"{}", `{"model":"safe","permission_mode":"plan"}`, `{"unknown":"value"}`} {
		request, err := http.NewRequest(http.MethodPatch, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer test-token")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, response.StatusCode)
		}
	}
	turnResponse := doJSON(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/turns", "test-token", http.MethodPost, StartTurnRequest{Prompt: "wait"})
	_ = turnResponse.Body.Close()
	<-created.started
	response := doJSON(t, url, "test-token", http.MethodPatch, UpdateExecutionSettingsRequest{PermissionMode: stringPointer("plan")})
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict || created.modeChanges != 0 {
		t.Fatalf("active mutation status=%d calls=%d", response.StatusCode, created.modeChanges)
	}
}

func TestExecutionSettingsProjectionIsBoundedAndSafe(t *testing.T) {
	entries := make([]provider.RuntimeInventoryEntry, 130)
	for index := range entries {
		entries[index] = provider.RuntimeInventoryEntry{Selector: "model-" + string(rune('a'+index%26)), Provider: "provider", APIModel: "api", ProfileID: "profile_id", RouteIdentityDigest: "route_digest", MetadataDigest: "metadata_digest"}
	}
	entries[129].Selector = "current"
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		return &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), inventory: provider.RuntimeInventorySnapshot{Entries: entries}, model: "current", mode: permission.ModeDefault}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	created := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	_ = created.Body.Close()
	response := getBearer(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token")
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	for _, forbidden := range []string{"profile_id", "route_digest", "metadata_digest", "metadata", "endpoint", "account", "token", "credential"} {
		if strings.Contains(raw.String(), `"`+forbidden+`"`) {
			t.Fatalf("unsafe projection contains %q: %s", forbidden, raw.String())
		}
	}
	var got ExecutionSettingsResponse
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != maxExecutionSettingsModels || !containsExecutionModel(got.Models, "current") {
		t.Fatalf("models = %+v", got.Models)
	}
}

func TestExecutionSettingsLegacyClaudeThinkingFallback(t *testing.T) {
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		return &executionSettingsEngine{
			fakeSessionEngine: newFakeSessionEngine(input, false),
			model:             "legacy:claude-sonnet-4-6",
			mode:              permission.ModeDefault,
			inventory: provider.RuntimeInventorySnapshot{Entries: []provider.RuntimeInventoryEntry{{
				Selector: "legacy:claude-sonnet-4-6", Provider: "agenticclaude", APIModel: "claude-sonnet-4-6",
			}}},
		}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	created := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	_ = created.Body.Close()
	var got ExecutionSettingsResponse
	getResponse := getBearer(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token")
	decodeResponse(t, getResponse, &got)
	_ = getResponse.Body.Close()
	want := []string{"default", "low", "medium", "high", "xhigh", "max"}
	if !equalStrings(got.ReasoningEffortOptions, want) {
		t.Fatalf("reasoning options = %#v, want %#v", got.ReasoningEffortOptions, want)
	}
}

func TestExecutionSettingsBlockedBindingAndUnavailableController(t *testing.T) {
	t.Run("blocked binding remains readable and model replacement is allowed", func(t *testing.T) {
		var created *executionSettingsEngine
		server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), model: "old", mode: permission.ModeDefault, capabilitySet: true, capabilityErr: errors.New("blocked"), block: &engine.ModelDispatchBlock{Code: engine.ModelDispatchBlockInvalidBinding, Selector: "old", Remediation: "choose a model"}, inventory: provider.RuntimeInventorySnapshot{Entries: []provider.RuntimeInventoryEntry{{Selector: "old", Provider: "agenticopenai", APIModel: "old"}, {Selector: "new", Provider: "agenticopenai", APIModel: "new"}}}}
			return created, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer shutdownTestServer(t, server)
		httpServer := httptest.NewServer(server.Handler())
		defer httpServer.Close()
		createdResponse := createExecutionSettingsTestSession(t, httpServer.URL)
		var summary SessionSummary
		decodeResponse(t, createdResponse, &summary)
		_ = createdResponse.Body.Close()
		url := httpServer.URL + "/v1/sessions/" + summary.ID + "/execution-settings"
		var got ExecutionSettingsResponse
		getResponse := getBearer(t, url, "test-token")
		decodeResponse(t, getResponse, &got)
		_ = getResponse.Body.Close()
		if got.DispatchBlock == nil || got.DispatchBlock.Code != engine.ModelDispatchBlockInvalidBinding || !equalStrings(got.ReasoningEffortOptions, []string{"default"}) {
			t.Fatalf("blocked projection = %+v", got)
		}
		updated := doJSON(t, url, "test-token", http.MethodPatch, UpdateExecutionSettingsRequest{Model: stringPointer("new")})
		decodeResponse(t, updated, &got)
		_ = updated.Body.Close()
		if created.modelChanges != 1 || got.Model != "new" {
			t.Fatalf("replacement = %+v", created)
		}
	})
	t.Run("missing controller is bounded unavailable", func(t *testing.T) {
		server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer shutdownTestServer(t, server)
		httpServer := httptest.NewServer(server.Handler())
		defer httpServer.Close()
		created := createExecutionSettingsTestSession(t, httpServer.URL)
		var summary SessionSummary
		decodeResponse(t, created, &summary)
		_ = created.Body.Close()
		response := getBearer(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token")
		if response.StatusCode != http.StatusNotImplemented {
			t.Fatalf("status = %d", response.StatusCode)
		}
		response.Body.Close()
	})
}

func TestExecutionSettingsMutationsAreIndependentAndInvalidInputsDoNotCallController(t *testing.T) {
	var created *executionSettingsEngine
	server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		created = &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), model: "first", effort: "low", mode: permission.ModeDefault, inventory: provider.RuntimeInventorySnapshot{Entries: []provider.RuntimeInventoryEntry{{Selector: "first", Provider: "agenticopenai", APIModel: "first", Metadata: modelMetadata([]string{"low", "high"})}, {Selector: "next", Provider: "agenticopenai", APIModel: "next"}}}}
		return created, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	createdResponse := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, createdResponse, &summary)
	_ = createdResponse.Body.Close()
	url := httpServer.URL + "/v1/sessions/" + summary.ID + "/execution-settings"
	for _, input := range []UpdateExecutionSettingsRequest{{Model: stringPointer(strings.Repeat("x", maxExecutionSettingsSelector+1))}, {Model: stringPointer("unknown")}, {ReasoningEffort: stringPointer("unknown")}, {PermissionMode: stringPointer("bypassPermissions")}, {PermissionMode: stringPointer("bubble")}} {
		response := doJSON(t, url, "test-token", http.MethodPatch, input)
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid input %#v status=%d", input, response.StatusCode)
		}
		response.Body.Close()
	}
	if created.modelChanges != 0 || created.effortChanges != 0 || created.modeChanges != 0 {
		t.Fatalf("invalid mutation calls = %+v", created)
	}
	for _, input := range []UpdateExecutionSettingsRequest{{ReasoningEffort: stringPointer("high")}, {Model: stringPointer("next")}, {PermissionMode: stringPointer("plan")}} {
		response := doJSON(t, url, "test-token", http.MethodPatch, input)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("valid input %#v status=%d", input, response.StatusCode)
		}
		response.Body.Close()
	}
	if created.modelChanges != 1 || created.effortChanges != 1 || created.modeChanges != 1 {
		t.Fatalf("valid mutation calls = %+v", created)
	}
}

func TestExecutionSettingsMapsEngineMutationRacePrecisely(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutationErr error
		wantStatus  int
	}{
		{
			name: "active boundary race is conflict",
			mutationErr: fmt.Errorf(
				"execution control cannot change while turn %s owns the runtime boundary",
				"turn-race",
			),
			wantStatus: http.StatusConflict,
		},
		{
			name:        "ordinary engine rejection is unprocessable",
			mutationErr: errors.New("durability checkpoint rejected"),
			wantStatus:  http.StatusUnprocessableEntity,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var created *executionSettingsEngine
			server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
				created = &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), model: "old", mode: permission.ModeDefault, modelErr: test.mutationErr, inventory: provider.RuntimeInventorySnapshot{Entries: []provider.RuntimeInventoryEntry{{Selector: "old", Provider: "agenticopenai", APIModel: "old"}, {Selector: "new", Provider: "agenticopenai", APIModel: "new"}}}}
				return created, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownTestServer(t, server)
			httpServer := httptest.NewServer(server.Handler())
			defer httpServer.Close()
			createdResponse := createExecutionSettingsTestSession(t, httpServer.URL)
			var summary SessionSummary
			decodeResponse(t, createdResponse, &summary)
			_ = createdResponse.Body.Close()
			response := doJSON(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token", http.MethodPatch, UpdateExecutionSettingsRequest{Model: stringPointer("new")})
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			var envelope ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if envelope.Error.Code != "execution_settings_rejected" || created.model != "old" || created.modelChanges != 1 {
				t.Fatalf("error=%+v engine=%+v", envelope, created)
			}
		})
	}
}

func TestExecutionSettingsMapsPlanTransitionSentinelPrecisely(t *testing.T) {
	for _, test := range []struct {
		name          string
		permissionErr error
		wantStatus    int
	}{
		{
			name:          "wrapped plan transition race is conflict",
			permissionErr: fmt.Errorf("permission update: %w", engine.ErrPlanTransitionInFlight),
			wantStatus:    http.StatusConflict,
		},
		{
			name:          "ordinary permission rejection is unprocessable",
			permissionErr: errors.New("permission checkpoint rejected"),
			wantStatus:    http.StatusUnprocessableEntity,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var created *executionSettingsEngine
			server, err := New(Config{Token: "test-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
				created = &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), mode: permission.ModeDefault, permissionErr: test.permissionErr}
				return created, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownTestServer(t, server)
			httpServer := httptest.NewServer(server.Handler())
			defer httpServer.Close()
			createdResponse := createExecutionSettingsTestSession(t, httpServer.URL)
			var summary SessionSummary
			decodeResponse(t, createdResponse, &summary)
			_ = createdResponse.Body.Close()
			response := doJSON(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/execution-settings", "test-token", http.MethodPatch, UpdateExecutionSettingsRequest{PermissionMode: stringPointer("plan")})
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			var envelope ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if envelope.Error.Code != "execution_settings_rejected" || created.mode != permission.ModeDefault || created.modeChanges != 1 {
				t.Fatalf("error=%+v engine=%+v", envelope, created)
			}
		})
	}
}

func modelMetadata(efforts []string) modelcaps.EffectiveModelMetadata {
	return modelcaps.EffectiveModelMetadata{SupportedReasoningEfforts: modelcaps.MetadataField[[]string]{Value: efforts}}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func containsExecutionModel(options []ExecutionModelOption, selector string) bool {
	for _, option := range options {
		if option.Selector == selector {
			return true
		}
	}
	return false
}

func createExecutionSettingsTestSession(t *testing.T, baseURL string) *http.Response {
	t.Helper()
	registered := registerWorkspace(t, baseURL, "test-token", t.TempDir())
	return doJSON(t, baseURL+"/v1/sessions", "test-token", http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
}

func stringPointer(value string) *string { return &value }
