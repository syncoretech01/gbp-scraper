package resultimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/mail"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var (
	//nolint:gochecknoglobals // Immutable compiled expressions are safe to share.
	phoneExtensionPattern = regexp.MustCompile(`(?i)(?:\s*(?:ext\.?|extension|x|#)\s*[:.]?\s*)([0-9]{1,8})\s*$`)
	//nolint:gochecknoglobals // Immutable compiled expressions are safe to share.
	statePostalPattern = regexp.MustCompile(`(?i)^(.+?)(?:\s+|,\s*)([A-Z]{2}|[A-Za-z][A-Za-z .'-]+)?\s*([A-Z0-9][A-Z0-9 -]{1,11})?$`)
)

var (
	errURLCredentials = errors.New("URL credentials are not permitted")
	errURLScheme      = errors.New("URL scheme is not supported")
)

// NormalizeURL validates and canonicalizes an HTTP(S) URL. Bare domains are
// treated as HTTPS. User credentials are rejected, fragments and common
// tracking parameters are removed, and Domain is the registrable domain when
// the public suffix list recognizes it.
func NormalizeURL(raw string) (URLValue, error) {
	result := URLValue{Raw: raw}
	value := strings.TrimSpace(norm.NFKC.String(raw))
	if value == "" || isMissingValue(value) {
		return result, ErrInvalidURL
	}

	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	} else if !strings.Contains(value, "://") {
		value = "https://" + value
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return result, ErrInvalidURL
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return result, errors.Join(ErrInvalidURL, errURLScheme)
	}
	if parsed.User != nil {
		return result, errors.Join(ErrInvalidURL, errURLCredentials)
	}

	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return result, ErrInvalidURL
	}
	if net.ParseIP(host) == nil {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return result, ErrInvalidURL
		}
		host = strings.ToLower(host)
	}

	port := parsed.Port()
	if port != "" {
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || portNumber < 1 || portNumber > 65535 {
			return result, ErrInvalidURL
		}
	}
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	parsed.Host = host
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}

	if parsed.Path == "/" {
		parsed.Path = ""
	} else if parsed.Path != "" {
		cleaned := path.Clean(parsed.Path)
		if cleaned == "/" {
			cleaned = ""
		} else if !strings.HasPrefix(cleaned, "/") {
			cleaned = "/" + cleaned
		}
		parsed.Path = cleaned
	}
	parsed.RawPath = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.RawQuery = cleanQuery(parsed.Query()).Encode()

	domain := host
	if net.ParseIP(host) == nil && host != "localhost" {
		if registrable, domainErr := publicsuffix.EffectiveTLDPlusOne(host); domainErr == nil {
			domain = strings.ToLower(registrable)
		}
	}
	domain = strings.TrimPrefix(domain, "www.")

	result.Canonical = parsed.String()
	result.Host = host
	result.Domain = domain
	result.Valid = true

	return result, nil
}

// NormalizeDomain returns an ASCII, lower-case registrable domain. It accepts
// either a host name or a complete URL.
func NormalizeDomain(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if normalized, err := NormalizeURL(value); err == nil {
		return normalized.Domain
	}

	value = strings.TrimSuffix(strings.ToLower(value), ".")
	value = strings.TrimPrefix(value, "www.")
	host, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return ""
	}
	if registrable, domainErr := publicsuffix.EffectiveTLDPlusOne(host); domainErr == nil {
		return strings.ToLower(registrable)
	}
	if net.ParseIP(host) != nil || host == "localhost" {
		return strings.ToLower(host)
	}

	return ""
}

func cleanQuery(values url.Values) url.Values {
	cleaned := make(url.Values, len(values))
	for key, entries := range values {
		if isTrackingParameter(key) {
			continue
		}
		for _, entry := range entries {
			cleaned.Add(key, entry)
		}
	}

	return cleaned
}

func isTrackingParameter(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if strings.HasPrefix(key, "utm_") || strings.HasPrefix(key, "mc_") {
		return true
	}

	switch key {
	case "fbclid", "gclid", "dclid", "msclkid", "twclid", "igshid", "_ga", "_gl":
		return true
	default:
		return false
	}
}

