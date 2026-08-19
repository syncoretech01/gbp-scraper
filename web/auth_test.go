package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// settingsJobRepository is a JobRepository with an in-memory settings store, so
// auth tests exercise the real hash persistence path.
type settingsJobRepository struct {
	fixedJobRepository

	mu     sync.Mutex
	values map[string]string
}

func (r *settingsJobRepository) LoadSettings(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	copied := make(map[string]string, len(r.values))
	for key, value := range r.values {
		copied[key] = value
	}

	return copied, nil
}

func (r *settingsJobRepository) SaveSettings(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.values == nil {
		r.values = make(map[string]string)
	}

	for key, value := range values {
		r.values[key] = value
	}

	return nil
}

func newAuthTestServer(t *testing.T) (*Server, *settingsJobRepository) {
	t.Helper()

	repository := &settingsJobRepository{}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server, repository
}

func authFullHandler(server *Server) http.Handler {
	// The same middleware composition web.New installs, around a marker
	// handler so tests can tell "request reached the app" from a block.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	return server.localAuthentication(inner)
}

func setTestPassword(t *testing.T, server *Server, password string) {
	t.Helper()

	if err := server.setPassword(context.Background(), password); err != nil {
		t.Fatalf("setPassword: %v", err)
	}
}

func loginAndGetCookie(t *testing.T, server *Server, password string) *http.Cookie {
	t.Helper()

	form := url.Values{"password": {password}, "csrf_token": {server.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "127.0.0.1:9999"
	recorder := httptest.NewRecorder()
	server.login(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == authSessionCookie && cookie.Value != "" {
			return cookie
		}
	}

	t.Fatal("login set no session cookie")

	return nil
}

func TestAuthDisabledByDefaultChangesNothing(t *testing.T) {
	t.Parallel()

	server, _ := newAuthTestServer(t)
	handler := authFullHandler(server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("request without a password set = %d, want pass-through", recorder.Code)
	}
}

func TestAuthGatesPagesAndAPIOnceEnabled(t *testing.T) {
	t.Parallel()

	server, _ := newAuthTestServer(t)
	setTestPassword(t, server, "correct horse battery")
	handler := authFullHandler(server)

	// A page request without a session redirects to the login form.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody))

	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("page without session = %d -> %q", recorder.Code, recorder.Header().Get("Location"))
	}

	// An API request without session or key is refused outright.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/results", http.NoBody))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("API without credentials = %d, want 401", recorder.Code)
	}

	// An API request presenting a key passes to the key middleware.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/results", http.NoBody)
	request.Header.Set("Authorization", "Bearer some-key")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("API with key header = %d, want pass-through to key validation", recorder.Code)
	}

	// The login page and static assets stay reachable.
	for _, path := range []string{"/login", "/static/css/app.css"} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, http.NoBody))

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("exempt path %s = %d", path, recorder.Code)
		}
	}
}

func TestLoginIssuesASessionThatUnlocksTheApp(t *testing.T) {
	t.Parallel()

	server, _ := newAuthTestServer(t)
	setTestPassword(t, server, "correct horse battery")
	handler := authFullHandler(server)

	// Wrong password is refused and counted.
	form := url.Values{"password": {"wrong"}, "csrf_token": {server.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "127.0.0.1:9999"
	recorder := httptest.NewRecorder()
	server.login(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", recorder.Code)
	}

	cookie := loginAndGetCookie(t, server, "correct horse battery")

	authed := httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody)
	authed.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authed)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("request with session = %d, want pass-through", recorder.Code)
	}
}

func TestLoginRateLimitsRepeatedFailures(t *testing.T) {
	t.Parallel()

	server, _ := newAuthTestServer(t)
	setTestPassword(t, server, "correct horse battery")

	form := url.Values{"password": {"wrong"}, "csrf_token": {server.csrfToken}}

	var last int

	for range authMaximumAttempts + 1 {
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "10.0.0.7:1234"
		recorder := httptest.NewRecorder()
		server.login(recorder, request)
		last = recorder.Code
	}

	if last != http.StatusTooManyRequests {
		t.Fatalf("after repeated failures = %d, want 429", last)
	}
}

func TestSessionExpiryAndPasswordChangeInvalidateSessions(t *testing.T) {
	t.Parallel()

	server, _ := newAuthTestServer(t)
	setTestPassword(t, server, "correct horse battery")
	handler := authFullHandler(server)

	cookie := loginAndGetCookie(t, server, "correct horse battery")

	// Force the session past its deadline.
	server.auth.mu.Lock()
	server.auth.sessions[cookie.Value] = authSession{expiresAt: time.Now().Add(-time.Minute)}
	server.auth.mu.Unlock()

	expired := httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody)
	expired.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, expired)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("expired session = %d, want redirect to login", recorder.Code)
	}

	// A fresh session dies when the password changes.
	cookie = loginAndGetCookie(t, server, "correct horse battery")
	setTestPassword(t, server, "a brand new password")

	stale := httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody)
	stale.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, stale)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("session after password change = %d, want redirect", recorder.Code)
	}
}

func TestPasswordPersistsAcrossServerRestart(t *testing.T) {
	t.Parallel()

	repository := &settingsJobRepository{}
	dataFolder := t.TempDir()

	first, err := New(NewService(repository, dataFolder), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	setTestPassword(t, first, "correct horse battery")

	// A second server over the same repository must come up protected.
	second, err := New(NewService(repository, dataFolder), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("restart New() error = %v", err)
	}

	if !second.authEnabled() {
		t.Fatal("password did not survive a restart")
	}

	if !second.verifyPassword("correct horse battery") {
		t.Fatal("restarted server rejects the stored password")
	}

	// Removing the password requires the current one and then disables auth.
	form := url.Values{
		"action": {"remove"}, "current_password": {"correct horse battery"},
		"csrf_token": {second.csrfToken},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/security/password", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	second.changeLocalPassword(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"enabled":false`) {
		t.Fatalf("remove password = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if second.authEnabled() {
		t.Fatal("auth still enabled after removal")
	}
}
