//nolint:testpackage // Package-internal tests verify the guarded dial path against rebinding.
package enrichment

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestURLGuardRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	resolver := ResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "public.example":
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		case "mixed.example":
			return []net.IPAddr{
				{IP: net.ParseIP("8.8.8.8")},
				{IP: net.ParseIP("10.0.0.2")},
			}, nil
		default:
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.2")}}, nil
		}
	})
	guard := URLGuard{Resolver: resolver}

	tests := []struct {
		name      string
		rawURL    string
		expected  error
		wantValid bool
	}{
		{name: "public", rawURL: "https://public.example/path#fragment", wantValid: true},
		{name: "private dns", rawURL: "http://private.example", expected: ErrUnsafeURL},
		{name: "mixed dns", rawURL: "https://mixed.example", expected: ErrUnsafeURL},
		{name: "loopback literal", rawURL: "http://127.0.0.1", expected: ErrUnsafeURL},
		{name: "link local metadata", rawURL: "http://169.254.169.254/latest", expected: ErrUnsafeURL},
		{name: "localhost", rawURL: "http://service.localhost", expected: ErrUnsafeURL},
		{name: "credentials", rawURL: "https://user:pass@public.example", expected: ErrUnsafeURL},
		{name: "unsupported scheme", rawURL: "file:///etc/passwd", expected: ErrUnsupportedScheme},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parsedURL, err := guard.ValidateURL(context.Background(), test.rawURL)
			if test.wantValid {
				if err != nil {
					t.Fatalf("ValidateURL() error = %v", err)
				}

				if parsedURL.Fragment != "" {
					t.Fatalf("fragment was not removed: %q", parsedURL.Fragment)
				}

				return
			}

			if !errors.Is(err, test.expected) {
				t.Fatalf("ValidateURL() error = %v, want %v", err, test.expected)
			}
		})
	}
}

func TestURLGuardPrivateNetworkOverrideIsExplicit(t *testing.T) {
	t.Parallel()

	guard := URLGuard{UnsafeAllowPrivateNetwork: true}
	if _, err := guard.ValidateURL(context.Background(), "http://127.0.0.1:8080"); err != nil {
		t.Fatalf("ValidateURL() with override error = %v", err)
	}

	if _, err := guard.ValidateURL(context.Background(), "http://0.0.0.0:8080"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("unspecified address error = %v, want ErrUnsafeURL", err)
	}
}

func TestURLGuardDialRejectsDNSRebindingBeforeConnecting(t *testing.T) {
	t.Parallel()

	var lookups int

	guard := URLGuard{Resolver: ResolverFunc(func(_ context.Context, _ string) ([]net.IPAddr, error) {
		lookups++
		if lookups == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{
			{IP: net.ParseIP("8.8.8.8")},
			{IP: net.ParseIP("127.0.0.1")},
		}, nil
	})}

	connection, err := guard.dialContext(context.Background(), "tcp", "rebind.example:80")
	if connection != nil {
		connection.Close()
		t.Fatal("dial unexpectedly returned a connection")
	}

	if !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("dial error = %v, want ErrUnsafeURL", err)
	}
}