// NormalizePhone converts a phone number into stable display and match forms.
// DefaultCallingCode is digits only (for example, "1") and is applied only to
// numbers that do not already carry an international prefix.
func NormalizePhone(raw, defaultCallingCode string) Phone {
	result := Phone{Raw: raw}
	value := strings.TrimSpace(norm.NFKC.String(raw))
	if value == "" || isMissingValue(value) {
		return result
	}

	if match := phoneExtensionPattern.FindStringSubmatch(value); len(match) == 2 {
		result.Extension = match[1]
		value = strings.TrimSpace(value[:len(value)-len(match[0])])
	}
	explicitInternational := strings.HasPrefix(value, "+") || strings.HasPrefix(value, "00")
	digits := onlyDigits(value)
	if strings.HasPrefix(value, "00") && strings.HasPrefix(digits, "00") {
		digits = strings.TrimPrefix(digits, "00")
	}
	if len(digits) < 7 || len(digits) > 15 {
		return result
	}

	callingCode := onlyDigits(defaultCallingCode)
	if !explicitInternational && callingCode != "" && !strings.HasPrefix(digits, callingCode) {
		digits = callingCode + digits
		explicitInternational = true
	}
	if len(digits) > 15 {
		return result
	}

	result.Normalized = digits
	if explicitInternational {
		result.Normalized = "+" + digits
	}
	result.MatchKey = digits
	// Treat explicit and domestic North American forms as the same exact key.
	if len(digits) == 11 && strings.HasPrefix(digits, "1") {
		result.MatchKey = digits[1:]
	}
	result.Valid = true

	return result
}

// NormalizePhones normalizes and de-duplicates a list while keeping the first
// raw representation of each valid match key.
func NormalizePhones(defaultCallingCode string, values ...string) []Phone {
	phones, _, _ := normalizePhones(defaultCallingCode, values...)

	return phones
}

func normalizePhones(defaultCallingCode string, values ...string) ([]Phone, int, int) {
	seen := make(map[string]struct{})
	result := make([]Phone, 0, len(values))
	invalid := 0
	duplicates := 0
	for _, value := range expandList(values...) {
		phone := NormalizePhone(value, defaultCallingCode)
		if !phone.Valid {
			if strings.TrimSpace(value) != "" {
				invalid++
			}
			continue
		}
		key := phone.MatchKey
		if phone.Extension != "" {
			key += "x" + phone.Extension
		}
		if _, exists := seen[key]; exists {
			duplicates++
			continue
		}
		seen[key] = struct{}{}
		result = append(result, phone)
	}

	return result, invalid, duplicates
}

// NormalizeEmail lowercases and validates a mailbox and canonicalizes its IDN
// domain. It does not perform network or mailbox-level verification.
func NormalizeEmail(raw string) Email {
	result := Email{Raw: raw}
	value := strings.TrimSpace(norm.NFKC.String(raw))
	if value == "" || isMissingValue(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return result
	}

	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address == "" {
		return result
	}
	address := parsed.Address
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		return result
	}
	local := strings.ToLower(address[:at])
	domain, err := idna.Lookup.ToASCII(strings.TrimSuffix(address[at+1:], "."))
	if err != nil || !validEmailLocal(local) || !validEmailDomain(domain) {
		return result
	}

	result.Domain = strings.ToLower(domain)
	result.Normalized = local + "@" + result.Domain
	result.Valid = true

	return result
}

// NormalizeEmails returns valid, normalized mailboxes with case-insensitive
// duplicates removed in first-seen order.
func NormalizeEmails(values ...string) []Email {
	emails, _, _ := normalizeEmails(values...)

	return emails
}

func normalizeEmails(values ...string) ([]Email, int, int) {
	seen := make(map[string]struct{})
	result := make([]Email, 0, len(values))
	invalid := 0
	duplicates := 0
	for _, value := range expandList(values...) {
		email := NormalizeEmail(value)
		if !email.Valid {
			if strings.TrimSpace(value) != "" {
				invalid++
			}
			continue
		}
		if _, exists := seen[email.Normalized]; exists {
			duplicates++
			continue
		}
		seen[email.Normalized] = struct{}{}
		result = append(result, email)
	}

	return result, invalid, duplicates
}

func validEmailLocal(local string) bool {
	if local == "" || len(local) > 64 || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") {
		return false
	}

	return !strings.Contains(local, "..")
}

func validEmailDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || strings.Contains(domain, "..") {
		return false
	}
	if net.ParseIP(domain) != nil {
		return true
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' {
				return false
			}
		}
	}

	return true
}

func expandList(values ...string) []string {
	var result []string
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		var encoded []string
		if strings.HasPrefix(trimmed, "[") && json.Unmarshal([]byte(trimmed), &encoded) == nil {
			result = append(result, encoded...)
			continue
		}
		result = append(result, strings.FieldsFunc(trimmed, func(char rune) bool {
			switch char {
			case ',', ';', '|', '\n', '\r':
				return true
			default:
				return false
			}
		})...)
	}

	return result
}

