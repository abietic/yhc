package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/provider"
	"github.com/cloudwego/eino/schema"
)

func TestP306LegacyRichEnqueueUsesTypedRefOnlyLedger(t *testing.T) {
	transcriptDir := t.TempDir()
	eng := newP302bTestEngine(
		t,
		"p306-legacy-rich",
		transcriptDir,
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := eng.EnqueueUserInput(UserTurnInput{
		Display: "  inspect image  ",
		Prompt:  "  compare exactly  ",
		Images: []UserImage{{
			Name:       "private.png",
			Path:       "/private/screen.png",
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID == "" ||
		queued.Display != "inspect image" ||
		queued.Prompt != "compare exactly" ||
		queued.State != RuntimeItemPending ||
		len(queued.Images) != 1 ||
		queued.Images[0].Name != "" ||
		queued.Images[0].Path != "" ||
		queued.Images[0].MIMEType != "image/png" ||
		queued.Images[0].Base64Data != testUserImagePNGBase64 {
		t.Fatalf("legacy projection = %#v", queued)
	}

	items := eng.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].UserPrompt == nil ||
		items[0].UserPrompt.Prompt != "" ||
		len(items[0].UserPrompt.Images) != 0 ||
		items[0].UserPrompt.durablePrompt == nil {
		t.Fatalf("runtime ledger item = %#v", items)
	}
	record := items[0].UserPrompt.durablePrompt
	if len(record.Parts) != 2 ||
		record.Parts[0].Kind != promptrecord.PartText ||
		record.Parts[0].Text == nil ||
		record.Parts[0].Text.Text != "compare exactly" ||
		record.Parts[1].Kind != promptrecord.PartImage ||
		record.Parts[1].Image == nil {
		t.Fatalf("durable prompt order = %#v", record)
	}
	snapshots, err := eng.QueuedPromptInputs()
	if err != nil ||
		len(snapshots) != 1 ||
		len(snapshots[0].Parts) != 2 ||
		snapshots[0].Parts[0].Kind != QueuedPromptPartText ||
		snapshots[0].Parts[1].Kind != QueuedPromptPartImage {
		t.Fatalf("public rich snapshot = %#v, %v", snapshots, err)
	}

	ledgerPath := RuntimeInputPersistencePath(eng.transcript.Path())
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		testUserImagePNGBase64,
		"private.png",
		"/private/screen.png",
		`"images"`,
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("ledger retained %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"prompt_envelope"`) ||
		!strings.Contains(string(raw), `"media_id"`) {
		t.Fatalf("ledger is not ref-backed: %s", raw)
	}
}

func TestP306LegacyRichEnqueueRejectsRouteBeforeLedgerMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resolver   ModelResolver
		capability PromptCapabilityResolver
		reason     string
	}{
		{
			name:       "unknown route",
			capability: DefaultPromptCapabilityResolver(),
			reason:     "route_unknown",
		},
		{
			name:     "unsupported capability",
			resolver: promptInputOpenAIResolver(),
			capability: PromptCapabilityResolverFunc(
				func(provider.Provider, string) PromptCapabilityDecision {
					return PromptCapabilityDecision{
						Status: PromptCapabilityUnsupported,
						Source: "p306-test",
					}
				},
			),
			reason: "capability_unsupported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := NewQueryEngine(QueryEngineConfig{
				ChatModel:                &captureInputModel{},
				CWD:                      t.TempDir(),
				TranscriptDir:            t.TempDir(),
				SessionID:                "p306-route-" + strings.ReplaceAll(tc.name, " ", "-"),
				MaxTurns:                 2,
				Model:                    "gpt-4o",
				ModelResolver:            tc.resolver,
				PromptCapabilityResolver: tc.capability,
				HookExecutor:             hooks.NewExecutor(),
			})
			t.Cleanup(eng.Close)
			ledgerPath := RuntimeInputPersistencePath(eng.transcript.Path())

			_, err := eng.EnqueueUserInput(UserTurnInput{
				Prompt: "private prompt",
				Images: []UserImage{{
					MIMEType:   "image/png",
					Base64Data: testUserImagePNGBase64,
				}},
			})
			var admissionErr *PromptInputAdmissionError
			if !errors.As(err, &admissionErr) ||
				admissionErr.PartIndex != 1 ||
				admissionErr.PartKind != string(promptPartImage) ||
				admissionErr.ReasonCode != tc.reason {
				t.Fatalf("admission error = %#v", err)
			}
			if items := eng.RuntimeItems(); len(items) != 0 {
				t.Fatalf("rejected route mutated runtime ledger: %#v", items)
			}
			if _, statErr := os.Stat(ledgerPath); !os.IsNotExist(statErr) {
				t.Fatalf("rejected route created ledger: %v", statErr)
			}
		})
	}
}

