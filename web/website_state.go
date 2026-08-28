package web

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// Canonical website states. Every business is in exactly one of them at any
// moment, and the value is derived from durable evidence only: the listed
// URL, the durable enrichment queue, and the immutable website audits.
// Nothing here is a second copy of truth, so the state can never drift away
// from the evidence that produced it.
//
// The taxonomy extends the two vocabularies the domain model already had
// rather than inventing a third:
//
//   - businesses.website_status, the legacy audit transport outcome
//     ('unknown', 'active', 'inactive', 'error'), maps in as NEVER_CHECKED /
//     LIVE / DEAD / ERROR. Those four values keep their stored spelling and
//     their API shape; this layer only names them.
//   - prospect.Status* (NO_WEBSITE, SOCIAL_ONLY, DEAD, PARKED, SSL_BROKEN,
//     FREE_BUILDER, NO_HTTPS, LIVE) stays the finer-grained "what is wrong
//     with this site" taxonomy used for outreach scoring. NO_WEBSITE and
//     SOCIAL_ONLY are shared verbatim.
//
// The two lifecycle states QUEUED and CHECKING are new: neither older
// vocabulary could say "an audit is on its way", which is exactly the
// distinction that made an unaudited website indistinguishable from a
// failed one.
const (
	// WebsiteStateNeverChecked means no website audit has ever completed for
	// this business and none is pending. It never covers an audit that ran
	// and failed - that is ERROR.
	WebsiteStateNeverChecked = "NEVER_CHECKED"
	// WebsiteStateQueued means a durable enrichment task exists and is
	// waiting for the local worker.
	WebsiteStateQueued = "QUEUED"
	// WebsiteStateChecking means the local worker has claimed the task and
	// the HTTP probe is in flight.
	WebsiteStateChecking = "CHECKING"
	// WebsiteStateLive means an audit reached the site and it answered with a
	// non-error HTTP status.
	WebsiteStateLive = "LIVE"
	// WebsiteStateDead means an audit reached the server but the site itself
	// is gone: an HTTP error status (>= 400).
	WebsiteStateDead = "DEAD"
	// WebsiteStateError means the audit ran and could not get an HTTP answer
	// at all - DNS failure, refused connection, TLS failure, or timeout. The
	// observation is real; the site's condition is not established.
	WebsiteStateError = "ERROR"
	// WebsiteStateNoWebsite means the listing carries no website of its own,
	// including a "website" field that points back at Google Maps.
	WebsiteStateNoWebsite = "NO_WEBSITE"
	// WebsiteStateSocialOnly means the only listed URL is a social,
	// messaging, link-in-bio, or review-directory profile. It is never an
	// owned website, so it is never graded for website health and never
	// satisfies a "has a website" requirement.
	WebsiteStateSocialOnly = "SOCIAL_ONLY"
)

// WebsiteStates lists the canonical states in reporting order.
func WebsiteStates() []string {
	return []string{
		WebsiteStateNeverChecked,
		WebsiteStateQueued,
		WebsiteStateChecking,
		WebsiteStateLive,
		WebsiteStateDead,
		WebsiteStateError,
		WebsiteStateNoWebsite,
		WebsiteStateSocialOnly,
	}
}

// websiteStateLabels are the operator-facing names. They deliberately say
// what was observed, never what it implies commercially.
var websiteStateLabels = map[string]string{
	WebsiteStateNeverChecked: "Never checked",
	WebsiteStateQueued:       "Audit queued",
	WebsiteStateChecking:     "Checking now",
	WebsiteStateLive:         "Live",
	WebsiteStateDead:         "Dead",
	WebsiteStateError:        "Audit error",
	WebsiteStateNoWebsite:    "No website",
	WebsiteStateSocialOnly:   "Social profile only",
}

// WebsiteStateLabel returns the operator-facing label for a canonical state,
// falling back to the raw value for anything unrecognised.
func WebsiteStateLabel(state string) string {
	if label, ok := websiteStateLabels[state]; ok {
		return label
	}

	return state
}