// NormalizeName produces a Unicode-aware comparison form and removes common
// trailing legal entity suffixes. The original display name is never changed.
func NormalizeName(value string) string {
	normalized := normalizeWords(strings.ReplaceAll(value, "&", " and "), true)
	parts := strings.Fields(normalized)
	for len(parts) > 1 {
		suffixLength := legalSuffixLength(parts)
		if suffixLength == 0 || suffixLength >= len(parts) {
			break
		}
		parts = parts[:len(parts)-suffixLength]
	}

	return strings.Join(parts, " ")
}

// NormalizeAddress produces a Unicode-aware exact-match form while retaining
// unit numbers and alphanumeric postal data.
func NormalizeAddress(value string) string {
	return normalizeWords(value, false)
}

// NormalizeCategory produces a stable category comparison form.
func NormalizeCategory(value string) string {
	return normalizeWords(value, false)
}

func normalizeWords(value string, joinApostrophes bool) string {
	value = cases.Fold().String(norm.NFKC.String(strings.TrimSpace(value)))
	var builder strings.Builder
	builder.Grow(len(value))
	wasSpace := true
	for _, char := range value {
		switch {
		case unicode.IsLetter(char), unicode.IsNumber(char), unicode.IsMark(char):
			builder.WriteRune(char)
			wasSpace = false
		case joinApostrophes && (char == '\'' || char == '\u2019'):
			continue
		case !wasSpace:
			builder.WriteByte(' ')
			wasSpace = true
		}
	}

	return strings.TrimSpace(builder.String())
}

func isLegalSuffix(value string) bool {
	switch value {
	case "inc", "incorporated", "llc", "ltd", "limited", "llp", "pllc", "plc", "pc", "corp", "corporation", "company", "co":
		return true
	default:
		return false
	}
}

func legalSuffixLength(parts []string) int {
	if len(parts) == 0 {
		return 0
	}
	if isLegalSuffix(parts[len(parts)-1]) {
		return 1
	}
	for _, suffix := range []string{"l l c", "l l p", "p l l c", "p l c", "p c", "l t d", "i n c", "c o"} {
		suffixParts := strings.Fields(suffix)
		if len(suffixParts) > len(parts) {
			continue
		}
		if strings.Join(parts[len(parts)-len(suffixParts):], " ") == suffix {
			return len(suffixParts)
		}
	}

	return 0
}

func parseAddress(raw string, complete JSONValue) Address {
	result := Address{Raw: raw, Normalized: NormalizeAddress(raw)}
	if complete.Valid {
		var parsed struct {
			Borough    string `json:"borough"`
			Street     string `json:"street"`
			City       string `json:"city"`
			PostalCode string `json:"postal_code"`
			State      string `json:"state"`
			Country    string `json:"country"`
		}
		if json.Unmarshal(complete.Value, &parsed) == nil {
			result.Borough = cleanDisplay(parsed.Borough)
			result.Street = cleanDisplay(parsed.Street)
			result.City = cleanDisplay(parsed.City)
			result.PostalCode = normalizePostalCode(parsed.PostalCode)
			result.State = normalizeState(parsed.State)
			result.Country = normalizeCountry(parsed.Country)
		}
	}
	if result.Street == "" && result.City == "" && strings.TrimSpace(raw) != "" {
		parseAddressText(&result, raw)
	}
	if result.Normalized == "" {
		result.Normalized = NormalizeAddress(strings.Join(nonEmpty(
			result.Street,
			result.City,
			result.State,
			result.PostalCode,
			result.Country,
		), ", "))
	}

	return result
}

func parseAddressText(result *Address, raw string) {
	parts := strings.Split(raw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := cleanDisplay(part); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return
	}

	last := len(cleaned) - 1
	if country := normalizeCountry(cleaned[last]); country != "" && isKnownCountry(cleaned[last]) {
		result.Country = country
		cleaned = cleaned[:last]
	}
	if len(cleaned) == 0 {
		return
	}

	last = len(cleaned) - 1
	state, postal := splitStatePostal(cleaned[last])
	if state != "" || postal != "" {
		result.State = state
		result.PostalCode = postal
		cleaned = cleaned[:last]
	}
	if len(cleaned) >= 2 {
		result.City = cleaned[len(cleaned)-1]
		cleaned = cleaned[:len(cleaned)-1]
	}
	if len(cleaned) > 0 {
		result.Street = strings.Join(cleaned, ", ")
	} else if result.City == "" {
		result.Street = cleanDisplay(raw)
	}
}

