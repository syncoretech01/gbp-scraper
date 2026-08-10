package jobruntime

import (
	"net/url"
	"regexp"
	"strings"
)

// RedactedValue is substituted for credentials and tokens in logs and API
// responses.
const RedactedValue = "***"

const internalRedactedValue = "REDACTED"

var (
	absoluteURLPattern         = regexp.MustCompile(`(?i)\b(?:https?|socks(?:4a?|5)?|ftp)://[^\s<>"']+`)
	fallbackUserInfoPattern    = regexp.MustCompile(`(?i)(\b(?:https?|socks(?:4a?|5)?|ftp)://[^\s/:@]+:)[^\s/@]+(@)`)
	authorizationPattern       = regexp.MustCompile(`(?i)(\b(?:proxy-)?authorization\s*[:=]\s*)(bearer|basic)(\s+)[^\s,;]+`)
	cookieHeaderPattern        = regexp.MustCompile(`(?i)(\b(?:set-cookie|cookie)\s*:\s*)[^\r\n]+`)
	sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(["']?(?:password|passwd|pwd|secret|client[_-]?secret|api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|auth[_-]?token|private[_-]?key)["']?\s*[:=]\s*)("[^"\r\n]*(?:"|\r?\n|$)|'[^'\r\n]*(?:'|\r?\n|$)|[^"'\s,;&}]+)`)
)

// RedactURL masks URL user-info passwords and sensitive query/fragment values.
// It leaves the scheme, username, host, port, path, and non-secret parameters
// visible for diagnostics.
func RedactURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return fallbackUserInfoPattern.ReplaceAllString(rawURL, `${1}`+RedactedValue+`${2}`)
	}

	query := parsed.Query()
	fragment, fragmentParseErr := url.ParseQuery(parsed.Fragment)
	marker := uniqueRedactionMarker(rawURL, parsed, query, fragment)
	changed := false
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(parsed.User.Username(), marker)
			changed = true
		}
	}

	for key := range query {
		if IsSensitiveKey(key) {
			query.Set(key, marker)
			changed = true
		}
	}

	if changed {
		parsed.RawQuery = query.Encode()
	}

	if parsed.Fragment != "" && strings.Contains(parsed.Fragment, "=") {
		if fragmentParseErr == nil {
			fragmentChanged := false
			for key := range fragment {
				if IsSensitiveKey(key) {
					fragment.Set(key, marker)
					fragmentChanged = true
				}
			}

			if fragmentChanged {
				parsed.Fragment = fragment.Encode()
				changed = true
			}
		}
	}

	if !changed {
		return rawURL
	}

	return strings.ReplaceAll(parsed.String(), marker, RedactedValue)
}

// RedactString masks credentials in URLs, authorization/cookie headers, and
// common key-value secret forms without changing unrelated diagnostic text.
func RedactString(value string) string {
	redacted := absoluteURLPattern.ReplaceAllStringFunc(value, func(match string) string {
		urlValue, suffix := trimURLPunctuation(match)

		return RedactURL(urlValue) + suffix
	})

	redacted = fallbackUserInfoPattern.ReplaceAllString(redacted, `${1}`+RedactedValue+`${2}`)
	redacted = authorizationPattern.ReplaceAllString(redacted, `${1}${2}${3}`+RedactedValue)
	redacted = cookieHeaderPattern.ReplaceAllString(redacted, `${1}`+RedactedValue)
	redacted = sensitiveAssignmentPattern.ReplaceAllStringFunc(redacted, redactSensitiveAssignment)

	return redacted
}

// IsSensitiveKey reports whether a structured field name conventionally holds
// a password, token, private key, authorization value, or cookie.
func IsSensitiveKey(key string) bool {
	normalized := normalizeSecretKey(key)
	if normalized == "" {
		return false
	}

	secretSuffixes := []string{
		"password",
		"passwd",
		"pwd",
		"secret",
		"clientsecret",
		"apikey",
		"accesstoken",
		"refreshtoken",
		"authtoken",
		"authorization",
		"proxyauthorization",
		"cookie",
		"setcookie",
		"privatekey",
	}

	for _, suffix := range secretSuffixes {
		if normalized == suffix || strings.HasSuffix(normalized, suffix) {
			return true
		}
	}

	return false
}

// RedactValue returns a deep redacted copy of JSON-like maps and slices. The
// input is never mutated. Unknown scalar types are returned unchanged.
func RedactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return RedactString(typed)
	case []string:
		copyValue := make([]string, len(typed))
		for index := range typed {
			copyValue[index] = RedactString(typed[index])
		}

		return copyValue
	case []any:
		copyValue := make([]any, len(typed))
		for index := range typed {
			copyValue[index] = RedactValue(typed[index])
		}

		return copyValue
	case map[string]string:
		copyValue := make(map[string]string, len(typed))
		for key, item := range typed {
			if IsSensitiveKey(key) {
				copyValue[key] = RedactedValue
			} else {
				copyValue[key] = RedactString(item)
			}
		}

		return copyValue
	case map[string]any:
		copyValue := make(map[string]any, len(typed))
		for key, item := range typed {
			if IsSensitiveKey(key) {
				copyValue[key] = RedactedValue
			} else {
				copyValue[key] = RedactValue(item)
			}
		}

		return copyValue
	default:
		return value
	}
}

func normalizeSecretKey(key string) string {
	var builder strings.Builder
	builder.Grow(len(key))

	for _, char := range strings.ToLower(key) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}

	return builder.String()
}

func uniqueRedactionMarker(rawURL string, parsed *url.URL, query, fragment url.Values) string {
	marker := internalRedactedValue
	for redactionMarkerExists(rawURL, parsed, query, fragment, marker) {
		marker = "_" + marker + "_"
	}

	return marker
}

func redactionMarkerExists(
	rawURL string,
	parsed *url.URL,
	query url.Values,
	fragment url.Values,
	marker string,
) bool {
	if strings.Contains(rawURL, marker) || strings.Contains(parsed.Path, marker) ||
		strings.Contains(parsed.Opaque, marker) || strings.Contains(parsed.Fragment, marker) {
		return true
	}

	if parsed.User != nil {
		if strings.Contains(parsed.User.Username(), marker) {
			return true
		}
		if password, exists := parsed.User.Password(); exists && strings.Contains(password, marker) {
			return true
		}
	}

	for _, values := range []url.Values{query, fragment} {
		for key, items := range values {
			if strings.Contains(key, marker) {
				return true
			}
			for _, item := range items {
				if strings.Contains(item, marker) {
					return true
				}
			}
		}
	}

	return false
}

func redactSensitiveAssignment(match string) string {
	parts := sensitiveAssignmentPattern.FindStringSubmatch(match)
	if len(parts) != 3 {
		return RedactedValue
	}

	prefix := parts[1]
	value := parts[2]
	if value == "" || value[0] != '\'' && value[0] != '"' {
		return prefix + RedactedValue
	}

	quote := value[:1]
	suffix := ""
	if strings.HasSuffix(value, quote) {
		suffix = quote
	} else if strings.HasSuffix(value, "\r\n") {
		suffix = "\r\n"
	} else if strings.HasSuffix(value, "\n") {
		suffix = "\n"
	}

	return prefix + quote + RedactedValue + suffix
}

func trimURLPunctuation(value string) (string, string) {
	index := len(value)
	for index > 0 {
		switch value[index-1] {
		case '.', ',', ';', ')', ']', '}':
			index--
		default:
			return value[:index], value[index:]
		}
	}

	return value, ""
}
