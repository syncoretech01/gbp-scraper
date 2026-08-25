package webrunner

// Fine-grained failure classification (stream: harden/failure-classification).
//
// The coarse buckets returned by classifyTaskFailure — "blocked",
// "browser-failure", "proxy-failure", "website-timeout", "parsing-failure" and
// "task-failed" — drive control flow: the adaptive controller measures its
// block rate from "blocked", proxy attribution reacts to "proxy-failure" and
// "blocked", and taskFailureBackoff switches on all of them. Those buckets are
// deliberately generic, which hides the actionable root cause behind a single
// "browser-failure" line in the operator's log.
//
// This file adds a FINER kind alongside every coarse bucket without repurposing
// it. classifyFailureKind reproduces the exact legacy coarse decision (same
// substrings, same order — see coarseFromMessage) so the value fed to
// scheduling never changes, then refines the branch it landed in into a stable
// fine kind and, where useful, a named sub-signal. The coarse buckets stay the
// mapping layer other code depends on; the fine kind is purely additive and is
// surfaced on the worker event so an operator sees the real cause.

import (
	"context"
	"errors"
	"strings"
)

// Fine-grained failure kinds. Each is a stable string constant surfaced on the
// worker event's "failure_kind" field. coarseForKind maps every one of them
// onto exactly one legacy coarse bucket, and a kind is only ever emitted from
// the coarse branch it maps to, so the mapping is single-valued.
const (
	// FailureKindBrowserCrash is a Chromium process that died mid-task: a
	// closed target, a crashed page, a lost driver connection. Under the
	// --single-process launch flag one tab crash takes the whole browser down,
	// which is the load-dependent failure this run hit.
	FailureKindBrowserCrash = "browser-crash"
	// FailureKindBrowserContextFailure is a browser that is alive but could not
	// hand out a usable context or page (context/page creation failed).
	FailureKindBrowserContextFailure = "browser-context-failure"
	// FailureKindEngineShutdownTimeout is a scrape engine that never returned
	// after its context was cancelled, because the upstream browser teardown
	// blocks on a browser that has stopped answering. The task abandons the
	// engine rather than waiting, keeping its rows and staying resumable.
	FailureKindEngineShutdownTimeout = "engine-shutdown-timeout"
	// FailureKindNavigationFailure is a page navigation that failed for a
	// reason that is not a timeout and not a recognised network fault
	// (net::ERR_ABORTED, net::ERR_FAILED, a detached frame).
	FailureKindNavigationFailure = "navigation-failure"
	// FailureKindNavigationTimeout is a navigation or task deadline that
	// elapsed before the page settled.
	FailureKindNavigationTimeout = "navigation-timeout"
	// FailureKindGoogleBlock is a refusal served by Google itself: a consent
	// wall, an unusual-traffic interstitial, a /sorry/ redirect, a CAPTCHA, or
	// an HTTP 403. The specific wall is carried in the sub-signal.
	FailureKindGoogleBlock = "google-block"
	// FailureKindRateLimit is an explicit throttling response (HTTP 429 or a
	// "too many requests" body).
	FailureKindRateLimit = "rate-limit"
	// FailureKindProxyConnect is a failure reaching or tunnelling through the
	// configured proxy.
	FailureKindProxyConnect = "proxy-connect"
	// FailureKindProxyAuth is a proxy that rejected the supplied credentials
	// (HTTP 407 / proxy authentication required).
	FailureKindProxyAuth = "proxy-auth"
	// FailureKindNetworkDNS is a name-resolution failure (no such host,
	// net::ERR_NAME_NOT_RESOLVED).
	FailureKindNetworkDNS = "network-dns"
	// FailureKindNetworkTLS is a TLS/certificate failure (x509, bad handshake,
	// net::ERR_CERT_*).
	FailureKindNetworkTLS = "network-tls"
	// FailureKindNetworkRefused is a transport-level refusal or reset
	// (connection refused/reset, no route to host, network unreachable).
	FailureKindNetworkRefused = "network-refused"
	// FailureKindOperatorCancelled is a task cut short by cancellation rather
	// than by a fault (context.Canceled). It normally reaches the operator via
	// the interrupted-task path; classifying it here keeps a stray
	// cancellation out of the "unknown" bucket.
	FailureKindOperatorCancelled = "operator-cancelled"
	// FailureKindResourcePressure is a local host limit: out of memory, out of
	// disk, too many open files, cannot fork.
	FailureKindResourcePressure = "resource-pressure"
	// FailureKindParsingFailure is a response that arrived but could not be
	// decoded into listings.
	FailureKindParsingFailure = "parsing-failure"
	// FailureKindUnknown is an attempt error that matched no signature.
	FailureKindUnknown = "unknown"
)

