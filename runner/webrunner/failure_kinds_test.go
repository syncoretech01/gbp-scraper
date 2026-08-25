package webrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestClassifyFailureKindMapsRealWorldErrors is the authoritative table: each
// representative attempt error string, taken from playwright-go, the scrapemate
// engine, the Chromium net stack and the app layer, must resolve to the right
// fine kind AND the right coarse bucket. The coarse column is what scheduling
// and the log-level map key off; the fine column is what the operator reads.
func TestClassifyFailureKindMapsRealWorldErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantFine   string
		wantCoarse string
		wantSignal string
	}{
		// Browser crash: the --single-process Chromium dying mid-task is the
		// load-dependent failure this run hit.
		{"target closed", errors.New("playwright: target closed"), FailureKindBrowserCrash, coarseBrowserFailure, "target-closed"},
		{"browser has been closed", errors.New("browserContext.NewPage: Target page, context or browser has been closed"), FailureKindBrowserContextFailure, coarseBrowserFailure, "new-page"},
		{"page crashed", errors.New("Navigation failed because page crashed! (chromium)"), FailureKindBrowserCrash, coarseBrowserFailure, "page-crashed"},
		{"chromium exited", errors.New("chromium exited unexpectedly with signal 11"), FailureKindBrowserCrash, coarseBrowserFailure, "process-exited"},
		{"single process crash", errors.New("chromium --single-process renderer gone"), FailureKindBrowserCrash, coarseBrowserFailure, "single-process"},

		// Browser context/page creation.
		{"new context failed", errors.New("browser.NewContext: failed to create browser context"), FailureKindBrowserContextFailure, coarseBrowserFailure, "new-context"},

		// Navigation timeout vs generic navigation failure.
		{"navigation timeout", errors.New("page.Goto: Timeout 30000ms exceeded"), FailureKindNavigationTimeout, coarseWebsiteTimeout, "navigation"},
		{"context deadline", errors.New("context deadline exceeded"), FailureKindNavigationTimeout, coarseWebsiteTimeout, "deadline"},
		{"inactivity timeout", errors.New("scrapemate inactivity timeout reached"), FailureKindNavigationTimeout, coarseWebsiteTimeout, "inactivity"},
		{"aborted navigation", errors.New("page.Goto: net::ERR_ABORTED at https://www.google.com/maps"), FailureKindNavigationFailure, coarseTaskFailed, "navigation"},

		// Google walls, each with its named sub-signal.
		{"http 429", errors.New("maps returned HTTP 429 Too Many Requests"), FailureKindRateLimit, coarseBlocked, rateLimitSignalHTTP429},
		{"too many requests", errors.New("server said too many requests, slow down"), FailureKindRateLimit, coarseBlocked, rateLimitSignalTooMany},
		{"captcha", errors.New("navigation hit /sorry/index captcha challenge"), FailureKindGoogleBlock, coarseBlocked, googleBlockSignalCaptcha},
		{"unusual traffic", errors.New("our systems have detected unusual traffic from your network"), FailureKindGoogleBlock, coarseBlocked, googleBlockSignalUnusualTraffic},
		{"sorry redirect", errors.New("redirected to https://www.google.com/sorry/index"), FailureKindGoogleBlock, coarseBlocked, googleBlockSignalSorryRedirect},
		{"consent wall", errors.New("landed on consent.google.com interstitial"), FailureKindGoogleBlock, coarseBlocked, googleBlockSignalConsent},
		{"http 403", errors.New("status 403 access from maps"), FailureKindGoogleBlock, coarseBlocked, googleBlockSignalForbidden},

		// Proxy faults.
		{"proxy connect", errors.New("proxyconnect tcp: dial failed"), FailureKindProxyConnect, coarseProxyFailure, "proxy-connect"},
		{"socks handshake", errors.New("socks5 handshake rejected"), FailureKindProxyConnect, coarseProxyFailure, "proxy-connect"},
		{"proxy auth", errors.New("proxy returned 407 proxy authentication required"), FailureKindProxyAuth, coarseProxyFailure, "proxy-auth"},

		// Network faults on a direct connection land in the task-failed bucket
		// but now carry a precise fine kind.
		{"dns no such host", errors.New("dial tcp: lookup maps.google.com: no such host"), FailureKindNetworkDNS, coarseTaskFailed, "no-such-host"},
		{"dns err name not resolved", errors.New("page.Goto: net::ERR_NAME_NOT_RESOLVED"), FailureKindNetworkDNS, coarseTaskFailed, "err-name-not-resolved"},
		{"tls certificate", errors.New("x509: certificate signed by unknown authority"), FailureKindNetworkTLS, coarseTaskFailed, "certificate"},
		{"connection refused", errors.New("dial tcp 127.0.0.1:9222: connect: connection refused"), FailureKindNetworkRefused, coarseTaskFailed, "connection-refused"},
		{"connection reset", errors.New("read tcp: connection reset by peer"), FailureKindNetworkRefused, coarseTaskFailed, "connection-reset"},

		// Host resource pressure — the shm/OOM class the compose file makes
		// likely with several single-process browsers.
		{"disk full", errors.New("write checkpoint: no space left on device"), FailureKindResourcePressure, coarseTaskFailed, "disk-full"},

		// Cancellation and parsing.
		{"operator cancelled", fmt.Errorf("aborting task: %w", context.Canceled), FailureKindOperatorCancelled, coarseTaskFailed, "cancelled"},
		{"parsing", errors.New("could not parse listing payload"), FailureKindParsingFailure, coarseParsingFailure, "parse"},
		{"unmarshal", errors.New("failed to unmarshal JSON: unexpected end of input"), FailureKindParsingFailure, coarseParsingFailure, "unmarshal"},

		// Nothing recognised.
		{"unknown", errors.New("something else entirely"), FailureKindUnknown, coarseTaskFailed, ""},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := classifyFailureKind(test.err)

			if got.Fine != test.wantFine {
				t.Errorf("fine kind = %q, want %q", got.Fine, test.wantFine)
			}

			if got.Coarse != test.wantCoarse {
				t.Errorf("coarse bucket = %q, want %q", got.Coarse, test.wantCoarse)
			}

			if test.wantSignal != "" && got.Signal != test.wantSignal {
				t.Errorf("signal = %q, want %q", got.Signal, test.wantSignal)
			}

			// The coarse the classifier chose must equal the documented
			// mapping-layer coarse for the fine kind it emitted, and must equal
			// what the coarse-only entry point returns.
			if mapped := coarseForKind(got.Fine); mapped != got.Coarse {
				t.Errorf("coarseForKind(%q) = %q, but classification coarse = %q", got.Fine, mapped, got.Coarse)
			}

			if legacy := classifyTaskFailure(test.err); legacy != got.Coarse {
				t.Errorf("classifyTaskFailure = %q, but classification coarse = %q", legacy, got.Coarse)
			}
		})
	}
}

