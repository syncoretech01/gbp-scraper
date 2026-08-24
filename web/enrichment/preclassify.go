package enrichment

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Preclassify probe limits. The probe is deliberately tighter than the full
// crawler: a single page, a small body cap, and a short redirect budget.
const (
	preclassifyDefaultTimeout      = 10 * time.Second
	preclassifyMaximumTimeout      = 15 * time.Second
	preclassifyDefaultMaxBodyBytes = int64(256) << 10
	preclassifyMaximumMaxBodyBytes = int64(512) << 10
	preclassifyMaximumRedirects    = 5
)

// Preclassify performs one cheap single-page DNS/TLS/HTTP probe of rawURL so
// a prospect classifier can distinguish dead, TLS-broken, http-only, parked,
// and live websites without paying for a full crawl. Only the homepage is
// fetched: no supporting pages, no internal link checks, and no MX lookups,
// regardless of what cfg requests.
//
// A URL without a scheme, or with an http scheme, is first tried over https:
//
//   - The https variant answering yields its result directly, with HTTPS set
//     and TLSValid taken from the verified certificate chain.
//   - The https variant failing at the TLS layer records CertificateError and
//     falls back to one http fetch so reachability, parked, and content
//     signals stay honest.
//   - The https variant failing below the TLS layer (connect refused, reset)
//     falls back to one http fetch whose result reports HTTPS=false.
//
// An explicit https URL is probed exactly as declared with no downgrade
// attempt. A DNS resolution failure yields Reachable=false with the failure
// recorded in Error. Unsafe (private, loopback, reserved) targets are
// rejected with ErrUnsafeURL exactly like the full crawler.
func Preclassify(ctx context.Context, rawURL string, cfg Config) (Result, error) {
	crawler, err := NewCrawler(preclassifyConfig(cfg))
	if err != nil {
		return Result{}, err
	}

	trimmed := strings.TrimSpace(rawURL)
	scheme, remainder := splitProbeScheme(trimmed)

	if scheme != "" && scheme != schemeHTTP {
		// An explicit https URL is probed exactly as declared, with no
		// downgrade attempt. Any other explicit scheme is rejected by the
		// crawler's own URL guard.
		result, _, probeErr := probeOnce(ctx, crawler, trimmed)

		return result, probeErr
	}

	httpsResult, fetched, err := probeOnce(ctx, crawler, schemeHTTPS+"://"+remainder)
	if err != nil {
		return httpsResult, err
	}

	if httpsResult.Error == "" {
		// The https variant answered: HTTPS and TLSValid come from the
		// verified fetch itself.
		return httpsResult, nil
	}

	if !fetched {
		// DNS or another pre-fetch failure. The http variant shares the same
		// host, so a downgrade cannot succeed either: report the dead result.
		return httpsResult, nil
	}

	httpURL := trimmed
	if scheme == "" {
		httpURL = schemeHTTP + "://" + remainder
	}

	httpResult, _, err := probeOnce(ctx, crawler, httpURL)
	if err != nil {
		return httpResult, err
	}

	if httpsResult.CertificateError != "" {
		// The https variant failed at the TLS layer. Record the certificate
		// error while keeping the honest http reachability and content
		// signals; without it the http fallback would look like a plain
		// http-only site instead of one with a broken certificate.
		httpResult.CertificateError = httpsResult.CertificateError
	}

	return httpResult, nil
}

// preclassifyConfig coerces cfg to the bounded single-page probe profile
// regardless of what the caller supplied.
func preclassifyConfig(cfg Config) Config {
	cfg.Scope = ScopeHomepage
	cfg.MaxPages = 1
	cfg.DisableInternalLinkChecks = true
	cfg.MaxInternalLinkChecks = 0
	cfg.CheckMX = false
	// Crawl URL patterns are cleared, not honoured. The probe fetches the
	// homepage and nothing else: it selects no supporting page and checks no
	// internal link, so the two places patterns act have nothing to act on.
	// The one remaining candidate, the homepage's own redirect target, must be
	// followed for a reachability probe to mean anything — a site that
	// redirects "/" to "/home" is live, and reporting it dead because a
	// pattern did not name "/home" would be a false signal, not a filter.
	// Clearing the set also keeps the probe from recording pattern evidence
	// that claims a filter was applied when none was.
	cfg.URLPatterns = URLPatternSet{}

	if cfg.Timeout <= 0 {
		cfg.Timeout = preclassifyDefaultTimeout
	}

	if cfg.Timeout > preclassifyMaximumTimeout {
		cfg.Timeout = preclassifyMaximumTimeout
	}

	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = preclassifyDefaultMaxBodyBytes
	}

	if cfg.MaxBodyBytes > preclassifyMaximumMaxBodyBytes {
		cfg.MaxBodyBytes = preclassifyMaximumMaxBodyBytes
	}

	if cfg.MaxRedirects <= 0 || cfg.MaxRedirects > preclassifyMaximumRedirects {
		cfg.MaxRedirects = preclassifyMaximumRedirects
	}

	return cfg
}

// probeOnce performs one bounded homepage probe through the shared crawler
// machinery. fetched reports whether an HTTP request was actually attempted:
// false means the URL failed validation before any request (for example DNS
// resolution), in which case result records the failure honestly. Unsafe
// targets, unsupported schemes, and context cancellation propagate as errors.
func probeOnce(ctx context.Context, crawler *Crawler, probeURL string) (Result, bool, error) {
	result, err := crawler.Analyze(ctx, probeURL)
	if err == nil {
		return result, true, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, false, ctxErr
	}

	if errors.Is(err, ErrUnsafeURL) || errors.Is(err, ErrUnsupportedScheme) {
		return result, false, err
	}

	if result.RequestedURL == "" {
		result.RequestedURL = probeURL
	}

	if result.Error == "" {
		result.Error = err.Error()
	}

	return result, false, nil
}

// splitProbeScheme splits "scheme://rest" without treating a bare "host:port"
// as carrying a scheme. The scheme is returned lowercased; input without
// "://" yields an empty scheme and the input itself as remainder.
func splitProbeScheme(rawURL string) (scheme, remainder string) {
	const separator = "://"

	index := strings.Index(rawURL, separator)
	if index < 0 {
		return "", rawURL
	}

	return strings.ToLower(rawURL[:index]), rawURL[index+len(separator):]
}
