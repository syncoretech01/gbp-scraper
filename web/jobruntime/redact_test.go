package jobruntime

import (
	"reflect"
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()

	positive := []string{
		"password",
		"db_password",
		"PASS-WD",
		"pwd",
		"client_secret",
		"client-secret",
		"API_KEY",
		"apikey",
		"access.token",
		"refresh_token",
		"authToken",
		"Authorization",
		"Proxy-Authorization",
		"cookie",
		"Set-Cookie",
		"private_key",
	}
	for _, key := range positive {
		if !IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = false", key)
		}
	}

	negative := []string{"", "username", "tokenizer", "secretary", "public_key", "proxy_url", "session_id"}
	for _, key := range negative {
		if IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = true", key)
		}
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()

	rawURL := "https://alice:supersecret@example.test:8443/maps?q=dentist&api_key=querysecret#view=full&access_token=fragmentsecret"
	redacted := RedactURL(rawURL)
	for _, secret := range []string{"supersecret", "querysecret", "fragmentsecret"} {
		if strings.Contains(redacted, secret) {
			t.Errorf("RedactURL() leaked %q in %q", secret, redacted)
		}
	}
	for _, visible := range []string{"https://alice:", RedactedValue + "@example.test:8443", "/maps", "q=dentist", "api_key=" + RedactedValue, "view=full", "access_token=" + RedactedValue} {
		if !strings.Contains(redacted, visible) {
			t.Errorf("RedactURL() = %q, missing %q", redacted, visible)
		}
	}
}

func TestRedactURLLeavesSafeURLUnchanged(t *testing.T) {
	t.Parallel()

	rawURL := "https://example.test/path?q=dentist&session_id=public#results"
	if got := RedactURL(rawURL); got != rawURL {
		t.Errorf("RedactURL() = %q, want unchanged %q", got, rawURL)
	}

	usernameOnly := "socks5://proxy-user@127.0.0.1:1080"
	if got := RedactURL(usernameOnly); got != usernameOnly {
		t.Errorf("RedactURL(username only) = %q, want unchanged %q", got, usernameOnly)
	}
}

func TestRedactURLFallbackMasksUserInfo(t *testing.T) {
	t.Parallel()

	rawURL := "https://alice:supersecret@example.test/%zz"
	redacted := RedactURL(rawURL)
	if strings.Contains(redacted, "supersecret") {
		t.Fatalf("RedactURL() leaked password in malformed URL: %q", redacted)
	}
	if !strings.Contains(redacted, "alice:"+RedactedValue+"@") {
		t.Errorf("RedactURL() = %q, missing masked user info", redacted)
	}
}

func TestRedactURLDoesNotReplaceLegitimateMarkerText(t *testing.T) {
	t.Parallel()

	rawURL := "https://example.test/REDACTED?api_key=secret&note=REDACTED#label=REDACTED"
	redacted := RedactURL(rawURL)
	if strings.Contains(redacted, "secret") {
		t.Fatalf("RedactURL() leaked secret: %q", redacted)
	}
	for _, visible := range []string{"/REDACTED", "note=REDACTED", "label=REDACTED"} {
		if !strings.Contains(redacted, visible) {
			t.Errorf("RedactURL() changed legitimate marker text; got %q, missing %q", redacted, visible)
		}
	}
}

func TestRedactStringCredentialsAndPunctuation(t *testing.T) {
	t.Parallel()

	input := "proxy https://bob:proxysecret@proxy.test:8080/path?api_key=querysecret, then continue."
	redacted := RedactString(input)
	for _, secret := range []string{"proxysecret", "querysecret"} {
		if strings.Contains(redacted, secret) {
			t.Errorf("RedactString() leaked %q in %q", secret, redacted)
		}
	}
	if !strings.HasSuffix(redacted, ", then continue.") {
		t.Errorf("RedactString() lost surrounding punctuation/text: %q", redacted)
	}
}

