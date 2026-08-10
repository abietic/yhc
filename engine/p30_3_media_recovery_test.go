package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/recovery"
	"github.com/abietic/yhc/engine/transcript"
)

func TestP303QueryEngineCommitsHistoricalProjectionBeforeRetry(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p303-historical"
	rawProviderBody := "private provider body /tmp/secret ref-123"
	chatModel := &scriptedOverflowModel{streams: [][]*schema.Message{
		{{Role: schema.Assistant, Content: "historical answer"}},
		{overflowAPIError("media_size", rawProviderBody)},
		{{Role: schema.Assistant, Content: "complete rich answer"}},
	}}
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:                chatModel,
		CWD:                      cwd,
		TranscriptDir:            transcriptDir,
		SessionID:                sessionID,
		MaxTurns:                 3,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
		HookExecutor:             hooks.NewExecutor(),
	})
	t.Cleanup(engine.Close)

	first, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("old-before"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailHigh,
			),
			NewPromptTextPart("old-after"),
		),
	)
	firstTerminal, _ := collectPromptInputEvents(t, first)
	if firstTerminal.Reason != TerminalCompleted {
		t.Fatalf("first terminal = %#v", firstTerminal)
	}

	second, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("now-before"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailLow,
			),
			NewPromptTextPart("now-after"),
		),
	)
	secondTerminal, events := collectPromptInputEvents(t, second)
	if secondTerminal.Reason != TerminalCompleted || chatModel.callCount != 3 {
		t.Fatalf(
			"terminal = %#v, calls = %d",
			secondTerminal,
			chatModel.callCount,
		)
	}

	boundaries := 0
	attachments := 0
	for _, event := range events {
		switch event.Type {
		case EventCompactBoundary:
			if event.CompactBoundaryMessage != nil &&
				event.CompactBoundaryMessage.Extra["media_recovery_version"] == 1 {
				boundaries++
			}
		case EventAttachment:
			if event.AttachmentMessage != nil &&
				event.AttachmentMessage.Extra["attachment_kind"] ==
					"media_recovery" {
				attachments++
			}
		case EventAssistant:
			if event.AssistantMessage != nil &&
				strings.Contains(event.AssistantMessage.Content, rawProviderBody) {
				t.Fatal("raw provider body reached an assistant event")
			}
		}
	}
	if boundaries != 1 || attachments != 1 {
		t.Fatalf(
			"recovery events: boundaries=%d attachments=%d events=%#v",
			boundaries,
			attachments,
			events,
		)
	}
	retryInput := chatModel.inputs[2]
	marker := "" +
		"[historical image omitted during media-size recovery: " +
		"mime=image/png detail=high]"
	if !p303InputContainsTextPart(retryInput, marker) {
		t.Fatalf("selected retry did not contain ordered marker: %#v", retryInput)
	}
	retryBoundaries := 0
	for _, message := range retryInput {
		if message != nil &&
			message.Extra["media_recovery_version"] == 1 &&
			message.Extra["subtype"] == "compact_boundary" {
			retryBoundaries++
		}
	}
	if retryBoundaries != 1 {
		t.Fatalf(
			"selected retry recovery boundaries = %d, input=%#v",
			retryBoundaries,
			retryInput,
		)
	}
	if p303InputContainsText(retryInput, rawProviderBody) {
		t.Fatalf("selected retry leaked provider body: %#v", retryInput)
	}

	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(testUserImagePNGBase64)) ||
		bytes.Contains(raw, []byte(rawProviderBody)) {
		t.Fatal("transcript contains inline media or raw provider body")
	}
	if count := bytes.Count(
		raw,
		[]byte(`"kind":"compact-boundary"`),
	); count != 1 {
		t.Fatalf("media recovery compact boundaries = %d\n%s", count, raw)
	}
	loaded, err := transcript.NewRecorder(sessionID, transcriptDir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	activeBoundaries := 0
	for _, message := range loaded.Messages {
		if message == nil {
			continue
		}
		if message.Extra != nil &&
			message.Extra["media_recovery_version"] == float64(1) &&
			message.Extra["subtype"] == "compact_boundary" {
			activeBoundaries++
		}
	}
	if activeBoundaries != 1 ||
		!p303InputContainsTextPart(loaded.Messages, marker) ||
		len(loaded.PromptRecords) != 1 {
		t.Fatalf(
			"reloaded active projection boundaries=%d prompts=%d messages=%#v",
			activeBoundaries,
			len(loaded.PromptRecords),
			loaded.Messages,
		)
	}
	mediaRoot := path + ".media"
	var manifest mediastore.Manifest
	manifestBytes, err := os.ReadFile(
		filepath.Join(mediaRoot, "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	blobCount := 0
	if err := filepath.WalkDir(
		filepath.Join(mediaRoot, "blobs"),
		func(path string, entry os.DirEntry, walkErr error) error {
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
	if len(manifest.Entries) != 2 || blobCount != 1 {
		t.Fatalf(
			"canonical media changed: entries=%d blobs=%d manifest=%#v",
			len(manifest.Entries),
			blobCount,
			manifest,
		)
	}
}

func TestP303QueuedRichTurnKeepsDurableTurnIdentityDuringRecovery(
	t *testing.T,
) {
	transcriptDir := t.TempDir()
	sessionID := "p303-queued"
	chatModel := &scriptedOverflowModel{streams: [][]*schema.Message{
		{{Role: schema.Assistant, Content: "historical answer"}},
		{overflowAPIError("media_size", "queued raw provider body")},
		{{Role: schema.Assistant, Content: "queued rich answer"}},
	}}
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:                chatModel,
		CWD:                      t.TempDir(),
		TranscriptDir:            transcriptDir,
		SessionID:                sessionID,
		MaxTurns:                 3,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
		HookExecutor:             hooks.NewExecutor(),
	})
	t.Cleanup(engine.Close)
	historical, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("old"),
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
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "queued-current",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := readP302bLedgerRecord(
		t,
		RuntimeInputPersistencePath(engine.transcript.Path()),
	)
	claimed, ok, err := engine.ClaimNextRuntimeItem()
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim=%#v ok=%v err=%v", claimed, ok, err)
	}
	events, _ := engine.SubmitRuntimeItem(context.Background(), claimed)
	terminal, observed := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted || chatModel.callCount != 3 {
		t.Fatalf("terminal=%#v calls=%d", terminal, chatModel.callCount)
	}
	for _, event := range observed {
		if event.TurnID != record.TurnID {
			t.Fatalf(
				"recovery event turn=%q want=%q",
				event.TurnID,
				record.TurnID,
			)
		}
		if message := assistantEventMessage(event); message != nil &&
			strings.Contains(message.Content, "queued raw provider body") {
			t.Fatalf("queued provider body leaked: %#v", message)
		}
	}
	if !p303InputContainsTextPart(
		chatModel.inputs[2],
		"[historical image omitted during media-size recovery: "+
			"mime=image/png detail=auto]",
	) {
		t.Fatalf("queued recovery input = %#v", chatModel.inputs[2])
	}
}

func TestP303MediaRecoveryUsesExactOnePlusOnePlusOneFallback(t *testing.T) {
	current := p303EngineCurrentMessage(t, largeP302aPNG(t))
	currentBinding, err := recovery.BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	usage := &p303UsageAdmitter{}
	models := make([]string, 0, 3)
	logicalRounds := make([]string, 0, 3)
	call := 0
	maxTurns := 3
	prepareFallbackCalls := 0
	fallbackGuardCalls := 0
	var events []QueryEvent
	terminal := Query(context.Background(), QueryParams{
		Messages:      []*schema.Message{current},
		SystemPrompt:  &schema.Message{Role: schema.System, Content: "system"},
		FallbackModel: "fallback-model",
		QuerySource:   QuerySourceSDK,
		MaxTurns:      &maxTurns,
		ChatModel:     &captureInputModel{},
		HookExecutor:  hooks.NewExecutor(),
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "selected-model",
		}},
		Deps: &QueryDeps{
			UUID:          func() string { return "p303-uuid" },
			ProviderUsage: usage,
			CallModel: func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				options execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				models = append(models, options.Model)
				logicalRounds = append(
					logicalRounds,
					options.UsageLogicalRoundID,
				)
				call++
				if call <= 2 {
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray(
							[]*schema.Message{
								overflowAPIError(
									"media_size",
									"raw secret provider rejection",
								),
							},
						),
					}, nil
				}
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray(
						[]*schema.Message{{
							Role:    schema.Assistant,
							Content: "complete rich answer",
						}},
					),
				}, nil
			},
		},
		promptRouteGuard: func(model string) error {
			if model != "selected-model" {
				return errors.New("selected route mismatch")
			}
			return nil
		},
		mediaRecovery: &mediaRecoveryContext{
			current:       currentBinding,
			selectedModel: "selected-model",
			prepareFallbackRoute: func(model string) error {
				prepareFallbackCalls++
				if model != "fallback-model" {
					return errors.New("fallback mismatch")
				}
				return nil
			},
			fallbackRouteGuard: func(model string) error {
				fallbackGuardCalls++
				if model != "fallback-model" {
					return errors.New("fallback stale")
				}
				return nil
			},
		},
	}, func(event QueryEvent) {
		events = append(events, event)
	})
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if call != 3 ||
		strings.Join(models, ",") !=
			"selected-model,selected-model,fallback-model" {
		t.Fatalf("calls=%d models=%#v", call, models)
	}
	if prepareFallbackCalls != 1 || fallbackGuardCalls != 1 {
		t.Fatalf(
			"fallback admission prepare=%d guard=%d",
			prepareFallbackCalls,
			fallbackGuardCalls,
		)
	}
	if usage.newRoundCalls != 1 ||
		len(logicalRounds) != 3 ||
		logicalRounds[0] == "" ||
		logicalRounds[0] != logicalRounds[1] ||
		logicalRounds[1] != logicalRounds[2] {
		t.Fatalf(
			"logical round owner calls=%d rounds=%#v",
			usage.newRoundCalls,
			logicalRounds,
		)
	}
	mediaAttachments := 0
	mediaBoundaries := 0
	for _, event := range events {
		if event.Type == EventCompactBoundary &&
			event.CompactBoundaryMessage != nil &&
			event.CompactBoundaryMessage.Extra["media_recovery_version"] == 1 {
			mediaBoundaries++
		}
		if event.Type == EventAttachment &&
			event.AttachmentMessage != nil &&
			event.AttachmentMessage.Extra["attachment_kind"] ==
				"media_recovery" {
			mediaAttachments++
		}
		if message := assistantEventMessage(event); message != nil &&
			strings.Contains(message.Content, "raw secret") {
			t.Fatalf("raw provider body leaked: %#v", message)
		}
	}
	if mediaAttachments != 2 || mediaBoundaries != 1 {
		t.Fatalf(
			"media recovery attachments=%d boundaries=%d",
			mediaAttachments,
			mediaBoundaries,
		)
	}
}

