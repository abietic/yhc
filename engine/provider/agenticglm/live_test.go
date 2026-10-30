package agenticglm

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func TestLiveGLM53FlashAndFilesLifecycle(t *testing.T) {
	if os.Getenv("GLM_LIVE_TEST") != "1" {
		t.Skip("set GLM_LIVE_TEST=1 to run the external canary")
	}
	apiKey := strings.TrimSpace(os.Getenv("ZAI_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("ZHIPUAI_API_KEY"))
	}
	if apiKey == "" {
		t.Fatal("ZAI_API_KEY or ZHIPUAI_API_KEY is required when GLM_LIVE_TEST=1")
	}
	baseURL := strings.TrimSpace(os.Getenv("ZAI_BASE_URL"))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	var statistics []map[string]any
	runID := uuid.NewString()
	// Record each attempted call before dispatch. Failure or missing terminal
	// usage remains unknown rather than being silently omitted from the bill.
	record := func(label, modelID string) func(*schema.AgenticResponseMeta) {
		usage := map[string]any{
			"provider_calls": 1, "known_calls": 0, "unknown_calls": 1,
			"in_flight": 0, "untracked_calls": 0, "complete": false,
			"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
			"cached_prompt_tokens": 0, "reasoning_tokens": 0,
		}
		statistics = append(statistics, map[string]any{
			"trial_path": "glm-live-" + runID + "-" + label,
			"manifest":   map[string]string{"model_request": modelID},
			"usage":      usage,
		})
		return func(meta *schema.AgenticResponseMeta) {
			if meta == nil || meta.TokenUsage == nil {
				t.Fatal("live call lacks provider-reported terminal usage")
			}
			u := meta.TokenUsage
			ext, ok := meta.Extension.(*ResponseMetaExtension)
			if !ok || ext == nil || ext.Model != modelID {
				t.Fatal("live call lacks matching response model identity")
			}
			usage["routes"] = []map[string]string{{"model": ext.Model}}
			usage["known_calls"], usage["unknown_calls"], usage["complete"] = 1, 0, true
			usage["prompt_tokens"], usage["completion_tokens"], usage["total_tokens"] = u.PromptTokens, u.CompletionTokens, u.TotalTokens
			usage["cached_prompt_tokens"], usage["reasoning_tokens"] = u.PromptTokenDetails.CachedTokens, u.CompletionTokensDetails.ReasoningTokens
			t.Logf("%s: model=%s input=%d cached=%d output=%d reasoning=%d total=%d", label, modelID,
				u.PromptTokens, u.PromptTokenDetails.CachedTokens, u.CompletionTokens, u.CompletionTokensDetails.ReasoningTokens, u.TotalTokens)
		}
	}
	t.Cleanup(func() {
		if path := os.Getenv("GLM_LIVE_USAGE_PATH"); path != "" {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Errorf("create usage report: %v", err)
				return
			}
			defer file.Close()
			if err := json.NewEncoder(file).Encode(statistics); err != nil {
				t.Errorf("write usage report: %v", err)
			}
		}
	})
	mode := os.Getenv("GLM_LIVE_CASES")
	if mode != "" && mode != "family" {
		t.Fatal("GLM_LIVE_CASES must be empty or family")
	}
	runMultimodal := func() {
		maxTokens := 256
		glm, err := New(ctx, &Config{
			APIKey:          apiKey,
			BaseURL:         baseURL,
			Model:           ModelGLM53Flash,
			Timeout:         90 * time.Second,
			MaxTokens:       &maxTokens,
			ReasoningEffort: ReasoningEffortLow,
		})
		if err != nil {
			t.Fatal(err)
		}

		recordStream := record("stream", ModelGLM53Flash)
		stream, err := glm.Stream(ctx, []*schema.AgenticMessage{
			schema.UserAgenticMessage("Reply with exactly YHC-GLM-OK."),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var streamText strings.Builder
		finishSeen := false
		var streamMeta *schema.AgenticResponseMeta
		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					break
				}
				t.Fatal(recvErr)
			}
			for _, block := range chunk.ContentBlocks {
				if block != nil && block.AssistantGenText != nil {
					streamText.WriteString(block.AssistantGenText.Text)
				}
			}
			if chunk.ResponseMeta != nil {
				streamMeta = chunk.ResponseMeta
				if ext, ok := chunk.ResponseMeta.Extension.(*ResponseMetaExtension); ok && ext != nil && ext.FinishReason != "" {
					finishSeen = true
				}
			}
		}
		recordStream(streamMeta)
		if !strings.Contains(streamText.String(), "YHC-GLM-OK") || !finishSeen {
			t.Fatalf("stream canary failed: text=%q finish=%t", streamText.String(), finishSeen)
		}

		pngBytes, err := makeGLMLiveCanaryPNG()
		if err != nil {
			t.Fatal(err)
		}
		recordVision := record("vision", ModelGLM53Flash)
		visionOut, err := glm.Generate(ctx, []*schema.AgenticMessage{{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "Name the dominant color in one English word."}),
				schema.NewContentBlock(&schema.UserInputImage{
					Base64Data: base64.StdEncoding.EncodeToString(pngBytes),
					MIMEType:   "image/png",
				}),
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		recordVision(visionOut.ResponseMeta)
		if !strings.Contains(strings.ToLower(glmOutputText(visionOut)), "red") {
			t.Fatal("inline vision response did not satisfy the red-image oracle")
		}

		files, err := NewFilesClient(&FilesConfig{APIKey: apiKey, BaseURL: baseURL, Timeout: 90 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		const fileBody = "YHC-GLM-FILE-CANARY"
		docxBytes, err := makeGLMLiveCanaryDOCX(fileBody)
		if err != nil {
			t.Fatal(err)
		}
		uploaded, err := files.Upload(ctx, UploadFileParams{
			Filename: "yhc-glm-live-canary.docx",
			Content:  bytes.NewReader(docxBytes),
			Size:     int64(len(docxBytes)),
			Purpose:  FilePurposeUserData,
		})
		if err != nil {
			t.Fatal(err)
		}
		deleted := false
		t.Cleanup(func() {
			if deleted {
				return
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			if _, cleanupErr := files.Delete(cleanupCtx, uploaded.ID); cleanupErr != nil {
				t.Errorf("delete live canary file: %v", cleanupErr)
			}
		})
		listed, err := files.List(ctx, &ListFilesOptions{Purpose: FilePurposeUserData, Limit: 20, Order: FileOrderCreatedAt})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range listed.Data {
			if entry.ID == uploaded.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("uploaded canary was not present in the Files API page")
		}
		recordFile := record("file", ModelGLM53Flash)
		fileOut, err := glm.Generate(ctx, []*schema.AgenticMessage{{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "Return the exact marker stored in this file."}),
				NewFileIDBlock(uploaded.ID, uploaded.Filename),
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		recordFile(fileOut.ResponseMeta)
		if !strings.Contains(glmOutputText(fileOut), fileBody) {
			t.Fatal("file-ID response did not satisfy the content oracle")
		}
		if _, err := files.Delete(ctx, uploaded.ID); err != nil {
			t.Fatal(err)
		}
		deleted = true
	}
	if mode != "family" {
		runMultimodal()
	}

	// One small call per additional mainstream agent model. No benchmark run,
	// automatic fallback, or retries that could hide access failure or spend.
	maxTokens := 512
	temperature := float32(0)
	for _, modelID := range []string{ModelGLM53, ModelGLM53FlashX} {
		client, err := New(ctx, &Config{
			APIKey: apiKey, BaseURL: baseURL, Model: modelID,
			Timeout: 90 * time.Second, MaxTokens: &maxTokens, Temperature: &temperature, ReasoningEffort: ReasoningEffortLow,
		})
		if err != nil {
			t.Fatal(err)
		}
		recordFamily := record(modelID, modelID)
		out, err := client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("Calculate 2 + 2. Reply only with the single digit answer.")})
		if err != nil {
			t.Fatal(err)
		}
		recordFamily(out.ResponseMeta)
		if text := strings.TrimSpace(glmOutputText(out)); text != "4" {
			t.Fatalf("%s did not satisfy the arithmetic oracle: text=%q", modelID, text)
		}
	}
}

func glmOutputText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range message.ContentBlocks {
		if block != nil && block.AssistantGenText != nil {
			text.WriteString(block.AssistantGenText.Text)
		}
	}
	return text.String()
}

func makeGLMLiveCanaryPNG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func makeGLMLiveCanaryDOCX(marker string) ([]byte, error) {
	var encoded bytes.Buffer
	writer := zip.NewWriter(&encoded)
	files := []struct {
		name string
		body string
	}{
		{
			name: "[Content_Types].xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
				`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
				`<Default Extension="xml" ContentType="application/xml"/>` +
				`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
				`</Types>`,
		},
		{
			name: "_rels/.rels",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
				`</Relationships>`,
		},
		{
			name: "word/document.xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
				`<w:body><w:p><w:r><w:t>` + marker + `</w:t></w:r></w:p></w:body></w:document>`,
		},
	}
	for _, file := range files {
		part, err := writer.Create(file.name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(part, file.body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
