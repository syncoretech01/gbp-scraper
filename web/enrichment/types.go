// Package enrichment provides bounded, local website and contact analysis.
package enrichment

import (
	"context"
	"net"
	"net/http"
	"time"
)

// CrawlScope controls which discovered pages are fetched after the homepage.
type CrawlScope string

const (
	// ScopeHomepage fetches only the supplied URL.
	ScopeHomepage CrawlScope = "homepage"
	// ScopeContact also fetches the best same-origin contact page found.
	ScopeContact CrawlScope = "homepage_contact"
	// ScopeContactAbout also fetches the best contact and about pages found.
	ScopeContactAbout CrawlScope = "homepage_contact_about"
)

// PageKind identifies why a page was selected for crawling.
type PageKind string

const (
	PageHomepage PageKind = "homepage"
	PageContact  PageKind = "contact"
	PageAbout    PageKind = "about"
)

// ExtractionMethod records how a contact value was found.
type ExtractionMethod string

const (
	MethodMailto         ExtractionMethod = "mailto"
	MethodVisibleText    ExtractionMethod = "visible_text"
	MethodDeobfuscated   ExtractionMethod = "deobfuscated_text"
	MethodStructuredData ExtractionMethod = "structured_data"
	MethodTelephoneLink  ExtractionMethod = "telephone_link"
	MethodSocialLink     ExtractionMethod = "social_link"
	MethodMicrodata      ExtractionMethod = "microdata"
)

// Resolver is the DNS dependency used by URL safety and email analysis.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// ResolverFunc adapts a function into a Resolver.
type ResolverFunc func(context.Context, string) ([]net.IPAddr, error)

// LookupIPAddr implements Resolver.
func (f ResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

// MXLookup is the optional DNS MX dependency used for local email checks.
type MXLookup interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// MXLookupFunc adapts a function into an MXLookup.
type MXLookupFunc func(context.Context, string) ([]*net.MX, error)

// LookupMX implements MXLookup.
func (f MXLookupFunc) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return f(ctx, name)
}

// Config bounds network use and selects optional analysis behavior.
type Config struct {
	Resolver                  Resolver
	MXLookup                  MXLookup
	HTTPClient                *http.Client
	Timeout                   time.Duration
	MaxPages                  int
	MaxBodyBytes              int64
	MaxRedirects              int
	MaxInternalLinkChecks     int
	DisableInternalLinkChecks bool
	Scope                     CrawlScope
	CheckMX                   bool
	UnsafeAllowPrivateNetwork bool
	UserAgent                 string
	DisposableDomains         []string
	// HostGate bounds how many requests this crawl may have in flight against
	// one host, and how closely spaced they may be. It is shared across a whole
	// worker pool so politeness is a property of the host, not of one task. A
	// nil gate keeps the historical behaviour: no per-host limit at all.
	HostGate HostGate
	// URLPatterns is the operator's control over which same-origin candidate
	// URLs the crawl may fetch beyond its entry page. The zero value filters
	// nothing and reproduces the built-in heuristic exactly.
	URLPatterns URLPatternSet
}

// Source records the page and method that produced a value.
type Source struct {
	PageURL  string           `json:"page_url"`
	PageKind PageKind         `json:"page_kind"`
	Method   ExtractionMethod `json:"method"`
}

// MXStatus describes the result of an optional local MX lookup.
type MXStatus string

const (
	MXNotChecked MXStatus = "not_checked"
	MXPresent    MXStatus = "present"
	MXMissing    MXStatus = "missing"
	MXError      MXStatus = "error"
)

// Email is an extracted address with local validation and ranking signals.
type Email struct {
	Address        string   `json:"address"`
	Domain         string   `json:"domain"`
	ValidSyntax    bool     `json:"valid_syntax"`
	Role           string   `json:"role,omitempty"`
	RoleAddress    bool     `json:"role_address"`
	PersonalLikely bool     `json:"personal_likely"`
	Disposable     bool     `json:"disposable"`
	MXStatus       MXStatus `json:"mx_status"`
	MXRecords      []string `json:"mx_records,omitempty"`
	MXError        string   `json:"mx_error,omitempty"`
	Relevance      int      `json:"relevance"`
	Rank           int      `json:"rank"`
	Sources        []Source `json:"sources"`
}

// EmailCandidate is an address and provenance pair accepted by AnalyzeEmails.
type EmailCandidate struct {
	Address string `json:"address"`
	Source  Source `json:"source"`
}

// EmailAnalysisConfig controls standalone local email assessment.
type EmailAnalysisConfig struct {
	WebsiteURL        string
	CheckMX           bool
	MXLookup          MXLookup
	DisposableDomains []string
}

// Phone is a normalized phone-like value and its extraction sources.
type Phone struct {
	Value   string   `json:"value"`
	Sources []Source `json:"sources"`
}

// PostalAddress is a postal address found on a crawled page, with whichever
// structured parts the source declared.
type PostalAddress struct {
	Value      string   `json:"value"`
	Street     string   `json:"street,omitempty"`
	Locality   string   `json:"locality,omitempty"`
	Region     string   `json:"region,omitempty"`
	PostalCode string   `json:"postal_code,omitempty"`
	Country    string   `json:"country,omitempty"`
	Sources    []Source `json:"sources"`
}

// ContentAudit records which basic quality elements the audited site actually
// shows. It is presence evidence, not a judgement: every field says only that
// the crawl did or did not find the element within its bounded page budget.
type ContentAudit struct {
	ContactPage     bool `json:"contact_page"`
	AboutPage       bool `json:"about_page"`
	VisiblePhone    bool `json:"visible_phone"`
	VisibleEmail    bool `json:"visible_email"`
	PostalAddress   bool `json:"postal_address"`
	SocialLinks     bool `json:"social_links"`
	PageTitle       bool `json:"page_title"`
	MetaDescription bool `json:"meta_description"`
	MobileViewport  bool `json:"mobile_viewport"`
	// Present and Checked report how many of the audited elements were found
	// out of how many were looked for, so a caller can show the ratio without
	// re-deriving it.
	Present int `json:"present"`
	Checked int `json:"checked"`
}

