package enrichment

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

var (
	// ErrUnsupportedScheme is returned for URLs other than HTTP or HTTPS.
	ErrUnsupportedScheme = errors.New("only http and https URLs are allowed")
	// ErrUnsafeURL is returned when a URL can reach a local, private, or reserved address.
	ErrUnsafeURL = errors.New("URL resolves to a non-public address")
	// ErrNoAddresses is returned when DNS produces no usable addresses.
	ErrNoAddresses = errors.New("host resolved without an address")
)

// URLGuard validates outbound URLs and DNS results before they are requested.
type URLGuard struct {
	Resolver                  Resolver
	UnsafeAllowPrivateNetwork bool
}

// ValidateURL parses a URL, permits only HTTP(S), and rejects local or reserved targets.
func (g URLGuard) ValidateURL(ctx context.Context, rawURL string) (*url.URL, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}

	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	if parsedURL.Scheme != schemeHTTP && parsedURL.Scheme != schemeHTTPS {
		return nil, ErrUnsupportedScheme
	}

	if parsedURL.User != nil {
		return nil, fmt.Errorf("URL user information is not allowed: %w", ErrUnsafeURL)
	}

	if parsedURL.Host == "" || parsedURL.Hostname() == "" {
		return nil, fmt.Errorf("URL has no host: %w", ErrUnsafeURL)
	}

	host := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("localhost is not allowed: %w", ErrUnsafeURL)
	}

	if err := g.validateHost(ctx, host); err != nil {
		return nil, err
	}

	parsedURL.Fragment = ""

	return parsedURL, nil
}

func (g URLGuard) validateHost(ctx context.Context, host string) error {
	if parsedAddress, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if !g.addressAllowed(parsedAddress) {
			return fmt.Errorf("address %s: %w", parsedAddress, ErrUnsafeURL)
		}

		return nil
	}

	resolver := g.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}

	if len(addresses) == 0 {
		return fmt.Errorf("resolve host %q: %w", host, ErrNoAddresses)
	}

	for _, address := range addresses {
		parsedAddress, parseErr := netip.ParseAddr(address.IP.String())
		if parseErr != nil || !g.addressAllowed(parsedAddress) {
			return fmt.Errorf("resolved address %q: %w", address.IP.String(), ErrUnsafeURL)
		}
	}

	return nil
}

func (g URLGuard) addressAllowed(address netip.Addr) bool {
	if address.Is4In6() {
		address = address.Unmap()
	}

	if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}

	if g.UnsafeAllowPrivateNetwork {
		return true
	}

	if !address.IsGlobalUnicast() {
		return false
	}

	// IsGlobalUnicast includes documentation and benchmarking ranges. They are
	// deliberately rejected so the default policy permits only routable targets.
	for _, prefixText := range []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
		"2001:db8::/32",
	} {
		prefix := netip.MustParsePrefix(prefixText)
		if prefix.Contains(address) {
			return false
		}
	}

	return true
}

func (g URLGuard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split network address: %w", err)
	}

	host = strings.Trim(host, "[]")
	if err := g.validateHost(ctx, host); err != nil {
		return nil, err
	}

	resolver := g.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	if parsedAddress, parseErr := netip.ParseAddr(host); parseErr == nil {
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(parsedAddress.String(), port))
	}

	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve before dial: %w", err)
	}

	resolvedAddresses := make([]netip.Addr, 0, len(addresses))

	for _, resolvedAddress := range addresses {
		parsedAddress, parseErr := netip.ParseAddr(resolvedAddress.IP.String())
		if parseErr != nil || !g.addressAllowed(parsedAddress) {
			return nil, fmt.Errorf("resolved address %q changed to unsafe target: %w", resolvedAddress.IP, ErrUnsafeURL)
		}

		resolvedAddresses = append(resolvedAddresses, parsedAddress)
	}

	if len(resolvedAddresses) == 0 {
		return nil, ErrNoAddresses
	}

	dialErrors := make([]error, 0, len(resolvedAddresses))

	for _, resolvedAddress := range resolvedAddresses {
		connection, dialErr := (&net.Dialer{}).DialContext(
			ctx,
			network,
			net.JoinHostPort(resolvedAddress.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}

		dialErrors = append(dialErrors, dialErr)
	}

	if len(dialErrors) == 0 {
		return nil, ErrNoAddresses
	}

	return nil, fmt.Errorf("dial resolved addresses: %w", errors.Join(dialErrors...))
}
