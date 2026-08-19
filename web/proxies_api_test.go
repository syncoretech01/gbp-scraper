package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPoolProxyTestRequiresCSRFAndReportsCapability(t *testing.T) {
	t.Parallel()

	// A repository without proxy storage must report the capability, not a 500.
	server, err := New(NewService(newFormEncodingRepository(), t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerProxyTestRoutes(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/proxies/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("pool test without CSRF = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/proxies/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented ||
		!strings.Contains(recorder.Body.String(), "proxies_unavailable") {
		t.Fatalf("pool test without proxy storage = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestPoolProxyTestSelectsEnabledProxiesAndMasksCredentials(t *testing.T) {
	t.Parallel()

	repository := &proxyTestRepository{proxies: []ProxyRecord{
		{ID: "p-1", PoolID: "pool-1", PoolName: "Local", MaskedURL: "http://user:***@10.0.0.1:8080", Enabled: true},
		{ID: "p-2", PoolID: "pool-1", PoolName: "Local", MaskedURL: "http://user:***@10.0.0.2:8080", Enabled: false},
	}}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerProxyTestRoutes(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/proxies/test", strings.NewReader(`{"pool_id":"pool-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("pool test = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()

	// Only the enabled proxy is contacted, and its result is recorded.
	if !strings.Contains(body, `"tested":1`) || !strings.Contains(body, `"p-1"`) {
		t.Fatalf("pool test did not test exactly the enabled proxy: %s", body)
	}

	if strings.Contains(body, `"p-2"`) {
		t.Fatalf("disabled proxy was tested in a whole-pool run: %s", body)
	}

	// The decrypted credential must never reach the response.
	if strings.Contains(body, "supersecret") {
		t.Fatalf("response leaked a proxy credential: %s", body)
	}

	if len(repository.recorded) != 1 || repository.recorded[0] != "p-1" {
		t.Fatalf("recorded proxy tests = %v", repository.recorded)
	}

	// An explicit selection may re-test a disabled proxy.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost, "/api/v1/proxies/test", strings.NewReader(`{"proxy_ids":["p-2"]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"p-2"`) {
		t.Fatalf("explicit selection = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestPoolProxyTestRejectsOversizedSelection(t *testing.T) {
	t.Parallel()

	server, err := New(NewService(&proxyTestRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerProxyTestRoutes(mux)

	ids := make([]string, 0, maximumProxyTestBatch+1)
	for index := 0; index <= maximumProxyTestBatch; index++ {
		ids = append(ids, `"p-`+strings.Repeat("x", index%3)+`"`)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/proxies/test",
		strings.NewReader(`{"proxy_ids":[`+strings.Join(ids, ",")+`]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized selection = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// proxyTestRepository is a JobRepository that also satisfies proxyRepository so
// the pool-test route can be exercised without real proxies.
type proxyTestRepository struct {
	proxies  []ProxyRecord
	recorded []string
}

func (repository *proxyTestRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (repository *proxyTestRepository) Create(context.Context, *Job) error   { return nil }
func (repository *proxyTestRepository) Delete(context.Context, string) error { return nil }
func (repository *proxyTestRepository) Update(context.Context, *Job) error   { return nil }

func (repository *proxyTestRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (repository *proxyTestRepository) ListProxyPools(context.Context) ([]ProxyPoolRecord, error) {
	return nil, nil
}

func (repository *proxyTestRepository) ListProxies(_ context.Context, poolID string) ([]ProxyRecord, error) {
	if poolID == "" {
		return repository.proxies, nil
	}

	matched := make([]ProxyRecord, 0, len(repository.proxies))

	for _, proxy := range repository.proxies {
		if proxy.PoolID == poolID {
			matched = append(matched, proxy)
		}
	}

	return matched, nil
}

func (repository *proxyTestRepository) ImportProxyPool(
	context.Context, string, string, []string,
) (ProxyPoolRecord, int, error) {
	return ProxyPoolRecord{}, 0, nil
}

func (repository *proxyTestRepository) ResolveProxyPool(context.Context, string) ([]string, error) {
	return nil, nil
}

func (repository *proxyTestRepository) GetProxySecret(_ context.Context, id string) (string, error) {
	// A credential pointing at a closed loopback port, so the dial fails fast. The
	// assertion is that this string never appears in the response.
	return "http://user:supersecret@127.0.0.1:1", nil
}

func (repository *proxyTestRepository) RecordProxyTest(_ context.Context, id string, _ ProxyTestResult) error {
	repository.recorded = append(repository.recorded, id)

	return nil
}

func (repository *proxyTestRepository) SetProxyEnabled(context.Context, string, bool) error {
	return nil
}
func (repository *proxyTestRepository) DeleteProxy(context.Context, string) error     { return nil }
func (repository *proxyTestRepository) DeleteProxyPool(context.Context, string) error { return nil }

var _ proxyRepository = (*proxyTestRepository)(nil)
