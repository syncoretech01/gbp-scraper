package prospect

import "strings"

// Website status taxonomy. Every classified business ends up in exactly
// one of these buckets; they are ordered here roughly from "hottest
// prospect" to "healthy website".
const (
	StatusNoWebsite   = "NO_WEBSITE"
	StatusSocialOnly  = "SOCIAL_ONLY"
	StatusDead        = "DEAD"
	StatusParked      = "PARKED"
	StatusSSLBroken   = "SSL_BROKEN"
	StatusFreeBuilder = "FREE_BUILDER"
	StatusNoHTTPS     = "NO_HTTPS"
	StatusLive        = "LIVE"
)

// Signals is everything the classifier and scorer look at for a single
// business. This package defines the shape; other layers (the scraper,
// the website auditor, enrichment) fill it in. Zero values mean
// "unknown / not observed" unless noted otherwise.
type Signals struct {
	WebsiteURL string // website URL from the GBP listing ("" = none listed)
	MapsURL    string // URL of the Google Maps listing itself

	AuditPerformed bool // a live website audit has been run
	Reachable      bool // audit: the site answered at all
	StatusCode     int  // audit: final HTTP status (0 = no response)

	HTTPS            bool   // audit: site served over HTTPS
	TLSValid         bool   // audit: certificate chain validated
	CertificateError string // audit: certificate error text ("" = none)

	Parked      bool // audit: registrar/host parking page detected
	ComingSoon  bool // audit: "coming soon" page detected
	Placeholder bool // audit: builder placeholder page detected

	ContentBytes int64 // audit: response body size; 0 = unknown

	Rating      float64 // GBP star rating (0 = unknown)
	ReviewCount int64   // GBP review count

	PhonePresent bool // listing has a phone number
	EmailFound   bool // enrichment found an email address
	MXPresent    bool // enrichment: email domain has MX records

	HasAdsTag     bool // audit: any analytics/pixel tracker detected
	CopyrightYear int  // audit: footer copyright year; 0 = unknown
	CurrentYear   int  // caller-supplied "now" year (keeps Classify/Score pure)
}

// socialHosts are hosts that stand in for a real website. Matched as a
// case-insensitive host suffix with any leading "www." ignored, so
// "m.facebook.com" and "www.instagram.com" both match.
var socialHosts = []string{
	"facebook.com",
	"instagram.com",
	"linktr.ee",
	"yelp.com",
	"tiktok.com",
	"twitter.com",
	"x.com",
	"linkedin.com",
	"youtube.com",
	"wa.me",
	"whatsapp.com",
}

// freeBuilderHosts are free website-builder hosts. Matched the same way
// as socialHosts (host suffix, case-insensitive, "www." ignored).
var freeBuilderHosts = []string{
	"wixsite.com",
	"weebly.com",
	"godaddysites.com",
	"square.site",
	"business.site",
	"wordpress.com",
	"blogspot.com",
	"webnode.page",
	"mystrikingly.com",
	"carrd.co",
}

// googleBusinessSiteSuffix is the host suffix of the retired Google
// Business Profile one-page websites. Google shut business.site pages
// down in March 2024, so a listing that still points at one signals an
// owner who has not touched their web presence for 2+ years.
const googleBusinessSiteSuffix = "business.site"

// mapsHosts are hosts that identify a Google Maps listing URL rather
// than a real business website (goo.gl also covers maps.app.goo.gl via
// suffix matching).
var mapsHosts = []string{
	"goo.gl",
	"g.page",
	"maps.google.com",
}

// IsGoogleBusinessSite reports whether the website is a retired Google
// business.site page — the named "owner absent 2+ years" edge case.
// These classify as FREE_BUILDER, but callers (and Score) can use this
// to surface the stronger, more specific reason.
func IsGoogleBusinessSite(website string) bool {
	host, _ := splitHostPath(website)

	return hostMatchesSuffix(host, googleBusinessSiteSuffix)
}

