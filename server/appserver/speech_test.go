package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNativeSpeechRoutesRequireBearerAndOperateIndependently(t *testing.T) {
	for _, asr := range []bool{false, true} {
		var calls int
		config := Config{
			DiscoveryCWD: t.TempDir(),
			Factory:      func(context.Context, EngineOptions) (SessionEngine, error) { return nil, nil },
		}
		if asr {
			config.Speech.Transcribe = func(_ context.Context, data []byte) (string, error) {
				calls++
				if string(data) != "wave" {
					t.Fatalf("unexpected audio: %q", data)
				}
				return "review this draft", nil
			}
		} else {
			config.Speech.ReadAloud = func(_ context.Context, text string) ([]byte, error) {
				calls++
				if text != "completed answer" {
					t.Fatalf("unexpected text: %q", text)
				}
				return []byte("synthetic wave"), nil
			}
		}
		s, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		request := func(method, path, body, contentType string, bearer bool) *httptest.ResponseRecorder {
			t.Helper()
			r := httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Content-Type", contentType)
			if bearer {
				r.Header.Set("Authorization", "Bearer "+s.Token())
			}
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, r)
			return w
		}
		if w := request("POST", "/v1/speech/transcribe", "wave", "audio/wav", false); w.Code != http.StatusUnauthorized {
			t.Fatal(w.Code)
		}
		if w := request("GET", "/v1/speech", "", "", true); w.Code != http.StatusOK ||
			strings.Contains(w.Body.String(), `"record_draft":true`) != asr ||
			strings.Contains(w.Body.String(), `"read_aloud":true`) == asr {
			t.Fatal(w.Code, w.Body)
		}
		if asr {
			if w := request("POST", "/v1/speech/transcribe", "wave", "audio/wav", true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "review this draft") {
				t.Fatal(w.Code, w.Body)
			}
			if w := request("POST", "/v1/speech/read-aloud", "", "application/json", true); w.Code != http.StatusNotFound {
				t.Fatal(w.Code)
			}
		} else {
			if w := request("POST", "/v1/speech/read-aloud", `{"text":"completed answer"}`, "application/json", true); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/wav" || w.Body.String() != "synthetic wave" {
				t.Fatal(w.Code, w.Body)
			}
			if w := request("POST", "/v1/speech/transcribe", "", "audio/wav", true); w.Code != http.StatusNotFound {
				t.Fatal(w.Code)
			}
		}
		if calls != 1 {
			t.Fatalf("paid requests: %d", calls)
		}
	}
}

func TestNativeSpeechRejectsPairedBrowser(t *testing.T) {
	server, err := New(Config{
		EnableWeb: true,
		WebAssets: os.DirFS(t.TempDir()),
		Factory:   func(context.Context, EngineOptions) (SessionEngine, error) { return nil, nil },
		Speech: SpeechConfig{ReadAloud: func(context.Context, string) ([]byte, error) {
			t.Fatal("paired browser reached paid speech API")
			return nil, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.authority = "127.0.0.1:1234"
	pair, _, err := server.browserAuth.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	token, browser, err := server.browserAuth.exchange(pair)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/speech/read-aloud", strings.NewReader(`{"text":"answer"}`))
	r.Host = server.authority
	r.AddCookie(&http.Cookie{Name: browserSessionCookieName, Value: token})
	r.Header.Set("Origin", requestOrigin(r))
	r.Header.Set("X-YHC-CSRF", browser.csrfToken)
	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"bearer_required"`) {
		t.Fatal(w.Code, w.Body)
	}
}
