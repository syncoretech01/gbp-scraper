package web

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Exit-IP and country evidence is collected from the proxy test that already
// runs, without contacting any third-party geolocation or IP-echo service:
//
//   - SOCKS5 reports the address it bound for the outbound connection in the
//     CONNECT reply (RFC 1928 BND.ADDR). When the server fills it in, that is
//     the genuine exit address.
//   - Every protocol yields the endpoint address the local host actually
//     connected to. For a single-hop proxy that is the exit; for a rotating
//     gateway it is only the entry, so the source is always recorded beside
//     the value and the interface never claims more than was measured.
//   - The country is whatever Google itself served the request as: the
//     redirect chain and the final URL carry the country domain or a `gl`
//     parameter. No geolocation database is consulted.
const (
	// ExitIPSourceSOCKS5Bind marks an address the SOCKS5 server reported as
	// the outbound address it bound for this connection.
	ExitIPSourceSOCKS5Bind = "socks5_bind"
	// ExitIPSourceEndpoint marks the address the local host connected to. It
	// is the exit only for a single-hop proxy.
	ExitIPSourceEndpoint = "endpoint"
)

const (
	socks5Version          byte = 0x05
	socks5AuthNone         byte = 0x00
	socks5AuthUserPass     byte = 0x02
	socks5AuthNoAcceptable byte = 0xFF
	socks5UserPassVersion  byte = 0x01
	socks5CommandConnect   byte = 0x01
	socks5AddressIPv4      byte = 0x01
	socks5AddressDomain    byte = 0x03
	socks5AddressIPv6      byte = 0x04
	socks5ReplySucceeded   byte = 0x00
	socks5Reserved         byte = 0x00
)

// socks5DialTimeout bounds the handshake itself, separately from the request
// budget, so a silent SOCKS server cannot hold the proxy test open.
const socks5DialTimeout = 8 * time.Second

// errSOCKS5Handshake identifies a SOCKS5 negotiation that failed before the
// tunnel was usable.
var errSOCKS5Handshake = errors.New("socks5 handshake failed")

// dialSOCKS5 opens a tunnel to target through a SOCKS5 proxy and reports the
// bound address from the CONNECT reply. The bound address is empty when the
// server reports the unspecified address, which many implementations do.
func dialSOCKS5(ctx context.Context, proxyURL *url.URL, target string) (net.Conn, string, error) {
	dialer := net.Dialer{Timeout: socks5DialTimeout}

	conn, err := dialer.DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, "", fmt.Errorf("dial socks5 proxy: %w", err)
	}

	bound, err := socks5Negotiate(ctx, conn, proxyURL, target)
	if err != nil {
		_ = conn.Close()

		return nil, "", err
	}

	return conn, bound, nil
}

func socks5Negotiate(ctx context.Context, conn net.Conn, proxyURL *url.URL, target string) (string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(socks5DialTimeout))
	}

	username, password := "", ""
	if proxyURL.User != nil {
		username = proxyURL.User.Username()
		password, _ = proxyURL.User.Password()
	}

	methods := []byte{socks5AuthNone}
	if username != "" {
		methods = []byte{socks5AuthUserPass, socks5AuthNone}
	}

	greeting := append([]byte{socks5Version, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return "", fmt.Errorf("%w: write greeting: %v", errSOCKS5Handshake, err)
	}

	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return "", fmt.Errorf("%w: read method: %v", errSOCKS5Handshake, err)
	}

	if response[0] != socks5Version || response[1] == socks5AuthNoAcceptable {
		return "", fmt.Errorf("%w: no acceptable authentication method", errSOCKS5Handshake)
	}

	if response[1] == socks5AuthUserPass {
		if err := socks5Authenticate(conn, username, password); err != nil {
			return "", err
		}
	}

	request, err := socks5ConnectRequest(target)
	if err != nil {
		return "", err
	}

	if _, err := conn.Write(request); err != nil {
		return "", fmt.Errorf("%w: write connect: %v", errSOCKS5Handshake, err)
	}

	return socks5ReadConnectReply(conn)
}

func socks5Authenticate(conn net.Conn, username, password string) error {
	if len(username) > 255 || len(password) > 255 {
		return fmt.Errorf("%w: credentials exceed the protocol limit", errSOCKS5Handshake)
	}

	message := make([]byte, 0, 3+len(username)+len(password))
	message = append(message, socks5UserPassVersion, byte(len(username)))
	message = append(message, username...)
	message = append(message, byte(len(password)))
	message = append(message, password...)

	if _, err := conn.Write(message); err != nil {
		return fmt.Errorf("%w: write credentials: %v", errSOCKS5Handshake, err)
	}

	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("%w: read authentication reply: %v", errSOCKS5Handshake, err)
	}

	if reply[1] != socks5ReplySucceeded {
		return fmt.Errorf("%w: authentication rejected", errSOCKS5Handshake)
	}

	return nil
}