// ValidWebsiteState reports whether a value is one of the canonical states.
func ValidWebsiteState(state string) bool {
	_, ok := websiteStateLabels[state]

	return ok
}

// Legacy businesses.website_status values. They are the stored audit
// transport outcome and must keep their spelling: existing saved views,
// filters, exports, and the dashboard all query them.
const (
	legacyWebsiteStatusActive   = "active"
	legacyWebsiteStatusInactive = "inactive"
	legacyWebsiteStatusError    = "error"
)

// WebsiteStateEvidence is everything the resolver reads. Every field is
// durable: the listing columns, the newest open enrichment task, and the
// newest completed audit (the business's own, or the one recorded for
// another business on the same domain).
type WebsiteStateEvidence struct {
	// Website is businesses.website as listed.
	Website string
	// MapsURL is the listing's own Google Maps URL, used to detect a website
	// field that just points back at Maps.
	MapsURL string
	// LegacyStatus is businesses.website_status.
	LegacyStatus string
	// TaskState is the state of the newest open enrichment task ("queued" or
	// "running"); empty when none is open.
	TaskState string
	// AuditCompleted reports that at least one audit finished.
	AuditCompleted bool
	// AuditReachable, AuditStatusCode and AuditError come from the newest
	// completed audit.
	AuditReachable  bool
	AuditStatusCode int
	AuditError      string
	// AuditCompletedAt is when that audit finished.
	AuditCompletedAt time.Time
	// EvidenceDomain is set when the audit evidence was recorded for a
	// different business on the same domain and is being reused.
	EvidenceDomain string
}

