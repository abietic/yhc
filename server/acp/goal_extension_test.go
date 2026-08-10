package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
)

func TestP49ACPGoalCapabilityDefaultsEnabled(t *testing.T) {
	capability := (&Agent{}).acpGoalCapabilityConfig(nil)
	if capability == nil ||
		!capability.Enabled ||
		capability.DefaultTokenBudget != nil ||
		capability.ACPNegotiated {
		t.Fatalf("unnegotiated nil config Goal capability = %#v", capability)
	}

	disabled := false
	negotiated := &Agent{
		initialized:   true,
		goalNamespace: &legacyACPGoalNamespace,
	}
	capability = negotiated.acpGoalCapabilityConfig(&config.Config{
		Goal: &config.GoalConfig{Enabled: &disabled},
	})
	if capability.Enabled || !capability.ACPNegotiated {
		t.Fatalf("negotiated explicitly disabled Goal capability = %#v", capability)
	}
}

func TestP245cInitializeNegotiatesGoalCapabilityImmutably(t *testing.T) {
	compatible := acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Meta: map[string]any{
				legacyGoalCapabilityKey: map[string]any{
					"versions":      []any{float64(1)},
					"notifications": true,
				},
				"unrelated": "ignored",
			},
		},
	}
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	response, err := agent.Initialize(t.Context(), compatible)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"version":       acpGoalProtocolVersion,
		"notifications": true,
	}
	if got := response.AgentCapabilities.Meta[legacyGoalCapabilityKey]; !equalJSONValue(got, want) {
		t.Fatalf("negotiated capability = %#v, want %#v", got, want)
	}
	repeated, err := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := repeated.AgentCapabilities.Meta[legacyGoalCapabilityKey]; !equalJSONValue(got, want) {
		t.Fatalf("repeated Initialize changed capability = %#v", got)
	}

	absent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(absent.Close)
	first, err := absent.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.AgentCapabilities.Meta != nil {
		t.Fatalf("absent offer advertised Goal = %#v", first.AgentCapabilities.Meta)
	}
	second, err := absent.Initialize(t.Context(), compatible)
	if err != nil {
		t.Fatal(err)
	}
	if second.AgentCapabilities.Meta != nil {
		t.Fatalf("late offer changed immutable negotiation = %#v", second.AgentCapabilities.Meta)
	}
}

func TestP245cGoalNegotiationRejectsMalformedOffers(t *testing.T) {
	for _, offer := range []any{
		map[string]any{"versions": []any{}, "notifications": true},
		map[string]any{"versions": []any{1.5}, "notifications": true},
		map[string]any{"versions": []any{65536}, "notifications": true},
		map[string]any{"versions": []any{2}, "notifications": true},
		map[string]any{"versions": []any{1}, "notifications": false},
		map[string]any{
			"versions": []any{1}, "notifications": true, "extra": true,
		},
		"invalid",
	} {
		agent, err := NewAgent(Config{CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		response, initializeErr := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
			ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
				legacyGoalCapabilityKey: offer,
			}},
		})
		agent.Close()
		if initializeErr != nil {
			t.Fatal(initializeErr)
		}
		if response.AgentCapabilities.Meta != nil {
			t.Fatalf("malformed offer %#v advertised %#v", offer, response.AgentCapabilities.Meta)
		}
	}
}