func socks5ConnectRequest(target string) ([]byte, error) {
	host, portValue, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target %q", errSOCKS5Handshake, target)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: invalid target port %q", errSOCKS5Handshake, portValue)
	}

	request := []byte{socks5Version, socks5CommandConnect, socks5Reserved}

	switch address := net.ParseIP(host); {
	case address == nil:
		if len(host) > 255 {
			return nil, fmt.Errorf("%w: target host is too long", errSOCKS5Handshake)
		}

		request = append(request, socks5AddressDomain, byte(len(host)))
		request = append(request, host...)
	case address.To4() != nil:
		request = append(request, socks5AddressIPv4)
		request = append(request, address.To4()...)
	default:
		request = append(request, socks5AddressIPv6)
		request = append(request, address.To16()...)
	}

	return binary.BigEndian.AppendUint16(request, uint16(port)), nil
}

func socks5ReadConnectReply(conn net.Conn) (string, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", fmt.Errorf("%w: read connect reply: %v", errSOCKS5Handshake, err)
	}

	if header[0] != socks5Version {
		return "", fmt.Errorf("%w: unexpected protocol version %d", errSOCKS5Handshake, header[0])
	}

	if header[1] != socks5ReplySucceeded {
		return "", fmt.Errorf("%w: connect refused with code %d", errSOCKS5Handshake, header[1])
	}

	var bound string

	switch header[3] {
	case socks5AddressIPv4:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", fmt.Errorf("%w: read bound address: %v", errSOCKS5Handshake, err)
		}

		bound = net.IP(address).String()
	case socks5AddressIPv6:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", fmt.Errorf("%w: read bound address: %v", errSOCKS5Handshake, err)
		}

		bound = net.IP(address).String()
	case socks5AddressDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", fmt.Errorf("%w: read bound host length: %v", errSOCKS5Handshake, err)
		}

		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", fmt.Errorf("%w: read bound host: %v", errSOCKS5Handshake, err)
		}

		bound = string(name)
	default:
		return "", fmt.Errorf("%w: unsupported bound address type %d", errSOCKS5Handshake, header[3])
	}

	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return "", fmt.Errorf("%w: read bound port: %v", errSOCKS5Handshake, err)
	}

	_ = conn.SetDeadline(time.Time{})

	return usableExitAddress(bound), nil
}

// usableExitAddress rejects the unspecified and loopback answers a SOCKS5
// server may return instead of a real outbound address.
func usableExitAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	address := net.ParseIP(value)
	if address == nil {
		return ""
	}

	if address.IsUnspecified() || address.IsLoopback() {
		return ""
	}

	return address.String()
}

// endpointAddress extracts the IP from a dialled remote address.
func endpointAddress(address net.Addr) string {
	if address == nil {
		return ""
	}

	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return usableExitAddress(address.String())
	}

	return usableExitAddress(host)
}

// specialCountryTLDs maps the country domains whose label is not already an
// ISO-3166 alpha-2 code.
var specialCountryTLDs = map[string]string{
	"uk": "GB",
	"eu": "",
}

// googleCountryHint derives the country Google served a proxied request as,
// using only the response we already made: the country domain of the final URL
// or of any hop, and the `gl` parameter Google adds to consent and redirect
// URLs. An unrecognised answer yields an empty string rather than a guess.
func googleCountryHint(urls ...string) string {
	for _, raw := range urls {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Host == "" {
			continue
		}

		if country := countryFromQuery(parsed); country != "" {
			return country
		}

		if country := countryFromGoogleHost(parsed.Hostname()); country != "" {
			return country
		}

		// Consent interstitials carry the real destination, and therefore the
		// country domain, in a continue parameter.
		if next := parsed.Query().Get("continue"); next != "" && next != raw {
			if country := googleCountryHint(next); country != "" {
				return country
			}
		}
	}

	return ""
}

func countryFromQuery(parsed *url.URL) string {
	for _, key := range []string{"gl", "gr", "region"} {
		value := strings.TrimSpace(parsed.Query().Get(key))
		if len(value) == 2 && isASCIILetters(value) {
			return normalizeCountryCode(value)
		}
	}

	return ""
}

func countryFromGoogleHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || !strings.Contains(host, "google.") {
		return ""
	}

	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]

	if len(last) != 2 || !isASCIILetters(last) {
		return ""
	}

	return normalizeCountryCode(last)
}

func normalizeCountryCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if mapped, found := specialCountryTLDs[strings.ToLower(value)]; found {
		return mapped
	}

	return value
}

func isASCIILetters(value string) bool {
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') {
			return false
		}
	}

	return value != ""
}