// WebsiteStateResolution is the resolved state plus the evidence trail that
// produced it.
type WebsiteStateResolution struct {
	State string `json:"state"`
	Label string `json:"label"`
	// Reason is a plain-language explanation of why this state, phrased as an
	// observation rather than a conclusion.
	Reason string `json:"reason"`
	// Platform names the social network when State is SOCIAL_ONLY.
	Platform string `json:"platform,omitempty"`
	// Domain is the extracted domain of the listed URL, using the Engine's
	// exact domain rule.
	Domain string `json:"domain,omitempty"`
	// Checked reports that a real audit observation backs this state.
	Checked bool `json:"checked"`
	// Auditable reports that an HTTP probe of this URL would produce
	// meaningful website-health evidence. It is false for NO_WEBSITE and
	// SOCIAL_ONLY: there is nothing of the owner's to measure.
	Auditable bool `json:"auditable"`
	// ReusedFromDomain reports that the observation came from another
	// business on the same domain.
	ReusedFromDomain string `json:"reused_from_domain,omitempty"`
	// CheckedAt is when the backing audit completed.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// ResolveWebsiteState maps durable evidence to exactly one canonical state.
// Precedence, first match wins:
//
//  1. No listed URL, or a URL that is really the Maps listing itself ->
//     NO_WEBSITE.
//  2. A recognised social/profile host -> SOCIAL_ONLY, whether or not the
//     page responds. Renting a page on someone else's network is not owning
//     a website, so this outranks every audit outcome.
//  3. An open enrichment task: running -> CHECKING, queued -> QUEUED.
//  4. A completed audit: no HTTP answer at all (status code 0) -> ERROR;
//     answered 2xx/3xx -> LIVE; anything else (>= 400, or unreachable with a
//     status) -> DEAD.
//  5. No audit row, but businesses.website_status still records an older
//     audit outcome -> LIVE / DEAD / ERROR accordingly. Evidence whose audit
//     row was pruned is still evidence.
//  6. Otherwise -> NEVER_CHECKED.
//
// Rule 4 is the whole point of the ERROR state: a site that does not answer
// has been checked, and reporting it as "never checked" both hides real work
// and invites the same probe forever.
func ResolveWebsiteState(evidence WebsiteStateEvidence) WebsiteStateResolution {
	website := strings.TrimSpace(evidence.Website)
	resolution := WebsiteStateResolution{Domain: prospect.DomainFromWebsite(website)}

	if website == "" {
		return finishWebsiteState(resolution, WebsiteStateNoWebsite,
			"The listing carries no website URL")
	}

	if status, conclusive := prospect.Classify(prospect.Signals{
		WebsiteURL: website,
		MapsURL:    evidence.MapsURL,
	}); conclusive && status == prospect.StatusNoWebsite {
		return finishWebsiteState(resolution, WebsiteStateNoWebsite,
			"The website field points back at the Google Maps listing, not at a site of the business")
	}

	if platform := prospect.SocialPlatform(website); platform != "" {
		resolution.Platform = platform

		return finishWebsiteState(resolution, WebsiteStateSocialOnly,
			fmt.Sprintf("The only listed URL is a social profile (%s), not a website the business owns", platform))
	}

	// An audit already in flight for this domain answers for every listing on
	// it, so a second probe is never queued for the same site.
	taskSuffix := ""
	if evidence.EvidenceDomain != "" {
		taskSuffix = fmt.Sprintf(" for %s", evidence.EvidenceDomain)
		resolution.ReusedFromDomain = evidence.EvidenceDomain
	}
	switch strings.ToLower(strings.TrimSpace(evidence.TaskState)) {
	case "running":
		return finishWebsiteState(resolution, WebsiteStateChecking,
			"A website audit is running right now"+taskSuffix)
	case "queued":
		return finishWebsiteState(resolution, WebsiteStateQueued,
			"A website audit is queued and waiting for the local worker"+taskSuffix)
	}
	resolution.ReusedFromDomain = ""

	if evidence.AuditCompleted {
		return finishAuditedWebsiteState(resolution, evidence)
	}

	switch strings.ToLower(strings.TrimSpace(evidence.LegacyStatus)) {
	case legacyWebsiteStatusActive:
		resolution.Checked = true

		return finishWebsiteState(resolution, WebsiteStateLive,
			"A previous audit recorded the site as reachable")
	case legacyWebsiteStatusInactive:
		resolution.Checked = true

		return finishWebsiteState(resolution, WebsiteStateDead,
			"A previous audit recorded the site as not serving")
	case legacyWebsiteStatusError:
		resolution.Checked = true

		return finishWebsiteState(resolution, WebsiteStateError,
			"A previous audit failed to reach the site")
	}

	return finishWebsiteState(resolution, WebsiteStateNeverChecked,
		"No website audit has ever run for this listing")
}

// finishAuditedWebsiteState resolves rule 4: a completed audit exists.
func finishAuditedWebsiteState(
	resolution WebsiteStateResolution,
	evidence WebsiteStateEvidence,
) WebsiteStateResolution {
	resolution.Checked = true
	if !evidence.AuditCompletedAt.IsZero() {
		completedAt := evidence.AuditCompletedAt.UTC()
		resolution.CheckedAt = &completedAt
	}
	resolution.ReusedFromDomain = evidence.EvidenceDomain

	suffix := ""
	if evidence.EvidenceDomain != "" {
		suffix = fmt.Sprintf(" (evidence reused from the audit of %s)", evidence.EvidenceDomain)
	}

	switch {
	case evidence.AuditStatusCode == 0:
		detail := "the site gave no HTTP answer"
		if trimmed := strings.TrimSpace(evidence.AuditError); trimmed != "" {
			detail = firstSentence(trimmed)
		}

		return finishWebsiteState(resolution, WebsiteStateError,
			fmt.Sprintf("The audit ran and could not reach the site: %s%s", detail, suffix))
	case evidence.AuditReachable && evidence.AuditStatusCode < 400:
		return finishWebsiteState(resolution, WebsiteStateLive,
			fmt.Sprintf("The audit reached the site and it answered HTTP %d%s", evidence.AuditStatusCode, suffix))
	default:
		return finishWebsiteState(resolution, WebsiteStateDead,
			fmt.Sprintf("The audit reached the server and the site answered HTTP %d%s", evidence.AuditStatusCode, suffix))
	}
}

// finishWebsiteState fills in the derived label and auditability.
func finishWebsiteState(resolution WebsiteStateResolution, state, reason string) WebsiteStateResolution {
	resolution.State = state
	resolution.Label = WebsiteStateLabel(state)
	resolution.Reason = reason
	resolution.Auditable = state != WebsiteStateNoWebsite && state != WebsiteStateSocialOnly

	return resolution
}

// WebsiteStateForResult resolves the canonical state from the three columns a
// normalized result row already carries.
//
// It is the presentation-layer entry point: a table row does not know about
// the enrichment queue, so a website with an audit pending shows as
// NEVER_CHECKED here until the audit lands. Everything else - no website, a
// social profile, live, dead, and a failed audit - is exact, which is what
// turns the Results column from one undifferentiated "unknown" into a state
// an operator can act on.
func WebsiteStateForResult(website, mapsURL, legacyStatus string) WebsiteStateResolution {
	return ResolveWebsiteState(WebsiteStateEvidence{
		Website:      website,
		MapsURL:      mapsURL,
		LegacyStatus: legacyStatus,
	})
}

// WebsiteStateFromLegacyStatus names a stored businesses.website_status value
// in the canonical vocabulary without any other evidence. It is the narrow
// helper for callers that hold only that one column.
func WebsiteStateFromLegacyStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case legacyWebsiteStatusActive:
		return WebsiteStateLive
	case legacyWebsiteStatusInactive:
		return WebsiteStateDead
	case legacyWebsiteStatusError:
		return WebsiteStateError
	default:
		return WebsiteStateNeverChecked
	}
}