func TestP303MediaRecoveryRejectsIneligibleFallbackBeforeAlternateCall(
	t *testing.T,
) {
	current := p303EngineCurrentMessage(t, largeP302aPNG(t))
	currentBinding, err := recovery.BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	call := 0
	maxTurns := 3
	terminal := Query(context.Background(), QueryParams{
		Messages:      []*schema.Message{current},
		SystemPrompt:  &schema.Message{Role: schema.System, Content: "system"},
		FallbackModel: "unknown-fallback",
		QuerySource:   QuerySourceSDK,
		MaxTurns:      &maxTurns,
		ChatModel:     &captureInputModel{},
		HookExecutor:  hooks.NewExecutor(),
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "selected-model",
		}},
		Deps: &QueryDeps{
			UUID: func() string { return "p303-uuid" },
			CallModel: func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				_ execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				call++
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray(
						[]*schema.Message{
							overflowAPIError(
								"media_size",
								"must stay private",
							),
						},
					),
				}, nil
			},
		},
		promptRouteGuard: func(string) error { return nil },
		mediaRecovery: &mediaRecoveryContext{
			current:       currentBinding,
			selectedModel: "selected-model",
			prepareFallbackRoute: func(string) error {
				return errors.New("capability unknown")
			},
			fallbackRouteGuard: func(string) error {
				t.Fatal("fallback guard ran without a candidate binding")
				return nil
			},
		},
	}, func(QueryEvent) {})
	if terminal.Reason != TerminalImageError || call != 2 {
		t.Fatalf("terminal=%#v calls=%d", terminal, call)
	}
	if terminal.Err == nil ||
		strings.Contains(terminal.Err.Error(), "must stay private") {
		t.Fatalf("terminal error was not bounded: %v", terminal.Err)
	}
}

