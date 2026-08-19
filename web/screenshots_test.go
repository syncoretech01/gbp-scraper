package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

var fakePNGHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

type fakeScreenshotCapturer struct {
	capturedURLs []string
	destinations []string
	err          error
	closed       int
}

func (capturer *fakeScreenshotCapturer) Capture(_ context.Context, pageURL, destinationPath string) error {
	capturer.capturedURLs = append(capturer.capturedURLs, pageURL)
	capturer.destinations = append(capturer.destinations, destinationPath)
	if capturer.err != nil {
		return capturer.err
	}

	return os.WriteFile(destinationPath, fakePNGHeader, 0o600)
}

func (capturer *fakeScreenshotCapturer) Close() { capturer.closed++ }

// stubScreenshotSeams swaps the browser seam and driver probe for one test
// and restores both afterwards. Tests using it must not run in parallel.
func stubScreenshotSeams(t *testing.T, capturer screenshotCapturer, driverPresent bool) {
	t.Helper()

	previousFactory := newScreenshotCapturer
	previousAvailable := screenshotDriverAvailable
	newScreenshotCapturer = func() screenshotCapturer { return capturer }
	screenshotDriverAvailable = func() bool { return driverPresent }
	t.Cleanup(func() {
		newScreenshotCapturer = previousFactory
		screenshotDriverAvailable = previousAvailable
	})
}

func screenshotTask(id string) EnrichmentTask {
	return EnrichmentTask{
		ID: id, BusinessID: "business-" + id, WebsiteURL: "https://example.com",
		State: "running", Options: EnrichmentOptions{
			Scope: string(enrichment.ScopeHomepage), MaxPages: 1, TimeoutSeconds: 5,
			MaxBodyBytes: 2048, MaxRedirects: 2, DisableInternalChecks: true,
			CaptureScreenshot: true,
		},
	}
}

func successfulAnalyzerFactory(finalURL string) websiteAnalyzerFactory {
	return func(EnrichmentOptions) (websiteAnalyzer, error) {
		return analyzerStub{result: enrichment.Result{
			RequestedURL: "https://example.com", FinalURL: finalURL,
			Reachable: true, StatusCode: 200,
		}}, nil
	}
}

func TestProcessEnrichmentQueueCapturesHomepageScreenshot(t *testing.T) {
	capturer := &fakeScreenshotCapturer{}
	stubScreenshotSeams(t, capturer, true)

	task := screenshotTask("task-shot")
	repository := &enrichmentRepositoryStub{next: &task}
	dataFolder := t.TempDir()
	service := NewService(repository, dataFolder)

	processed, err := service.processEnrichmentQueue(
		context.Background(), 2, successfulAnalyzerFactory("https://www.example.com/"),
	)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}
	if repository.finished != task.ID || repository.finishErr != nil {
		t.Fatalf("task completion = %q, %v", repository.finished, repository.finishErr)
	}
	// The stub audit ID is 42, so the file and relative path derive from it.
	if repository.attachedAudit != 42 || repository.attachedPath != "screenshots/42.png" {
		t.Fatalf("attached screenshot = %d, %q", repository.attachedAudit, repository.attachedPath)
	}
	if len(capturer.capturedURLs) != 1 || capturer.capturedURLs[0] != "https://www.example.com/" {
		t.Fatalf("captured URLs = %v, want the final URL", capturer.capturedURLs)
	}
	stored, err := os.ReadFile(filepath.Join(dataFolder, "screenshots", "42.png"))
	if err != nil || !bytes.Equal(stored, fakePNGHeader) {
		t.Fatalf("stored screenshot = %v, %v", stored, err)
	}
	if capturer.closed != 1 {
		t.Fatalf("capturer closed %d times, want once per queue pass", capturer.closed)
	}
	if len(repository.events) != 0 {
		t.Fatalf("unexpected screenshot events %v", repository.events)
	}
}

func TestProcessEnrichmentQueueSkipsScreenshotWithoutDriverOncePerPass(t *testing.T) {
	capturer := &fakeScreenshotCapturer{}
	stubScreenshotSeams(t, capturer, false)

	repository := &enrichmentRepositoryStub{
		pending: []EnrichmentTask{screenshotTask("task-a"), screenshotTask("task-b")},
	}
	dataFolder := t.TempDir()
	service := NewService(repository, dataFolder)

	processed, err := service.processEnrichmentQueue(
		context.Background(), 5, successfulAnalyzerFactory("https://www.example.com/"),
	)
	if err != nil || processed != 2 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}
	if len(capturer.capturedURLs) != 0 {
		t.Fatalf("capture attempted without a driver: %v", capturer.capturedURLs)
	}
	if len(repository.events) != 1 || repository.events[0] != "screenshot_skipped_no_driver" {
		t.Fatalf("driver-skip events = %v, want exactly one screenshot_skipped_no_driver", repository.events)
	}
	if repository.attachedPath != "" {
		t.Fatalf("screenshot attached without a driver: %q", repository.attachedPath)
	}
	if _, err := os.Stat(filepath.Join(dataFolder, "screenshots")); !os.IsNotExist(err) {
		t.Fatalf("screenshots directory unexpectedly created: %v", err)
	}
}

