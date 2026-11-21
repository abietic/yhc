package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider/agenticdeepseek"
)

const (
	maxDeepSeekASRBytes = 8 << 20
	maxDeepSeekOCRBytes = 100 << 20
	maxDeepSeekOCRPages = 1000
	maxDeepSeekOCRReply = 16 << 20
	maxDeepSeekTTSReply = 16 << 20
)

// DeepSeekMediaConfig selects the credential and budget owner for explicit
// standalone media operations. It is separate from the active chat profile.
type DeepSeekMediaConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Usage      execution.ProviderUsageAdmitter
}

// DeepSeekMedia executes standalone operations that can be called directly or
// composed as typed Eino Lambda nodes. Construction performs no HTTP.
type DeepSeekMedia struct {
	apiKey   string
	endpoint string
	http     http.Client
	usage    execution.ProviderUsageAdmitter
}

func NewDeepSeekMedia(config DeepSeekMediaConfig) (*DeepSeekMedia, error) {
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" || len(apiKey) > 4096 || strings.IndexFunc(apiKey, func(ch rune) bool { return ch <= ' ' || ch >= 0x7f }) >= 0 {
		return nil, fmt.Errorf("deepseek media: invalid credential")
	}
	if config.Usage == nil {
		return nil, fmt.Errorf("deepseek media: budget owner required")
	}
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = agenticdeepseek.DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return nil, fmt.Errorf("deepseek media: invalid base URL")
	}
	prefix := strings.TrimRight(u.Path, "/")
	if path.Base(prefix) != "v1" {
		prefix = path.Join(prefix, "v1")
	}
	u.Path = path.Join("/", prefix, "tools")
	client := http.Client{}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
	}
	if client.Transport == nil {
		client.Transport = http.DefaultTransport
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		return nil, fmt.Errorf("deepseek media: bounded calls require a standard HTTP transport")
	}
	client.Transport = transport.Clone()
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &DeepSeekMedia{apiKey: apiKey, endpoint: u.String(), http: client, usage: config.Usage}, nil
}

// DeepSeekTranscript is the completed text and the service-reported audio duration.
type DeepSeekTranscript struct {
	Text       string `json:"text"`
	DurationMS uint64 `json:"duration_ms"`
}

// Transcribe accepts a complete PCM WAV. Inspecting the actual frame count
// before sending gives the budget owner a bound on billable audio seconds.
func (c *DeepSeekMedia) Transcribe(ctx context.Context, wav []byte) (DeepSeekTranscript, error) {
	if len(wav) > maxDeepSeekASRBytes {
		return DeepSeekTranscript{}, fmt.Errorf("deepseek ASR: audio exceeds 8 MiB")
	}
	wav = bytes.Clone(wav)
	info, err := readDeepSeekWAV(wav, false)
	if err != nil {
		return DeepSeekTranscript{}, err
	}
	if !slices.Contains([]uint64{8000, 16000, 22050, 24000, 32000, 44100, 48000}, info.sampleRate) {
		return DeepSeekTranscript{}, fmt.Errorf("deepseek ASR: unsupported sample rate")
	}
	seconds := (info.frames + info.sampleRate - 1) / info.sampleRate
	if info.frames*1000 < info.sampleRate {
		return DeepSeekTranscript{}, fmt.Errorf("deepseek ASR: recording is shorter than one millisecond")
	}
	if seconds > 120 {
		return DeepSeekTranscript{}, fmt.Errorf("deepseek ASR: recording exceeds 120 seconds")
	}
	return deepSeekUpload(ctx, c, "asr", "speech.wav", "", wav,
		execution.MeteredUsage{Unit: execution.BillingAudioSecond, Quantity: seconds},
		2*time.Minute, func(res *http.Response) (DeepSeekTranscript, uint64, error) {
			var wire struct {
				Text       *string `json:"text"`
				DurationMS uint64  `json:"duration_ms"`
			}
			if err := readDeepSeekMediaJSON(res.Body, 256<<10, &wire); err != nil {
				return DeepSeekTranscript{}, 0, err
			}
			// Provider duration is integer milliseconds. A valid sub-ms clip
			// cannot yield a usable receipt with this precision.
			if wire.Text == nil || wire.DurationMS == 0 || wire.DurationMS > 120000 {
				return DeepSeekTranscript{}, 0, fmt.Errorf("deepseek ASR: invalid duration or transcript")
			}
			out := DeepSeekTranscript{Text: *wire.Text, DurationMS: wire.DurationMS}
			return out, (out.DurationMS + 999) / 1000, nil
		})
}