// Google-block sub-signals name which wall Google served, so the operator can
// tell a consent redirect (fixable by locale/cookies) from a CAPTCHA
// (concurrency too high) from a hard 403.
const (
	googleBlockSignalCaptcha        = "captcha"
	googleBlockSignalUnusualTraffic = "unusual-traffic"
	googleBlockSignalSorryRedirect  = "sorry-redirect"
	googleBlockSignalConsent        = "consent"
	googleBlockSignalForbidden      = "http-403"
	googleBlockSignalAccessDenied   = "access-denied"
	googleBlockSignalGeneric        = "generic-block"

	rateLimitSignalHTTP429 = "http-429"
	rateLimitSignalTooMany = "too-many-requests"
)

// Coarse buckets. These are the exact string values classifyTaskFailure has
// always returned; other code (task_pool scheduling, taskFailureBackoff, the
// web log-level map) keys off them, so they must never change.
const (
	coarseBlocked        = "blocked"
	coarseBrowserFailure = "browser-failure"
	coarseProxyFailure   = "proxy-failure"
	coarseWebsiteTimeout = "website-timeout"
	coarseParsingFailure = "parsing-failure"
	coarseTaskFailed     = "task-failed"
)

// failureClassification is the full result of inspecting one attempt error: the
// coarse bucket control flow acts on, the fine kind an operator reads, and an
// optional sub-signal that names the exact marker within the fine kind.
type failureClassification struct {
	// Coarse is one of the six legacy buckets. It is what classifyTaskFailure
	// returns and is byte-for-byte identical to the pre-refinement value.
	Coarse string
	// Fine is one of the FailureKind* constants.
	Fine string
	// Signal is an optional finer marker (e.g. a google-block wall). It is
	// empty when the fine kind needs no further discrimination.
	Signal string
}

// annotate copies the classification onto an event context map so the operator
// and the lead's forensic tooling see the fine cause and its sub-signal beside
// the coarse bucket. It never overwrites keys the caller has already set except
// the three it owns.
func (c failureClassification) annotate(fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}

	fields["failure_kind"] = c.Fine
	fields["failure_class"] = c.Coarse

	if c.Signal != "" {
		fields["failure_signal"] = c.Signal
	}

	return fields
}

// coarseForKind maps a fine kind onto the single legacy coarse bucket it is
// always emitted under. It is the documented "coarse mapping layer": every kind
// classifyFailureKind can produce appears here, and the classifier only ever
// emits a kind from the matching coarse branch, so coarseForKind(c.Fine) always
// equals c.Coarse. A kind that is not listed falls back to task-failed, which
// is the neutral bucket.
func coarseForKind(fine string) string {
	switch fine {
	case FailureKindGoogleBlock, FailureKindRateLimit:
		return coarseBlocked
	case FailureKindBrowserCrash, FailureKindBrowserContextFailure, FailureKindEngineShutdownTimeout:
		return coarseBrowserFailure
	case FailureKindProxyConnect, FailureKindProxyAuth:
		return coarseProxyFailure
	case FailureKindNavigationTimeout:
		return coarseWebsiteTimeout
	case FailureKindParsingFailure:
		return coarseParsingFailure
	default:
		// navigation-failure, network-dns, network-tls, network-refused,
		// operator-cancelled, resource-pressure and unknown all live in the
		// legacy else branch.
		return coarseTaskFailed
	}
}

