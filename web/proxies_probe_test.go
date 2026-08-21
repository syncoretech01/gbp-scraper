package web

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"testing"
	"time"
)

// mustParseProxyURL keeps the SOCKS5 tests readable.
func mustParseProxyURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse proxy URL %q: %v", raw, err)
	}

	return parsed
}

func TestGoogleCountryHintReadsTheRequestGoogleActuallyServed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		urls []string
		want string
	}{
		{
			name: "country domain in the final URL",
			urls: []string{"https://www.google.com/maps?hl=en", "https://www.google.de/maps?hl=en"},
			want: "DE",
		},
		{
			name: "second level country domain",
			urls: []string{"https://www.google.com.au/maps"},
			want: "AU",
		},
		{
			name: "United Kingdom maps to its ISO code",
			urls: []string{"https://www.google.co.uk/maps"},
			want: "GB",
		},
		{
			name: "gl parameter wins over the generic domain",
			urls: []string{"https://www.google.com/maps?hl=en&gl=fr"},
			want: "FR",
		},
		{
			name: "consent interstitial carries the destination",
			urls: []string{"https://consent.google.com/m?continue=https%3A%2F%2Fwww.google.es%2Fmaps"},
			want: "ES",
		},
		{
			name: "a generic domain alone reports nothing",
			urls: []string{"https://www.google.com/maps?hl=en"},
			want: "",
		},
		{
			name: "an unrelated host reports nothing",
			urls: []string{"https://example.com/maps"},
			want: "",
		},
		{
			name: "no evidence at all reports nothing",
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := googleCountryHint(test.urls...); got != test.want {
				t.Fatalf("googleCountryHint(%v) = %q, want %q", test.urls, got, test.want)
			}
		})
	}
}

func TestUsableExitAddressRejectsPlaceholders(t *testing.T) {
	t.Parallel()

	if got := usableExitAddress("203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("usableExitAddress(routable) = %q, want the address", got)
	}

	for _, value := range []string{"", "0.0.0.0", "::", "127.0.0.1", "not-an-ip"} {
		if got := usableExitAddress(value); got != "" {
			t.Errorf("usableExitAddress(%q) = %q, want an empty result", value, got)
		}
	}
}

func TestExitIPPrefersTheSOCKS5BoundAddress(t *testing.T) {
	t.Parallel()

	observation := &proxyExitObservation{}
	if address, source := observation.exitIP(); address != "" || source != "" {
		t.Fatalf("empty observation = (%q, %q), want no evidence", address, source)
	}

	observation.record("", "198.51.100.5")

	address, source := observation.exitIP()
	if address != "198.51.100.5" || source != ExitIPSourceEndpoint {
		t.Fatalf("endpoint observation = (%q, %q), want the endpoint", address, source)
	}

	observation.record("203.0.113.9", "198.51.100.5")

	address, source = observation.exitIP()
	if address != "203.0.113.9" || source != ExitIPSourceSOCKS5Bind {
		t.Fatalf("bound observation = (%q, %q), want the SOCKS5 bound address", address, source)
	}
}