// Classify maps the collected Signals to a website status.
//
// Precedence (first match wins):
//
//  1. Empty/blank WebsiteURL -> NO_WEBSITE.
//  2. WebsiteURL that is really the Maps listing itself — equal to
//     MapsURL, or hosted on google.com/maps, maps.google.com, goo.gl
//     (incl. maps.app.goo.gl) or g.page -> NO_WEBSITE (the named
//     "website field points back at Google Maps" edge case).
//  3. Social host (facebook.com, instagram.com, linktr.ee, yelp.com,
//     tiktok.com, twitter.com, x.com, linkedin.com, youtube.com,
//     wa.me, whatsapp.com) -> SOCIAL_ONLY.
//  4. Free website-builder host (wixsite.com, weebly.com,
//     godaddysites.com, square.site, business.site, wordpress.com,
//     blogspot.com, webnode.page, mystrikingly.com, carrd.co)
//     -> FREE_BUILDER. business.site is the named "owner absent 2+
//     years" edge case; see IsGoogleBusinessSite.
//  5. No audit performed yet -> ("", false): the classifier cannot
//     conclude anything beyond the static classes above, so callers
//     keep the previous value. Classes 1-4 never need an audit and are
//     always conclusive.
//  6. Audit says unreachable, HTTP status >= 400, or no response at
//     all (StatusCode == 0 with the audit performed) -> DEAD.
//  7. Certificate error reported, or HTTPS without a valid TLS chain
//     -> SSL_BROKEN.
//  8. Parked, coming-soon or placeholder page detected, or a known
//     body size under 500 bytes -> PARKED.
//  9. Served without HTTPS -> NO_HTTPS.
//  10. Otherwise -> LIVE.
//
// The second return value is false only for case 5; every other
// outcome is conclusive.
func Classify(signals Signals) (status string, conclusive bool) {
	website := strings.TrimSpace(signals.WebsiteURL)
	if website == "" {
		return StatusNoWebsite, true
	}

	if isMapsListingURL(website, signals.MapsURL) {
		return StatusNoWebsite, true
	}

	host, _ := splitHostPath(website)

	for _, social := range socialHosts {
		if hostMatchesSuffix(host, social) {
			return StatusSocialOnly, true
		}
	}

	for _, builder := range freeBuilderHosts {
		if hostMatchesSuffix(host, builder) {
			return StatusFreeBuilder, true
		}
	}

	if !signals.AuditPerformed {
		return "", false
	}

	if !signals.Reachable || signals.StatusCode >= 400 || signals.StatusCode == 0 {
		return StatusDead, true
	}

	if signals.CertificateError != "" || (signals.HTTPS && !signals.TLSValid) {
		return StatusSSLBroken, true
	}

	if signals.Parked || signals.ComingSoon || signals.Placeholder ||
		(signals.ContentBytes > 0 && signals.ContentBytes < 500) {
		return StatusParked, true
	}

	if !signals.HTTPS {
		return StatusNoHTTPS, true
	}

	return StatusLive, true
}

// DomainFromWebsite implements the Engine's exact domain rule:
// trim surrounding whitespace, lowercase, strip a leading "http://" or
// "https://", strip a leading "www.", and keep everything before the
// first "/". An empty input yields "".
func DomainFromWebsite(website string) string {
	s := strings.ToLower(strings.TrimSpace(website))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")

	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}

	return s
}

// isMapsListingURL reports whether the website URL is really the Google
// Maps listing itself (precedence rule 2 of Classify).
func isMapsListingURL(website, mapsURL string) bool {
	w := strings.ToLower(strings.TrimSpace(website))
	m := strings.ToLower(strings.TrimSpace(mapsURL))

	if m != "" && w == m {
		return true
	}

	host, path := splitHostPath(website)

	for _, mh := range mapsHosts {
		if hostMatchesSuffix(host, mh) {
			return true
		}
	}

	if hostMatchesSuffix(host, "google.com") && strings.HasPrefix(path, "maps") {
		return true
	}

	return false
}

// splitHostPath extracts a normalized (lowercase, scheme-less,
// "www."-less, port/query/fragment-less) host and the path after it.
// Unlike DomainFromWebsite it also cuts ports and query strings off the
// host, because it feeds fuzzy host matching rather than the Engine's
// exact domain rule.
func splitHostPath(website string) (host, path string) {
	s := strings.ToLower(strings.TrimSpace(website))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")

	host = s
	if i := strings.Index(s, "/"); i >= 0 {
		host = s[:i]
		path = s[i+1:]
	}

	for _, sep := range []string{":", "?", "#"} {
		if i := strings.Index(host, sep); i >= 0 {
			host = host[:i]
		}
	}

	return host, path
}

// hostMatchesSuffix reports whether host is suffix itself or a
// subdomain of it ("m.facebook.com" matches "facebook.com", while
// "notfacebook.com" does not).
func hostMatchesSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}
