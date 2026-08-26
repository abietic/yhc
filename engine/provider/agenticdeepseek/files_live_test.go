package agenticdeepseek

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestLiveDeepSeekResponsesAndFilesLifecycle(t *testing.T) {
	if os.Getenv("DEEPSEEK_LIVE_TEST") != "1" {
		t.Skip("set DEEPSEEK_LIVE_TEST=1 to run the billable external canary")
	}
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		t.Fatal("DEEPSEEK_API_KEY is required when DEEPSEEK_LIVE_TEST=1")
	}
	baseURL := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	pngBytes, err := makeLiveCanaryPNG()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	maxOutputTokens := 64
	textModel, err := New(ctx, &Config{
		APIKey:          apiKey,
		BaseURL:         baseURL,
		Model:           "deepseek-v4-flash",
		Timeout:         90 * time.Second,
		MaxOutputTokens: &maxOutputTokens,
		ReasoningEffort: ReasoningEffortNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	textOut, err := textModel.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("Reply with exactly YHC-DEEPSEEK-OK."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agenticOutputText(textOut), "YHC-DEEPSEEK-OK") {
		t.Fatal("text response did not satisfy the canary oracle")
	}

	vision, err := New(ctx, &Config{
		APIKey:          apiKey,
		BaseURL:         baseURL,
		Model:           VisionModel,
		Timeout:         90 * time.Second,
		MaxOutputTokens: &maxOutputTokens,
		ReasoningEffort: ReasoningEffortNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	inlineOut, err := vision.Generate(ctx, []*schema.AgenticMessage{{
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
	if !strings.Contains(strings.ToLower(agenticOutputText(inlineOut)), "red") {
		t.Fatal("inline vision response did not satisfy the red-image oracle")
	}

	files, err := NewFilesClient(&FilesConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := files.Upload(ctx, UploadFileParams{
		Filename:            "yhc-deepseek-live-canary.png",
		Content:             bytes.NewReader(pngBytes),
		Size:                int64(len(pngBytes)),
		ExpiresAfterSeconds: minFileExpirySeconds,
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

	retrieved, err := files.Retrieve(ctx, uploaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.ID != uploaded.ID || retrieved.Bytes != int64(len(pngBytes)) {
		t.Fatal("retrieved live file metadata did not match the upload receipt")
	}
	if _, err := files.List(ctx, &ListFilesOptions{
		Limit:   1,
		Order:   FileOrderDesc,
		Purpose: FilePurposeUserData,
	}); err != nil {
		t.Fatal(err)
	}

	fileOut, err := vision.Generate(ctx, []*schema.AgenticMessage{{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: "Name the dominant color in one English word."}),
			NewFileIDImageBlock(uploaded.ID),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(agenticOutputText(fileOut)), "red") {
		t.Fatal("file-ID vision response did not satisfy the red-image oracle")
	}

	if _, err := files.Delete(ctx, uploaded.ID); err != nil {
		t.Fatal(err)
	}
	deleted = true
}

func makeLiveCanaryPNG() ([]byte, error) {
	imageData := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			imageData.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func agenticOutputText(message *schema.AgenticMessage) string {
	if message == nil || message.Role != schema.AgenticRoleTypeAssistant {
		return ""
	}
	var output strings.Builder
	for _, block := range message.ContentBlocks {
		if block != nil && block.AssistantGenText != nil {
			output.WriteString(block.AssistantGenText.Text)
		}
	}
	return output.String()
}
