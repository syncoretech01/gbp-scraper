//nolint:testpackage // Package-internal tests cover bounded crawl page selection.
package enrichment

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// patternTestConfig is the bounded, offline crawler configuration these tests
// share: a local server, no MX lookups, and a scope that would normally pick a
// contact and an about page.
func patternTestConfig(patterns URLPatternSet) Config {
	return Config{
		Timeout:                   2 * time.Second,
		MaxPages:                  4,
		MaxBodyBytes:              64 * 1024,
		MaxInternalLinkChecks:     10,
		Scope:                     ScopeContactAbout,
		UnsafeAllowPrivateNetwork: true,
		URLPatterns:               patterns,
	}
}

// TestCrawlerURLPatternsGovernSupportingPageSelection checks the clause the
// specification asks for: the operator's patterns decide which same-origin
// pages a bounded audit visits, and the run records what was applied.
func TestCrawlerURLPatternsGovernSupportingPageSelection(t *testing.T) {
	t.Parallel()

	var aboutRequests atomic.Int64

	handler := http.NewServeMux()
	handler.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Acme</title></head><body>
<a href="/contact">Contact</a><a href="/about">About</a>
</body></html>`)
	})
	handler.HandleFunc("/contact", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Contact</title></head><body>
<a href="mailto:hello@example.com">Email</a></body></html>`)
	})
	handler.HandleFunc("/about", func(response http.ResponseWriter, _ *http.Request) {
		aboutRequests.Add(1)
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>About</title></head><body>About us.</body></html>`)
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	crawler, err := NewCrawler(patternTestConfig(URLPatternSet{Exclude: []string{"/about*"}}))
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	result, err := crawler.Analyze(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	// An excluded path must never be requested at all: the page must not be
	// selected, and the bounded link-health probe must not reach it either.
	if got := aboutRequests.Load(); got != 0 {
		t.Fatalf("the excluded /about page was requested %d time(s); it must never be fetched", got)
	}

	var sawContact bool

	for _, page := range result.Pages {
		if page.Kind == PageContact {
			sawContact = true
		}

		if page.Kind == PageAbout {
			t.Fatalf("an excluded about page was still visited: %+v", page)
		}
	}

	if !sawContact {
		t.Fatal("excluding /about must not stop the contact page from being visited")
	}

	if result.URLPatterns == nil {
		t.Fatal("a filtered run must record its pattern evidence")
	}

	if !slices.Contains(result.URLPatterns.Exclude, "/about*") {
		t.Fatalf("evidence exclude = %v, want the configured pattern", result.URLPatterns.Exclude)
	}

	if result.URLPatterns.SkippedCount == 0 {
		t.Fatal("evidence must record that a candidate was kept out")
	}
}

// TestCrawlerWithoutURLPatternsKeepsHistoricalSelection pins the compatibility
// clause: an unset pattern set reproduces exactly the behaviour the crawler
// had before the control existed, and stores no evidence.
func TestCrawlerWithoutURLPatternsKeepsHistoricalSelection(t *testing.T) {
	t.Parallel()

	handler := http.NewServeMux()
	handler.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Acme</title></head><body>
<a href="/contact">Contact</a><a href="/about">About</a></body></html>`)
	})
	handler.HandleFunc("/contact", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Contact</title></head><body>Contact.</body></html>`)
	})
	handler.HandleFunc("/about", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>About</title></head><body>About.</body></html>`)
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	crawler, err := NewCrawler(patternTestConfig(URLPatternSet{}))
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	result, err := crawler.Analyze(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if result.URLPatterns != nil {
		t.Fatalf("an unfiltered run must record no pattern evidence, got %+v", result.URLPatterns)
	}

	kinds := make(map[PageKind]bool, len(result.Pages))
	for _, page := range result.Pages {
		kinds[page.Kind] = true
	}

	if !kinds[PageContact] || !kinds[PageAbout] {
		t.Fatalf("an unfiltered crawl must still visit both supporting pages, saw %v", kinds)
	}
}

// TestCrawlerURLPatternsApplyToRedirectTargets covers the second place the
// specification names: a supporting page whose redirect lands on an excluded
// path must not be followed, while the entry page's own redirect always is.
func TestCrawlerURLPatternsApplyToRedirectTargets(t *testing.T) {
	t.Parallel()

	var privateRequests atomic.Int64

	handler := http.NewServeMux()
	handler.HandleFunc("/start", func(response http.ResponseWriter, request *http.Request) {
		// The entry page redirects into a path the exclude list names. It must
		// still be followed, because refusing it would report a live site as
		// unreachable.
		http.Redirect(response, request, "/private/home", http.StatusFound)
	})
	handler.HandleFunc("/private/home", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Acme</title></head><body>
<a href="/contact">Contact</a></body></html>`)
	})
	handler.HandleFunc("/contact", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/private/contact", http.StatusFound)
	})
	handler.HandleFunc("/private/contact", func(response http.ResponseWriter, _ *http.Request) {
		privateRequests.Add(1)
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Private</title></head><body>Secret.</body></html>`)
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	crawler, err := NewCrawler(patternTestConfig(URLPatternSet{Exclude: []string{"/private/contact*"}}))
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	result, err := crawler.Analyze(context.Background(), server.URL+"/start")
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	// The entry page's redirect was followed despite landing under /private.
	if !result.Reachable {
		t.Fatal("the entry page's own redirect must always be followed")
	}

	if got := privateRequests.Load(); got != 0 {
		t.Fatalf("an excluded redirect target was fetched %d time(s)", got)
	}

	for _, page := range result.Pages {
		if page.Kind == PageContact && page.StatusCode == http.StatusOK {
			t.Fatalf("a supporting page redirecting to an excluded path must not be kept: %+v", page)
		}
	}

	if result.URLPatterns == nil || result.URLPatterns.SkippedCount == 0 {
		t.Fatalf("the refused redirect target must appear in the evidence, got %+v", result.URLPatterns)
	}
}
