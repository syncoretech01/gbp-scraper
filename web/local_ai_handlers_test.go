package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests pin the local AI route contract while the feature is disabled,
// which is the state every fresh workspace starts in: the status endpoint must
// say so with a 200 instead of an error, and the assist endpoint must refuse
// with a CSRF failure or a conflict before ever contacting an Ollama process.

func newLocalAIHandlersServer(t *testing.T) *Server {
	t.Helper()

	server, err := New(NewService(&localAIHandlersRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func TestLocalAIStatusReportsDisabledWithoutContactingOllama(t *testing.T) {
	t.Parallel()

	server := newLocalAIHandlersServer(t)
	mux := http.NewServeMux()
	server.registerLocalAIRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ai/status", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"enabled":false`) || !strings.Contains(body, `"reachable":false`) {
		t.Fatalf("disabled AI status body = %s", body)
	}
}

func TestLocalAIAssistEnforcesCSRFAndRefusesWhileDisabled(t *testing.T) {
	t.Parallel()

	server := newLocalAIHandlersServer(t)
	mux := http.NewServeMux()
	server.registerLocalAIRoutes(mux)
	requestBody := `{"task":"keyword_variations","input":"dentists in san francisco"}`

	// Without the CSRF token the mutation must be refused before any settings
	// or model work happens.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/assist", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "invalid request token") {
		t.Fatalf("assist without CSRF = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// With a valid token but local AI disabled, the handler answers 409 with the
	// documented ai_disabled code instead of reaching for a model.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/ai/assist", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("assist while disabled = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"ai_disabled"`) {
		t.Fatalf("assist while disabled body = %s", recorder.Body.String())
	}
}

// localAIHandlersRepository is the minimal repository these handler tests
// need: job persistence is inert and settings storage reports every feature,
// including local AI, in its default disabled state.
type localAIHandlersRepository struct{}

func (repository *localAIHandlersRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (repository *localAIHandlersRepository) Create(context.Context, *Job) error   { return nil }
func (repository *localAIHandlersRepository) Delete(context.Context, string) error { return nil }
func (repository *localAIHandlersRepository) Update(context.Context, *Job) error   { return nil }

func (repository *localAIHandlersRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (repository *localAIHandlersRepository) LoadSettings(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (repository *localAIHandlersRepository) SaveSettings(context.Context, map[string]string) error {
	return nil
}
