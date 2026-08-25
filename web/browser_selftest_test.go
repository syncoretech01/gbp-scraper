package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBrowserLaunchCheckPassed(t *testing.T) {
	probe := func(context.Context) (time.Duration, error) {
		return 1234 * time.Millisecond, nil
	}

	check := browserLaunchCheck(context.Background(), probe, time.Now())

	if check.Name != "browser_launch" {
		t.Fatalf("name = %q, want browser_launch", check.Name)
	}
	if check.State != "passed" {
		t.Fatalf("state = %q, want passed", check.State)
	}
	if !strings.Contains(check.Message, "1234 ms") {
		t.Fatalf("message %q does not report the launch duration", check.Message)
	}
}

func TestBrowserLaunchCheckWarnsOnFailure(t *testing.T) {
	probe := func(context.Context) (time.Duration, error) {
		return 42 * time.Millisecond, errors.New("launch chromium: driver not installed")
	}

	check := browserLaunchCheck(context.Background(), probe, time.Now())

	if check.State != "warning" {
		t.Fatalf("state = %q, want warning", check.State)
	}
	// The failure must not be a hard "failed": Fast mode needs no browser, so a
	// host that cannot launch Chromium is degraded, not broken.
	if !strings.Contains(check.Message, "Fast mode") {
		t.Fatalf("message %q should explain Fast mode is unaffected", check.Message)
	}
	if !strings.Contains(check.Message, "driver not installed") {
		t.Fatalf("message %q should surface the redacted reason", check.Message)
	}
}

func TestBrowserLaunchCheckRedactsSecretsInFailure(t *testing.T) {
	probe := func(context.Context) (time.Duration, error) {
		return 0, errors.New("launch chromium via http://user:supersecret@proxy.example:8080")
	}

	check := browserLaunchCheck(context.Background(), probe, time.Now())

	if strings.Contains(check.Message, "supersecret") {
		t.Fatalf("message %q leaked a credential", check.Message)
	}
}

func TestBrowserLaunchCheckSkippedWhenProbeNil(t *testing.T) {
	check := browserLaunchCheck(context.Background(), nil, time.Now())

	if check.State != "skipped" {
		t.Fatalf("state = %q, want skipped", check.State)
	}
}

func TestBrowserLaunchCheckHonoursContextCancellation(t *testing.T) {
	// A probe that blocks until its context ends models a wedged launch. The
	// check must return whatever the probe reports without hanging.
	probe := func(ctx context.Context) (time.Duration, error) {
		started := time.Now()
		<-ctx.Done()
		return time.Since(started), errors.New("browser launch did not complete in time")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan systemSelfTestCheck, 1)
	go func() { done <- browserLaunchCheck(ctx, probe, time.Now()) }()

	select {
	case check := <-done:
		if check.State != "warning" {
			t.Fatalf("state = %q, want warning", check.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("browserLaunchCheck did not return after its probe context expired")
	}
}

func TestParseExplicitBrowserCheck(t *testing.T) {
	cases := []struct {
		value string
		want  bool
		isErr bool
	}{
		{"", false, false},
		{"false", false, false},
		{"0", false, false},
		{"true", true, false},
		{"1", true, false},
		{"TRUE", true, false},
		{"maybe", false, true},
	}
	for _, tc := range cases {
		got, err := parseExplicitBrowserCheck(tc.value)
		if tc.isErr {
			if err == nil {
				t.Fatalf("value %q: expected an error", tc.value)
			}
			if !strings.Contains(err.Error(), "include_browser") {
				t.Fatalf("value %q: error %q should name the field", tc.value, err.Error())
			}
			continue
		}
		if err != nil {
			t.Fatalf("value %q: unexpected error %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("value %q: got %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestSystemSelfTestSkipsBrowserLaunchByDefault(t *testing.T) {
	server := newBrowserSelfTestServer(t)
	called := 0
	server.browserProbe = func(context.Context) (time.Duration, error) {
		called++
		return 0, nil
	}

	report := runDiagnosticsSelfTest(t, server)

	check := findSelfTestCheck(t, report, "browser_launch")
	if check.State != "skipped" {
		t.Fatalf("browser_launch default state = %q, want skipped", check.State)
	}
	if called != 0 {
		t.Fatalf("browser probe ran %d times without include_browser", called)
	}
}

func TestSystemSelfTestRunsBrowserLaunchWhenRequested(t *testing.T) {
	server := newBrowserSelfTestServer(t)
	called := 0
	server.browserProbe = func(context.Context) (time.Duration, error) {
		called++
		return 900 * time.Millisecond, nil
	}

	report := runDiagnosticsSelfTestQuery(t, server, "include_browser=true")

	check := findSelfTestCheck(t, report, "browser_launch")
	if check.State != "passed" {
		t.Fatalf("browser_launch with include_browser = %+v", check)
	}
	if called != 1 {
		t.Fatalf("browser probe ran %d times, want 1", called)
	}
}

func TestSystemSelfTestBrowserLaunchWarningDegradesStatus(t *testing.T) {
	server := newBrowserSelfTestServer(t)
	server.browserProbe = func(context.Context) (time.Duration, error) {
		return 10 * time.Millisecond, errors.New("launch chromium: target closed")
	}

	report := runDiagnosticsSelfTestQuery(t, server, "include_browser=true")

	check := findSelfTestCheck(t, report, "browser_launch")
	if check.State != "warning" {
		t.Fatalf("browser_launch failure state = %q, want warning", check.State)
	}
	if report.Status != "degraded" {
		t.Fatalf("self-test status with browser warning = %q, want degraded", report.Status)
	}
}

func TestSystemSelfTestRejectsInvalidBrowserFlag(t *testing.T) {
	server := newBrowserSelfTestServer(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/self-test?include_browser=maybe", http.NoBody)
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid include_browser status = %d, want 422; body = %s", recorder.Code, recorder.Body.String())
	}
}

func newBrowserSelfTestServer(t *testing.T) *Server {
	t.Helper()

	repository := &diagnosticJobRepository{snapshot: SystemDatabaseSnapshot{SchemaVersion: 5}}
	service := NewService(repository, t.TempDir())
	service.RecordSchedulerHeartbeat(time.Now().UTC())
	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.systemProbe = &fakeLocalSystemProbe{resources: healthyTestResources()}

	return server
}

func runDiagnosticsSelfTestQuery(t *testing.T, server *Server, query string) systemSelfTestResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/self-test?"+query, http.NoBody)
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self-test status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data systemSelfTestResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode self-test response: %v; body = %s", err, recorder.Body.String())
	}

	return payload.Data
}
