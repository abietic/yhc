package agenticglm

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
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

	stream, err := glm.Stream(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("Reply with exactly YHC-GLM-OK."),
	})
	if err != nil {
		t.Fatal(err)
	}
	var streamText strings.Builder
	finishSeen := false
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
			if ext, ok := chunk.ResponseMeta.Extension.(*ResponseMetaExtension); ok && ext != nil && ext.FinishReason != "" {
				finishSeen = true
			}
		}
	}
	if !strings.Contains(streamText.String(), "YHC-GLM-OK") || !finishSeen {
		t.Fatalf("stream canary failed: text=%q finish=%t", streamText.String(), finishSeen)
	}

	pngBytes, err := makeGLMLiveCanaryPNG()
	if err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(strings.ToLower(glmOutputText(visionOut)), "red") {
		t.Fatal("inline vision response did not satisfy the red-image oracle")
	}

	files, err := NewFilesClient(&FilesConfig{APIKey: apiKey, BaseURL: baseURL, Timeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	const fileBody = "YHC-GLM-FILE-CANARY"
	uploaded, err := files.Upload(ctx, UploadFileParams{
		Filename: "yhc-glm-live-canary.txt",
		Content:  strings.NewReader(fileBody),
		Size:     int64(len(fileBody)),
		Purpose:  FilePurposeAgent,
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
	listed, err := files.List(ctx, &ListFilesOptions{Purpose: FilePurposeAgent, Limit: 20, Order: FileOrderCreatedAt})
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
	if !strings.Contains(glmOutputText(fileOut), fileBody) {
		t.Fatal("file-ID response did not satisfy the content oracle")
	}
	if _, err := files.Delete(ctx, uploaded.ID); err != nil {
		t.Fatal(err)
	}
	deleted = true
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
