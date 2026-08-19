//nolint:testpackage // Package-internal tests exercise deterministic extraction helpers.
package enrichment

import (
	"fmt"
	"strings"
	"testing"
)

// TestSocialProfileURLsDropTrackingParametersAndFragments feeds real-shaped
// social links through page extraction and requires campaign tracking noise
// (utm_*, fbclid, igshid, ref, ref_src, mibextid, s, feature) and fragments to
// disappear while meaningful path and query parts survive.
func TestSocialProfileURLsDropTrackingParametersAndFragments(t *testing.T) {
	t.Parallel()

	links := []string{
		"https://www.facebook.com/AcmePlumbing?utm_source=website&utm_medium=footer&fbclid=IwAR2xY9examplE",
		"https://www.facebook.com/profile.php?id=100063456789012&mibextid=LQQJ4d",
		"https://www.instagram.com/acme.plumbing/?igshid=MzRlODBiNWFlZA%3D%3D&utm_campaign=bio",
		"https://www.linkedin.com/company/acme-plumbing/?ref=footer&utm_source=site&utm_content=header",
		"https://x.com/acmeplumbing?ref_src=twsrc%5Etfw&s=21",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=share&utm_source=newsletter#t=42",
	}
	var builder strings.Builder
	builder.WriteString("<html><body>")
	for _, link := range links {
		fmt.Fprintf(&builder, `<a href=%q>social</a>`, link)
	}
	// Share and intent URLs must still be dropped entirely.
	builder.WriteString(`<a href="https://www.facebook.com/sharer/sharer.php?u=https%3A%2F%2Facme.example">share</a>`)
	builder.WriteString(`<a href="https://twitter.com/intent/tweet?text=hello">tweet</a>`)
	builder.WriteString("</body></html>")

	extracted, err := extractPage([]byte(builder.String()), "https://acme.example/", PageHomepage)
	if err != nil {
		t.Fatalf("extractPage() error = %v", err)
	}

	got := make(map[string]string, len(extracted.socials))
	for _, social := range extracted.socials {
		got[social.url] = social.platform
	}

	want := map[string]string{
		"https://www.facebook.com/AcmePlumbing":                   "facebook",
		"https://www.facebook.com/profile.php?id=100063456789012": "facebook",
		"https://www.instagram.com/acme.plumbing/":                "instagram",
		"https://www.linkedin.com/company/acme-plumbing/":         "linkedin",
		"https://x.com/acmeplumbing":                              "x",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":             "youtube",
	}
	if len(got) != len(want) {
		t.Fatalf("extracted socials = %v, want %v", got, want)
	}
	for url, platform := range want {
		if got[url] != platform {
			t.Fatalf("social %q platform = %q, want %q (all: %v)", url, got[url], platform, got)
		}
	}
}

// TestSocialTrackingParameterMatcher pins the parameter list so a rename does
// not silently start keeping campaign identifiers.
func TestSocialTrackingParameterMatcher(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"utm_source", "utm_medium", "UTM_Campaign", "fbclid", "gclid", "igshid",
		"ref", "ref_src", "mibextid", "share_id", "s", "feature", " ref ",
	} {
		if !isSocialTrackingParameter(key) {
			t.Fatalf("isSocialTrackingParameter(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"id", "v", "list", "tab", "sk", "q"} {
		if isSocialTrackingParameter(key) {
			t.Fatalf("isSocialTrackingParameter(%q) = true, want false", key)
		}
	}
}