func TestYHCACPGoalNamespaceNegotiationMatrix(t *testing.T) {
	validCanonical := yhcGoalOffer(1)
	validLegacy := map[string]any{
		"versions":      []any{float64(1)},
		"notifications": true,
	}
	malformed := map[string]any{"versions": []any{float64(1)}}
	canonicalCapability := map[string]any{
		"version":       1,
		"notifications": true,
	}

	for _, test := range []struct {
		name       string
		meta       map[string]any
		wantErr    bool
		wantKey    string
		wantAbsent bool
		retry      bool
	}{
		{name: "canonical only", meta: map[string]any{yhcGoalCapabilityKey: validCanonical}, wantKey: yhcGoalCapabilityKey},
		{name: "legacy only", meta: map[string]any{legacyGoalCapabilityKey: validLegacy}, wantKey: legacyGoalCapabilityKey},
		{name: "matching dual", meta: map[string]any{yhcGoalCapabilityKey: validCanonical, legacyGoalCapabilityKey: validLegacy}, wantKey: yhcGoalCapabilityKey},
		{name: "different valid dual", meta: map[string]any{yhcGoalCapabilityKey: validCanonical, legacyGoalCapabilityKey: yhcGoalOffer(2)}, wantErr: true, retry: true},
		{name: "malformed canonical with valid legacy", meta: map[string]any{yhcGoalCapabilityKey: malformed, legacyGoalCapabilityKey: validLegacy}, wantErr: true, retry: true},
		{name: "valid canonical with malformed legacy", meta: map[string]any{yhcGoalCapabilityKey: validCanonical, legacyGoalCapabilityKey: malformed}, wantErr: true, retry: true},
		{name: "malformed canonical only", meta: map[string]any{yhcGoalCapabilityKey: malformed}, wantAbsent: true},
		{name: "malformed legacy only", meta: map[string]any{legacyGoalCapabilityKey: malformed}, wantAbsent: true},
		{name: "neither", meta: nil, wantAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent, err := NewAgent(Config{CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(agent.Close)

			response, initializeErr := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
				ProtocolVersion:    acpsdk.ProtocolVersionNumber,
				ClientCapabilities: acpsdk.ClientCapabilities{Meta: test.meta},
			})
			if test.wantErr {
				if initializeErr == nil {
					t.Fatalf("Initialize(%#v) succeeded", test.meta)
				}
				yhcRequireNoGoalSurface(t, agent)
				if test.retry {
					retried, err := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
						ProtocolVersion: acpsdk.ProtocolVersionNumber,
						ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
							yhcGoalCapabilityKey: validCanonical,
						}},
					})
					if err != nil {
						t.Fatalf("retry Initialize: %v", err)
					}
					yhcRequireGoalCapability(t, retried, yhcGoalCapabilityKey, canonicalCapability)
				}
				return
			}
			if initializeErr != nil {
				t.Fatal(initializeErr)
			}
			if test.wantAbsent {
				if response.AgentCapabilities.Meta != nil {
					t.Fatalf("Initialize(%#v) advertised Goal = %#v", test.meta, response.AgentCapabilities.Meta)
				}
				return
			}
			yhcRequireGoalCapability(t, response, test.wantKey, canonicalCapability)
		})
	}
}

func TestYHCACPGoalNamespaceRequestAndNotificationPairing(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability string
		selected   []string
		unselected []string
		updated    string
	}{
		{
			name:       "canonical",
			capability: yhcGoalCapabilityKey,
			selected:   yhcGoalRequestMethods,
			unselected: legacyGoalRequestMethods,
			updated:    yhcGoalUpdatedMethod,
		},
		{
			name:       "legacy",
			capability: legacyGoalCapabilityKey,
			selected:   legacyGoalRequestMethods,
			unselected: yhcGoalRequestMethods,
			updated:    legacyGoalUpdatedMethod,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
			response, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
				ProtocolVersion: acpsdk.ProtocolVersionNumber,
				ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
					test.capability: yhcGoalOffer(1),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			yhcRequireAgentIdentity(t, response)
			yhcRequireGoalCapability(t, response, test.capability, map[string]any{"version": 1, "notifications": true})
			sess := installP245cGoalSession(t, agent, &mockChatModel{})

			before := len(client.getExtensions())
			raw, err := conn.CallExtension(t.Context(), test.selected[1], map[string]any{
				"schemaVersion":    1,
				"sessionId":        string(sess.ID),
				"requestId":        test.name + "-create",
				"operation":        "create",
				"expectedRevision": 0,
				"objective":        "pair every Goal request namespace with its notification namespace",
			})
			if err != nil {
				t.Fatalf("control: %v", err)
			}
			var control acpGoalResponse
			if err := json.Unmarshal(raw, &control); err != nil {
				t.Fatal(err)
			}
			yhcRequireMatchingGoalNotification(t, client, before, test.updated, control)

			if _, err := conn.CallExtension(t.Context(), test.selected[0], map[string]any{
				"schemaVersion": 1,
				"sessionId":     string(sess.ID),
				"requestId":     test.name + "-get",
			}); err != nil {
				t.Fatalf("selected get method %q: %v", test.selected[0], err)
			}
			_, err = conn.CallExtension(t.Context(), test.selected[2], map[string]any{
				"schemaVersion": 1,
				"sessionId":     string(sess.ID),
				"requestId":     test.name + "-continue",
			})
			if err == nil {
				t.Fatalf("selected continue method %q accepted malformed request", test.selected[2])
			}
			{
				var requestErr *acpsdk.RequestError
				if errors.As(err, &requestErr) && requestErr.Code == CodeMethodNotFound {
					t.Fatalf("selected continue method %q returned method not found", test.selected[2])
				}
			}

			before = len(client.getExtensions())
			for _, method := range test.unselected {
				_, err := conn.CallExtension(t.Context(), method, map[string]any{
					"schemaVersion": 1,
					"sessionId":     string(sess.ID),
					"requestId":     test.name + "-unselected",
				})
				requireP245cWireError(t, err, CodeMethodNotFound)
			}
			if got := len(client.getExtensions()); got != before {
				t.Fatalf("unselected namespace emitted notifications: before=%d after=%d", before, got)
			}
		})
	}
}