func TestP306RichPromptCompactionRestartKeepsRefsBounded(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p306-compact-restart"
	firstModel := &scriptedOverflowModel{streams: [][]*schema.Message{{
		{Role: schema.Assistant, Content: "historical answer"},
	}}}
	first := NewQueryEngine(QueryEngineConfig{
		ChatModel:                firstModel,
		CWD:                      cwd,
		TranscriptDir:            transcriptDir,
		SessionID:                sessionID,
		MaxTurns:                 2,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
		HookExecutor:             hooks.NewExecutor(),
	})
	historical, _ := first.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("historical"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailAuto,
			),
		),
	)
	historicalTerminal, _ := collectPromptInputEvents(t, historical)
	if historicalTerminal.Reason != TerminalCompleted {
		t.Fatalf("historical terminal = %#v", historicalTerminal)
	}
	queued, err := first.EnqueueUserInput(UserTurnInput{
		Prompt: "current",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := RuntimeInputPersistencePath(first.transcript.Path())
	beforeRecord := readP302bLedgerRecord(t, ledgerPath)
	beforeRefs, err := beforeRecord.MediaRefs()
	if err != nil || len(beforeRefs) != 1 {
		t.Fatalf("before refs = %#v, %v", beforeRefs, err)
	}
	first.Close()

	restartedModel := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("media_size", "p306 private provider body")},
		{{Role: schema.Assistant, Content: "complete"}},
	}}
	restarted := NewQueryEngine(QueryEngineConfig{
		ChatModel:                restartedModel,
		CWD:                      cwd,
		TranscriptDir:            transcriptDir,
		SessionID:                sessionID,
		MaxTurns:                 2,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
		HookExecutor:             hooks.NewExecutor(),
	})
	t.Cleanup(restarted.Close)
	afterRecord := readP302bLedgerRecord(t, ledgerPath)
	afterRefs, err := afterRecord.MediaRefs()
	if err != nil ||
		beforeRecord.TurnID != afterRecord.TurnID ||
		!reflect.DeepEqual(beforeRefs, afterRefs) {
		t.Fatalf(
			"restart changed durable prompt: before=%#v after=%#v err=%v",
			beforeRecord,
			afterRecord,
			err,
		)
	}
	claimed, ok, err := restarted.ClaimNextRuntimeItem()
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim=%#v ok=%v err=%v", claimed, ok, err)
	}
	events, _ := restarted.SubmitRuntimeItem(context.Background(), claimed)
	terminal, observed := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted || restartedModel.callCount != 2 {
		t.Fatalf("terminal=%#v calls=%d", terminal, restartedModel.callCount)
	}
	boundaries := 0
	for _, event := range observed {
		if event.Type == EventCompactBoundary {
			boundaries++
		}
	}
	if boundaries != 1 {
		t.Fatalf("compact boundaries = %d, events=%#v", boundaries, observed)
	}
	if items := restarted.RuntimeItems(); len(items) != 0 {
		t.Fatalf("settled rich prompt remains queued: %#v", items)
	}

	transcriptBytes, err := os.ReadFile(restarted.transcript.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(transcriptBytes, []byte(testUserImagePNGBase64)) ||
		bytes.Contains(transcriptBytes, []byte("p306 private provider body")) {
		t.Fatal("compaction transcript retained inline or provider-private data")
	}
	mediaRoot := restarted.transcript.Path() + ".media"
	manifestBytes, err := os.ReadFile(filepath.Join(mediaRoot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest mediastore.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	blobCount := 0
	if err := filepath.WalkDir(
		filepath.Join(mediaRoot, "blobs"),
		func(_ string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type().IsRegular() {
				blobCount++
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) > 2 || blobCount > 2 {
		t.Fatalf(
			"compaction retained unbounded media derivatives: entries=%d blobs=%d",
			len(manifest.Entries),
			blobCount,
		)
	}
}

func TestP306PromptWriteOwnersRemainSealed(t *testing.T) {
	engineFiles := map[string][]string{
		"queued_input.go": {
			"EnqueueUserInput",
			"enqueueUserInputWithOwner",
		},
		"input_coordinator.go": {
			"enqueueBatchLocked",
		},
		"runtime_prompt_persistence.go": {
			"buildDurableRuntimePromptFromAdmitted",
		},
	}
	for path, names := range engineFiles {
		source, functions := p306SourceFunctions(t, path)
		for _, name := range names {
			if functions[name] == nil {
				t.Fatalf("%s is missing %s", path, name)
			}
		}
		switch path {
		case "queued_input.go":
			if !p306FunctionContains(
				source,
				functions["EnqueueUserInput"],
				"EnqueuePromptInput",
				"NewUntrustedPromptInput",
			) {
				t.Fatal("legacy rich enqueue no longer delegates to typed admission")
			}
			if p306FunctionContains(
				source,
				functions["enqueueUserInputWithOwner"],
				"Images:",
			) {
				t.Fatal("legacy queue helper can still construct inline image payloads")
			}
		case "input_coordinator.go":
			if !p306FunctionContains(
				source,
				functions["enqueueBatchLocked"],
				"len(item.UserPrompt.Images) > 0",
				"runtimeInlineImagesDecodeOnly",
			) {
				t.Fatal("generic enqueue no longer rejects inline user images")
			}
		}
	}

	_, persistenceFunctions := p306SourceFunctions(
		t,
		"runtime_prompt_persistence.go",
	)
	if persistenceFunctions["buildDurableRuntimePrompt"] != nil {
		t.Fatal("legacy direct durable prompt writer still exists")
	}

	tuiSource, tuiFunctions := p306SourceFunctions(
		t,
		filepath.Join("..", "internal", "tui", "app.go"),
	)
	if tuiFunctions["startEngineRequestWithImages"] != nil ||
		tuiFunctions["startEngineRequestWithMetadata"] != nil {
		t.Fatal("TUI alternate rich request helpers still exist")
	}
	start := tuiFunctions["startEngineRequest"]
	if start == nil ||
		!p306FunctionContains(tuiSource, start, ".SubmitMessage(ctx, prompt)") ||
		p306FunctionContains(tuiSource, start, "SubmitMessageWithImages") ||
		p306FunctionContains(tuiSource, start, "SubmitMessageWithMetadata") {
		t.Fatal("TUI start owner is not the string-only request path")
	}
}

func BenchmarkP306DurablePromptRecordBytesMaxParts(b *testing.B) {
	_, record := p306BenchmarkRecord(b)
	encoded, err := json.Marshal(record)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := json.Marshal(record); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(encoded)), "record-bytes")
}

func BenchmarkP306DurablePromptMaterializationMaxParts(b *testing.B) {
	store, record := p306BenchmarkRecord(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		input, err := materializeDurableRuntimePrompt(
			context.Background(),
			store,
			record,
		)
		if err != nil {
			b.Fatal(err)
		}
		if len(input.Parts) != maxPromptInputParts {
			b.Fatalf("materialized parts = %d", len(input.Parts))
		}
	}
}

func p306SourceFunctions(
	t *testing.T,
	path string,
) ([]byte, map[string]*ast.FuncDecl) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	functions := make(map[string]*ast.FuncDecl)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok {
			functions[function.Name.Name] = function
		}
	}
	return source, functions
}