// TestClassifyFailureKindSignalSpotChecks pins the sub-signals whose table rows
// left wantFine blank, to keep the table above readable.
func TestClassifyFailureKindSignalSpotChecks(t *testing.T) {
	t.Parallel()

	if got := classifyFailureKind(errors.New("playwright driver connection closed")); got.Fine != FailureKindBrowserCrash || got.Signal != "browser-closed" {
		t.Errorf("driver connection closed = %+v, want browser-crash/browser-closed", got)
	}

	if got := classifyFailureKind(errors.New("cannot allocate memory")); got.Fine != FailureKindResourcePressure || got.Signal != "memory" {
		t.Errorf("cannot allocate memory = %+v, want resource-pressure/memory", got)
	}
}

// TestCoarseForKindCoversEveryEmittableKind guards the mapping layer: every
// FailureKind* the classifier can emit must map to a non-empty coarse bucket
// that is one of the six legacy values.
func TestCoarseForKindCoversEveryEmittableKind(t *testing.T) {
	t.Parallel()

	valid := map[string]bool{
		coarseBlocked: true, coarseBrowserFailure: true, coarseProxyFailure: true,
		coarseWebsiteTimeout: true, coarseParsingFailure: true, coarseTaskFailed: true,
	}

	kinds := []string{
		FailureKindBrowserCrash, FailureKindBrowserContextFailure,
		FailureKindNavigationFailure, FailureKindNavigationTimeout,
		FailureKindGoogleBlock, FailureKindRateLimit,
		FailureKindProxyConnect, FailureKindProxyAuth,
		FailureKindNetworkDNS, FailureKindNetworkTLS, FailureKindNetworkRefused,
		FailureKindOperatorCancelled, FailureKindResourcePressure,
		FailureKindParsingFailure, FailureKindUnknown,
	}

	for _, kind := range kinds {
		if coarse := coarseForKind(kind); !valid[coarse] {
			t.Errorf("coarseForKind(%q) = %q, not a legacy coarse bucket", kind, coarse)
		}
	}
}