// classifyFailureKind inspects one attempt error and returns its coarse bucket,
// fine kind and optional sub-signal. The coarse decision reproduces the legacy
// classifyTaskFailure branch order exactly (block, browser, proxy, timeout,
// parse, else); the refinement only chooses a fine kind within the branch that
// already won, so it can never move an error into a different coarse bucket.
func classifyFailureKind(err error) failureClassification {
	if err == nil {
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindUnknown}
	}

	// A wedged engine shutdown is recognised by identity, not by text: it is
	// our own sentinel, and its coarse bucket stays browser-failure so every
	// existing scheduling decision behaves exactly as before.
	if errors.Is(err, errEngineShutdownTimeout) {
		return failureClassification{
			Coarse: coarseBrowserFailure,
			Fine:   FailureKindEngineShutdownTimeout,
			Signal: "browser-teardown-blocked",
		}
	}

	message := strings.ToLower(err.Error())

	switch {
	case isBlockedFailure(message):
		return refineBlocked(message)
	case matchesBrowserFailure(message):
		return refineBrowser(message)
	case matchesProxyFailure(message):
		return refineProxy(message)
	case matchesWebsiteTimeout(message):
		return failureClassification{Coarse: coarseWebsiteTimeout, Fine: FailureKindNavigationTimeout, Signal: timeoutSignal(message)}
	case matchesParsingFailure(message):
		return failureClassification{Coarse: coarseParsingFailure, Fine: FailureKindParsingFailure, Signal: parsingSignal(message)}
	default:
		return refineTaskFailed(err, message)
	}
}

// The branch predicates below are a faithful copy of the legacy
// classifyTaskFailure switch. Keeping them in one place guarantees the coarse
// decision never drifts from the value scheduling depends on.

func matchesBrowserFailure(message string) bool {
	return strings.Contains(message, "browser") ||
		strings.Contains(message, "playwright") ||
		strings.Contains(message, "target closed") ||
		strings.Contains(message, "driver") ||
		strings.Contains(message, "chromium")
}

func matchesProxyFailure(message string) bool {
	return strings.Contains(message, "proxy") ||
		strings.Contains(message, "socks") ||
		strings.Contains(message, "tunnel") ||
		strings.Contains(message, "407")
}

func matchesWebsiteTimeout(message string) bool {
	return strings.Contains(message, "timeout") || strings.Contains(message, "deadline")
}

func matchesParsingFailure(message string) bool {
	return strings.Contains(message, "parse") ||
		strings.Contains(message, "unmarshal") ||
		strings.Contains(message, "unexpected")
}

// refineBlocked splits a platform refusal into an explicit rate limit and the
// various Google walls, naming which wall was served.
func refineBlocked(message string) failureClassification {
	switch {
	case strings.Contains(message, "429"):
		return failureClassification{Coarse: coarseBlocked, Fine: FailureKindRateLimit, Signal: rateLimitSignalHTTP429}
	case strings.Contains(message, "too many requests"):
		return failureClassification{Coarse: coarseBlocked, Fine: FailureKindRateLimit, Signal: rateLimitSignalTooMany}
	case strings.Contains(message, "rate limit"), strings.Contains(message, "rate-limit"), strings.Contains(message, "ratelimit"):
		return failureClassification{Coarse: coarseBlocked, Fine: FailureKindRateLimit, Signal: rateLimitSignalTooMany}
	}

	return failureClassification{Coarse: coarseBlocked, Fine: FailureKindGoogleBlock, Signal: googleBlockSignal(message)}
}