// firstSentence trims a transport error down to something an operator can
// read in a table cell without a whole stack of wrapping.
func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.Index(text, ": "); index > 0 && index < 60 {
		text = strings.TrimSpace(text[index+2:])
	}
	if len(text) > 160 {
		text = strings.TrimSpace(text[:160]) + "..."
	}

	return text
}

// WebsiteHealthEvidence is the audit-only input to the website health score.
// Every field comes from one completed audit; nothing here is inferred from
// the listing.
type WebsiteHealthEvidence struct {
	State            string
	StatusCode       int
	Reachable        bool
	HTTPS            bool
	TLSValid         bool
	CertificateError string
	ResponseMS       int64
	Parked           bool
	ComingSoon       bool
	Placeholder      bool
	MixedContent     bool
	BrokenLinks      int
	LinksChecked     int
	PageTitle        string
	MetaDescription  string
	MobileViewport   bool
	CopyrightYear    int
	CurrentYear      int
	CompletedAt      time.Time
}

// WebsiteHealthCheck is one explainable component of the health score.
type WebsiteHealthCheck struct {
	Check   string  `json:"check"`
	Points  float64 `json:"points"`
	Maximum float64 `json:"maximum"`
	Passed  bool    `json:"passed"`
	Detail  string  `json:"detail"`
}

// WebsiteHealthReport is score #2 of the three the application keeps apart:
// the condition of the site the business actually owns.
//
// It is NOT the prospect score (how attractive the business is to call) and
// NOT the record confidence (how much the stored row can be trusted). It
// exists only when an audit reached an owned site, and it is absent - never
// zero, never a default - for every other state. A missing health score is
// information; a fabricated one is not.
type WebsiteHealthReport struct {
	BusinessID string               `json:"business_id"`
	State      string               `json:"state"`
	Available  bool                 `json:"available"`
	Score      float64              `json:"score,omitempty"`
	Grade      string               `json:"grade,omitempty"`
	Checks     []WebsiteHealthCheck `json:"checks,omitempty"`
	Reason     string               `json:"reason"`
	Domain     string               `json:"domain,omitempty"`
	MeasuredAt *time.Time           `json:"measured_at,omitempty"`
	// RuleVersion identifies the health model so a stored score can be told
	// apart from one produced by a later model.
	RuleVersion string `json:"rule_version"`
}