// TestAnnotateSurfacesFineKindOnEventContext proves the fields an operator will
// read on the worker event.
func TestAnnotateSurfacesFineKindOnEventContext(t *testing.T) {
	t.Parallel()

	classification := classifyFailureKind(errors.New("playwright: target closed"))
	fields := classification.annotate(map[string]any{"task_key": "t-1"})

	if fields["task_key"] != "t-1" {
		t.Errorf("annotate dropped caller field task_key: %v", fields["task_key"])
	}

	if fields["failure_kind"] != FailureKindBrowserCrash {
		t.Errorf("failure_kind = %v, want %q", fields["failure_kind"], FailureKindBrowserCrash)
	}

	if fields["failure_class"] != coarseBrowserFailure {
		t.Errorf("failure_class = %v, want %q", fields["failure_class"], coarseBrowserFailure)
	}

	if fields["failure_signal"] != "target-closed" {
		t.Errorf("failure_signal = %v, want target-closed", fields["failure_signal"])
	}

	// A kind with no sub-signal must not add an empty failure_signal key.
	plain := classifyFailureKind(errors.New("something else entirely")).annotate(nil)
	if _, ok := plain["failure_signal"]; ok {
		t.Errorf("failure_signal should be absent for a signal-less kind, got %v", plain["failure_signal"])
	}
}

// TestClassifyTaskFailureCoarseIsUnchanged is the drift guard: for a broad
// corpus the refactored classifyTaskFailure must return exactly what the
// original inline switch returned. legacyCoarse is a verbatim copy of the
// pre-refinement logic; any divergence here means the refactor changed a value
// scheduling depends on.
func TestClassifyTaskFailureCoarseIsUnchanged(t *testing.T) {
	t.Parallel()

	corpus := []string{
		"playwright: target closed",
		"chromium exited unexpectedly",
		"browser.NewContext: failed to create browser context",
		"driver connection closed",
		"proxyconnect tcp: dial failed",
		"socks5 handshake rejected",
		"proxy returned 407 proxy authentication required",
		"tunnel connection failed",
		"context deadline exceeded",
		"page.Goto: Timeout 30000ms exceeded",
		"scrapemate inactivity timeout reached",
		"could not parse listing payload",
		"failed to unmarshal JSON: unexpected end of input",
		"unexpected page type",
		"maps returned HTTP 429 Too Many Requests",
		"navigation hit /sorry/index captcha challenge",
		"our systems have detected unusual traffic from your network",
		"consent.google.com interstitial",
		"status 403 access denied from maps",
		"temporarily blocked by google",
		"dial tcp: lookup maps.google.com: no such host",
		"page.Goto: net::ERR_NAME_NOT_RESOLVED",
		"x509: certificate signed by unknown authority",
		"dial tcp 127.0.0.1:9222: connect: connection refused",
		"read tcp: connection reset by peer",
		"cannot allocate memory",
		"write checkpoint: no space left on device",
		"context canceled",
		"something else entirely",
		"",
	}

	for _, message := range corpus {
		var err error
		if message != "" {
			err = errors.New(message)
		}

		if got, want := classifyTaskFailure(err), legacyCoarse(err); got != want {
			t.Errorf("classifyTaskFailure(%q) = %q, legacy = %q", message, got, want)
		}
	}
}

// legacyCoarse is the exact pre-refinement classifyTaskFailure body, kept only
// as the oracle for TestClassifyTaskFailureCoarseIsUnchanged.
func legacyCoarse(err error) string {
	if err == nil {
		return "task-failed"
	}

	message := strings.ToLower(err.Error())

	switch {
	case isBlockedFailure(message):
		return "blocked"
	case strings.Contains(message, "browser"), strings.Contains(message, "playwright"),
		strings.Contains(message, "target closed"), strings.Contains(message, "driver"),
		strings.Contains(message, "chromium"):
		return "browser-failure"
	case strings.Contains(message, "proxy"), strings.Contains(message, "socks"),
		strings.Contains(message, "tunnel"), strings.Contains(message, "407"):
		return "proxy-failure"
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "website-timeout"
	case strings.Contains(message, "parse"), strings.Contains(message, "unmarshal"),
		strings.Contains(message, "unexpected"):
		return "parsing-failure"
	default:
		return "task-failed"
	}
}
