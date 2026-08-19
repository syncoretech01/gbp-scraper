package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestSystemHealthAndMetricsUseLightweightLocalProbes(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataFolder, "sample.bin"), []byte(strings.Repeat("x", 123)), 0o600); err != nil {
		t.Fatalf("write storage fixture: %v", err)
	}
	now := time.Now().UTC()
	repository := &diagnosticJobRepository{snapshot: SystemDatabaseSnapshot{
		SchemaVersion: 5, SQLiteVersion: "3.test", DatabaseBytes: 4096,
		JobCount: 4, BusinessCount: 36, SourceCount: 60, ExportCount: 2,
		BackupCount: 1, QueuedJobs: 2, RunningJobs: 1, LastWriteAt: &now,
	}}
	service := NewService(repository, dataFolder)
	service.RecordSchedulerHeartbeat(now)
	probe := &fakeLocalSystemProbe{resources: healthyTestResources()}
	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.systemProbe = probe

	healthRecorder := httptest.NewRecorder()
	healthRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/health", http.NoBody)
	server.srv.Handler.ServeHTTP(healthRecorder, healthRequest)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", healthRecorder.Code, healthRecorder.Body.String())
	}
	healthBody := healthRecorder.Body.String()
	for _, expected := range []string{
		`"Integrity":"not_checked"`, `"SchemaVersion":5`, `"queued_jobs":2`,
		`"running_jobs":1`, `"status":"healthy"`,
	} {
		if !strings.Contains(healthBody, expected) {
			t.Errorf("health response missing %s: %s", expected, healthBody)
		}
	}
	if probe.resourceCalls != 0 {
		t.Fatalf("lightweight health unexpectedly sampled OS resources %d time(s)", probe.resourceCalls)
	}

	metricsRecorder := httptest.NewRecorder()
	metricsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", http.NoBody)
	server.srv.Handler.ServeHTTP(metricsRecorder, metricsRequest)
	if metricsRecorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body = %s", metricsRecorder.Code, metricsRecorder.Body.String())
	}
	metricsBody := metricsRecorder.Body.String()
	for _, expected := range []string{
		`"go_version":"go`, `"cpu_percent":12.5`, `"memory_available_bytes":1073741824`,
		`"disk_free_bytes":2147483648`, `"database_bytes":4096`, `"data_bytes":123`,
	} {
		if !strings.Contains(metricsBody, expected) {
			t.Errorf("metrics response missing %s: %s", expected, metricsBody)
		}
	}
	if probe.resourceCalls != 1 {
		t.Fatalf("resource calls = %d, want 1", probe.resourceCalls)
	}
}