func TestYHCACPGoalDualOfferFailureExposesNoGoalSurface(t *testing.T) {
	conn, _, agent := setupTestACPWithAgent(t, &mockChatModel{})
	_, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
			yhcGoalCapabilityKey:    yhcGoalOffer(1),
			legacyGoalCapabilityKey: yhcGoalOffer(2),
		}},
	})
	if err == nil {
		t.Fatal("different valid dual offer Initialize succeeded")
	}
	requireP245cWireError(t, err, CodeInvalidParams)
	yhcRequireNoGoalSurface(t, agent)
	retried, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
			yhcGoalCapabilityKey: yhcGoalOffer(1),
		}},
	})
	if err != nil {
		t.Fatalf("retry Initialize after dual failure: %v", err)
	}
	yhcRequireAgentIdentity(t, retried)
	yhcRequireGoalCapability(
		t,
		retried,
		yhcGoalCapabilityKey,
		map[string]any{"version": 1, "notifications": true},
	)
}

func TestYHCACPGoalNamespaceConnectionIsolation(t *testing.T) {
	canonicalConn, canonicalClient, canonicalAgent := setupTestACPWithAgent(t, &mockChatModel{})
	legacyConn, legacyClient, legacyAgent := setupTestACPWithAgent(t, &mockChatModel{})
	type connection struct {
		name           string
		conn           *acpsdk.ClientSideConnection
		client         *testClient
		agent          *Agent
		capability     string
		controlMethod  string
		rejectedMethod string
		updatedMethod  string
		session        *Session
		before         int
	}
	connections := []*connection{
		{
			name:           "canonical",
			conn:           canonicalConn,
			client:         canonicalClient,
			agent:          canonicalAgent,
			capability:     yhcGoalCapabilityKey,
			controlMethod:  yhcGoalControlMethod,
			rejectedMethod: legacyGoalControlMethod,
			updatedMethod:  yhcGoalUpdatedMethod,
		},
		{
			name:           "legacy",
			conn:           legacyConn,
			client:         legacyClient,
			agent:          legacyAgent,
			capability:     legacyGoalCapabilityKey,
			controlMethod:  legacyGoalControlMethod,
			rejectedMethod: yhcGoalControlMethod,
			updatedMethod:  legacyGoalUpdatedMethod,
		},
	}

	type initializeResult struct {
		index    int
		response acpsdk.InitializeResponse
		err      error
	}
	initialized := make(chan initializeResult, len(connections))
	ctx := t.Context()
	for index, current := range connections {
		go func() {
			response, err := current.conn.Initialize(ctx, acpsdk.InitializeRequest{
				ProtocolVersion: acpsdk.ProtocolVersionNumber,
				ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
					current.capability: yhcGoalOffer(1),
				}},
			})
			initialized <- initializeResult{index: index, response: response, err: err}
		}()
	}
	for range connections {
		result := <-initialized
		current := connections[result.index]
		if result.err != nil {
			t.Fatalf("%s Initialize: %v", current.name, result.err)
		}
		yhcRequireGoalCapability(
			t,
			result.response,
			current.capability,
			map[string]any{"version": 1, "notifications": true},
		)
		current.session = installP245cGoalSession(t, current.agent, &mockChatModel{})
		current.before = len(current.client.getExtensions())
	}

	type controlResult struct {
		index int
		raw   json.RawMessage
		err   error
	}
	controlled := make(chan controlResult, len(connections))
	for index, current := range connections {
		go func() {
			raw, err := current.conn.CallExtension(ctx, current.controlMethod, map[string]any{
				"schemaVersion":    1,
				"sessionId":        string(current.session.ID),
				"requestId":        current.name + "-isolation",
				"operation":        "create",
				"expectedRevision": 0,
				"objective":        "keep Goal namespaces isolated by connection",
			})
			controlled <- controlResult{index: index, raw: raw, err: err}
		}()
	}
	for range connections {
		result := <-controlled
		current := connections[result.index]
		if result.err != nil {
			t.Fatalf("%s control: %v", current.name, result.err)
		}
		var response acpGoalResponse
		if err := json.Unmarshal(result.raw, &response); err != nil {
			t.Fatal(err)
		}
		yhcRequireMatchingGoalNotification(
			t,
			current.client,
			current.before,
			current.updatedMethod,
			response,
		)
		beforeRejected := len(current.client.getExtensions())
		_, err := current.conn.CallExtension(ctx, current.rejectedMethod, map[string]any{
			"schemaVersion": 1,
			"sessionId":     string(current.session.ID),
			"requestId":     current.name + "-cross-namespace",
		})
		requireP245cWireError(t, err, CodeMethodNotFound)
		if got := len(current.client.getExtensions()); got != beforeRejected {
			t.Fatalf("%s rejected namespace emitted a notification", current.name)
		}
	}
}

