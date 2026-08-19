package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const (
	// screenshotCaptureTimeout bounds one homepage capture end to end so a
	// hanging page can never stall the enrichment queue.
	screenshotCaptureTimeout = 20 * time.Second
	// screenshotViewportWidth and screenshotViewportHeight fix the captured
	// viewport so stored previews stay comparable between audits.
	screenshotViewportWidth  = 1280
	screenshotViewportHeight = 800
	// screenshotsDirectoryName is the only folder under the data directory
	// that ever stores or serves captured homepage images.
	screenshotsDirectoryName = "screenshots"
)

// errScreenshotUnsupportedURL rejects capture targets that are not plain
// web pages; the browser must never be pointed at file, javascript, or other
// non-http(s) schemes.
var errScreenshotUnsupportedURL = errors.New("screenshots are limited to http and https URLs")

// screenshotNamePattern is the complete set of file names the screenshot
// route will serve: a conservative token that always ends in .png.
var screenshotNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*\.png$`)

// screenshotCapturer renders one URL into a PNG at destinationPath. The
// production implementation drives a headless browser; tests substitute a
// fake through newScreenshotCapturer.
type screenshotCapturer interface {
	Capture(ctx context.Context, pageURL string, destinationPath string) error
}

// newScreenshotCapturer is the test seam for browser-backed captures. The
// enrichment queue constructs one capturer lazily per pass and closes it when
// the pass finishes.
var newScreenshotCapturer = func() screenshotCapturer {
	return &playwrightScreenshotCapturer{}
}

// screenshotDriverAvailable reports whether a Playwright driver installation
// looks present on disk without launching anything. It mirrors the
// browser_runtime self-test check: an explicit PLAYWRIGHT_DRIVER_PATH
// override, otherwise the playwright-go cache directories.
var screenshotDriverAvailable = func() bool {
	_, found := playwrightDriverDirectory()

	return found
}

// playwrightScreenshotCapturer captures homepage screenshots with a headless
// Chromium. The driver and browser start lazily on the first capture and are
// reused until Close, so one queue pass pays the launch cost at most once.
type playwrightScreenshotCapturer struct {
	mu      sync.Mutex
	driver  *playwright.Playwright
	browser playwright.Browser
}

// Capture renders pageURL at the fixed viewport and writes a PNG to
// destinationPath. It refuses non-http(s) URLs and never runs longer than
// screenshotCaptureTimeout.
func (capturer *playwrightScreenshotCapturer) Capture(ctx context.Context, pageURL, destinationPath string) error {
	if err := validateScreenshotURL(pageURL); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, screenshotCaptureTimeout)
	defer cancel()

	// The playwright-go API has no context support, so the capture runs on
	// its own goroutine while this caller keeps the hard deadline. The inner
	// navigation and screenshot timeouts below stop the goroutine itself.
	done := make(chan error, 1)
	go func() { done <- capturer.capture(pageURL, destinationPath) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("homepage screenshot did not finish within %s: %w", screenshotCaptureTimeout, ctx.Err())
	}
}

func (capturer *playwrightScreenshotCapturer) capture(pageURL, destinationPath string) error {
	capturer.mu.Lock()
	defer capturer.mu.Unlock()

	if err := capturer.ensureBrowser(); err != nil {
		return err
	}
	page, err := capturer.browser.NewPage(playwright.BrowserNewPageOptions{
		Viewport: &playwright.Size{Width: screenshotViewportWidth, Height: screenshotViewportHeight},
	})
	if err != nil {
		return fmt.Errorf("open screenshot page: %w", err)
	}
	defer func() { _ = page.Close() }()

	if _, err := page.Goto(pageURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateLoad,
		Timeout:   playwright.Float(float64((screenshotCaptureTimeout - 4*time.Second).Milliseconds())),
	}); err != nil {
		return fmt.Errorf("load %s: %w", pageURL, err)
	}
	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(false),
		Path:     playwright.String(destinationPath),
		Timeout:  playwright.Float(float64((screenshotCaptureTimeout / 4).Milliseconds())),
	}); err != nil {
		return fmt.Errorf("capture screenshot: %w", err)
	}

	return nil
}

// ensureBrowser starts the driver and one headless browser lazily. A missing
// driver surfaces as an ordinary error that the queue records without ever
// failing the audit itself.
func (capturer *playwrightScreenshotCapturer) ensureBrowser() error {
	if capturer.browser != nil {
		return nil
	}
	driver, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright driver: %w", err)
	}
	browser, err := driver.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		_ = driver.Stop()

		return fmt.Errorf("launch headless browser: %w", err)
	}
	capturer.driver = driver
	capturer.browser = browser

	return nil
}

// Close releases the shared browser and driver after a queue pass.
func (capturer *playwrightScreenshotCapturer) Close() {
	capturer.mu.Lock()
	defer capturer.mu.Unlock()

	if capturer.browser != nil {
		_ = capturer.browser.Close()
		capturer.browser = nil
	}
	if capturer.driver != nil {
		_ = capturer.driver.Stop()
		capturer.driver = nil
	}
}

// validateScreenshotURL refuses anything but an absolute http(s) URL so the
// browser can never be steered at local files or internal schemes.
func validateScreenshotURL(pageURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		return fmt.Errorf("%w: %v", errScreenshotUnsupportedURL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return errScreenshotUnsupportedURL
	}

	return nil
}

// screenshotFileName derives the stored file name from the immutable audit
// row ID, which keeps every name inside the safe character set by
// construction.
func screenshotFileName(auditID int64) string {
	return strconv.FormatInt(auditID, 10) + ".png"
}

// captureAuditScreenshot renders the audited site's final homepage into
// <dataFolder>/screenshots/<auditID>.png and records the relative path on the
// audit and its website row. Failures never propagate: they are appended to
// audit_logs as a screenshot_failed action and the completed audit stays
// untouched.
func (s *Service) captureAuditScreenshot(
	ctx context.Context,
	repository enrichmentRepository,
	capturer screenshotCapturer,
	task EnrichmentTask,
	auditID int64,
	finalURL string,
) {
	targetURL := strings.TrimSpace(finalURL)
	if targetURL == "" {
		targetURL = strings.TrimSpace(task.WebsiteURL)
	}
	name := screenshotFileName(auditID)
	relativePath := path.Join(screenshotsDirectoryName, name)
	directory := filepath.Join(s.dataFolder, screenshotsDirectoryName)

	err := func() error {
		if err := validateScreenshotURL(targetURL); err != nil {
			return err
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create screenshots directory: %w", err)
		}
		if err := capturer.Capture(ctx, targetURL, filepath.Join(directory, name)); err != nil {
			return err
		}

		return repository.AttachAuditScreenshot(context.WithoutCancel(ctx), auditID, relativePath)
	}()
	if err != nil {
		_ = repository.RecordScreenshotEvent(
			context.WithoutCancel(ctx),
			"screenshot_failed",
			strconv.FormatInt(auditID, 10),
			screenshotEventDetails(map[string]string{
				"business_id": task.BusinessID,
				"task_id":     task.ID,
				"url":         targetURL,
				"error":       err.Error(),
			}),
		)
	}
}

// screenshotEventDetails encodes audit-log details defensively; audit trail
// writes must never fail because of a marshalling problem.
func screenshotEventDetails(details map[string]string) string {
	encoded, err := json.Marshal(details)
	if err != nil {
		return "{}"
	}

	return string(encoded)
}

// registerScreenshotRoutes exposes stored homepage screenshots to the local
// app UI. Only conservative .png names inside the screenshots folder are ever
// served.
func (s *Server) registerScreenshotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/screenshots/{name}", s.appScreenshot)
}

func (s *Server) appScreenshot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !screenshotNamePattern.MatchString(name) {
		http.NotFound(w, r)

		return
	}
	fullPath, err := safeDataPath(s.svc.dataFolder, screenshotsDirectoryName+"/"+name)
	if err != nil {
		http.NotFound(w, r)

		return
	}
	file, err := os.Open(fullPath)
	if err != nil {
		http.NotFound(w, r)

		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)

		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_, _ = io.Copy(w, file)
}
