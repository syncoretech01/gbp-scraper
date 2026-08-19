package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Optional local login. The application trusts the loopback interface by
// default; an operator who shares the machine (or fronts the app with a
// reverse proxy) can set a password, after which every page and mutation
// requires a session and API requests require an API key.
//
// Sessions are held in memory on purpose: a restart signs everyone out, which
// is the safe failure mode for a local tool, and nothing secret is persisted
// beyond the bcrypt hash in settings.

const (
	authPasswordSettingKey = "security.password_hash"
	authTimeoutSettingKey  = "security.session_minutes"

	authSessionCookie = "gmaps_session"

	authMinimumPasswordLength = 8
	authMaximumPasswordLength = 128

	authDefaultSessionMinutes = 720
	authMinimumSessionMinutes = 15
	authMaximumSessionMinutes = 10080

	// authMaximumAttempts failed logins per window from one address trip the
	// limiter; the window resets authAttemptWindow after the first failure.
	authMaximumAttempts = 5
	authAttemptWindow   = 5 * time.Minute
)

// ErrInvalidAuthChange indicates a rejected password set/change/removal.
var ErrInvalidAuthChange = errors.New("invalid password change")

type authSession struct {
	expiresAt time.Time
}

type authState struct {
	// hash holds the current bcrypt hash ("" = login disabled). Atomic so the
	// per-request middleware never takes a lock on the read path.
	hash atomic.Value

	mu       sync.Mutex
	sessions map[string]authSession
	attempts map[string]authAttempt
}

type authAttempt struct {
	count      int
	windowFrom time.Time
}

// initializeAuth loads the stored password hash so the middleware reflects the
// configuration from the first request.
func (s *Server) initializeAuth() {
	s.auth.hash.Store("")
	s.auth.sessions = make(map[string]authSession)
	s.auth.attempts = make(map[string]authAttempt)

	if s.svc == nil {
		return
	}

	values, err := s.svc.LoadSettings(context.Background())
	if err != nil {
		return
	}

	if hash := strings.TrimSpace(values[authPasswordSettingKey]); hash != "" {
		s.auth.hash.Store(hash)
	}
}

// authEnabled reports whether a password is currently required.
func (s *Server) authEnabled() bool {
	hash, _ := s.auth.hash.Load().(string)

	return hash != ""
}

// localAuthentication gates every request once a password is set. Exemptions
// are deliberate and narrow: the login page itself, embedded static assets,
// and API requests that present an API key (whose validity the API-key
// middleware enforces downstream).
func (s *Server) localAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			next.ServeHTTP(w, r)

			return
		}

		path := r.URL.Path

		if path == "/login" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)

			return
		}

		if s.sessionValid(r) {
			next.ServeHTTP(w, r)

			return
		}

		// API clients authenticate with a key instead of a session. Passing
		// the request through is safe: the API-key middleware rejects invalid
		// keys, and a request that presents no key at all is rejected here.
		if strings.HasPrefix(path, "/api/") {
			if r.Header.Get("Authorization") != "" || r.URL.Query().Get("api_key") != "" {
				next.ServeHTTP(w, r)

				return
			}

			renderLocalAPIError(w, http.StatusUnauthorized, "authentication_required",
				"A session or API key is required while local login is enabled")

			return
		}

		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (s *Server) sessionValid(r *http.Request) bool {
	cookie, err := r.Cookie(authSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}

	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	session, ok := s.auth.sessions[cookie.Value]
	if !ok {
		return false
	}

	if time.Now().After(session.expiresAt) {
		delete(s.auth.sessions, cookie.Value)

		return false
	}

	return true
}

func (s *Server) createSession() (string, time.Time, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("create session token: %w", err)
	}

	token := hex.EncodeToString(raw[:])
	expires := time.Now().Add(s.sessionLifetime())

	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	// Opportunistically drop expired sessions so the map cannot grow without
	// bound on a long-lived process.
	now := time.Now()
	for key, session := range s.auth.sessions {
		if now.After(session.expiresAt) {
			delete(s.auth.sessions, key)
		}
	}

	s.auth.sessions[token] = authSession{expiresAt: expires}

	return token, expires, nil
}

func (s *Server) sessionLifetime() time.Duration {
	minutes := authDefaultSessionMinutes

	if s.svc != nil {
		if values, err := s.svc.LoadSettings(context.Background()); err == nil {
			if raw := strings.TrimSpace(values[authTimeoutSettingKey]); raw != "" {
				if value, parseErr := strconv.Atoi(raw); parseErr == nil &&
					value >= authMinimumSessionMinutes && value <= authMaximumSessionMinutes {
					minutes = value
				}
			}
		}
	}

	return time.Duration(minutes) * time.Minute
}

// loginRateLimited reports whether an address has failed too often recently.
func (s *Server) loginRateLimited(address string) bool {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	attempt, ok := s.auth.attempts[address]
	if !ok {
		return false
	}

	if time.Since(attempt.windowFrom) > authAttemptWindow {
		delete(s.auth.attempts, address)

		return false
	}

	return attempt.count >= authMaximumAttempts
}

func (s *Server) recordLoginFailure(address string) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	attempt := s.auth.attempts[address]
	if attempt.count == 0 || time.Since(attempt.windowFrom) > authAttemptWindow {
		attempt = authAttempt{windowFrom: time.Now()}
	}

	attempt.count++
	s.auth.attempts[address] = attempt
}

func (s *Server) clearLoginFailures(address string) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	delete(s.auth.attempts, address)
}

// verifyPassword checks a candidate against the stored hash.
func (s *Server) verifyPassword(candidate string) bool {
	hash, _ := s.auth.hash.Load().(string)
	if hash == "" {
		return false
	}

	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(candidate)) == nil
}

// setPassword stores a new hash (or removes it when empty) and invalidates
// every session, because a credential change must not leave old sessions live.
func (s *Server) setPassword(ctx context.Context, password string) error {
	value := ""

	if password != "" {
		if len(password) < authMinimumPasswordLength || len(password) > authMaximumPasswordLength {
			return fmt.Errorf("%w: password must be %d to %d characters",
				ErrInvalidAuthChange, authMinimumPasswordLength, authMaximumPasswordLength)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		value = string(hash)
	}

	if err := s.svc.SaveSettings(ctx, map[string]string{authPasswordSettingKey: value}); err != nil {
		return fmt.Errorf("store password setting: %w", err)
	}

	s.auth.hash.Store(value)

	s.auth.mu.Lock()
	s.auth.sessions = make(map[string]authSession)
	s.auth.mu.Unlock()

	return nil
}

// loginPage is deliberately self-contained: it renders before any session
// exists, uses no scripts, and inherits the app's strict CSP.
var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in — Google Maps Scraper</title>
<link rel="stylesheet" href="/static/css/app.css">
</head>
<body class="login-body">
<main class="login-card" aria-labelledby="login-title">
<h1 id="login-title">Local workspace</h1>
<p>This workspace is protected by a local password.</p>
{{if .Error}}<p class="notice notice-error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/login">
<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
<label for="login-password">Password</label>
<input class="input" id="login-password" name="password" type="password" autocomplete="current-password" required autofocus>
<button class="button button-primary" type="submit">Sign in</button>
</form>
</main>
</body>
</html>`))