func TestYHCACPGoalSelectionIsImmutableAcrossRepeatedInitialize(t *testing.T) {
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	first, err := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
			yhcGoalCapabilityKey: yhcGoalOffer(1),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	yhcRequireGoalCapability(t, first, yhcGoalCapabilityKey, map[string]any{"version": 1, "notifications": true})
	repeated, err := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
			legacyGoalCapabilityKey: yhcGoalOffer(1),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	yhcRequireGoalCapability(t, repeated, yhcGoalCapabilityKey, map[string]any{"version": 1, "notifications": true})
	yhcRequireNoGoalMethod(t, agent, legacyGoalGetMethod)
}

const (
	yhcGoalCapabilityKey     = "yhc.goal"
	yhcGoalGetMethod         = "_yhc/goal/get"
	yhcGoalControlMethod     = "_yhc/goal/control"
	yhcGoalContinueMethod    = "_yhc/goal/continue"
	yhcGoalUpdatedMethod     = "_yhc/goal/updated"
	legacyGoalCapabilityKey  = "eino-agent.goal"
	legacyGoalGetMethod      = "_eino/goal/get"
	legacyGoalControlMethod  = "_eino/goal/control"
	legacyGoalContinueMethod = "_eino/goal/continue"
	legacyGoalUpdatedMethod  = "_eino/goal/updated"
)

var (
	yhcGoalRequestMethods    = []string{yhcGoalGetMethod, yhcGoalControlMethod, yhcGoalContinueMethod}
	legacyGoalRequestMethods = []string{legacyGoalGetMethod, legacyGoalControlMethod, legacyGoalContinueMethod}
)

func yhcGoalOffer(version int) map[string]any {
	return map[string]any{"versions": []any{float64(version)}, "notifications": true}
}

func yhcRequireGoalCapability(t *testing.T, response acpsdk.InitializeResponse, key string, want any) {
	t.Helper()
	if response.AgentCapabilities.Meta == nil {
		t.Fatalf("Goal capability %q was not advertised", key)
	}
	got, ok := response.AgentCapabilities.Meta[key]
	if !ok || !equalJSONValue(got, want) || len(response.AgentCapabilities.Meta) != 1 {
		t.Fatalf("Goal capability = %#v, want only %q = %#v", response.AgentCapabilities.Meta, key, want)
	}
}

func yhcRequireAgentIdentity(t *testing.T, response acpsdk.InitializeResponse) {
	t.Helper()
	if response.AgentInfo == nil ||
		response.AgentInfo.Name != "yhc" ||
		response.AgentInfo.Title == nil ||
		*response.AgentInfo.Title != "YHC — Yet Hooked on Coding" {
		t.Fatalf("ACP AgentInfo = %#v", response.AgentInfo)
	}
}

func yhcRequireNoGoalSurface(t *testing.T, agent *Agent) {
	t.Helper()
	for _, method := range append(append([]string{}, yhcGoalRequestMethods...), legacyGoalRequestMethods...) {
		yhcRequireNoGoalMethod(t, agent, method)
	}
}

func yhcRequireNoGoalMethod(t *testing.T, agent *Agent, method string) {
	t.Helper()
	_, err := agent.HandleExtensionMethod(t.Context(), method, json.RawMessage(`{"schemaVersion":1,"sessionId":"missing","requestId":"no-goal"}`))
	requireP245cWireError(t, err, CodeMethodNotFound)
}

