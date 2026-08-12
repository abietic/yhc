package appserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const browserSessionCookieName = "yhc_browser_session"

var (
	errBrowserPairingInvalid = errors.New("browser pairing token is invalid or expired")
	errBrowserSessionInvalid = errors.New("browser session is invalid or expired")
)

type browserSession struct {
	csrfToken string
	expiresAt time.Time
}

type browserAuth struct {
	mu sync.Mutex

	now        func() time.Time
	pairTTL    time.Duration
	sessionTTL time.Duration
	pairings   map[[sha256.Size]byte]time.Time
	sessions   map[[sha256.Size]byte]browserSession
}

func newBrowserAuth(
	now func() time.Time,
	pairTTL time.Duration,
	sessionTTL time.Duration,
) *browserAuth {
	return &browserAuth{
		now:        now,
		pairTTL:    pairTTL,
		sessionTTL: sessionTTL,
		pairings:   make(map[[sha256.Size]byte]time.Time),
		sessions:   make(map[[sha256.Size]byte]browserSession),
	}
}

func (a *browserAuth) newPairing() (string, time.Time, error) {
	token, err := generateToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := a.now().UTC()
	expiresAt := now.Add(a.pairTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(now)
	a.pairings[tokenDigest(token)] = expiresAt
	return token, expiresAt, nil
}

func (a *browserAuth) exchange(pairingToken string) (string, browserSession, error) {
	pairingToken = strings.TrimSpace(pairingToken)
	if pairingToken == "" {
		return "", browserSession{}, errBrowserPairingInvalid
	}
	now := a.now().UTC()
	pairingDigest := tokenDigest(pairingToken)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(now)
	expiresAt, ok := a.pairings[pairingDigest]
	if !ok || !expiresAt.After(now) {
		delete(a.pairings, pairingDigest)
		return "", browserSession{}, errBrowserPairingInvalid
	}
	delete(a.pairings, pairingDigest)

	sessionToken, err := generateToken()
	if err != nil {
		return "", browserSession{}, err
	}
	csrfToken, err := generateToken()
	if err != nil {
		return "", browserSession{}, err
	}
	session := browserSession{
		csrfToken: csrfToken,
		expiresAt: now.Add(a.sessionTTL),
	}
	a.sessions[tokenDigest(sessionToken)] = session
	return sessionToken, session, nil
}

func (a *browserAuth) validate(sessionToken string) (browserSession, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return browserSession{}, errBrowserSessionInvalid
	}
	now := a.now().UTC()
	digest := tokenDigest(sessionToken)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(now)
	session, ok := a.sessions[digest]
	if !ok || !session.expiresAt.After(now) {
		delete(a.sessions, digest)
		return browserSession{}, errBrowserSessionInvalid
	}
	return session, nil
}

func (a *browserAuth) revoke(sessionToken string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.sessions, tokenDigest(sessionToken))
	a.mu.Unlock()
}

func (a *browserAuth) clear() {
	if a == nil {
		return
	}
	a.mu.Lock()
	clear(a.pairings)
	clear(a.sessions)
	a.mu.Unlock()
}

func (a *browserAuth) pruneLocked(now time.Time) {
	for digest, expiresAt := range a.pairings {
		if !expiresAt.After(now) {
			delete(a.pairings, digest)
		}
	}
	for digest, session := range a.sessions {
		if !session.expiresAt.After(now) {
			delete(a.sessions, digest)
		}
	}
}

func tokenDigest(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(value))
}

func csrfMatches(provided, expected string) bool {
	provided = strings.TrimSpace(provided)
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// Browser cookies are Secure even though the app-server uses HTTP: the server
// admits only explicit loopback authorities, which Chromium treats as secure
// cookie origins. The same flags are kept on deletion so logout cannot weaken
// the cookie before expiring it.
func browserCookie(token string, expiresAt, now time.Time) *http.Cookie {
	maxAge := int(expiresAt.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name:     browserSessionCookieName,
		Value:    token,
		Path:     "/v1",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
		MaxAge:   maxAge,
	}
}

func expiredBrowserCookie() *http.Cookie {
	return &http.Cookie{
		Name:     browserSessionCookieName,
		Path:     "/v1",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
	}
}