// fakeSOCKS5Server answers one CONNECT with a fixed bound address and then
// echoes bytes, which is enough to prove the handshake and the exit-address
// read without any network access beyond the loopback listener.
func fakeSOCKS5Server(t *testing.T, bound net.IP, requireAuth bool) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		greeting := make([]byte, 2)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			return
		}

		methods := make([]byte, int(greeting[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}

		method := socks5AuthNone
		if requireAuth {
			method = socks5AuthUserPass
		}

		if _, err := conn.Write([]byte{socks5Version, method}); err != nil {
			return
		}

		if requireAuth {
			header := make([]byte, 2)
			if _, err := io.ReadFull(conn, header); err != nil {
				return
			}

			user := make([]byte, int(header[1]))
			if _, err := io.ReadFull(conn, user); err != nil {
				return
			}

			lengthByte := make([]byte, 1)
			if _, err := io.ReadFull(conn, lengthByte); err != nil {
				return
			}

			password := make([]byte, int(lengthByte[0]))
			if _, err := io.ReadFull(conn, password); err != nil {
				return
			}

			if _, err := conn.Write([]byte{socks5UserPassVersion, socks5ReplySucceeded}); err != nil {
				return
			}
		}

		request := make([]byte, 4)
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}

		switch request[3] {
		case socks5AddressDomain:
			lengthByte := make([]byte, 1)
			if _, err := io.ReadFull(conn, lengthByte); err != nil {
				return
			}

			if _, err := io.ReadFull(conn, make([]byte, int(lengthByte[0]))); err != nil {
				return
			}
		case socks5AddressIPv4:
			if _, err := io.ReadFull(conn, make([]byte, net.IPv4len)); err != nil {
				return
			}
		case socks5AddressIPv6:
			if _, err := io.ReadFull(conn, make([]byte, net.IPv6len)); err != nil {
				return
			}
		}

		if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
			return
		}

		reply := []byte{socks5Version, socks5ReplySucceeded, socks5Reserved, socks5AddressIPv4}
		reply = append(reply, bound.To4()...)
		reply = binary.BigEndian.AppendUint16(reply, 443)
		_, _ = conn.Write(reply)

		// Hold the tunnel open briefly so the caller can inspect it.
		time.Sleep(50 * time.Millisecond)
	}()

	return listener
}

func TestDialSOCKS5ReportsTheServerBoundExitAddress(t *testing.T) {
	t.Parallel()

	listener := fakeSOCKS5Server(t, net.IPv4(203, 0, 113, 42), false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proxyURL := mustParseProxyURL(t, "socks5://"+listener.Addr().String())

	conn, bound, err := dialSOCKS5(ctx, proxyURL, "www.google.com:443")
	if err != nil {
		t.Fatalf("dialSOCKS5: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if bound != "203.0.113.42" {
		t.Fatalf("bound address = %q, want 203.0.113.42", bound)
	}
}

func TestDialSOCKS5NegotiatesUsernameAndPasswordAndDropsUnusableBinds(t *testing.T) {
	t.Parallel()

	listener := fakeSOCKS5Server(t, net.IPv4zero, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proxyURL := mustParseProxyURL(t, "socks5://operator:secret@"+listener.Addr().String())

	conn, bound, err := dialSOCKS5(ctx, proxyURL, "www.google.com:443")
	if err != nil {
		t.Fatalf("dialSOCKS5 with credentials: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// 0.0.0.0 is the "not reported" answer and must never be stored as an
	// exit address.
	if bound != "" {
		t.Fatalf("bound address = %q, want an empty result for the unspecified address", bound)
	}
}

func TestProxyStatusClassMapsEveryStatusOntoTheComponentLibrary(t *testing.T) {
	t.Parallel()

	expected := map[string]string{
		ProxyStatusHealthy:        "healthy",
		ProxyStatusSlow:           "warning",
		ProxyStatusRateLimited:    "warning",
		ProxyStatusBlocked:        "failed",
		ProxyStatusOffline:        "failed",
		ProxyStatusAuthentication: "failed",
		ProxyStatusCoolingDown:    "paused",
		ProxyStatusUnknown:        "unknown",
	}

	for status, want := range expected {
		if got := proxyStatusClass(status); got != want {
			t.Errorf("proxyStatusClass(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestEffectiveStatusReportsCoolingDownInsideTheWindow(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	future := now.Add(10 * time.Minute)
	past := now.Add(-10 * time.Minute)

	cooling := ProxyRecord{Status: ProxyStatusRateLimited, CooldownUntil: &future}
	if got := cooling.EffectiveStatus(now); got != ProxyStatusCoolingDown {
		t.Fatalf("EffectiveStatus inside the window = %q, want %q", got, ProxyStatusCoolingDown)
	}

	expired := ProxyRecord{Status: ProxyStatusRateLimited, CooldownUntil: &past}
	if got := expired.EffectiveStatus(now); got != ProxyStatusRateLimited {
		t.Fatalf("EffectiveStatus after the window = %q, want %q", got, ProxyStatusRateLimited)
	}

	healthy := ProxyRecord{Status: ProxyStatusHealthy}
	if got := healthy.EffectiveStatus(now); got != ProxyStatusHealthy {
		t.Fatalf("EffectiveStatus without a cooldown = %q, want %q", got, ProxyStatusHealthy)
	}
}