// WebsiteHealthRuleVersion is the immutable identifier of the health model
// below. Bump it whenever a check or a weight changes.
const WebsiteHealthRuleVersion = "website-health-v1"

// websiteHealthResponseTargetMS is the response time a site has to beat for
// full speed credit; three times that scores zero.
const websiteHealthResponseTargetMS = 1500

// ScoreWebsiteHealth grades one audited site out of 100.
//
// It refuses to produce a number unless an audit actually reached an owned
// site: NEVER_CHECKED, QUEUED, CHECKING, NO_WEBSITE, SOCIAL_ONLY and ERROR
// all return Available=false with the reason. DEAD returns a real score,
// because "the server answered with an error page" is a measured condition
// rather than an unknown one.
func ScoreWebsiteHealth(businessID string, evidence WebsiteHealthEvidence) WebsiteHealthReport {
	report := WebsiteHealthReport{
		BusinessID:  businessID,
		State:       evidence.State,
		RuleVersion: WebsiteHealthRuleVersion,
	}
	if !evidence.CompletedAt.IsZero() {
		measuredAt := evidence.CompletedAt.UTC()
		report.MeasuredAt = &measuredAt
	}

	switch evidence.State {
	case WebsiteStateNoWebsite:
		report.Reason = "No website is listed, so there is no site to grade"

		return report
	case WebsiteStateSocialOnly:
		report.Reason = "The listed URL is a social profile. Website health grades a site the business owns, " +
			"and a profile page on someone else's network is not one"

		return report
	case WebsiteStateNeverChecked:
		report.Reason = "No audit has run yet, so the condition of this site is unknown"

		return report
	case WebsiteStateQueued, WebsiteStateChecking:
		report.Reason = "An audit is in progress; the condition of this site is not established yet"

		return report
	case WebsiteStateError:
		report.Reason = "The audit could not get an answer from the site, so its condition is unknown. " +
			"An unreachable site is not the same as a bad site"

		return report
	}

	checks := make([]WebsiteHealthCheck, 0, 10)
	add := func(name string, points, maximum float64, passed bool, detail string) {
		checks = append(checks, WebsiteHealthCheck{
			Check: name, Points: roundHealth(points), Maximum: maximum, Passed: passed, Detail: detail,
		})
	}

	serving := evidence.Reachable && evidence.StatusCode > 0 && evidence.StatusCode < 400
	add("serving", boolPoints(serving, 25), 25, serving,
		fmt.Sprintf("The homepage answered HTTP %d", evidence.StatusCode))

	switch {
	case !evidence.HTTPS:
		add("https", 0, 15, false, "The site is served over plain HTTP, so browsers mark it not secure")
	case evidence.CertificateError != "" || !evidence.TLSValid:
		add("https", 0, 15, false, "HTTPS is offered but the certificate does not validate")
	default:
		add("https", 15, 15, true, "HTTPS is served with a valid certificate")
	}

	realContent := !(evidence.Parked || evidence.ComingSoon || evidence.Placeholder)
	contentDetail := "The homepage serves real content"
	switch {
	case evidence.Parked:
		contentDetail = "The homepage is a registrar or host parking page"
	case evidence.ComingSoon:
		contentDetail = "The homepage is a coming-soon placeholder"
	case evidence.Placeholder:
		contentDetail = "The homepage is an unfinished builder placeholder"
	}
	add("real_content", boolPoints(realContent, 20), 20, realContent, contentDetail)

	speedPoints := 0.0
	speedDetail := "Response time was not measured"
	fast := false
	if evidence.ResponseMS > 0 {
		ratio := float64(evidence.ResponseMS) / float64(websiteHealthResponseTargetMS)
		switch {
		case ratio <= 1:
			speedPoints, fast = 10, true
		case ratio >= 3:
			speedPoints = 0
		default:
			speedPoints = 10 * (3 - ratio) / 2
		}
		speedDetail = fmt.Sprintf("Answered in %d ms; target %d ms", evidence.ResponseMS, websiteHealthResponseTargetMS)
	}
	add("speed", speedPoints, 10, fast, speedDetail)

	linksOK := evidence.BrokenLinks == 0
	linkDetail := "No internal link was checked"
	if evidence.LinksChecked > 0 {
		linkDetail = fmt.Sprintf("%d of %d checked internal links are broken",
			evidence.BrokenLinks, evidence.LinksChecked)
	}
	add("internal_links", boolPoints(linksOK, 10), 10, linksOK, linkDetail)

	add("mixed_content", boolPoints(!evidence.MixedContent, 5), 5, !evidence.MixedContent,
		chooseHealthDetail(evidence.MixedContent,
			"The page loads insecure sub-resources over HTTP",
			"No insecure sub-resource was loaded"))

	metadata := 0
	if strings.TrimSpace(evidence.PageTitle) != "" {
		metadata++
	}
	if strings.TrimSpace(evidence.MetaDescription) != "" {
		metadata++
	}
	add("metadata", 5*float64(metadata)/2, 5, metadata == 2,
		fmt.Sprintf("%d of 2 basic metadata tags (title, description) are present", metadata))

	add("mobile_viewport", boolPoints(evidence.MobileViewport, 5), 5, evidence.MobileViewport,
		chooseHealthDetail(evidence.MobileViewport,
			"The homepage declares a mobile viewport",
			"The homepage declares no mobile viewport"))

	freshness := 5.0
	freshDetail := "No copyright year was found, so maintenance age is unknown"
	freshPassed := false
	if evidence.CopyrightYear > 0 && evidence.CurrentYear > 0 {
		age := evidence.CurrentYear - evidence.CopyrightYear
		switch {
		case age <= 1:
			freshness, freshPassed = 5, true
			freshDetail = fmt.Sprintf("Copyright footer reads %d", evidence.CopyrightYear)
		case age >= 4:
			freshness = 0
			freshDetail = fmt.Sprintf("Copyright footer is stuck at %d, %d years stale", evidence.CopyrightYear, age)
		default:
			freshness = 5 * float64(4-age) / 3
			freshDetail = fmt.Sprintf("Copyright footer reads %d, %d years old", evidence.CopyrightYear, age)
		}
	}
	add("maintenance", freshness, 5, freshPassed, freshDetail)

	total := 0.0
	for _, check := range checks {
		total += check.Points
	}
	report.Available = true
	report.Checks = checks
	report.Score = roundHealth(math.Min(100, math.Max(0, total)))
	report.Grade = websiteHealthGrade(report.Score)
	report.Reason = "Graded from the completed website audit"
	if report.MeasuredAt != nil {
		report.Reason = fmt.Sprintf("Graded from the website audit completed on %s",
			report.MeasuredAt.UTC().Format(time.RFC3339))
	}

	return report
}

// websiteHealthGrade buckets a health score. The bands are fixed, not
// configurable: unlike the prospect score, health is a measurement and its
// grade must mean the same thing in every workspace.
func websiteHealthGrade(score float64) string {
	switch {
	case score >= 85:
		return "healthy"
	case score >= 65:
		return "fair"
	case score >= 40:
		return "weak"
	default:
		return "poor"
	}
}

func boolPoints(condition bool, points float64) float64 {
	if condition {
		return points
	}

	return 0
}

func chooseHealthDetail(condition bool, yes, no string) string {
	if condition {
		return yes
	}

	return no
}

func roundHealth(value float64) float64 {
	return math.Round(value*100) / 100
}