// DeepSeekReadAloud is an explicit request to render already selected prose.
// Callers prepare readable text and may select stella or tide.
type DeepSeekReadAloud struct {
	Text  string
	Voice string
}

// Synthesize returns a complete, playable WAV after transport validation and
// settlement. It leaves conversation state and playback to its caller.
func (c *DeepSeekMedia) Synthesize(ctx context.Context, input DeepSeekReadAloud) ([]byte, error) {
	if !utf8.ValidString(input.Text) || strings.TrimSpace(input.Text) == "" || len(input.Text) > 64<<10 {
		return nil, fmt.Errorf("deepseek TTS: invalid text")
	}
	var characters uint64
	for _, ch := range input.Text {
		characters++
		if unicode.Is(unicode.Han, ch) {
			characters++
		}
	}
	if characters > 10000 || (input.Voice != "" && input.Voice != "stella" && input.Voice != "tide") {
		return nil, fmt.Errorf("deepseek TTS: unsupported voice or billable character count")
	}
	body, err := json.Marshal(struct {
		Text           string `json:"text"`
		Voice          string `json:"voice_id,omitempty"`
		Format         string `json:"format"`
		FilterMarkdown bool   `json:"filter_markdown"`
	}{Text: input.Text, Voice: input.Voice, Format: "wav"})
	if err != nil || len(body) > 64<<10 {
		return nil, fmt.Errorf("deepseek TTS: request exceeds 64 KiB")
	}
	return runDeepSeekMedia(ctx, c, "tts", "application/json", bytes.NewReader(body), int64(len(body)),
		execution.MeteredUsage{Unit: execution.BillingCharacter, Quantity: characters},
		2*time.Minute, func(res *http.Response) ([]byte, uint64, error) {
			contentType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
			if contentType != "audio/wav" {
				return nil, 0, fmt.Errorf("deepseek TTS: expected WAV response")
			}
			wav, err := readDeepSeekMediaBody(res.Body, maxDeepSeekTTSReply)
			if err != nil {
				return nil, 0, err
			}
			framed := res.ContentLength >= 0 || res.ProtoMajor >= 2 || slices.Contains(res.TransferEncoding, "chunked")
			if err := finalizeDeepSeekWAV(wav, framed); err != nil {
				return nil, 0, err
			}
			// The provider's billing rule applies to the supplied text.
			// Server-side Markdown filtering is disabled on this path.
			return wav, characters, nil
		})
}

type DeepSeekOCRInput struct {
	Filename   string
	Content    []byte
	RichLayout bool
}