func p306FunctionContains(
	source []byte,
	function *ast.FuncDecl,
	values ...string,
) bool {
	if function == nil {
		return false
	}
	start := int(function.Pos()) - 1
	end := int(function.End()) - 1
	if start < 0 || end < start || end > len(source) {
		return false
	}
	body := string(source[start:end])
	for _, value := range values {
		if !strings.Contains(body, value) {
			return false
		}
	}
	return true
}

func p306BenchmarkRecord(b *testing.B) (*mediastore.Store, promptrecord.Record) {
	b.Helper()
	store := mediastore.New(filepath.Join(b.TempDir(), "media"))
	record := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  "p306-benchmark",
		Parts:   make([]promptrecord.Part, 0, maxPromptInputParts),
	}
	for index := range maxPromptInputParts {
		if index%2 == 0 {
			record.Parts = append(record.Parts, promptrecord.Part{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "bounded text"},
			})
			continue
		}
		source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		source.SetNRGBA(0, 0, color.NRGBA{
			R: uint8(index),
			G: uint8(index * 3),
			B: uint8(index * 7),
			A: 0xff,
		})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, source); err != nil {
			b.Fatal(err)
		}
		ref, err := store.Put(
			context.Background(),
			encoded.Bytes(),
			mediastore.Metadata{
				MIMEType: "image/png",
				Width:    1,
				Height:   1,
				Kind:     "prompt_image",
			},
		)
		if err != nil {
			b.Fatal(err)
		}
		record.Parts = append(record.Parts, promptrecord.Part{
			Kind: promptrecord.PartImage,
			Image: &promptrecord.ImagePart{
				Ref:    ref,
				Detail: string(PromptImageDetailAuto),
			},
		})
	}
	if err := record.Validate(); err != nil {
		b.Fatal(err)
	}
	return store, record
}
