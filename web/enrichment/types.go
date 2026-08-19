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

// Result contains a bounded website, contact, and quality analysis.
type Result struct {
	RequestedURL            string          `json:"requested_url"`
	FinalURL                string          `json:"final_url,omitempty"`
	Reachable               bool            `json:"reachable"`
	StatusCode              int             `json:"status_code,omitempty"`
	HTTPS                   bool            `json:"https"`
	TLSValid                bool            `json:"tls_valid"`
	CertificateError        string          `json:"certificate_error,omitempty"`
	ResponseTime            time.Duration   `json:"response_time"`
	RedirectChain           []Redirect      `json:"redirect_chain,omitempty"`
	Pages                   []PageResult    `json:"pages"`
	Emails                  []Email         `json:"emails"`
	Phones                  []Phone         `json:"phones"`
	SocialProfiles          []SocialProfile `json:"social_profiles"`
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
	Error                   string          `json:"error,omitempty"`
}