func TestSystemSelfTestIsOfflineByDefaultAndBoundsExplicitNetworkChecks(t *testing.T) {
	t.Parallel()

	repository := &diagnosticJobRepository{snapshot: SystemDatabaseSnapshot{SchemaVersion: 5}}
	service := NewService(repository, t.TempDir())
	service.RecordSchedulerHeartbeat(time.Now().UTC())
	probe := &fakeLocalSystemProbe{resources: healthyTestResources()}
	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.systemProbe = probe

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/self-test", http.NoBody)
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("offline self-test status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if probe.reachCalls != 0 {
		t.Fatalf("default self-test made %d network calls", probe.reachCalls)
	}
	if repository.writeChecks != 1 || !strings.Contains(recorder.Body.String(), `"network_requested":false`) ||
		strings.Count(recorder.Body.String(), `"state":"skipped"`) != 2 {
		t.Fatalf("offline self-test response = %s; write checks = %d", recorder.Body.String(), repository.writeChecks)
	}
	leftovers, err := filepath.Glob(filepath.Join(service.dataFolder, ".system-self-test-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("self-test temporary files = %v, error = %v", leftovers, err)
	}

	probe.reachError = errors.New("dial https://alice:super-secret@proxy.example:8443 failed")
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/system/self-test?include_network=true", http.NoBody)
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("network self-test status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if probe.reachCalls != 2 || !probe.reachHadDeadline {
		t.Fatalf("network calls = %d, deadline observed = %t", probe.reachCalls, probe.reachHadDeadline)
	}
	if strings.Contains(body, "super-secret") || !strings.Contains(body, `"network_requested":true`) {
		t.Fatalf("network self-test leaked a secret or omitted mode: %s", body)
	}
}

func TestBoundedDirectorySizeStopsAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	for index := range 5 {
		path := filepath.Join(directory, fmt.Sprintf("%d.txt", index))
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	_, entries, truncated, err := boundedDirectorySize(context.Background(), directory, 2)
	if err != nil || !truncated || entries != 3 {
		t.Fatalf("bounded scan entries = %d, truncated = %t, error = %v", entries, truncated, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := boundedDirectorySize(cancelled, directory, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error = %v", err)
	}
}

func TestPlaywrightModuleVersionReadsBuildMetadata(t *testing.T) {
	t.Parallel()

	if got := playwrightModuleVersion(nil); got != "not embedded" {
		t.Fatalf("nil build info version = %q, want fallback", got)
	}

	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/other/dependency", Version: "v1.0.0"}}}
	if got := playwrightModuleVersion(info); got != "not embedded" {
		t.Fatalf("unrelated dependency version = %q, want fallback", got)
	}

	info.Deps = append(info.Deps, &debug.Module{Path: playwrightModulePath, Version: "v0.6100.0"})
	if got := playwrightModuleVersion(info); got != "playwright-go v0.6100.0" {
		t.Fatalf("embedded dependency version = %q", got)
	}

	replaced := &debug.BuildInfo{Deps: []*debug.Module{{
		Path: playwrightModulePath, Version: "v0.5.0",
		Replace: &debug.Module{Path: "example.com/fork", Version: "v0.9.9"},
	}}}
	if got := playwrightModuleVersion(replaced); got != "playwright-go v0.9.9" {
		t.Fatalf("replaced dependency version = %q", got)
	}

	// The system page reads the value through a method on its page data, so the
	// template lookup keeps compiling and always has something honest to show.
	if got := (systemPageData{}).BrowserAutomation(); got == "" {
		t.Fatal("systemPageData.BrowserAutomation() returned an empty string")
	}
}

// Template lookups only fail at execution time, so the system page must render
// with the Browser automation row instead of silently 500ing in production.
func TestSystemPageTemplateRendersBrowserAutomationVersion(t *testing.T) {
	t.Parallel()

	repository := &diagnosticJobRepository{}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "system", appPageData{
		Title: "System", ActiveNav: "system", Theme: "system",
		Page: systemPageData{},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("system page render = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Browser automation") {
		t.Fatal("system page misses the Browser automation version row")
	}
	if !strings.Contains(body, browserAutomationVersion()) {
		t.Fatalf("system page misses the reported driver version %q", browserAutomationVersion())
	}
}

// The self-test must always include the browser-runtime and proxy-credential
// checks, and with no proxies configured neither may fail or reach a network.
func TestSystemSelfTestReportsBrowserRuntimeAndProxyCredentialChecks(t *testing.T) {
	repository := &diagnosticJobRepository{snapshot: SystemDatabaseSnapshot{SchemaVersion: 5}}
	service := NewService(repository, t.TempDir())
	service.RecordSchedulerHeartbeat(time.Now().UTC())
	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	probe := &fakeLocalSystemProbe{resources: healthyTestResources()}
	server.systemProbe = probe

	driverDirectory := t.TempDir()
	t.Setenv(playwrightDriverPathVariable, driverDirectory)

	report := runDiagnosticsSelfTest(t, server)
	browser := findSelfTestCheck(t, report, "browser_runtime")
	if browser.State != "passed" || !strings.Contains(browser.Message, "only a real scrape") {
		t.Fatalf("browser_runtime with driver directory = %+v", browser)
	}
	proxy := findSelfTestCheck(t, report, "proxy_credentials")
	if proxy.State != "passed" || !strings.Contains(proxy.Message, "No proxies configured") {
		t.Fatalf("proxy_credentials without proxies = %+v", proxy)
	}
	if probe.reachCalls != 0 {
		t.Fatalf("offline self-test with new checks made %d network calls", probe.reachCalls)
	}

	// A missing driver directory is a warning that degrades the run, never a failure.
	t.Setenv(playwrightDriverPathVariable, filepath.Join(driverDirectory, "missing"))
	report = runDiagnosticsSelfTest(t, server)
	browser = findSelfTestCheck(t, report, "browser_runtime")
	if browser.State != "warning" || !strings.Contains(browser.Message, "not found") {
		t.Fatalf("browser_runtime without driver directory = %+v", browser)
	}
	if report.Status != "degraded" {
		t.Fatalf("self-test status with missing driver = %q, want degraded", report.Status)
	}
}

func runDiagnosticsSelfTest(t *testing.T, server *Server) systemSelfTestResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/self-test", http.NoBody)
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

func findSelfTestCheck(t *testing.T, report systemSelfTestResponse, name string) systemSelfTestCheck {
	t.Helper()

	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("self-test report has no %q check: %+v", name, report.Checks)

	return systemSelfTestCheck{}
}

func healthyTestResources() localResourceSnapshot {
	return localResourceSnapshot{
		CPUPercent: 12.5, LogicalCPUs: 8,
		MemoryTotalBytes: 2 << 30, MemoryAvailableBytes: 1 << 30, MemoryUsedBytes: 1 << 30,
		MemoryUsedPercent: 50, DiskTotalBytes: 4 << 30, DiskFreeBytes: 2 << 30,
		DiskUsedBytes: 2 << 30, DiskUsedPercent: 50,
	}
}

type diagnosticJobRepository struct {
	snapshot    SystemDatabaseSnapshot
	snapshotErr error
	writableErr error
	writeChecks int
}

func (repository *diagnosticJobRepository) SystemDatabaseSnapshot(context.Context) (SystemDatabaseSnapshot, error) {
	return repository.snapshot, repository.snapshotErr
}

func (repository *diagnosticJobRepository) CheckDatabaseWritable(context.Context) error {
	repository.writeChecks++
	return repository.writableErr
}

func (repository *diagnosticJobRepository) Get(context.Context, string) (Job, error) {
	return Job{}, nil
}

func (repository *diagnosticJobRepository) Create(context.Context, *Job) error {
	return nil
}

func (repository *diagnosticJobRepository) Delete(context.Context, string) error {
	return nil
}

func (repository *diagnosticJobRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (repository *diagnosticJobRepository) Update(context.Context, *Job) error {
	return nil
}

type fakeLocalSystemProbe struct {
	resources        localResourceSnapshot
	resourceErr      error
	resourceCalls    int
	reachCalls       int
	reachError       error
	reachHadDeadline bool
}

func (probe *fakeLocalSystemProbe) Resources(context.Context, string) (localResourceSnapshot, error) {
	probe.resourceCalls++
	return probe.resources, probe.resourceErr
}

func (probe *fakeLocalSystemProbe) Reach(ctx context.Context, _ string) (int, error) {
	probe.reachCalls++
	if _, ok := ctx.Deadline(); ok {
		probe.reachHadDeadline = true
	}
	if probe.reachError != nil {
		return 0, probe.reachError
	}

	return http.StatusNoContent, nil
}
