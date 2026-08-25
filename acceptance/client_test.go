package acceptance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientCreateJobReturnsID(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	id, err := client.CreateJob(context.Background(), testConfig().Job)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if id != defaultScenario().jobID {
		t.Errorf("id = %q", id)
	}
}

func TestClientReadbacks(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)
	ctx := context.Background()
	id := defaultScenario().jobID

	progress, err := client.Progress(ctx, id)
	if err != nil || progress.State != "partial" {
		t.Fatalf("Progress = %+v err=%v", progress, err)
	}

	coverage, ok, err := client.Coverage(ctx, id)
	if err != nil || !ok || coverage.Totals.TasksTotal != 48 {
		t.Fatalf("Coverage ok=%v err=%v totals=%+v", ok, err, coverage.Totals)
	}

	benchmark, ok, err := client.Benchmark(ctx, id)
	if err != nil || !ok || benchmark.Totals.UniqueBusinesses != 100 {
		t.Fatalf("Benchmark ok=%v err=%v", ok, err)
	}

	total, ok, err := client.ResultsTotal(ctx, id)
	if err != nil || !ok || total != 100 {
		t.Fatalf("ResultsTotal ok=%v err=%v total=%d", ok, err, total)
	}

	logs, ok, err := client.Logs(ctx, id)
	if err != nil || !ok || logs == "" {
		t.Fatalf("Logs ok=%v err=%v empty=%v", ok, err, logs == "")
	}

	metrics, err := client.SystemMetrics(ctx)
	if err != nil || metrics.Resources.CPUPercent != 73.5 {
		t.Fatalf("SystemMetrics = %+v err=%v", metrics.Resources, err)
	}
}

func TestClientUnavailableReadbacksAreNotErrors(t *testing.T) {
	sc := defaultScenario()
	sc.benchmarkUnavailable = true
	sc.coverageUnavailable = true
	sc.logsUnavailable = true
	sc.resultsUnavailable = true
	fake := newFakeServer(t, sc)
	client := newClientFor(t, fake)
	ctx := context.Background()
	id := sc.jobID

	if _, ok, err := client.Benchmark(ctx, id); ok || err != nil {
		t.Errorf("Benchmark unavailable: ok=%v err=%v", ok, err)
	}
	if _, ok, err := client.Coverage(ctx, id); ok || err != nil {
		t.Errorf("Coverage unavailable: ok=%v err=%v", ok, err)
	}
	if _, ok, err := client.Logs(ctx, id); ok || err != nil {
		t.Errorf("Logs unavailable: ok=%v err=%v", ok, err)
	}
	if _, ok, err := client.ResultsTotal(ctx, id); ok || err != nil {
		t.Errorf("ResultsTotal unavailable: ok=%v err=%v", ok, err)
	}
}

func TestClientNon2xxIsAPIStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"boom","message":"nope"}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Progress(context.Background(), "id")
	if err == nil {
		t.Fatalf("expected error")
	}
	var status *ErrAPIStatus
	if !errors.As(err, &status) || status.Status != http.StatusInternalServerError {
		t.Errorf("error = %v, want ErrAPIStatus 500", err)
	}
}

func TestClientSendsBearerToken(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, WithToken("secret"), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.SystemMetrics(context.Background()); err != nil {
		t.Fatalf("SystemMetrics: %v", err)
	}
	if got := <-seen; got != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", got)
	}
}

func TestNewClientRejectsBadURL(t *testing.T) {
	if _, err := NewClient(""); err == nil {
		t.Errorf("empty URL should error")
	}
	if _, err := NewClient("ftp://x"); err == nil {
		t.Errorf("non-http scheme should error")
	}
}