func TestP303MediaRecoveryPersistenceFailurePrecedesStateEventAndRetry(
	t *testing.T,
) {
	historical := p303EngineCurrentMessage(t, largeP302aPNG(t))
	current := p303EngineCurrentMessage(t, largeP302aPNG(t))
	currentBinding, err := recovery.BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	recorder, historicalRecord := p303RecorderWithPrompt(
		t,
		historical,
		"turn-historical",
	)
	call := 0
	commitCalls := 0
	maxTurns := 2
	var events []QueryEvent
	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{historical, current},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    &captureInputModel{},
		HookExecutor: hooks.NewExecutor(),
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "selected-model",
		}},
		Deps: &QueryDeps{
			UUID:       func() string { return "p303-uuid" },
			Transcript: recorder,
			CallModel: func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				_ execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				call++
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray(
						[]*schema.Message{
							overflowAPIError(
								"media_size",
								"raw persistence-adjacent body",
							),
						},
					),
				}, nil
			},
		},
		promptRouteGuard: func(string) error { return nil },
		mediaRecovery: &mediaRecoveryContext{
			current:       currentBinding,
			selectedModel: "selected-model",
			commitBoundary: func(
				context.Context,
				[]*schema.Message,
			) error {
				commitCalls++
				return errors.New("private filesystem failure /tmp/secret")
			},
		},
	}, func(event QueryEvent) {
		events = append(events, event)
	})
	if terminal.Reason != TerminalPersistenceError ||
		call != 1 ||
		commitCalls != 1 {
		t.Fatalf(
			"terminal=%#v calls=%d commits=%d record=%#v",
			terminal,
			call,
			commitCalls,
			historicalRecord,
		)
	}
	for _, event := range events {
		if event.Type == EventCompactBoundary ||
			event.Type == EventAttachment &&
				event.AttachmentMessage != nil &&
				event.AttachmentMessage.Extra["attachment_kind"] ==
					"media_recovery" {
			t.Fatalf("post-commit event escaped failed commit: %#v", event)
		}
	}
	if terminal.Err == nil ||
		strings.Contains(terminal.Err.Error(), "/tmp/secret") {
		t.Fatalf("persistence error leaked private detail: %v", terminal.Err)
	}
}

