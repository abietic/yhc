package session

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

func TestP292PersistedModelBindingValidProjectionAndClone(t *testing.T) {
	t.Parallel()

	contextWindow := 200000
	outputLimit := 32000
	binding := &PersistedModelBinding{
		Version:             PersistedModelBindingVersion,
		Kind:                ModelBindingKindProfile,
		Value:               "primary",
		Provider:            "openai",
		APIModel:            "gpt-5",
		PortfolioRevision:   bindingTestDigest('a'),
		RouteIdentityDigest: bindingTestDigest('b'),
		MetadataDigest:      bindingTestDigest('c'),
		ContextWindowTokens: &contextWindow,
		MaxOutputTokens:     &outputLimit,
		ReasoningEffort:     "high",
	}
	if err := binding.ValidateV1(); err != nil {
		t.Fatalf("ValidateV1() error = %v", err)
	}
	if got := SafeModelBindingProjection(binding); got != (ModelBindingProjection{
		State: ModelBindingStateValid,
		Kind:  ModelBindingKindProfile,
		Value: "primary",
	}) {
		t.Fatalf("projection = %#v", got)
	}

	clone := binding.Clone()
	*clone.ContextWindowTokens = 1
	*clone.MaxOutputTokens = 2
	if *binding.ContextWindowTokens != contextWindow ||
		*binding.MaxOutputTokens != outputLimit {
		t.Fatal("Clone() aliased known limit pointers")
	}
}

func TestPersistedModelBindingAcceptsSafeAdapterOwnedReasoningID(t *testing.T) {
	binding := &PersistedModelBinding{
		Version:             PersistedModelBindingVersion,
		Kind:                ModelBindingKindProfile,
		Value:               "primary",
		Provider:            "future-provider",
		APIModel:            "future-model",
		PortfolioRevision:   bindingTestDigest('a'),
		RouteIdentityDigest: bindingTestDigest('b'),
		MetadataDigest:      bindingTestDigest('c'),
		ReasoningEffort:     "ultra-2",
	}
	if err := binding.ValidateV1(); err != nil {
		t.Fatalf("safe adapter-owned effort rejected: %v", err)
	}
	binding.ReasoningEffort = "has space"
	if err := binding.ValidateV1(); err == nil {
		t.Fatal("unsafe reasoning ID was accepted")
	}
}

func TestP292PersistedModelBindingPreservesOpaqueNestedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		raw   string
		state string
	}{
		{
			name:  "field type mismatch",
			raw:   `{"version":1,"kind":["profile"],"future":{"x":1}}`,
			state: ModelBindingStateInvalid,
		},
		{
			name:  "unsupported version",
			raw:   `{"version":7,"kind":"profile","value":"do-not-project","future":{"x":1}}`,
			state: ModelBindingStateUnsupportedVersion,
		},
		{
			name:  "valid json scalar",
			raw:   `"do-not-project"`,
			state: ModelBindingStateInvalid,
		},
		{
			name:  "unknown v1 field",
			raw:   `{"version":1,"kind":"profile","value":"do-not-project","future":{"x":1}}`,
			state: ModelBindingStateInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var metadata SessionMetadataFull
			envelope := []byte(`{"session_id":"session","model_binding":` + tc.raw + `}`)
			if err := json.Unmarshal(envelope, &metadata); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			projection := SafeModelBindingProjection(metadata.ModelBinding)
			if projection.State != tc.state ||
				projection.Kind != "" ||
				projection.Value != "" {
				t.Fatalf("projection = %#v", projection)
			}

			clone := metadata.ModelBinding.Clone()
			encoded, err := json.Marshal(&SessionMetadataFull{
				SessionID:    "session",
				ModelBinding: clone,
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if !bytes.Contains(encoded, []byte(tc.raw)) {
				t.Fatalf("opaque binding changed: %s", encoded)
			}
		})
	}
}

func TestP292PersistedModelBindingRejectsUntrustedProjection(t *testing.T) {
	t.Parallel()

	binding := &PersistedModelBinding{
		Version:             PersistedModelBindingVersion,
		Kind:                ModelBindingKindProfile,
		Value:               "Primary",
		Provider:            "openai",
		APIModel:            "gpt-5",
		PortfolioRevision:   bindingTestDigest('a'),
		RouteIdentityDigest: bindingTestDigest('b'),
		MetadataDigest:      bindingTestDigest('c'),
	}
	if err := binding.ValidateV1(); err == nil {
		t.Fatal("ValidateV1() accepted a non-canonical profile")
	}
	if got := SafeModelBindingProjection(binding); got != (ModelBindingProjection{
		State: ModelBindingStateInvalid,
	}) {
		t.Fatalf("projection = %#v", got)
	}
}