// SocialProfile is a recognized social link and its extraction sources.
type SocialProfile struct {
	Platform string   `json:"platform"`
	URL      string   `json:"url"`
	Sources  []Source `json:"sources"`
}

// Detection is a signature-based technology or tracker signal.
type Detection struct {
	Name       string   `json:"name"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence,omitempty"`
}

// Redirect records one HTTP redirect hop.
type Redirect struct {
	From       string `json:"from"`
	To         string `json:"to"`
	StatusCode int    `json:"status_code"`
}

// LinkCheck is the bounded result of checking one same-origin link.
type LinkCheck struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Broken     bool   `json:"broken"`
	Error      string `json:"error,omitempty"`
}

// PageResult contains bounded analysis for one crawled HTML page.
type PageResult struct {
	RequestedURL    string        `json:"requested_url"`
	FinalURL        string        `json:"final_url,omitempty"`
	Kind            PageKind      `json:"kind"`
	StatusCode      int           `json:"status_code,omitempty"`
	ResponseTime    time.Duration `json:"response_time"`
	SizeBytes       int64         `json:"size_bytes"`
	BodyTruncated   bool          `json:"body_truncated"`
	ContentType     string        `json:"content_type,omitempty"`
	Title           string        `json:"title,omitempty"`
	MetaDescription string        `json:"meta_description,omitempty"`
	Language        string        `json:"language,omitempty"`
	MobileViewport  bool          `json:"mobile_viewport"`
	MixedContent    bool          `json:"mixed_content"`
	CopyrightYear   int           `json:"copyright_year,omitempty"`
	OldCopyright    bool          `json:"old_copyright"`
	Redirects       []Redirect    `json:"redirects,omitempty"`
	Error           string        `json:"error,omitempty"`
}

// AuditVersion identifies the extraction and hygiene rules one stored audit
// was produced with. It is what makes the domain cache safe: evidence written
// by an older, worse extractor is never reused, so a fix to extraction reaches
// every business the next time its domain is audited.
//
// Version 1 is every audit written before the version existed. Version 2 adds
// element-boundary text separation and email hygiene.
const AuditVersion = 2

// CacheProvenance records that an audit was served from the domain cache
// rather than crawled. It is present only on a reused audit, so an audit that
// was genuinely fetched serialises exactly as it always did.
type CacheProvenance struct {
	// ReusedFromAuditID is the audit whose evidence was copied.
	ReusedFromAuditID int64 `json:"reused_from_audit_id"`
	// Domain is the normalized domain the two businesses share.
	Domain string `json:"domain"`
	// ObservedAt is when the original crawl completed. It is the honest
	// "last checked" time for this evidence, not the reuse time.
	ObservedAt time.Time `json:"observed_at"`
}

// Result contains a bounded website, contact, and quality analysis.
type Result struct {
	RequestedURL     string        `json:"requested_url"`
	FinalURL         string        `json:"final_url,omitempty"`
	Reachable        bool          `json:"reachable"`
	StatusCode       int           `json:"status_code,omitempty"`
	HTTPS            bool          `json:"https"`
	TLSValid         bool          `json:"tls_valid"`
	CertificateError string        `json:"certificate_error,omitempty"`
	ResponseTime     time.Duration `json:"response_time"`
	RedirectChain    []Redirect    `json:"redirect_chain,omitempty"`
	Pages            []PageResult  `json:"pages"`
	Emails           []Email       `json:"emails"`
	// EmailFunnel is the candidate accounting behind Emails: how many
	// candidates the crawl found, how many survived hygiene, and why the rest
	// did not. It makes "emails discovered" and "emails exported" reconcilable
	// instead of merely different numbers.
	EmailFunnel             EmailFunnel     `json:"email_funnel"`
	Phones                  []Phone         `json:"phones"`
	Addresses               []PostalAddress `json:"addresses"`
	SocialProfiles          []SocialProfile `json:"social_profiles"`
	ContentAudit            ContentAudit    `json:"content_audit"`
	Technologies            []Detection     `json:"technologies"`
	Trackers                []Detection     `json:"trackers"`
	InternalLinksChecked    int             `json:"internal_links_checked"`
	BrokenInternalLinkCount int             `json:"broken_internal_link_count"`
	BrokenInternalLinks     []LinkCheck     `json:"broken_internal_links,omitempty"`
	MixedContent            bool            `json:"mixed_content"`
	Parked                  bool            `json:"parked"`
	ComingSoon              bool            `json:"coming_soon"`
	Placeholder             bool            `json:"placeholder"`
	TemplateIndicators      []string        `json:"template_indicators,omitempty"`
	// URLPatterns records the operator-configured crawl URL patterns that were
	// in force for this run and the candidate URLs they kept out. It is nil
	// whenever no pattern was configured, so an unfiltered audit serialises
	// exactly as it always did.
	URLPatterns *URLPatternEvidence `json:"url_patterns,omitempty"`
	// AuditVersion is the extraction ruleset that produced this result. Zero
	// means the result predates versioning.
	AuditVersion int `json:"audit_version,omitempty"`
	// Cache is set when this evidence was reused from another business's audit
	// of the same domain instead of being crawled again.
	Cache *CacheProvenance `json:"cache,omitempty"`
	Error string           `json:"error,omitempty"`
}