func TestP303CancellationAfterBoundaryCommitPreventsRecoveryCall(
	t *testing.T,
) {
	historical := p303EngineCurrentMessage(t, largeP302aPNG(t))
	current := p303EngineCurrentMessage(t, largeP302aPNG(t))
	currentBinding, err := recovery.BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := p303RecorderWithPrompt(
		t,
		historical,
		"turn-historical",
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := 0
	commitCalls := 0
	var committedMessages []*schema.Message
	maxTurns := 2
	var events []QueryEvent
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{historical, current},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    &captureInputModel{},
		HookExecutor: hooks.NewExecutor(),
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "selected-model",
		}},
		Deps: &QueryDeps{
			UUID:       func() string { return "p303-uuid" },
			Transcript: recorder,
			CallModel: func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				_ execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				call++
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray(
						[]*schema.Message{
							overflowAPIError(
								"media_size",
								"raw cancellation-adjacent body",
							),
						},
					),
				}, nil
			},
		},
		promptRouteGuard: func(string) error { return nil },
		mediaRecovery: &mediaRecoveryContext{
			current:       currentBinding,
			selectedModel: "selected-model",
			commitBoundary: func(
				_ context.Context,
				messages []*schema.Message,
			) error {
				commitCalls++
				committedMessages = append(
					[]*schema.Message(nil),
					messages...,
				)
				cancel()
				return nil
			},
		},
	}, func(event QueryEvent) {
		events = append(events, event)
	})
	if terminal.Reason != TerminalAbortedStreaming ||
		call != 1 ||
		commitCalls != 1 ||
		len(committedMessages) == 0 {
		t.Fatalf(
			"terminal=%#v calls=%d commits=%d messages=%#v",
			terminal,
			call,
			commitCalls,
			committedMessages,
		)
	}
	if committedMessages[0].Extra["media_recovery_version"] != 1 ||
		!p303InputContainsTextPart(
			committedMessages,
			"[historical image omitted during media-size recovery: "+
				"mime=image/png detail=auto]",
		) {
		t.Fatalf("committed truth = %#v", committedMessages)
	}
	for _, event := range events {
		if event.Type == EventCompactBoundary ||
			event.Type == EventAttachment &&
				event.AttachmentMessage != nil &&
				event.AttachmentMessage.Extra["attachment_kind"] ==
					"media_recovery" {
			t.Fatalf("post-cancel recovery event escaped: %#v", event)
		}
	}
}

