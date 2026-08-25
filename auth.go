package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "ax9000_proxy_session"

type authSession struct {
	CSRF      string
	ExpiresAt time.Time
}

type loginAttempt struct {
	Failures int
	ResetAt  time.Time
}

type authStore struct {
	mu       sync.Mutex
	sessions map[string]authSession
	attempts map[string]loginAttempt
	ttl      time.Duration
}

func newAuthStore(ttl time.Duration) *authStore {
	return &authStore{
		sessions: make(map[string]authSession),
		attempts: make(map[string]loginAttempt),
		ttl:      ttl,
	}
}

func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *authStore) allowLogin(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	attempt := a.attempts[ip]
	if now.After(attempt.ResetAt) {
		delete(a.attempts, ip)
		return true
	}
	return attempt.Failures < 5
}

func (a *authStore) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	attempt := a.attempts[ip]
	if now.After(attempt.ResetAt) {
		attempt = loginAttempt{ResetAt: now.Add(5 * time.Minute)}
	}
	attempt.Failures++
	a.attempts[ip] = attempt
}

func (a *authStore) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}

func (a *authStore) create() (id, csrf string, expires time.Time, err error) {
	id, err = randomHex(32)
	if err != nil {
		return "", "", time.Time{}, err
	}
	csrf, err = randomHex(24)
	if err != nil {
		return "", "", time.Time{}, err
	}
	expires = time.Now().Add(a.ttl)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removeExpiredLocked()
	a.sessions[id] = authSession{CSRF: csrf, ExpiresAt: expires}
	return id, csrf, expires, nil
}

func (a *authStore) get(id string) (authSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removeExpiredLocked()
	session, ok := a.sessions[id]
	return session, ok
}

func (a *authStore) delete(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
}

func (a *authStore) clearSessions() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions = make(map[string]authSession)
}

func (a *authStore) removeExpiredLocked() {
	now := time.Now()
	for id, session := range a.sessions {
		if now.After(session.ExpiresAt) {
			delete(a.sessions, id)
		}
	}
}

func setSessionCookie(w http.ResponseWriter, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
