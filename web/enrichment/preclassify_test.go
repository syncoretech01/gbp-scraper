//nolint:testpackage // Package-internal tests cover the single-page probe.
package enrichment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPreclassifyUpgradesToHTTPSAndStaysSinglePage(t *testing.T) {
	t.Parallel()

	var contactRequests atomic.Int32

	handler := http.NewServeMux()
	handler.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(response, `<!doctype html>
<html lang="en"><head><title>Acme Plumbing</title></head><body>
<a href="/contact">Contact</a>
<a href="mailto:info@example.com">Email us</a>
<p>Family plumbing since 1998. Call our office today.</p>
</body></html>`)
	})
	handler.HandleFunc("/contact", func(response http.ResponseWriter, _ *http.Request) {
		contactRequests.Add(1)
		fmt.Fprint(response, "<html><body>contact page</body></html>")
	})

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	config := Config{
		HTTPClient:                server.Client(),
		UnsafeAllowPrivateNetwork: true,
		// Every heavyweight knob below must be coerced away by the probe.
		Scope:    ScopeContactAbout,
		MaxPages: 7,
		CheckMX:  true,
		MXLookup: MXLookupFunc(func(context.Context, string) ([]*net.MX, error) {
			t.Errorf("MX lookup must not run during pre-classification")
			return nil, errors.New("unexpected MX lookup")
		}),
	}

	result, err := Preclassify(context.Background(), strings.TrimPrefix(server.URL, "https://"), config)
	if err != nil {
		t.Fatalf("Preclassify() error = %v", err)
	}

	if !result.Reachable || result.StatusCode != http.StatusOK || result.Error != "" {
		t.Fatalf("reachability = %#v", result)
	}

	if !result.HTTPS || !result.TLSValid || result.CertificateError != "" {
		t.Fatalf("TLS signals = HTTPS %v TLSValid %v certificate %q", result.HTTPS, result.TLSValid, result.CertificateError)
	}

	if len(result.Pages) != 1 || result.InternalLinksChecked != 0 || contactRequests.Load() != 0 {
		t.Fatalf("probe was not single-page: pages=%d links=%d contact=%d",
			len(result.Pages), result.InternalLinksChecked, contactRequests.Load())
	}

	email := emailByAddress(t, result.Emails, "info@example.com")
	if email.MXStatus != MXNotChecked {
		t.Fatalf("email MX status = %q, want %q", email.MXStatus, MXNotChecked)
	}

	direct, err := Preclassify(context.Background(), server.URL, config)
	if err != nil || !direct.HTTPS || !direct.TLSValid || !direct.Reachable {
		t.Fatalf("explicit https probe = %#v, %v", direct, err)
	}
}

func TestPreclassifyRecordsCertificateErrorAndFallsBackToHTTP(t *testing.T) {
	t.Parallel()

	tlsServer := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(response, "<html><body>secure</body></html>")
	}))
	tlsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsServer.StartTLS()
	t.Cleanup(tlsServer.Close)

	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Acme</title></head><body><p>Open six days a week.</p></body></html>`)
	}))
	t.Cleanup(httpServer.Close)

	// The client does not trust the test certificate, so the https attempt
	// fails at the TLS layer while the http attempt succeeds.
	config := Config{
		Resolver:   publicProbeResolver(),
		HTTPClient: &http.Client{Transport: portRoutingTransport(tlsServer, httpServer)},
	}

	result, err := Preclassify(context.Background(), "http://site.test", config)
	if err != nil {
		t.Fatalf("Preclassify() error = %v", err)
	}

	if result.CertificateError == "" {
		t.Fatalf("certificate error was not recorded: %#v", result)
	}

	if !result.Reachable || result.StatusCode != http.StatusOK || result.Error != "" {
		t.Fatalf("http fallback reachability = %#v", result)
	}

	if result.HTTPS || result.TLSValid || !strings.HasPrefix(result.FinalURL, "http://") {
		t.Fatalf("fallback fetch was not plain http: %#v", result)
	}
}

func TestPreclassifyReportsHTTPOnlyWebsite(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Acme</title></head><body><p>Serving the whole valley.</p></body></html>`)
	}))
	t.Cleanup(httpServer.Close)

	// Port 443 refuses the connection below the TLS layer; port 80 works.
	config := Config{
		Resolver:   publicProbeResolver(),
		HTTPClient: &http.Client{Transport: portRoutingTransport(nil, httpServer)},
	}

	result, err := Preclassify(context.Background(), "site.test", config)
	if err != nil {
		t.Fatalf("Preclassify() error = %v", err)
	}

	if !result.Reachable || result.StatusCode != http.StatusOK || result.Error != "" {
		t.Fatalf("http reachability = %#v", result)
	}

	if result.HTTPS || result.TLSValid || result.CertificateError != "" {
		t.Fatalf("https signals must stay clear: %#v", result)
	}
}

func TestPreclassifyReportsDNSFailureAsUnreachable(t *testing.T) {
	t.Parallel()

	config := Config{
		Resolver: ResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}),
	}

	result, err := Preclassify(context.Background(), "http://missing.example", config)
	if err != nil {
		t.Fatalf("Preclassify() error = %v", err)
	}

	if result.Reachable || result.Error == "" || !strings.Contains(result.Error, "no such host") {
		t.Fatalf("DNS failure result = %#v", result)
	}

	if result.CertificateError != "" {
		t.Fatalf("DNS failure must not fabricate a certificate error: %#v", result)
	}
}

func TestPreclassifyDetectsParkedHomepage(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		fmt.Fprint(response, `<html><head><title>Parked</title></head><body>
<p>Buy this domain today. This domain has expired.</p></body></html>`)
	}))
	t.Cleanup(server.Close)

	config := Config{HTTPClient: server.Client(), UnsafeAllowPrivateNetwork: true}

	result, err := Preclassify(context.Background(), strings.TrimPrefix(server.URL, "https://"), config)
	if err != nil {
		t.Fatalf("Preclassify() error = %v", err)
	}

	if !result.Reachable || !result.HTTPS || !result.Parked {
		t.Fatalf("parked probe = %#v", result)
	}
}

func TestPreclassifyRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	if _, err := Preclassify(context.Background(), "http://169.254.169.254/latest", Config{}); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("link-local target error = %v, want ErrUnsafeURL", err)
	}

	privateResolver := ResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.168.1.2")}}, nil
	})
	if _, err := Preclassify(
		context.Background(),
		"https://private.example",
		Config{Resolver: privateResolver},
	); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("private DNS target error = %v, want ErrUnsafeURL", err)
	}
}

// publicProbeResolver resolves every host to a public address so URL safety
// validation passes while the test transport controls the actual dial.
func publicProbeResolver() Resolver {
	return ResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	})
}

// portRoutingTransport routes https (port 443) dials to tlsServer and http
// (port 80) dials to httpServer. A nil tlsServer refuses https connections
// below the TLS layer, simulating a host with no https listener at all.
func portRoutingTransport(tlsServer, httpServer *httptest.Server) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("split dial address: %w", err)
			}

			switch port {
			case "443":
				if tlsServer == nil {
					return nil, errors.New("connect: connection refused")
				}

				return (&net.Dialer{}).DialContext(ctx, network, tlsServer.Listener.Addr().String())
			case "80":
				return (&net.Dialer{}).DialContext(ctx, network, httpServer.Listener.Addr().String())
			default:
				return nil, fmt.Errorf("unexpected dial port %q", port)
			}
		},
	}
}