type DeepSeekOCRPage struct {
	Index        int    `json:"index"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
}

type DeepSeekOCRResult struct {
	ID        string            `json:"id"`
	Purpose   string            `json:"purpose"`
	PageCount uint64            `json:"page_count"`
	Pages     []DeepSeekOCRPage `json:"pages"`
}

// Recognize returns OCR pages with the original markers and finish reasons.
// The standalone protocol's 1000-page limit bounds exposure for opaque files.
func (c *DeepSeekMedia) Recognize(ctx context.Context, input DeepSeekOCRInput) (DeepSeekOCRResult, error) {
	name := strings.TrimSpace(input.Filename)
	if len(input.Content) == 0 || len(input.Content) > maxDeepSeekOCRBytes || name == "" || len(name) > 512 ||
		strings.ContainsAny(name, "/\\\r\n\x00") || !utf8.ValidString(name) {
		return DeepSeekOCRResult{}, fmt.Errorf("deepseek OCR: invalid document name or size")
	}
	purpose := string(agenticdeepseek.FilePurposeTextRecognition)
	if input.RichLayout {
		purpose = string(agenticdeepseek.FilePurposeRichLayoutRecognition)
	}
	return deepSeekUpload(ctx, c, "ocr", name, purpose, input.Content,
		execution.MeteredUsage{Unit: execution.BillingPage, Quantity: maxDeepSeekOCRPages},
		180*time.Second, func(res *http.Response) (DeepSeekOCRResult, uint64, error) {
			var wire struct {
				DeepSeekOCRResult
				Object string          `json:"object"`
				Error  json.RawMessage `json:"error"`
			}
			if err := readDeepSeekMediaJSON(res.Body, maxDeepSeekOCRReply, &wire); err != nil {
				return wire.DeepSeekOCRResult, 0, err
			}
			if len(wire.Error) != 0 && string(wire.Error) != "null" {
				return wire.DeepSeekOCRResult, 0, fmt.Errorf("deepseek OCR: recognition returned an error")
			}
			if wire.Object != "ocr.result" || wire.ID == "" || wire.Purpose != purpose ||
				wire.PageCount == 0 || wire.PageCount != uint64(len(wire.Pages)) {
				return wire.DeepSeekOCRResult, 0, fmt.Errorf("deepseek OCR: invalid page receipt")
			}
			for i, page := range wire.Pages {
				if page.Index != i || (page.FinishReason != "stop" && page.FinishReason != "length") {
					return wire.DeepSeekOCRResult, 0, fmt.Errorf("deepseek OCR: invalid page order or status")
				}
			}
			return wire.DeepSeekOCRResult, wire.PageCount, nil
		})
}

func readDeepSeekMediaBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("deepseek media: incomplete response")
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("deepseek media: response exceeds size limit")
	}
	return data, nil
}

func readDeepSeekMediaJSON(body io.Reader, limit int64, dst any) error {
	data, err := readDeepSeekMediaBody(body, limit)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) || json.Unmarshal(data, dst) != nil {
		return fmt.Errorf("deepseek media: invalid response JSON")
	}
	return nil
}

func deepSeekUpload[T any](ctx context.Context, c *DeepSeekMedia, operation, filename, purpose string, data []byte,
	bound execution.MeteredUsage, timeout time.Duration, decode func(*http.Response) (T, uint64, error),
) (T, error) {
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	var zero T
	if purpose != "" {
		if err := writer.WriteField("purpose", purpose); err != nil {
			return zero, fmt.Errorf("deepseek media: multipart purpose")
		}
	}
	if _, err := writer.CreateFormFile("file", filename); err != nil {
		return zero, fmt.Errorf("deepseek media: multipart file")
	}
	prefixLen := prefix.Len()
	if err := writer.Close(); err != nil {
		return zero, fmt.Errorf("deepseek media: multipart close")
	}
	parts := prefix.Bytes()
	body := io.MultiReader(bytes.NewReader(parts[:prefixLen]), bytes.NewReader(data), bytes.NewReader(parts[prefixLen:]))
	return runDeepSeekMedia(ctx, c, operation, writer.FormDataContentType(), body,
		int64(len(parts))+int64(len(data)), bound, timeout, decode)
}

func runDeepSeekMedia[T any](ctx context.Context, c *DeepSeekMedia, operation, contentType string, body io.Reader, length int64,
	bound execution.MeteredUsage, timeout time.Duration, decode func(*http.Response) (T, uint64, error),
) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/"+operation, body)
	if err != nil {
		return zero, fmt.Errorf("deepseek media: invalid request")
	}
	req.ContentLength = length
	req.GetBody = nil
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	d := execution.ProviderUsageDescriptor{Provider: string(ProviderAgenticDeepSeek), Model: operation, QuerySource: "sdk", MeteredBound: bound}
	if !execution.RequestUsageAvailable(c.usage, d) {
		return zero, &execution.ProviderUsageTerminalError{Err: execution.ErrRunUsageUnknown}
	}
	d.LogicalRoundID = c.usage.NewLogicalRoundID()
	call, err := c.usage.AdmitProviderUsage(ctx, d)
	if err != nil {
		return zero, err
	}
	if call == nil || call.ProviderCallID() == "" {
		return zero, execution.ReleaseProviderUsageBeforeDispatch(call, &execution.ProviderUsageTerminalError{Err: execution.ErrRunUsageUnknown})
	}
	if err := ctx.Err(); err != nil {
		return zero, execution.ReleaseProviderUsageBeforeDispatch(call, err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		cause := ctx.Err()
		if cause == nil {
			cause = fmt.Errorf("deepseek media: HTTP transport failed")
		}
		return zero, execution.MarkProviderUsageAmbiguous(call, cause)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return zero, execution.MarkProviderUsageAmbiguous(call, fmt.Errorf("deepseek %s: HTTP %d", operation, res.StatusCode))
	}
	out, quantity, err := decode(res)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return zero, execution.MarkProviderUsageAmbiguous(call, err)
	}
	if err := execution.CompleteMeteredUsage(call, execution.MeteredUsage{Unit: bound.Unit, Quantity: quantity}); err != nil {
		return zero, err
	}
	return out, nil
}