func yhcRequireMatchingGoalNotification(t *testing.T, client *testClient, before int, method string, response acpGoalResponse) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		extensions := client.getExtensions()
		if len(extensions) > before {
			got := extensions[before:]
			if len(got) != 1 {
				t.Fatalf("Goal transition emitted %d notifications: %#v", len(got), got)
			}
			if got[0].Method != method {
				t.Fatalf("Goal notification method = %q, want %q", got[0].Method, method)
			}
			var projected acpGoalResponse
			if err := json.Unmarshal(got[0].Params, &projected); err != nil {
				t.Fatal(err)
			}
			if !equalJSONValue(projected, response) {
				t.Fatalf("notification payload changed: got %#v want %#v", projected, response)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("missing %q notification after %#v: %#v", method, response, extensions)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestP245cGoalExtensionsRequireNegotiationAndStrictSchema(t *testing.T) {
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	_, err = agent.HandleExtensionMethod(
		t.Context(),
		legacyGoalGetMethod,
		json.RawMessage(`{"schemaVersion":1,"sessionId":"s","requestId":"r"}`),
	)
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != CodeMethodNotFound {
		t.Fatalf("unnegotiated Goal error = %#v", err)
	}

	initializeP245cGoalAgent(t, agent)
	_, err = agent.HandleExtensionMethod(
		t.Context(),
		legacyGoalGetMethod,
		json.RawMessage(`{"schemaVersion":1,"sessionId":"s","requestId":"r","extra":true}`),
	)
	if !errors.As(err, &requestErr) || requestErr.Code != CodeInvalidParams {
		t.Fatalf("unknown-field error = %#v", err)
	}
	_, err = agent.HandleExtensionMethod(
		t.Context(),
		legacyGoalGetMethod,
		json.RawMessage(`{"schemaVersion":1,"sessionId":"missing","requestId":"r"}`),
	)
	if !errors.As(err, &requestErr) || requestErr.Code != CodeSessionNotFound {
		t.Fatalf("missing-session error = %#v", err)
	}
}

func TestP245cACPGoalEngineConstructionCapturesNegotiation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	settingsDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(settingsDir, "settings.json"),
		[]byte(`{"goal":{"enabled":true,"default_token_budget":10000}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	negotiated, err := NewAgent(Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	negotiated.mockModel = &mockChatModel{}
	initializeP245cGoalAgent(t, negotiated)
	fresh, err := negotiated.createEngine("p24-5c-fresh", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if available, reason := fresh.GoalCommandAvailability(); !available {
		t.Fatalf("fresh negotiated ACP Goal unavailable: %s", reason)
	}
	if command := fresh.GetCommandRegistry().GetFor(
		commands.EntrypointACP,
		"goal",
	); command != nil {
		t.Fatalf("negotiated ACP leaked Goal slash command = %#v", command)
	}
	fresh.Close()
	restored, err := negotiated.createEngineForSession("p24-5c-restored", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if available, reason := restored.GoalCommandAvailability(); !available {
		t.Fatalf("restore negotiated ACP Goal unavailable: %s", reason)
	}
	restored.Close()
	negotiated.Close()

	unnegotiated, err := NewAgent(Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	unnegotiated.mockModel = &mockChatModel{}
	if _, err := unnegotiated.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	unsupported, err := unnegotiated.createEngine("p24-5c-unnegotiated", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if available, _ := unsupported.GoalCommandAvailability(); available {
		t.Fatal("unnegotiated ACP engine gained Goal authority")
	}
	unsupported.Close()
	unnegotiated.Close()

	disabledCWD := t.TempDir()
	disabledSettingsDir := filepath.Join(disabledCWD, ".claude")
	if err := os.MkdirAll(disabledSettingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(disabledSettingsDir, "settings.json"),
		[]byte(`{"goal":{"enabled":false}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	disabled, err := NewAgent(Config{CWD: disabledCWD})
	if err != nil {
		t.Fatal(err)
	}
	disabled.mockModel = &mockChatModel{}
	initializeP245cGoalAgent(t, disabled)
	disabledEngine, err := disabled.createEngine(
		"p24-5c-disabled",
		disabledCWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	if available, _ := disabledEngine.GoalCommandAvailability(); available {
		t.Fatal("negotiation bypassed the disabled Goal feature")
	}
	disabledEngine.Close()
	disabled.Close()
}

func TestP245cGoalControlProjectsOneDurableTransition(t *testing.T) {
	characterizeACPGoalControlAndRevision(t)
}

func characterizeACPGoalControlAndRevision(t *testing.T) {
	conn, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
	initializeP245cGoalConnection(t, conn)
	sess := installP245cGoalSession(t, agent, &mockChatModel{})

	raw, err := conn.CallExtension(t.Context(), legacyGoalControlMethod, map[string]any{
		"schemaVersion":    1,
		"sessionId":        string(sess.ID),
		"requestId":        "create-1",
		"operation":        "create",
		"expectedRevision": 0,
		"objective":        "finish the negotiated ACP Goal contract",
	})
	if err != nil {
		t.Fatal(err)
	}
	var created acpGoalResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Goal == nil ||
		created.Goal.Revision != 1 ||
		created.Goal.Status != "active" ||
		created.Goal.TokenBudget != nil ||
		!created.RequiresPrompt ||
		created.Phase != string(engine.GoalLifecycleCreated) {
		t.Fatalf("create response = %#v", created)
	}
	requireP245cGoalNotification(t, client, created.EventID, "create-1")

	_, err = conn.CallExtension(t.Context(), legacyGoalControlMethod, map[string]any{
		"schemaVersion":    1,
		"sessionId":        string(sess.ID),
		"requestId":        "create-duplicate",
		"operation":        "create",
		"expectedRevision": 0,
		"objective":        "must not replace",
	})
	requireP245cWireError(t, err, CodeGoalConflict)
	current, _ := sess.Engine.GoalSnapshot()
	if current.Revision != created.Goal.Revision ||
		current.Objective != created.Goal.Objective {
		t.Fatalf("duplicate create changed Goal = %#v", current)
	}

	raw, err = conn.CallExtension(t.Context(), legacyGoalGetMethod, map[string]any{
		"schemaVersion": 1,
		"sessionId":     string(sess.ID),
		"requestId":     "get-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got acpGoalResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Goal == nil || got.Goal.GoalID != created.Goal.GoalID ||
		got.RequestID != "get-1" || got.EventID == "" {
		t.Fatalf("get response = %#v", got)
	}
}

func TestP245cGoalContinueConsumesExactCursorOnce(t *testing.T) {
	characterizeACPGoalCursorConsumption(t)
}

func characterizeACPGoalCursorConsumption(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{
		p245cUsageMessage("first turn", 10),
		p245cUsageMessage("continued turn", 11),
	}}
	conn, _, agent := setupTestACPWithAgent(t, model)
	initializeP245cGoalConnection(t, conn)
	sess := installP245cGoalSession(t, agent, model)
	createP245cActiveGoal(t, sess.Engine)

	events, _ := sess.Engine.SubmitMessage(t.Context(), "start the Goal")
	for range events {
	}
	before, ok := sess.Engine.GoalSnapshot()
	if !ok || before.ContinuationOrdinal == 0 {
		t.Fatalf("first turn did not publish continuation = %#v", before)
	}
	request := map[string]any{
		"schemaVersion":               1,
		"sessionId":                   string(sess.ID),
		"requestId":                   "continue-1",
		"expectedGoalId":              before.GoalID,
		"expectedRevision":            before.Revision,
		"expectedObjectiveRevision":   before.ObjectiveRevision,
		"expectedContinuationOrdinal": before.ContinuationOrdinal,
	}
	raw, err := conn.CallExtension(t.Context(), legacyGoalContinueMethod, request)
	if err != nil {
		t.Fatal(err)
	}
	var continued acpGoalResponse
	if err := json.Unmarshal(raw, &continued); err != nil {
		t.Fatal(err)
	}
	if continued.Goal == nil ||
		continued.Goal.ContinuationOrdinal <= before.ContinuationOrdinal ||
		continued.GoalTurnID == "" ||
		continued.TerminalReason == "" ||
		model.CallCount() != 2 {
		t.Fatalf("continue response = %#v, calls=%d", continued, model.CallCount())
	}

	_, err = conn.CallExtension(t.Context(), legacyGoalContinueMethod, request)
	requireP245cWireError(t, err, CodeGoalConflict)
	if model.CallCount() != 2 {
		t.Fatalf("duplicate continue model calls = %d", model.CallCount())
	}
}

func TestP245cGoalContinueCancelJoinsAndPauses(t *testing.T) {
	blocking := &p245cSecondCallBlockingModel{
		secondStarted: make(chan struct{}),
	}
	conn, _, agent := setupTestACPWithAgent(t, blocking)
	initializeP245cGoalConnection(t, conn)
	sess := installP245cGoalSession(t, agent, blocking)
	createP245cActiveGoal(t, sess.Engine)

	events, _ := sess.Engine.SubmitMessage(t.Context(), "start the Goal")
	for range events {
	}
	before, ok := sess.Engine.GoalSnapshot()
	if !ok || before.ContinuationOrdinal == 0 {
		t.Fatalf("first turn did not publish continuation = %#v", before)
	}
	request := map[string]any{
		"schemaVersion":               1,
		"sessionId":                   string(sess.ID),
		"requestId":                   "continue-cancel",
		"expectedGoalId":              before.GoalID,
		"expectedRevision":            before.Revision,
		"expectedObjectiveRevision":   before.ObjectiveRevision,
		"expectedContinuationOrdinal": before.ContinuationOrdinal,
	}
	type continuationResult struct {
		raw json.RawMessage
		err error
	}
	done := make(chan continuationResult, 1)
	go func() {
		raw, callErr := conn.CallExtension(
			t.Context(),
			legacyGoalContinueMethod,
			request,
		)
		done <- continuationResult{raw: raw, err: callErr}
	}()
	select {
	case <-blocking.secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Goal continuation did not reach the model")
	}
	if err := conn.Cancel(t.Context(), acpsdk.CancelNotification{
		SessionId: sess.ID,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		var response acpGoalResponse
		if err := json.Unmarshal(result.raw, &response); err != nil {
			t.Fatal(err)
		}
		if response.Goal == nil || response.Goal.Status != "paused" {
			t.Fatalf(
				"cancelled continuation response status=%q reason=%q full=%#v",
				response.Goal.Status,
				response.Goal.StatusReasonCode,
				response,
			)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Goal continuation did not join")
	}
	sess.mu.Lock()
	promptActive := sess.promptActive
	sess.mu.Unlock()
	if promptActive {
		t.Fatal("cancelled Goal continuation retained Session prompt ownership")
	}
	if blocking.CallCount() != 2 {
		t.Fatalf("cancelled continuation model calls = %d", blocking.CallCount())
	}
}

func TestP245cGoalContinueCloseJoinsAndPauses(t *testing.T) {
	blocking := &p245cSecondCallBlockingModel{
		secondStarted: make(chan struct{}),
	}
	conn, _, agent := setupTestACPWithAgent(t, blocking)
	initializeP245cGoalConnection(t, conn)
	sess := installP245cGoalSession(t, agent, blocking)
	createP245cActiveGoal(t, sess.Engine)

	events, _ := sess.Engine.SubmitMessage(t.Context(), "start the Goal")
	for range events {
	}
	before, ok := sess.Engine.GoalSnapshot()
	if !ok || before.ContinuationOrdinal == 0 {
		t.Fatalf("first turn did not publish continuation = %#v", before)
	}
	request := map[string]any{
		"schemaVersion":               1,
		"sessionId":                   string(sess.ID),
		"requestId":                   "continue-close",
		"expectedGoalId":              before.GoalID,
		"expectedRevision":            before.Revision,
		"expectedObjectiveRevision":   before.ObjectiveRevision,
		"expectedContinuationOrdinal": before.ContinuationOrdinal,
	}
	type continuationResult struct {
		raw json.RawMessage
		err error
	}
	done := make(chan continuationResult, 1)
	go func() {
		raw, callErr := conn.CallExtension(
			t.Context(),
			legacyGoalContinueMethod,
			request,
		)
		done <- continuationResult{raw: raw, err: callErr}
	}()
	select {
	case <-blocking.secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Goal continuation did not reach the model")
	}
	closed := make(chan struct{})
	go func() {
		sess.close()
		close(closed)
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		var response acpGoalResponse
		if err := json.Unmarshal(result.raw, &response); err != nil {
			t.Fatal(err)
		}
		if response.Goal == nil || response.Goal.Status != "paused" {
			t.Fatalf("closed continuation response = %#v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Session close did not join the Goal continuation")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Session close did not finish after Goal continuation joined")
	}
	sess.mu.Lock()
	sessionClosed := sess.closed
	promptActive := sess.promptActive
	sess.mu.Unlock()
	if !sessionClosed || promptActive {
		t.Fatalf(
			"closed Session state: closed=%t promptActive=%t",
			sessionClosed,
			promptActive,
		)
	}
	if blocking.CallCount() != 2 {
		t.Fatalf("closed continuation model calls = %d", blocking.CallCount())
	}
}

func TestP245cGoalContinueDeliveryFailureCannotReplayTurn(t *testing.T) {
	characterizeACPGoalDeliveryFailure(t)
}

func characterizeACPGoalDeliveryFailure(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{
		p245cUsageMessage("first turn", 10),
		p245cUsageMessage("continued turn", 11),
	}}
	agent := newP231FailingWireAgent(t)
	initializeP245cGoalAgent(t, agent)
	sess := installP245cGoalSession(t, agent, model)
	createP245cActiveGoal(t, sess.Engine)

	events, _ := sess.Engine.SubmitMessage(t.Context(), "start the Goal")
	for range events {
	}
	before, ok := sess.Engine.GoalSnapshot()
	if !ok || before.ContinuationOrdinal == 0 {
		t.Fatalf("first turn did not publish continuation = %#v", before)
	}
	params, err := json.Marshal(map[string]any{
		"schemaVersion":               1,
		"sessionId":                   string(sess.ID),
		"requestId":                   "continue-delivery-failure",
		"expectedGoalId":              before.GoalID,
		"expectedRevision":            before.Revision,
		"expectedObjectiveRevision":   before.ObjectiveRevision,
		"expectedContinuationOrdinal": before.ContinuationOrdinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.HandleExtensionMethod(
		t.Context(),
		legacyGoalContinueMethod,
		params,
	); err == nil || !strings.Contains(err.Error(), "delivery failed") {
		t.Fatalf("delivery failure error = %v", err)
	}
	after, _ := sess.Engine.GoalSnapshot()
	if after == nil || after.Revision <= before.Revision {
		t.Fatalf(
			"delivery failure did not settle durable truth: before=%#v after=%#v",
			before,
			after,
		)
	}
	if model.CallCount() != 2 {
		t.Fatalf("delivery failure model calls = %d", model.CallCount())
	}
	if _, err := agent.HandleExtensionMethod(
		t.Context(),
		legacyGoalContinueMethod,
		params,
	); err == nil {
		t.Fatal("delivery-unknown continuation replayed without conflict")
	} else {
		var requestErr *acpsdk.RequestError
		if !errors.As(err, &requestErr) ||
			requestErr.Code != CodeGoalConflict {
			t.Fatalf("delivery-unknown retry error = %#v", err)
		}
	}
	if model.CallCount() != 2 {
		t.Fatalf("delivery-unknown retry model calls = %d", model.CallCount())
	}
}

func characterizeYHCProtocolMigrationGoalBehavior(t *testing.T) {
	t.Helper()
	t.Run("control get revision and notification", characterizeACPGoalControlAndRevision)
	t.Run("continue consumes one cursor", characterizeACPGoalCursorConsumption)
	t.Run("delivery failure cannot replay", characterizeACPGoalDeliveryFailure)
}

func installP245cGoalSession(
	t *testing.T,
	agent *Agent,
	chatModel model.BaseChatModel,
) *Session {
	t.Helper()
	cwd := t.TempDir()
	sessionID := acpsdk.SessionId("p24-5c-" + filepath.Base(cwd))
	queryEngine := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         string(sessionID),
		ThreadID:          string(sessionID),
		CWD:               cwd,
		TranscriptDir:     filepath.Join(cwd, "transcripts"),
		ChatModel:         chatModel,
		PermissionMode:    permission.ModeBypassPermissions,
		CommandEntrypoint: commands.EntrypointACP,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled:       true,
			ACPNegotiated: true,
		},
	})
	sess := newSession(sessionID, queryEngine, cwd)
	agent.mu.Lock()
	agent.sessions[sessionID] = sess
	agent.mu.Unlock()
	return sess
}

type p245cSecondCallBlockingModel struct {
	mu            sync.Mutex
	calls         int
	secondStarted chan struct{}
	once          sync.Once
}

func (m *p245cSecondCallBlockingModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return p245cUsageMessage("first turn", 10), nil
	}
	m.once.Do(func() { close(m.secondStarted) })
	<-ctx.Done()
	return p245cUsageMessage("cancelled turn usage", 1), nil
}

func (m *p245cSecondCallBlockingModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *p245cSecondCallBlockingModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func createP245cActiveGoal(t *testing.T, queryEngine *engine.QueryEngine) {
	t.Helper()
	if _, err := queryEngine.ApplyGoalControl(engine.GoalControlRequest{
		Operation:        engine.GoalControlCreate,
		ExpectedRevision: 0,
		Objective:        "complete the ACP continuation test",
	}); err != nil {
		t.Fatal(err)
	}
}

func initializeP245cGoalAgent(t *testing.T, agent *Agent) {
	t.Helper()
	response, err := agent.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
			legacyGoalCapabilityKey: map[string]any{
				"versions":      []int{1},
				"notifications": true,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.AgentCapabilities.Meta == nil {
		t.Fatal("Goal capability was not negotiated")
	}
}

func initializeP245cGoalConnection(
	t *testing.T,
	conn *acpsdk.ClientSideConnection,
) {
	t.Helper()
	response, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{Meta: map[string]any{
			legacyGoalCapabilityKey: map[string]any{
				"versions":      []int{1},
				"notifications": true,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.AgentCapabilities.Meta == nil {
		t.Fatal("Goal capability was not negotiated")
	}
}

func p245cUsageMessage(content string, tokens int) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:     tokens - 1,
			CompletionTokens: 1,
			TotalTokens:      tokens,
		}},
	}
}

func requireP245cGoalNotification(
	t *testing.T,
	client *testClient,
	eventID string,
	requestID string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, notification := range client.getExtensions() {
			if notification.Method != legacyGoalUpdatedMethod {
				continue
			}
			var projected acpGoalResponse
			if err := json.Unmarshal(notification.Params, &projected); err != nil {
				t.Fatal(err)
			}
			if projected.EventID == eventID &&
				projected.RequestID == requestID {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"missing Goal notification event=%q request=%q: %#v",
				eventID,
				requestID,
				client.getExtensions(),
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func requireP245cWireError(t *testing.T, err error, code int) {
	t.Helper()
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != code {
		t.Fatalf("request error = %#v, want code %d", err, code)
	}
}

func equalJSONValue(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil &&
		rightErr == nil &&
		string(leftJSON) == string(rightJSON)
}