// googleBlockSignal names the most specific wall present, most specific first.
func googleBlockSignal(message string) string {
	switch {
	case strings.Contains(message, "captcha"), strings.Contains(message, "recaptcha"):
		return googleBlockSignalCaptcha
	case strings.Contains(message, "unusual traffic"):
		return googleBlockSignalUnusualTraffic
	case strings.Contains(message, "/sorry/"), strings.Contains(message, "sorry/index"):
		return googleBlockSignalSorryRedirect
	case strings.Contains(message, "consent"):
		return googleBlockSignalConsent
	case strings.Contains(message, "403"):
		return googleBlockSignalForbidden
	case strings.Contains(message, "access denied"), strings.Contains(message, "temporarily blocked"), strings.Contains(message, "blocked by"):
		return googleBlockSignalAccessDenied
	default:
		return googleBlockSignalGeneric
	}
}

// refineBrowser splits a browser-branch error into a crash of the underlying
// process and a failure to create a context or page from a live browser.
func refineBrowser(message string) failureClassification {
	if isContextCreationFailure(message) {
		return failureClassification{Coarse: coarseBrowserFailure, Fine: FailureKindBrowserContextFailure, Signal: browserContextSignal(message)}
	}

	return failureClassification{Coarse: coarseBrowserFailure, Fine: FailureKindBrowserCrash, Signal: browserCrashSignal(message)}
}

func isContextCreationFailure(message string) bool {
	return strings.Contains(message, "newcontext") ||
		strings.Contains(message, "new context") ||
		strings.Contains(message, "create context") ||
		strings.Contains(message, "context creation") ||
		strings.Contains(message, "creating context") ||
		strings.Contains(message, "failed to create") ||
		strings.Contains(message, "newpage") ||
		strings.Contains(message, "new page")
}

func browserContextSignal(message string) string {
	switch {
	case strings.Contains(message, "newpage"), strings.Contains(message, "new page"):
		return "new-page"
	default:
		return "new-context"
	}
}

func browserCrashSignal(message string) string {
	switch {
	case strings.Contains(message, "target page, context or browser has been closed"):
		return "target-closed"
	case strings.Contains(message, "target closed"):
		return "target-closed"
	case strings.Contains(message, "page crashed"), strings.Contains(message, "crash"):
		return "page-crashed"
	case strings.Contains(message, "single-process"):
		return "single-process"
	case strings.Contains(message, "has been closed"), strings.Contains(message, "browser closed"), strings.Contains(message, "connection closed"):
		return "browser-closed"
	case strings.Contains(message, "disconnected"), strings.Contains(message, "lost connection"):
		return "disconnected"
	case strings.Contains(message, "exited"), strings.Contains(message, "signal"):
		return "process-exited"
	default:
		return "browser-error"
	}
}

// refineProxy separates a credential rejection from a connection failure.
func refineProxy(message string) failureClassification {
	if strings.Contains(message, "407") ||
		strings.Contains(message, "proxy authentication") ||
		strings.Contains(message, "authentication required") ||
		strings.Contains(message, "auth failed") {
		return failureClassification{Coarse: coarseProxyFailure, Fine: FailureKindProxyAuth, Signal: "proxy-auth"}
	}

	return failureClassification{Coarse: coarseProxyFailure, Fine: FailureKindProxyConnect, Signal: "proxy-connect"}
}

func timeoutSignal(message string) string {
	switch {
	case strings.Contains(message, "navigat"), strings.Contains(message, "goto"):
		return "navigation"
	case strings.Contains(message, "inactiv"):
		return "inactivity"
	case strings.Contains(message, "deadline"):
		return "deadline"
	default:
		return "timeout"
	}
}

func parsingSignal(message string) string {
	switch {
	case strings.Contains(message, "unmarshal"):
		return "unmarshal"
	case strings.Contains(message, "parse"):
		return "parse"
	default:
		return "unexpected"
	}
}

