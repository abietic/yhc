package appserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/abietic/yhc/engine/execution"
)

const (
	maxSpeechCaptureBytes  = 44 + 60*16000*2 // 60 seconds of mono PCM.
	maxSpeechPlaybackBytes = 16 << 20
)

// SpeechConfig exposes explicit Desktop actions independently of session turns.
type SpeechConfig struct {
	Diagnostic string
	Transcribe func(context.Context, []byte) (string, error)
	ReadAloud  func(context.Context, string) ([]byte, error)
}

func (s *Server) speechRoutes(api *http.ServeMux) {
	api.Handle("GET /v1/speech", speechBearerOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			RecordDraft bool   `json:"record_draft"`
			ReadAloud   bool   `json:"read_aloud"`
			Diagnostic  string `json:"diagnostic,omitempty"`
		}{
			RecordDraft: s.speech.Transcribe != nil,
			ReadAloud:   s.speech.ReadAloud != nil,
			Diagnostic:  s.speech.Diagnostic,
		})
	})))
	if s.speech.Transcribe != nil {
		api.Handle("POST /v1/speech/transcribe", speechBearerOnly(http.HandlerFunc(s.handleSpeechTranscribe)))
	}
	if s.speech.ReadAloud != nil {
		api.Handle("POST /v1/speech/read-aloud", speechBearerOnly(http.HandlerFunc(s.handleSpeechReadAloud)))
	}
}

func speechBearerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requestPrincipalFromContext(r.Context())
		if !ok || !principal.bearer {
			writeError(w, http.StatusForbidden, "bearer_required", "trusted Desktop credentials required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSpeechTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "audio/wav" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_speech_request", "expected a WAV recording")
		return
	}
	wav, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSpeechCaptureBytes))
	if err != nil || len(wav) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_speech_request", "invalid or oversized recording")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	text, err := s.speech.Transcribe(ctx, wav)
	if err != nil {
		writeSpeechError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (s *Server) handleSpeechReadAloud(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.Text) == "" {
		writeError(w, http.StatusBadRequest, "invalid_speech_request", "expected text to read aloud")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	wav, err := s.speech.ReadAloud(ctx, input.Text)
	if err != nil {
		writeSpeechError(w, err)
		return
	}
	if len(wav) == 0 || len(wav) > maxSpeechPlaybackBytes {
		writeSpeechError(w, nil)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(wav)
}

func writeSpeechError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, execution.ErrRunBudgetExceeded):
		writeError(w, http.StatusTooManyRequests, "speech_budget_exhausted", "Desktop speech budget reached")
	case errors.Is(err, execution.ErrRunUsageUnknown):
		writeError(w, http.StatusConflict, "speech_usage_unknown", "Desktop speech paused because usage or pricing is uncertain")
	default:
		writeError(w, http.StatusBadGateway, "speech_failed", "Desktop speech request failed")
	}
}