func TestP292BranchPreservesOpaqueBindingWithoutProjectingIt(t *testing.T) {
	dir := t.TempDir()
	sourceID := "p292-opaque-source"
	source := transcript.NewRecorder(sourceID, dir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}
	if err := source.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	var opaque PersistedModelBinding
	raw := `{"version":7,"kind":"profile","value":"unsafe-profile","future_secret":"must-not-project"}`
	if err := json.Unmarshal([]byte(raw), &opaque); err != nil {
		t.Fatal(err)
	}
	sourceMetadata := &SessionMetadataFull{
		SessionID:    sourceID,
		Model:        "resolved-model",
		Provider:     "resolved-provider",
		ModelBinding: &opaque,
	}
	if err := WriteSessionMetadata(source, sourceMetadata); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := BranchSession(BranchOptions{
		SourceSessionID: sourceID,
		MessageIndex:    len(messages),
		NewSessionID:    "p292-opaque-child",
		Dir:             dir,
		Metadata:        sourceMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := transcript.NewRecorder(result.NewSessionID, dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	childMetadata := ReadSessionMetadataFull(child)
	if childMetadata == nil || childMetadata.ModelBinding == nil {
		t.Fatalf("child metadata = %#v", childMetadata)
	}
	encoded, err := json.Marshal(childMetadata.ModelBinding)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != raw {
		t.Fatalf("opaque child binding changed: %s", encoded)
	}

	listed, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var childProjection ModelBindingProjection
	for _, info := range listed {
		if info.SessionID == result.NewSessionID {
			childProjection = info.ModelBinding
		}
	}
	if childProjection != (ModelBindingProjection{
		State: ModelBindingStateUnsupportedVersion,
	}) {
		t.Fatalf("child listing projection = %#v", childProjection)
	}

	exported, err := ExportSession(ExportOptions{
		SessionID:       result.NewSessionID,
		Dir:             dir,
		Format:          ExportJSON,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var exportedSession ExportedSession
	if err := json.Unmarshal(
		[]byte(exported.Content),
		&exportedSession,
	); err != nil {
		t.Fatal(err)
	}
	if exportedSession.Metadata == nil ||
		exportedSession.Metadata.ModelBinding != (ModelBindingProjection{
			State: ModelBindingStateUnsupportedVersion,
		}) {
		t.Fatalf("safe export projection = %s", exported.Content)
	}
	for _, forbidden := range []string{
		"unsafe-profile",
		"future_secret",
		"must-not-project",
	} {
		if strings.Contains(exported.Content, forbidden) {
			t.Fatalf("export projected opaque field %q: %s", forbidden, exported.Content)
		}
	}
}

func TestP292ValidBindingListingAndExportsExposeOnlySafeProjection(t *testing.T) {
	dir := t.TempDir()
	sessionID := "p292-valid-projection"
	recorder := transcript.NewRecorder(sessionID, dir)
	if err := recorder.Record([]*schema.Message{{
		Role: schema.User, Content: "hello",
	}}, false); err != nil {
		t.Fatal(err)
	}
	binding := &PersistedModelBinding{
		Version:             PersistedModelBindingVersion,
		Kind:                ModelBindingKindProfile,
		Value:               "primary",
		Provider:            "agenticopenai",
		APIModel:            "gpt-4o",
		PortfolioRevision:   bindingTestDigest('a'),
		RouteIdentityDigest: bindingTestDigest('b'),
		MetadataDigest:      bindingTestDigest('c'),
	}
	if err := WriteSessionMetadata(recorder, &SessionMetadataFull{
		SessionID:    sessionID,
		Model:        binding.APIModel,
		Provider:     binding.Provider,
		ModelBinding: binding,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	listed, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ModelBinding != (ModelBindingProjection{
		State: ModelBindingStateValid,
		Kind:  ModelBindingKindProfile,
		Value: "primary",
	}) {
		t.Fatalf("listing projection = %#v", listed)
	}

	for _, format := range []ExportFormat{ExportJSON, ExportMarkdown} {
		exported, exportErr := ExportSession(ExportOptions{
			SessionID:       sessionID,
			Dir:             dir,
			Format:          format,
			IncludeMetadata: true,
		})
		if exportErr != nil {
			t.Fatal(exportErr)
		}
		if !strings.Contains(exported.Content, "primary") {
			t.Fatalf("%v export omitted safe selector: %s", format, exported.Content)
		}
		for _, forbidden := range []string{
			binding.PortfolioRevision,
			binding.RouteIdentityDigest,
			binding.MetadataDigest,
		} {
			if strings.Contains(exported.Content, forbidden) {
				t.Fatalf("%v export exposed digest: %s", format, exported.Content)
			}
		}
	}
}

func bindingTestDigest(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