// refineTaskFailed inspects the legacy else branch, where the coarse bucket is
// always task-failed, and pulls out network faults, resource pressure and
// cancellation before falling back to a generic navigation failure or unknown.
// Every kind here maps to task-failed, so the refinement cannot change control
// flow.
func refineTaskFailed(err error, message string) failureClassification {
	switch {
	case isOperatorCancelled(err, message):
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindOperatorCancelled, Signal: "cancelled"}
	case isDNSFailure(message):
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindNetworkDNS, Signal: dnsSignal(message)}
	case isTLSFailure(message):
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindNetworkTLS, Signal: tlsSignal(message)}
	case isRefusedFailure(message):
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindNetworkRefused, Signal: refusedSignal(message)}
	case isResourcePressure(message):
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindResourcePressure, Signal: resourceSignal(message)}
	case isNavigationFailure(message):
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindNavigationFailure, Signal: "navigation"}
	default:
		return failureClassification{Coarse: coarseTaskFailed, Fine: FailureKindUnknown}
	}
}

func isOperatorCancelled(err error, message string) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}

	return strings.Contains(message, "context canceled") ||
		strings.Contains(message, "context cancelled") ||
		strings.Contains(message, "operation was canceled") ||
		strings.Contains(message, "operation was cancelled") ||
		strings.Contains(message, "request cancelled")
}

func isDNSFailure(message string) bool {
	return strings.Contains(message, "no such host") ||
		strings.Contains(message, "err_name_not_resolved") ||
		strings.Contains(message, "name resolution") ||
		strings.Contains(message, "could not resolve") ||
		strings.Contains(message, "server misbehaving") ||
		strings.Contains(message, "dns")
}

func dnsSignal(message string) string {
	if strings.Contains(message, "err_name_not_resolved") {
		return "err-name-not-resolved"
	}

	return "no-such-host"
}

func isTLSFailure(message string) bool {
	return strings.Contains(message, "x509") ||
		strings.Contains(message, "certificate") ||
		strings.Contains(message, "tls") ||
		strings.Contains(message, "err_cert") ||
		strings.Contains(message, "err_ssl") ||
		strings.Contains(message, "ssl handshake")
}

func tlsSignal(message string) string {
	switch {
	case strings.Contains(message, "x509"), strings.Contains(message, "certificate"), strings.Contains(message, "err_cert"):
		return "certificate"
	default:
		return "tls-handshake"
	}
}

func isRefusedFailure(message string) bool {
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "econnrefused") ||
		strings.Contains(message, "err_connection_refused") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "err_connection_reset") ||
		strings.Contains(message, "err_connection_closed") ||
		strings.Contains(message, "no route to host") ||
		strings.Contains(message, "network is unreachable") ||
		strings.Contains(message, "err_internet_disconnected")
}

func refusedSignal(message string) string {
	switch {
	case strings.Contains(message, "reset"):
		return "connection-reset"
	case strings.Contains(message, "no route to host"), strings.Contains(message, "network is unreachable"), strings.Contains(message, "err_internet_disconnected"):
		return "unreachable"
	default:
		return "connection-refused"
	}
}

func isResourcePressure(message string) bool {
	return strings.Contains(message, "cannot allocate memory") ||
		strings.Contains(message, "out of memory") ||
		strings.Contains(message, "oom") ||
		strings.Contains(message, "no space left") ||
		strings.Contains(message, "too many open files") ||
		strings.Contains(message, "resource temporarily unavailable") ||
		strings.Contains(message, "cannot fork") ||
		strings.Contains(message, "insufficient resources")
}

func resourceSignal(message string) string {
	switch {
	case strings.Contains(message, "no space left"):
		return "disk-full"
	case strings.Contains(message, "too many open files"):
		return "file-descriptors"
	case strings.Contains(message, "fork"):
		return "cannot-fork"
	default:
		return "memory"
	}
}

func isNavigationFailure(message string) bool {
	return strings.Contains(message, "net::err") ||
		strings.Contains(message, "navigation") ||
		strings.Contains(message, "goto") ||
		strings.Contains(message, "frame was detached") ||
		strings.Contains(message, "navigating")
}