func TestRedactStringHeadersAndAssignments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		secret string
		want   string
	}{
		{name: "bearer authorization", input: "Authorization: Bearer bearer-secret", secret: "bearer-secret", want: "Authorization: Bearer " + RedactedValue},
		{name: "basic proxy authorization", input: "Proxy-Authorization=Basic basic-secret", secret: "basic-secret", want: "Proxy-Authorization=Basic " + RedactedValue},
		{name: "cookie", input: "Cookie: session=cookie-secret; other=value", secret: "cookie-secret", want: "Cookie: " + RedactedValue},
		{name: "set cookie", input: "Set-Cookie: session=set-cookie-secret; Secure", secret: "set-cookie-secret", want: "Set-Cookie: " + RedactedValue},
		{name: "unquoted password", input: "password=hunter2 status=retry", secret: "hunter2", want: "password=" + RedactedValue + " status=retry"},
		{name: "JSON API key", input: `{"api_key": "json-secret"}`, secret: "json-secret", want: `{"api_key": "` + RedactedValue + `"}`},
		{name: "quoted password with spaces", input: `password="correct horse battery staple" status=retry`, secret: "correct horse battery staple", want: `password="` + RedactedValue + `" status=retry`},
		{name: "single quoted client secret with spaces", input: `client-secret='alpha beta gamma'`, secret: "alpha beta gamma", want: `client-secret='` + RedactedValue + `'`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := RedactString(test.input)
			if strings.Contains(got, test.secret) {
				t.Errorf("RedactString() leaked %q in %q", test.secret, got)
			}
			if got != test.want {
				t.Errorf("RedactString() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRedactStringLeavesDiagnosticsUntouched(t *testing.T) {
	t.Parallel()

	input := "query=dentists status=retry endpoint=https://example.test/maps?q=dentists"
	if got := RedactString(input); got != input {
		t.Errorf("RedactString() = %q, want unchanged %q", got, input)
	}
}

func TestRedactValueDeepCopy(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"api_key": "top-level-secret",
		"message": "connect to https://bob:url-secret@example.test",
		"nested": []any{
			map[string]string{
				"password": "nested-secret",
				"endpoint": "https://example.test/?access_token=query-secret",
			},
		},
		"lines": []string{"Authorization: Bearer bearer-secret", "safe line"},
		"count": 7,
	}
	original := map[string]any{
		"api_key": "top-level-secret",
		"message": "connect to https://bob:url-secret@example.test",
		"nested": []any{
			map[string]string{
				"password": "nested-secret",
				"endpoint": "https://example.test/?access_token=query-secret",
			},
		},
		"lines": []string{"Authorization: Bearer bearer-secret", "safe line"},
		"count": 7,
	}

	redacted, ok := RedactValue(input).(map[string]any)
	if !ok {
		t.Fatalf("RedactValue() type = %T, want map[string]any", RedactValue(input))
	}
	if got := redacted["api_key"]; got != RedactedValue {
		t.Errorf("api_key = %#v, want %q", got, RedactedValue)
	}
	message, ok := redacted["message"].(string)
	if !ok {
		t.Fatalf("message type = %T, want string", redacted["message"])
	}
	if strings.Contains(message, "url-secret") {
		t.Errorf("message leaked URL password: %q", message)
	}
	nestedValues, ok := redacted["nested"].([]any)
	if !ok || len(nestedValues) != 1 {
		t.Fatalf("nested value = %#v, want one-element []any", redacted["nested"])
	}
	nested, ok := nestedValues[0].(map[string]string)
	if !ok {
		t.Fatalf("nested entry type = %T, want map[string]string", nestedValues[0])
	}
	if nested["password"] != RedactedValue || strings.Contains(nested["endpoint"], "query-secret") {
		t.Errorf("nested map was not redacted: %#v", nested)
	}
	lines, ok := redacted["lines"].([]string)
	if !ok || len(lines) != 2 {
		t.Fatalf("lines value = %#v, want two-element []string", redacted["lines"])
	}
	if strings.Contains(lines[0], "bearer-secret") || lines[1] != "safe line" {
		t.Errorf("string slice was not safely redacted: %#v", lines)
	}
	if redacted["count"] != 7 {
		t.Errorf("unknown scalar changed: %#v", redacted["count"])
	}
	if !reflect.DeepEqual(input, original) {
		t.Errorf("RedactValue() mutated input:\n got: %#v\nwant: %#v", input, original)
	}

	const changedValue = "changed"
	redacted["api_key"] = changedValue
	nested["password"] = changedValue
	lines[0] = changedValue
	if !reflect.DeepEqual(input, original) {
		t.Error("redacted output aliases input storage")
	}
}

func TestRedactValueMapStringStringAndUnknownType(t *testing.T) {
	t.Parallel()

	input := map[string]string{
		"private_key": "private-secret",
		"log":         "password=inline-secret",
		"safe":        "value",
	}
	redacted := RedactValue(input).(map[string]string)
	if redacted["private_key"] != RedactedValue || strings.Contains(redacted["log"], "inline-secret") || redacted["safe"] != "value" {
		t.Errorf("RedactValue() = %#v", redacted)
	}
	if input["private_key"] != "private-secret" || input["log"] != "password=inline-secret" {
		t.Errorf("RedactValue() mutated input: %#v", input)
	}

	type sample struct{ Value string }
	value := sample{Value: "unchanged"}
	if got := RedactValue(value); got != value {
		t.Errorf("RedactValue(unknown) = %#v, want %#v", got, value)
	}
}