func TestScreenshotCaptureFailureRecordsEventWithoutFailingTask(t *testing.T) {
	capturer := &fakeScreenshotCapturer{err: errors.New("browser crashed")}
	stubScreenshotSeams(t, capturer, true)

	task := screenshotTask("task-broken")
	repository := &enrichmentRepositoryStub{next: &task}
	service := NewService(repository, t.TempDir())

	processed, err := service.processEnrichmentQueue(
		context.Background(), 1, successfulAnalyzerFactory("https://www.example.com/"),
	)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v; screenshot failures must never fail the pass", processed, err)
	}
	if repository.finished != task.ID || repository.finishErr != nil {
		t.Fatalf("task completion = %q, %v", repository.finished, repository.finishErr)
	}
	if len(repository.events) != 1 || repository.events[0] != "screenshot_failed" {
		t.Fatalf("failure events = %v, want one screenshot_failed", repository.events)
	}
	if !strings.Contains(repository.eventDetails[0], "browser crashed") {
		t.Fatalf("failure details = %q, want the capture error", repository.eventDetails[0])
	}
	if repository.attachedPath != "" {
		t.Fatalf("failed capture still attached %q", repository.attachedPath)
	}
}

func TestAppScreenshotRouteServesOnlySafePNGs(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	directory := filepath.Join(dataFolder, "screenshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create screenshots directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "7.png"), fakePNGHeader, 0o600); err != nil {
		t.Fatalf("write screenshot fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write non-png fixture: %v", err)
	}

	server, err := New(NewService(&enrichmentRepositoryStub{}, dataFolder), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	server.registerScreenshotRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/screenshots/7.png", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("screenshot status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), fakePNGHeader) {
		t.Fatalf("served body = %v", recorder.Body.Bytes())
	}

	for _, target := range []string{
		"/app/screenshots/..%2f7.png",
		"/app/screenshots/..%2F..%2Fjobs.db",
		"/app/screenshots/notes.txt",
		"/app/screenshots/missing.png",
		"/app/screenshots/.hidden.png",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, recorder.Code)
		}
	}
}

func TestValidateScreenshotURLRefusesNonHTTP(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"", "file:///etc/passwd", "javascript:alert(1)", "ftp://example.com/a", "https://", "chrome://settings",
	} {
		if err := validateScreenshotURL(target); !errors.Is(err, errScreenshotUnsupportedURL) {
			t.Fatalf("validateScreenshotURL(%q) = %v, want unsupported-URL error", target, err)
		}
	}
	for _, target := range []string{"https://example.com", "http://example.com/home"} {
		if err := validateScreenshotURL(target); err != nil {
			t.Fatalf("validateScreenshotURL(%q) = %v, want nil", target, err)
		}
	}
}

func TestDecodeEnrichmentOptionsAcceptsCaptureScreenshot(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/results/biz/enrich",
		strings.NewReader(`{"options":{"capture_screenshot":true,"scope":"homepage"}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	options, err := decodeEnrichmentOptions(httptest.NewRecorder(), request)
	if err != nil || !options.CaptureScreenshot || options.Scope != "homepage" {
		t.Fatalf("decodeEnrichmentOptions(JSON) = %+v, %v", options, err)
	}

	form := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/results/biz/enrich",
		strings.NewReader("enrichment_capture_screenshot=on"),
	)
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	options, err = decodeEnrichmentOptions(httptest.NewRecorder(), form)
	if err != nil || !options.CaptureScreenshot {
		t.Fatalf("decodeEnrichmentOptions(form) = %+v, %v", options, err)
	}

	empty := httptest.NewRequest(http.MethodPost, "/api/v1/results/biz/enrich", nil)
	options, err = decodeEnrichmentOptions(httptest.NewRecorder(), empty)
	if err != nil || options.CaptureScreenshot {
		t.Fatalf("decodeEnrichmentOptions(empty) = %+v, %v; default must stay false", options, err)
	}
}
