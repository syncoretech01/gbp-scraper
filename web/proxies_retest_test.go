package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRetestDisabledProxiesReenablesHealthyProxies is not parallel: it swaps
// the package-level proxyAccessCheck probe for a hermetic stub.
func TestRetestDisabledProxiesReenablesHealthyProxies(t *testing.T) {
	repository := &retestProxyRepository{proxies: []ProxyRecord{
		{ID: "p-enabled", PoolID: "pool-1", PoolName: "Local", MaskedURL: "http://user:***@10.0.0.1:8080", Enabled: true},
		{ID: "p-recovered", PoolID: "pool-1", PoolName: "Local", MaskedURL: "http://user:***@10.0.0.2:8080", Enabled: false},
		{ID: "p-broken", PoolID: "pool-1", PoolName: "Local", MaskedURL: "http://user:***@10.0.0.3:8080", Enabled: false},
		{ID: "p-other-pool", PoolID: "pool-2", PoolName: "Other", MaskedURL: "http://user:***@10.0.0.4:8080", Enabled: false},
	}}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	originalProbe := proxyAccessCheck
	t.Cleanup(func() { proxyAccessCheck = originalProbe })

	proxyAccessCheck = func(_ context.Context, secret string) ProxyTestResult {
		latency := int64(42)
		if strings.Contains(secret, "recovered") {
			return ProxyTestResult{Status: "healthy", LatencyMS: &latency}
		}

		return ProxyTestResult{Status: "offline", LatencyMS: &latency, Error: "dial refused"}
	}

	mux := http.NewServeMux()
	server.registerProxyRetestRoutes(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy-pools/pool-1/retest-disabled", http.NoBody)
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	request.Header.Set("Accept", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("retest = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()

	if !strings.Contains(body, `"tested":2`) || !strings.Contains(body, `"reenabled":1`) {
		t.Fatalf("retest summary missing from response: %s", body)
	}

	if strings.Contains(body, "p-enabled") || strings.Contains(body, "p-other-pool") {
		t.Fatalf("retest touched proxies outside the disabled set of the pool: %s", body)
	}

	// The decrypted credential must never reach the response.
	if strings.Contains(body, "supersecret") {
		t.Fatalf("response leaked a proxy credential: %s", body)
	}

	if len(repository.recorded) != 2 || repository.recorded[0] != "p-recovered" || repository.recorded[1] != "p-broken" {
		t.Fatalf("recorded proxy tests = %v, want both disabled pool-1 proxies", repository.recorded)
	}

	if len(repository.enabledCalls) != 1 || repository.enabledCalls[0].id != "p-recovered" ||
		!repository.enabledCalls[0].enabled {
		t.Fatalf("SetProxyEnabled calls = %+v, want exactly one re-enable of p-recovered", repository.enabledCalls)
	}
}

// TestRetestDisabledProxiesBrowserFlowRedirects is not parallel: it swaps the
// package-level proxyAccessCheck probe for a hermetic stub.
func TestRetestDisabledProxiesBrowserFlowRedirects(t *testing.T) {
	repository := &retestProxyRepository{proxies: []ProxyRecord{
		{ID: "p-down", PoolID: "pool-1", PoolName: "Local", MaskedURL: "http://user:***@10.0.0.9:8080", Enabled: false},
	}}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	originalProbe := proxyAccessCheck
	t.Cleanup(func() { proxyAccessCheck = originalProbe })

	proxyAccessCheck = func(context.Context, string) ProxyTestResult {
		return ProxyTestResult{Status: "offline", Error: "dial refused"}
	}

	mux := http.NewServeMux()
	server.registerProxyRetestRoutes(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/proxy-pools/pool-1/retest-disabled",
		strings.NewReader("csrf_token="+server.csrfToken),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("browser retest = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/app/proxies?notice=") || !strings.Contains(location, "retested") {
		t.Fatalf("browser retest redirect = %q", location)
	}

	if len(repository.recorded) != 1 || repository.recorded[0] != "p-down" {
		t.Fatalf("recorded proxy tests = %v", repository.recorded)
	}

	if len(repository.enabledCalls) != 0 {
		t.Fatalf("an unhealthy proxy was re-enabled: %+v", repository.enabledCalls)
	}
}

func TestRetestDisabledProxiesRequiresCSRFAndReportsCapability(t *testing.T) {
	t.Parallel()

	// A repository without proxy storage must report the capability, not a 500.
	server, err := New(NewService(newFormEncodingRepository(), t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerProxyRetestRoutes(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy-pools/pool-1/retest-disabled", http.NoBody)
	request.Header.Set("Accept", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("retest without CSRF = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/proxy-pools/pool-1/retest-disabled", http.NoBody)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented ||
		!strings.Contains(recorder.Body.String(), "proxies_unavailable") {
		t.Fatalf("retest without proxy storage = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type retestEnableCall struct {
	id      string
	enabled bool
}

// retestProxyRepository is a JobRepository that also satisfies proxyRepository
// so the disabled-proxy retest route can be exercised without real proxies.
type retestProxyRepository struct {
	proxies      []ProxyRecord
	recorded     []string
	enabledCalls []retestEnableCall
}

func (repository *retestProxyRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (repository *retestProxyRepository) Create(context.Context, *Job) error   { return nil }
func (repository *retestProxyRepository) Delete(context.Context, string) error { return nil }
func (repository *retestProxyRepository) Update(context.Context, *Job) error   { return nil }

func (repository *retestProxyRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (repository *retestProxyRepository) ListProxyPools(context.Context) ([]ProxyPoolRecord, error) {
	return nil, nil
}

func (repository *retestProxyRepository) ListProxies(_ context.Context, poolID string) ([]ProxyRecord, error) {
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

func (repository *retestProxyRepository) ImportProxyPool(
	context.Context, string, string, []string,
) (ProxyPoolRecord, int, error) {
	return ProxyPoolRecord{}, 0, nil
}

func (repository *retestProxyRepository) ResolveProxyPool(context.Context, string) ([]string, error) {
	return nil, nil
}

func (repository *retestProxyRepository) GetProxySecret(_ context.Context, id string) (string, error) {
	// The credential embeds the proxy ID so the stubbed probe can vary its
	// verdict per proxy. The assertion is that this string never appears in
	// any response.
	return "http://user:supersecret-" + id + "@127.0.0.1:1", nil
}

func (repository *retestProxyRepository) RecordProxyTest(_ context.Context, id string, _ ProxyTestResult) error {
	repository.recorded = append(repository.recorded, id)

	return nil
}

func (repository *retestProxyRepository) SetProxyEnabled(_ context.Context, id string, enabled bool) error {
	repository.enabledCalls = append(repository.enabledCalls, retestEnableCall{id: id, enabled: enabled})

	return nil
}

func (repository *retestProxyRepository) DeleteProxy(context.Context, string) error     { return nil }
func (repository *retestProxyRepository) DeleteProxyPool(context.Context, string) error { return nil }

var _ proxyRepository = (*retestProxyRepository)(nil)
