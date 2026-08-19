//nolint:testpackage // Package-internal tests cover bounded HTML signal extraction.
package enrichment

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCrawlerAnalyzesBoundedWebsiteAndContacts(t *testing.T) {
	t.Parallel()

	handler := http.NewServeMux()
	handler.HandleFunc("/start", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/", http.StatusMovedPermanently)
	})
	handler.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(response, `<!doctype html>
<html lang="en"><head>
<title>Acme Dental</title>
<meta name="description" content="Friendly local dentists">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="/wp-content/site.css">
</head><body>
<a href="/contact">Contact us</a><a href="/about">About</a><a href="/broken">Broken</a>
<a href="mailto:Info@Example.com">info@example.com</a>
<p>Personal: jane.doe@example.com</p><p>Sales: sales [at] example [dot] com</p>
<a href="tel:+1 (415) 555-0100">Call</a>
<a href="https://www.facebook.com/acme-dental">Facebook</a>
<script id="__NEXT_DATA__" type="application/json">{"buildId":"abc"}</script>
<script src="https://www.googletagmanager.com/gtm.js?id=GTM-TEST"></script>
<script type="application/ld+json">{"email":"structured@example.com","telephone":"+1 415 555 0101"}</script>
</body></html>`)
	})
	handler.HandleFunc("/contact", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Contact Acme</title></head><body>
<p>info@example.com</p><a href="mailto:contact@example.com">Email our office</a>
</body></html>`)
	})
	handler.HandleFunc("/about", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>About Acme</title></head><body>
<a href="https://linkedin.com/company/acme-dental">LinkedIn</a>
<p>Lorem ipsum dolor sit amet. Copyright 2018 Acme.</p>
</body></html>`)
	})
	handler.HandleFunc("/broken", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	lookup := MXLookupFunc(func(_ context.Context, domain string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx." + domain + ".", Pref: 10}}, nil
	})

	crawler, err := NewCrawler(Config{
		Timeout:                   time.Second,
		MaxPages:                  3,
		MaxBodyBytes:              128 * 1024,
		MaxInternalLinkChecks:     5,
		Scope:                     ScopeContactAbout,
		CheckMX:                   true,
		MXLookup:                  lookup,
		UnsafeAllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	result, err := crawler.Analyze(context.Background(), server.URL+"/start")
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if !result.Reachable || result.StatusCode != http.StatusOK || result.FinalURL != server.URL+"/" {
		t.Fatalf("reachability = %#v", result)
	}

	if len(result.RedirectChain) != 1 || result.RedirectChain[0].StatusCode != http.StatusMovedPermanently {
		t.Fatalf("redirect chain = %#v", result.RedirectChain)
	}

	if len(result.Pages) != 3 {
		t.Fatalf("pages = %d, want 3: %#v", len(result.Pages), result.Pages)
	}

	if result.Pages[0].Title != "Acme Dental" || result.Pages[0].MetaDescription != "Friendly local dentists" ||
		result.Pages[0].Language != "en" || !result.Pages[0].MobileViewport {
		t.Fatalf("homepage metadata = %#v", result.Pages[0])
	}

	info := emailByAddress(t, result.Emails, "info@example.com")
	if !sourceMethodPresent(info.Sources, MethodMailto) || !sourceMethodPresent(info.Sources, MethodVisibleText) {
		t.Fatalf("info source union = %#v", info.Sources)
	}

	emailByAddress(t, result.Emails, "sales@example.com")
	emailByAddress(t, result.Emails, "structured@example.com")

	contact := emailByAddress(t, result.Emails, "contact@example.com")
	if contact.MXStatus != MXPresent || contact.Rank < 1 {
		t.Fatalf("contact assessment = %#v", contact)
	}

	if len(result.Phones) != 2 || result.Phones[0].Value != "+14155550100" {
		t.Fatalf("phones = %#v", result.Phones)
	}

	if !socialPresent(result.SocialProfiles, "facebook") || !socialPresent(result.SocialProfiles, "linkedin") {
		t.Fatalf("social profiles = %#v", result.SocialProfiles)
	}

	if !detectionPresent(result.Technologies, "Next.js") || !detectionPresent(result.Technologies, "WordPress") {
		t.Fatalf("technologies = %#v", result.Technologies)
	}

	if !detectionPresent(result.Trackers, "Google Tag Manager") {
		t.Fatalf("trackers = %#v", result.Trackers)
	}

	if !result.Placeholder || len(result.TemplateIndicators) == 0 {
		t.Fatalf("placeholder signals = %v %#v", result.Placeholder, result.TemplateIndicators)
	}

	if result.InternalLinksChecked != 1 || result.BrokenInternalLinkCount != 1 ||
		len(result.BrokenInternalLinks) != 1 || result.BrokenInternalLinks[0].StatusCode != http.StatusNotFound {
		t.Fatalf("broken link analysis = %#v", result)
	}
}

func TestCrawlerHonorsPageBodyAndTimeoutCaps(t *testing.T) {
	t.Parallel()

	var contactRequests atomic.Int32

	handler := http.NewServeMux()
	handler.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(response, `<html><body><a href="/contact">Contact</a>%s</body></html>`, strings.Repeat("x", 512))
	})
	handler.HandleFunc("/contact", func(response http.ResponseWriter, _ *http.Request) {
		contactRequests.Add(1)
		fmt.Fprint(response, "<html><body>contact@example.com</body></html>")
	})
	handler.HandleFunc("/slow", func(response http.ResponseWriter, _ *http.Request) {
		time.Sleep(75 * time.Millisecond)
		fmt.Fprint(response, "<html><body>late</body></html>")
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	crawler, err := NewCrawler(Config{
		Timeout:                   time.Second,
		MaxPages:                  1,
		MaxBodyBytes:              64,
		Scope:                     ScopeContactAbout,
		DisableInternalLinkChecks: true,
		UnsafeAllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	result, err := crawler.Analyze(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if len(result.Pages) != 1 || !result.Pages[0].BodyTruncated || result.Pages[0].SizeBytes != 64 {
		t.Fatalf("bounded page = %#v", result.Pages)
	}

	if contactRequests.Load() != 0 {
		t.Fatalf("contact requests = %d, want 0", contactRequests.Load())
	}

	timeoutCrawler, err := NewCrawler(Config{
		Timeout:                   20 * time.Millisecond,
		Scope:                     ScopeHomepage,
		DisableInternalLinkChecks: true,
		UnsafeAllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("NewCrawler() timeout config error = %v", err)
	}

	timedResult, err := timeoutCrawler.Analyze(context.Background(), server.URL+"/slow")
	if err != nil {
		t.Fatalf("timeout Analyze() error = %v", err)
	}

	if timedResult.Reachable || timedResult.Error == "" {
		t.Fatalf("timeout result = %#v", timedResult)
	}
}

func TestCrawlerRejectsUnsafeRedirect(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(response, request, "http://169.254.169.254/latest", http.StatusFound)
			return
		}

		response.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	resolver := ResolverFunc(func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	})

	crawler, err := NewCrawler(Config{
		Resolver:                  resolver,
		HTTPClient:                &http.Client{Transport: transport},
		Scope:                     ScopeHomepage,
		DisableInternalLinkChecks: true,
	})
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	_, err = crawler.Analyze(context.Background(), "http://public.example/start")
	if !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("Analyze() unsafe redirect error = %v, want ErrUnsafeURL", err)
	}
}

func TestExtractPageDetectsMixedContentSignaturesAndStatusHeuristics(t *testing.T) {
	t.Parallel()

	body := []byte(`<html><head><meta name="generator" content="Wix"><meta name="viewport" content="width=device-width"></head>
<body><img src="http://assets.example.com/logo.png"><script src="https://connect.facebook.net/en_US/fbevents.js"></script>
<p>Coming soon. Buy this domain. Replace this text. Copyright 2017.</p></body></html>`)

	extracted, err := extractPage(body, "https://example.com/", PageHomepage)
	if err != nil {
		t.Fatalf("extractPage() error = %v", err)
	}

	if !extracted.page.MixedContent || !extracted.page.MobileViewport || !extracted.page.OldCopyright {
		t.Fatalf("page quality signals = %#v", extracted.page)
	}

	if !extracted.parked || !extracted.comingSoon || !extracted.placeholder {
		t.Fatalf("status signals parked=%v coming=%v placeholder=%v", extracted.parked, extracted.comingSoon, extracted.placeholder)
	}

	if !detectionPresent(extracted.technologies, "Wix") || !detectionPresent(extracted.trackers, "Meta Pixel") {
		t.Fatalf("signature results technologies=%#v trackers=%#v", extracted.technologies, extracted.trackers)
	}
}

func sourceMethodPresent(sources []Source, method ExtractionMethod) bool {
	for _, source := range sources {
		if source.Method == method {
			return true
		}
	}

	return false
}

func socialPresent(profiles []SocialProfile, platform string) bool {
	for _, profile := range profiles {
		if profile.Platform == platform {
			return true
		}
	}

	return false
}

func detectionPresent(detections []Detection, name string) bool {
	for _, detection := range detections {
		if detection.Name == name {
			return true
		}
	}

	return false
}
