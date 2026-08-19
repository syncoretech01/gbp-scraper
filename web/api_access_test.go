package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAPIAccessMiddlewareEnforcesPermissionsAndMasksRequestLogs(t *testing.T) {
	t.Parallel()

	readToken, readHash, err := newLocalAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	fullToken, fullHash, err := newLocalAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	repository := &apiAccessTestRepository{keys: map[string]APIKeyRecord{
		readHash: {ID: "read-id", Name: "Read", Permission: "read", Enabled: true},
		fullHash: {ID: "full-id", Name: "Full", Permission: "full", Enabled: true},
	}}
	server := &Server{svc: NewService(repository, t.TempDir())}
	server.apiRateLimit.Store(100)
	handler := server.apiAccessMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name   string
		method string
		token  string
		want   int
	}{
		{name: "missing", method: http.MethodGet, want: http.StatusUnauthorized},
		{name: "read get", method: http.MethodGet, token: readToken, want: http.StatusNoContent},
		{name: "read mutation", method: http.MethodPost, token: readToken, want: http.StatusForbidden},
		{name: "full mutation", method: http.MethodPost, token: fullToken, want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/api/v1/results?api_key=must-not-be-logged", http.NoBody)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.logs) != len(tests) {
		t.Fatalf("request logs = %d, want %d", len(repository.logs), len(tests))
	}
	for _, record := range repository.logs {
		if record.Path != "/api/v1/results" || strings.Contains(record.Path, "must-not-be-logged") {
			t.Fatalf("unsafe logged path = %q", record.Path)
		}
	}
}

func TestAPIRateLimitAndSameOriginBrowserCompatibility(t *testing.T) {
	t.Parallel()

	repository := &apiAccessTestRepository{keys: map[string]APIKeyRecord{
		"unused": {ID: "key", Permission: "full", Enabled: true},
	}}
	server := &Server{svc: NewService(repository, t.TempDir())}
	server.apiRateLimit.Store(1)
	handler := server.apiAccessMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for index, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "http://local.test/api/v1/results", http.NoBody)
		request.Host = "local.test"
		request.Header.Set("Origin", "http://local.test")
		request.RemoteAddr = "127.0.0.1:1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("request %d status = %d, want %d", index+1, recorder.Code, want)
		}
	}
}

func TestLocalAPIKeyHashDoesNotContainToken(t *testing.T) {
	t.Parallel()

	token, hash, err := newLocalAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, apiKeyPrefix) || len(hash) != 64 || strings.Contains(hash, token) || hashLocalAPIKey(token) != hash {
		t.Fatalf("invalid token/hash shape: token prefix=%v hash=%q", strings.HasPrefix(token, apiKeyPrefix), hash)
	}
}

type apiAccessTestRepository struct {
	mu   sync.Mutex
	keys map[string]APIKeyRecord
	logs []APIRequestLog
}

func (repository *apiAccessTestRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrPlacesNotFound
}

func (repository *apiAccessTestRepository) Create(context.Context, *Job) error   { return nil }
func (repository *apiAccessTestRepository) Delete(context.Context, string) error { return nil }
func (repository *apiAccessTestRepository) Update(context.Context, *Job) error   { return nil }
func (repository *apiAccessTestRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (repository *apiAccessTestRepository) CreateAPIKey(_ context.Context, record APIKeyRecord, hash string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.keys == nil {
		repository.keys = make(map[string]APIKeyRecord)
	}
	repository.keys[hash] = record
	return nil
}

func (repository *apiAccessTestRepository) ListAPIKeys(context.Context, int) ([]APIKeyRecord, error) {
	return nil, nil
}

func (repository *apiAccessTestRepository) EnabledAPIKeyCount(context.Context) (int, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	count := 0
	for _, key := range repository.keys {
		if key.Enabled {
			count++
		}
	}
	return count, nil
}

func (repository *apiAccessTestRepository) AuthenticateAPIKey(_ context.Context, hash string, _ time.Time) (APIKeyRecord, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key, ok := repository.keys[hash]
	if !ok || !key.Enabled {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	return key, nil
}

func (repository *apiAccessTestRepository) SetAPIKeyEnabled(context.Context, string, bool) error {
	return nil
}

func (repository *apiAccessTestRepository) RecordAPIRequest(_ context.Context, record APIRequestLog) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.logs = append(repository.logs, record)
	return nil
}

func (repository *apiAccessTestRepository) ListAPIRequestLogs(context.Context, int) ([]APIRequestLog, error) {
	return nil, errors.New("unused")
}