type p303UsageAdmitter struct {
	newRoundCalls int
}

func (a *p303UsageAdmitter) NewLogicalRoundID() string {
	a.newRoundCalls++
	return "logical-p303"
}

func (a *p303UsageAdmitter) AdmitProviderUsage(
	context.Context,
	execution.ProviderUsageDescriptor,
) (execution.ProviderUsageCall, error) {
	return nil, nil
}

func p303EngineCurrentMessage(t *testing.T, data []byte) *schema.Message {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(data)
	return &schema.Message{
		Role:    schema.User,
		Content: "beforenow",
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "before"},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   "image/png",
					},
					Detail: schema.ImageURLDetailAuto,
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: "now"},
		},
	}
}

func p303InputContainsTextPart(
	messages []*schema.Message,
	want string,
) bool {
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, part := range message.UserInputMultiContent {
			if part.Type == schema.ChatMessagePartTypeText &&
				part.Text == want {
				return true
			}
		}
	}
	return false
}

func p303InputContainsText(messages []*schema.Message, want string) bool {
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}

func p303RecorderWithPrompt(
	t *testing.T,
	message *schema.Message,
	turnID string,
) (*transcript.Recorder, promptrecord.Record) {
	t.Helper()
	if message == nil ||
		len(message.UserInputMultiContent) != 3 ||
		message.UserInputMultiContent[1].Image == nil ||
		message.UserInputMultiContent[1].Image.Base64Data == nil {
		t.Fatalf("invalid P30.3 prompt fixture: %#v", message)
	}
	data, err := base64.StdEncoding.DecodeString(
		*message.UserInputMultiContent[1].Image.Base64Data,
	)
	if err != nil {
		t.Fatal(err)
	}
	record := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  turnID,
		Parts: []promptrecord.Part{
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "before"},
			},
			{
				Kind: promptrecord.PartImage,
				Image: &promptrecord.ImagePart{
					Ref: mediastore.Ref{
						Version:   mediastore.RefVersion,
						MediaID:   strings.Repeat("A", 43),
						MIMEType:  "image/png",
						SizeBytes: int64(len(data)),
						Width:     1300,
						Height:    1300,
					},
					Detail: "auto",
				},
			},
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "now"},
			},
		},
	}
	clear(data)
	recorder := transcript.NewRecorder("p303-persistence", t.TempDir())
	if err := recorder.RecordUserPrompt(record, message); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = recorder.Close()
	})
	return recorder, record
}
