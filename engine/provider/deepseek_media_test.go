package provider

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/compose"
)

func mediaWAV(rate, frames int) []byte {
	wav := make([]byte, 44+frames*2)
	copy(wav[:], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], uint32(rate))
	binary.LittleEndian.PutUint32(wav[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(frames*2))
	return wav
}

func mediaBudget(t *testing.T, limit string) *execution.RunUsage {
	t.Helper()
	routes := []execution.RunPriceRoute{
		{Model: "asr", Metered: &execution.RunUnitPrice{Unit: execution.BillingAudioSecond, Amount: "0.78", Per: 3600}},
		{Model: "tts", Metered: &execution.RunUnitPrice{Unit: execution.BillingCharacter, Amount: "3", Per: 10000}},
		{Model: "ocr", Metered: &execution.RunUnitPrice{Unit: execution.BillingPage, Amount: "1.5", Per: 1000}},
	}
	for i := range routes {
		routes[i].Provider = string(ProviderAgenticDeepSeek)
		routes[i].APIModel = routes[i].Model
		routes[i].CachedInputPerMillion = "0"
		routes[i].UncachedInputPerMillion = "0"
		routes[i].OutputPerMillion = "0"
	}
	usage, err := execution.NewPricedRunUsage(execution.RunUsageLimits{
		MaxEstimatedCost: limit, ReserveCostBeforeDispatch: true,
	}, execution.RunPriceCard{
		Version: 1, Currency: "CNY", Source: "https://api-docs.deepseek.com/zh-cn/quick_start/pricing/",
		AsOf: "2026-11-21T00:00:00Z", Window: "upper_bound", Routes: routes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestDeepSeekMediaSDKComposeAndSettlement(t *testing.T) {
	usage := mediaBudget(t, "2")
	recording := mediaWAV(16000, 24000)
	served := mediaWAV(24000, 10)
	binary.LittleEndian.PutUint32(served[4:], wavStreamingSize)
	binary.LittleEndian.PutUint32(served[40:], wavStreamingSize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pending := usage.Snapshot()
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture" ||
			pending.InFlight != 1 || pending.Cost.Reservation.OutstandingAmount == "0.000000000000" {
			t.Errorf("request escaped admission: %s %s %+v", r.Method, r.URL.Path, pending.Cost)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/proxy/v1/tools/asr":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart: %v", err)
				w.WriteHeader(400)
				return
			}
			part, err := reader.NextPart()
			if err != nil || part.FormName() != "file" || part.FileName() != "speech.wav" {
				t.Errorf("ASR part: %v", err)
				w.WriteHeader(400)
				return
			}
			content, _ := io.ReadAll(part)
			if !bytes.Equal(content, recording) {
				t.Error("ASR changed original PCM")
			}
			_, _ = io.WriteString(w, `{"text":"draft text","duration_ms":1500}`)
		case "/proxy/v1/tools/tts":
			var input struct {
				Text   string `json:"text"`
				Voice  string `json:"voice_id"`
				Format string `json:"format"`
				Filter *bool  `json:"filter_markdown"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Text != "你好 Go" ||
				input.Voice != "tide" || input.Format != "wav" || input.Filter == nil || *input.Filter {
				t.Errorf("TTS wire: %+v, %v", input, err)
			}
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(served)
		case "/proxy/v1/tools/ocr":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("OCR multipart: %v", err)
				w.WriteHeader(400)
				return
			}
			first, err := reader.NextPart()
			if err != nil || first.FormName() != "purpose" {
				t.Errorf("missing OCR purpose: %v", err)
				w.WriteHeader(400)
				return
			}
			value, _ := io.ReadAll(first)
			part, err := reader.NextPart()
			if err != nil || part.FormName() != "file" || part.FileName() != "fixture.pdf" || string(value) != "rich_layout_recognition" {
				t.Errorf("OCR wire: %s, %v", value, err)
			}
			_, _ = io.WriteString(w, "\n\n"+`{"object":"ocr.result","id":"ocr-fixture","purpose":"rich_layout_recognition","page_count":1,"pages":[{"index":0,"text":"raw P95 < 250 ms","finish_reason":"length"}],"usage":{"total_tokens":100}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	media, err := NewDeepSeekMedia(DeepSeekMediaConfig{APIKey: "fixture", BaseURL: server.URL + "/proxy/v1", HTTPClient: server.Client(), Usage: usage})
	if err != nil {
		t.Fatal(err)
	}
	graph := compose.NewGraph[[]byte, DeepSeekTranscript]()
	if err := graph.AddLambdaNode("asr", compose.InvokableLambda(media.Transcribe)); err != nil {
		t.Fatal(err)
	}
	if err := graph.AddEdge(compose.START, "asr"); err != nil {
		t.Fatal(err)
	}
	if err := graph.AddEdge("asr", compose.END); err != nil {
		t.Fatal(err)
	}
	compiled, err := graph.Compile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := compiled.Invoke(t.Context(), recording); err != nil || got.Text != "draft text" || got.DurationMS != 1500 {
		t.Fatalf("ASR composition = %+v, %v", got, err)
	}
	wav, err := media.Synthesize(t.Context(), DeepSeekReadAloud{Text: "你好 Go", Voice: "tide"})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := readDeepSeekWAV(wav, false); err != nil || info.frames != 10 {
		t.Fatalf("normalized playback artifact: %+v, %v", info, err)
	}
	ocr, err := media.Recognize(t.Context(), DeepSeekOCRInput{Filename: "fixture.pdf", Content: []byte("synthetic PDF"), RichLayout: true})
	if err != nil || ocr.PageCount != 1 || ocr.Pages[0].Text != "raw P95 < 250 ms" || ocr.Pages[0].FinishReason != "length" {
		t.Fatalf("OCR = %+v, %v", ocr, err)
	}
	snapshot := usage.Snapshot()
	if snapshot.ProviderCalls != 3 || !snapshot.Cost.Complete || snapshot.TotalTokens != 0 ||
		snapshot.Cost.KnownEstimatedAmount != "0.004033333334" {
		t.Fatalf("wrong standalone cost: %+v", snapshot.Cost)
	}
}

func TestDeepSeekMediaPreDispatchAndAmbiguousFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", "200")
		_, _ = w.Write(mediaWAV(24000, 2))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, limit string
		want        error
		dispatched  int32
	}{
		{"budget_denied", "0.0001", execution.ErrRunBudgetExceeded, 0},
		{"truncated", "1", execution.ErrRunUsageUnknown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls.Store(0)
			usage := mediaBudget(t, tc.limit)
			media, err := NewDeepSeekMedia(DeepSeekMediaConfig{APIKey: "fixture", BaseURL: server.URL, HTTPClient: server.Client(), Usage: usage})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := media.Transcribe(t.Context(), []byte("bad WAV")); err == nil || len(usage.Snapshot().CallLedger.Records) != 0 {
				t.Fatal("local validation must occur before admission")
			}
			input := DeepSeekReadAloud{Text: "abcdef"}
			if got, err := media.Synthesize(t.Context(), input); got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("synthesis = %v, %v", got, err)
			}
			if tc.dispatched != 0 {
				if _, err := media.Synthesize(t.Context(), input); !errors.Is(err, execution.ErrRunUsageUnknown) {
					t.Fatalf("ambiguous use did not fence another attempt: %v", err)
				}
				if got := usage.Snapshot().Cost.Reservation.RetainedAmount; got != "0.001800000000" {
					t.Fatalf("retained fee = %s", got)
				}
			}
			if got := calls.Load(); got != tc.dispatched {
				t.Fatalf("HTTP count = %d, want %d", got, tc.dispatched)
			}
		})
	}
}

func TestDeepSeekMediaRejectsInvalidBearerBeforeAdmission(t *testing.T) {
	usage := mediaBudget(t, "1")
	for _, key := range []string{"fixture\x00key", "fixture\x7fkey", "some key"} {
		if client, err := NewDeepSeekMedia(DeepSeekMediaConfig{APIKey: key, Usage: usage}); client != nil || err == nil {
			t.Fatal("invalid credential must fail during construction")
		}
	}
	if s := usage.Snapshot(); s.ProviderCalls != 0 || !s.Cost.Complete {
		t.Fatalf("invalid credential changed accounting: %+v", s.Cost)
	}
}