func splitStatePostal(value string) (string, string) {
	value = cleanDisplay(value)
	match := statePostalPattern.FindStringSubmatch(value)
	if len(match) != 4 {
		return "", ""
	}
	state := normalizeState(match[1])
	postal := normalizePostalCode(match[3])
	if state == "" {
		state = normalizeState(match[2])
		if postal == "" {
			postal = normalizePostalCode(match[3])
		}
	}
	if state == "" && postal == "" {
		return "", ""
	}

	return state, postal
}

func normalizeState(value string) string {
	normalized := strings.ToUpper(strings.TrimSpace(strings.Trim(value, ".")))
	if len(normalized) == 2 {
		return normalized
	}
	switch strings.ToLower(normalizeWords(value, false)) {
	case "alabama":
		return "AL"
	case "alaska":
		return "AK"
	case "arizona":
		return "AZ"
	case "arkansas":
		return "AR"
	case "california":
		return "CA"
	case "colorado":
		return "CO"
	case "connecticut":
		return "CT"
	case "delaware":
		return "DE"
	case "district of columbia":
		return "DC"
	case "florida":
		return "FL"
	case "georgia":
		return "GA"
	case "hawaii":
		return "HI"
	case "idaho":
		return "ID"
	case "illinois":
		return "IL"
	case "indiana":
		return "IN"
	case "iowa":
		return "IA"
	case "kansas":
		return "KS"
	case "kentucky":
		return "KY"
	case "louisiana":
		return "LA"
	case "maine":
		return "ME"
	case "maryland":
		return "MD"
	case "massachusetts":
		return "MA"
	case "michigan":
		return "MI"
	case "minnesota":
		return "MN"
	case "mississippi":
		return "MS"
	case "missouri":
		return "MO"
	case "montana":
		return "MT"
	case "nebraska":
		return "NE"
	case "nevada":
		return "NV"
	case "new hampshire":
		return "NH"
	case "new jersey":
		return "NJ"
	case "new mexico":
		return "NM"
	case "new york":
		return "NY"
	case "north carolina":
		return "NC"
	case "north dakota":
		return "ND"
	case "ohio":
		return "OH"
	case "oklahoma":
		return "OK"
	case "oregon":
		return "OR"
	case "pennsylvania":
		return "PA"
	case "rhode island":
		return "RI"
	case "south carolina":
		return "SC"
	case "south dakota":
		return "SD"
	case "tennessee":
		return "TN"
	case "texas":
		return "TX"
	case "utah":
		return "UT"
	case "vermont":
		return "VT"
	case "virginia":
		return "VA"
	case "washington":
		return "WA"
	case "west virginia":
		return "WV"
	case "wisconsin":
		return "WI"
	case "wyoming":
		return "WY"
	default:
		return ""
	}
}

func normalizeCountry(value string) string {
	switch strings.ToLower(normalizeWords(value, false)) {
	case "us", "usa", "united states", "united states of america":
		return "US"
	case "ca", "canada":
		return "CA"
	case "uk", "gb", "great britain", "united kingdom":
		return "GB"
	case "au", "australia":
		return "AU"
	case "nz", "new zealand":
		return "NZ"
	default:
		return strings.ToUpper(cleanDisplay(value))
	}
}

func isKnownCountry(value string) bool {
	switch strings.ToLower(normalizeWords(value, false)) {
	case "us", "usa", "united states", "united states of america", "ca", "canada", "uk", "gb", "great britain", "united kingdom", "au", "australia", "nz", "new zealand":
		return true
	default:
		return false
	}
}

func normalizePostalCode(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(cleanDisplay(value)), " "))
}

func cleanDisplay(value string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(strings.TrimSpace(value))), " ")
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}

	return result
}

func parseJSONValue(raw string) JSONValue {
	value := strings.TrimSpace(raw)
	if value == "" {
		return JSONValue{}
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return JSONValue{Present: true}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return JSONValue{Present: true}
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return JSONValue{Present: true}
	}

	return JSONValue{Present: true, Valid: true, Value: canonical}
}

func canonicalJSON(value JSONValue) string {
	if !value.Valid {
		return ""
	}

	return string(value.Value)
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}

	return builder.String()
}

func isMissingValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "n/a", "na", "none", "null", "nil", "unknown", "-", "--":
		return true
	default:
		return false
	}
}

func sortEmails(values []Email) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Normalized)
	}
	sort.Strings(result)

	return result
}

func sortPhones(values []Phone) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := value.MatchKey
		if value.Extension != "" {
			key += "x" + value.Extension
		}
		result = append(result, key)
	}
	sort.Strings(result)

	return result
}

func compactJSON(raw string) string {
	value := parseJSONValue(raw)
	if !value.Valid {
		return strings.TrimSpace(raw)
	}

	return string(bytes.TrimSpace(value.Value))
}
