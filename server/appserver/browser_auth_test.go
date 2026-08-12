package appserver

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestBrowserAuthPairingIsSingleUseAndSessionsExpire(t *testing.T) {
	now := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	auth := newBrowserAuth(
		func() time.Time { return now },
		2*time.Minute,
		12*time.Hour,
	)

	pairing, pairingExpiry, err := auth.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	if pairing == "" || !pairingExpiry.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("pairing = %q, expiry = %s", pairing, pairingExpiry)
	}
	sessionToken, session, err := auth.exchange(pairing)
	if err != nil {
		t.Fatal(err)
	}
	if sessionToken == "" || session.csrfToken == "" ||
		!session.expiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("session = %+v, token empty = %v", session, sessionToken == "")
	}
	if _, _, err := auth.exchange(pairing); !errors.Is(err, errBrowserPairingInvalid) {
		t.Fatalf("second exchange error = %v", err)
	}
	if _, err := auth.validate(sessionToken); err != nil {
		t.Fatalf("validate session: %v", err)
	}

	now = now.Add(12 * time.Hour)
	if _, err := auth.validate(sessionToken); !errors.Is(err, errBrowserSessionInvalid) {
		t.Fatalf("expired session error = %v", err)
	}
}

func TestBrowserCookieIsHttpOnlyAndStrictSameSite(t *testing.T) {
	if browserSessionCookieName != "yhc_browser_session" {
		t.Fatalf("cookie name = %q", browserSessionCookieName)
	}
	now := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	cookie := browserCookie("secret", now.Add(time.Hour), now)
	if cookie.Name != browserSessionCookieName ||
		cookie.Path != "/v1" ||
		!cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteStrictMode ||
		cookie.MaxAge != 3600 {
		t.Fatalf("cookie = %#v", cookie)
	}
}
