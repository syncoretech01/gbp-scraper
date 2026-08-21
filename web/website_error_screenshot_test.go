package web

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

func failingAnalyzerFactory(result enrichment.Result) websiteAnalyzerFactory {
	return func(EnrichmentOptions) (websiteAnalyzer, error) {
		return analyzerStub{result: result}, nil
	}
}

func TestShouldCaptureErrorScreenshotCoversEveryFailingState(t *testing.T) {
	t.Parallel()

	failing := []enrichment.Result{
		{Reachable: true, StatusCode: 404},
		{Reachable: true, StatusCode: 500},
		{Reachable: true, StatusCode: 200, Parked: true},
		{Reachable: true, StatusCode: 200, ComingSoon: true},
		{Reachable: true, StatusCode: 200, Placeholder: true},
		{Reachable: true, StatusCode: 200, CertificateError: "x509: certificate expired"},
		{Error: "dial tcp: no such host"},
	}
	for index, result := range failing {
		if !shouldCaptureErrorScreenshot(result) {
			t.Errorf("case %d: shouldCaptureErrorScreenshot(%#v) = false, want true", index, result)
		}
	}

	healthy := enrichment.Result{Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true}
	if shouldCaptureErrorScreenshot(healthy) {
		t.Fatal("a healthy audit must not trigger an error capture")
	}
}

func TestProcessEnrichmentQueueCapturesAnErrorScreenshotForAFailingSite(t *testing.T) {
	capturer := &fakeScreenshotCapturer{}
	stubScreenshotSeams(t, capturer, true)

	task := screenshotTask("task-error")
	repository := &enrichmentRepositoryStub{next: &task}
	dataFolder := t.TempDir()
	service := NewService(repository, dataFolder)

	processed, err := service.processEnrichmentQueue(context.Background(), 1, failingAnalyzerFactory(
		enrichment.Result{
			RequestedURL: "https://example.com", FinalURL: "https://example.com/",
			Reachable: true, StatusCode: 503,
		},
	))
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}

	if repository.errorAudit != 42 || repository.errorPath != "screenshots/42-error.png" {
		t.Fatalf("attached error screenshot = %d, %q", repository.errorAudit, repository.errorPath)
	}

	stored, err := os.ReadFile(filepath.Join(dataFolder, "screenshots", "42-error.png"))
	if err != nil || !bytes.Equal(stored, fakePNGHeader) {
		t.Fatalf("stored error screenshot = %v, %v", stored, err)
	}

	// The homepage capture still runs for a reachable site, so both files
	// exist and neither replaces the other.
	if repository.attachedPath != "screenshots/42.png" {
		t.Fatalf("homepage screenshot = %q, want it kept alongside the error capture", repository.attachedPath)
	}

	if len(repository.events) != 0 {
		t.Fatalf("unexpected screenshot events %v", repository.events)
	}
}

func TestProcessEnrichmentQueueSkipsTheErrorScreenshotForAHealthySite(t *testing.T) {
	capturer := &fakeScreenshotCapturer{}
	stubScreenshotSeams(t, capturer, true)

	task := screenshotTask("task-healthy")
	repository := &enrichmentRepositoryStub{next: &task}
	service := NewService(repository, t.TempDir())

	processed, err := service.processEnrichmentQueue(
		context.Background(), 1, successfulAnalyzerFactory("https://www.example.com/"),
	)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}

	if repository.errorPath != "" {
		t.Fatalf("healthy audit attached an error screenshot %q", repository.errorPath)
	}

	if len(capturer.capturedURLs) != 1 {
		t.Fatalf("captured %d pages, want only the homepage", len(capturer.capturedURLs))
	}
}

func TestErrorScreenshotFileNameStaysInsideTheServedNameSet(t *testing.T) {
	t.Parallel()

	name := errorScreenshotFileName(1234)
	if name != "1234-error.png" {
		t.Fatalf("errorScreenshotFileName = %q", name)
	}

	if !screenshotNamePattern.MatchString(name) {
		t.Fatalf("error screenshot name %q is not servable by the screenshot route", name)
	}
}
