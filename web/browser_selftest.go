package web

import (
	"context"
	"fmt"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const (
	// systemBrowserLaunchTimeout bounds the optional real Chromium launch probe.
	// A first launch pays a cold-start cost (the driver spawns Node and a fresh
	// browser process), so it is deliberately larger than the lightweight
	// self-test budget. The probe is opt-in for exactly this reason.
	systemBrowserLaunchTimeout = 20 * time.Second

	// browserLaunchInternalTimeoutMS bounds the Playwright launch call itself so
	// a wedged driver cannot hold the probe goroutine open past the outer
	// context. It is kept below systemBrowserLaunchTimeout so the launch error
	// surfaces before the outer deadline fires.
	browserLaunchInternalTimeoutMS = 15000
)

// browserLaunchProbe attempts one bounded, headless Chromium launch against
// about:blank and reports how long the attempt took. A nil error means a
// browser genuinely started in this environment; any error means browser-mode
// scrapes cannot run here (Fast mode, which uses no browser, is unaffected).
//
// It is a function type so the self-test can inject a fake in unit tests and
// never spawn a real browser off the test host.
type browserLaunchProbe func(ctx context.Context) (time.Duration, error)

// browserHardeningArgs mirrors the process-hardening switches the scrape engine
// passes to Chromium: no sandbox (the container has no user namespaces),
// single-process, and shared memory kept off the small default /dev/shm. The
// self-check launches with the same switches so a pass reflects the real scrape
// runtime rather than a softer, more forgiving configuration.
//
// The headless switch is intentionally omitted here: it is supplied through the
// Playwright Headless option so the driver and the browser agree on the mode.
var browserHardeningArgs = []string{
	"--no-sandbox",
	"--disable-setuid-sandbox",
	"--disable-dev-shm-usage",
	"--no-zygote",
	"--single-process",
	"--disable-gpu",
	"--mute-audio",
	"--disable-extensions",
	"--disable-breakpad",
	"--disable-default-apps",
	"--disable-notifications",
	"--disable-blink-features=AutomationControlled",
}

// defaultBrowserLaunchProbe launches a real headless Chromium through the
// compiled-in Playwright driver and opens about:blank. It never reaches the
// network and never downloads anything: SkipInstallBrowsers keeps a missing
// driver an honest, fast failure rather than a multi-hundred-megabyte fetch.
//
// The launch runs on its own goroutine so the outer context deadline is a hard
// cap even if the driver wedges before Playwright's own timeout fires.
func defaultBrowserLaunchProbe(ctx context.Context) (time.Duration, error) {
	started := time.Now()
	result := make(chan error, 1)

	go func() {
		result <- runBrowserLaunchProbe()
	}()

	select {
	case <-ctx.Done():
		return time.Since(started), fmt.Errorf("browser launch did not complete in time: %w", ctx.Err())
	case err := <-result:
		return time.Since(started), err
	}
}

// runBrowserLaunchProbe performs the blocking Playwright calls. Every resource
// it opens is released before it returns, in reverse order, so a repeated
// self-test never leaks a browser process.
func runBrowserLaunchProbe() (err error) {
	pw, runErr := playwright.Run(&playwright.RunOptions{SkipInstallBrowsers: true})
	if runErr != nil {
		return fmt.Errorf("start browser driver: %w", runErr)
	}
	defer func() {
		if stopErr := pw.Stop(); stopErr != nil && err == nil {
			err = fmt.Errorf("stop browser driver: %w", stopErr)
		}
	}()

	browser, launchErr := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		Args:     browserHardeningArgs,
		Timeout:  playwright.Float(browserLaunchInternalTimeoutMS),
	})
	if launchErr != nil {
		return fmt.Errorf("launch chromium: %w", launchErr)
	}
	defer func() { _ = browser.Close() }()

	page, pageErr := browser.NewPage()
	if pageErr != nil {
		return fmt.Errorf("open browser context: %w", pageErr)
	}
	defer func() { _ = page.Close() }()

	if _, gotoErr := page.Goto("about:blank"); gotoErr != nil {
		return fmt.Errorf("navigate about:blank: %w", gotoErr)
	}

	return nil
}

// browserLaunchCheck reports whether a real Chromium can launch in this
// environment. Unlike browserRuntimeCheck, which only inspects the driver
// directory on disk, this actually starts a browser with the scrape engine's
// hardening flags.
//
// A failure is reported as a warning rather than a hard failure: the local-first
// product ships Fast mode, a pure-HTTP path that needs no browser at all, so a
// host that cannot launch Chromium is degraded, not broken. The message states
// the impact and the usual causes so an operator can act.
func browserLaunchCheck(ctx context.Context, probe browserLaunchProbe, started time.Time) systemSelfTestCheck {
	if probe == nil {
		return newSystemCheck("browser_launch", "skipped",
			"Browser launch probe is not configured in this build", started)
	}

	duration, err := probe(ctx)
	if err != nil {
		return newSystemCheck("browser_launch", "warning",
			fmt.Sprintf(
				"Chromium did not launch after %d ms: %s. Browser-mode scrapes will fail in this environment; "+
					"Fast mode does not use a browser and is unaffected. Common causes: the browser driver is not "+
					"installed, too little free memory for another browser process, or a container with too small a "+
					"/dev/shm. See docs/local-workspace.md (browser runtime).",
				duration.Milliseconds(), redactedDiagnosticError(err)),
			started)
	}

	return newSystemCheck("browser_launch", "passed",
		fmt.Sprintf(
			"Chromium launched headless and opened about:blank in %d ms; this environment can run browser-mode scrapes",
			duration.Milliseconds()),
		started)
}

// parseExplicitBrowserCheck parses the opt-in query flag that turns on the real
// browser-launch probe. Like the network flag, it defaults to off so the
// lightweight self-test never spawns a browser process unless asked.
func parseExplicitBrowserCheck(value string) (bool, error) {
	return parseExplicitBoolFlag(value, "include_browser")
}
