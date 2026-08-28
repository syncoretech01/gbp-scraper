package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

var (
	// ErrEnrichmentUnsupported indicates that durable local enrichment is not
	// available for the configured repository.
	ErrEnrichmentUnsupported = errors.New("website enrichment is unavailable")
	// ErrInvalidEnrichment identifies an invalid or unbounded enrichment request.
	ErrInvalidEnrichment = errors.New("invalid website enrichment request")
	// ErrEnrichmentTaskNotFound indicates that an enrichment task is unknown.
	ErrEnrichmentTaskNotFound = errors.New("enrichment task not found")
)

const (
	maximumEnrichmentBatch         = 500
	maximumEnrichmentPages         = 10
	maximumEnrichmentTimeout       = 60
	maximumEnrichmentBodyBytes     = 10 << 20
	maximumEnrichmentRedirects     = 20
	maximumEnrichmentInternalLinks = 100
)

// Preclassify coercions: the lightweight single-page probe keeps every
// network dimension tighter than the full crawl allows.
const (
	preclassifyDefaultTimeoutSeconds = 10
	preclassifyMaximumTimeoutSeconds = 15
	preclassifyDefaultBodyBytes      = 256 << 10
	preclassifyMaximumBodyBytes      = 512 << 10
	preclassifyMaximumRedirects      = 5
)

// Adaptive timeout wiring. The window is the number of recent audits read
// back per task; it is small on purpose because the policy only needs a
// recent trend, and a wider window would pay more I/O per claimed task.
const (
	adaptiveTimeoutHistoryWindow = 10
	adaptiveTimeoutEventAction   = "enrichment_timeout_adapted"
)

// JobEnrichmentOptions is the compatible, nested scrape configuration used
// to queue full website analysis after normalized Maps rows are committed.
type JobEnrichmentOptions struct {
	Website               bool   `json:"website"`
	Emails                bool   `json:"emails"`
	SocialProfiles        bool   `json:"social_profiles"`
	Scope                 string `json:"scope,omitempty"`
	MaxPages              int    `json:"max_pages,omitempty"`
	TimeoutSeconds        int    `json:"timeout_seconds,omitempty"`
	MaxBodyBytes          int64  `json:"max_body_bytes,omitempty"`
	MaxRedirects          int    `json:"max_redirects,omitempty"`
	MaxInternalLinkChecks int    `json:"max_internal_link_checks,omitempty"`
	DisableInternalChecks bool   `json:"disable_internal_link_checks,omitempty"`
	CheckMX               bool   `json:"check_mx,omitempty"`
	CaptureScreenshot     bool   `json:"capture_screenshot,omitempty"`
	Preclassify           bool   `json:"preclassify,omitempty"`
	AdaptiveTimeout       bool   `json:"adaptive_timeout,omitempty"`
	// StaleAfterHours re-audits a business only when its last completed
	// audit is older than this many hours. Zero keeps the historical
	// 24-hour default, so an older saved job is unchanged.
	StaleAfterHours int `json:"stale_after_hours,omitempty"`
	// ForceReaudit ignores StaleAfterHours and re-audits every business the
	// job observed.
	ForceReaudit bool `json:"force_reaudit,omitempty"`
	// IncludeURLPatterns and ExcludeURLPatterns steer which same-origin pages
	// the local audit visits beyond the homepage. They are glob-style path
	// patterns, never regular expressions; see enrichment.URLPatternSet for
	// the grammar and bounds. Both empty keeps the built-in heuristic exactly
	// as it has always behaved.
	IncludeURLPatterns []string `json:"include_url_patterns,omitempty"`
	ExcludeURLPatterns []string `json:"exclude_url_patterns,omitempty"`
}

// DefaultEnrichmentStaleHours is the staleness window a job uses when none is
// configured. It is the value every job used before the window became an
// option, so leaving it unset changes nothing.
const DefaultEnrichmentStaleHours = 24

// MaximumEnrichmentStaleHours bounds the configurable staleness window at one
// year so a stored job can never carry an absurd value.
const MaximumEnrichmentStaleHours = 8760

// EnrichmentOptionsForJob translates both the nested configuration and the
// legacy Email flag. Existing saved jobs therefore keep their historical
// behavior while gaining the durable audit after normalized import.
func EnrichmentOptionsForJob(data JobData) (EnrichmentOptions, bool, error) {
	if data.Enrichment == nil {
		if !data.Email {
			return EnrichmentOptions{}, false, nil
		}

		options, err := (EnrichmentOptions{CheckMX: false}).normalized()
		return options, true, err
	}
	if !data.Enrichment.Website {
		return EnrichmentOptions{}, false, nil
	}
	options, err := (EnrichmentOptions{
		Scope:                 data.Enrichment.Scope,
		MaxPages:              data.Enrichment.MaxPages,
		TimeoutSeconds:        data.Enrichment.TimeoutSeconds,
		MaxBodyBytes:          data.Enrichment.MaxBodyBytes,
		MaxRedirects:          data.Enrichment.MaxRedirects,
		MaxInternalLinkChecks: data.Enrichment.MaxInternalLinkChecks,
		DisableInternalChecks: data.Enrichment.DisableInternalChecks,
		CheckMX:               data.Enrichment.CheckMX && data.Enrichment.Emails,
		CaptureScreenshot:     data.Enrichment.CaptureScreenshot,
		Preclassify:           data.Enrichment.Preclassify,
		AdaptiveTimeout:       data.Enrichment.AdaptiveTimeout,
		StaleAfterHours:       jobEnrichmentStaleHours(data),
		Force:                 data.Enrichment.ForceReaudit,
		IncludeURLPatterns:    data.Enrichment.IncludeURLPatterns,
		ExcludeURLPatterns:    data.Enrichment.ExcludeURLPatterns,
	}).normalized()

	return options, true, err
}

// jobEnrichmentStaleHours resolves the staleness window one job's local
// website audit uses. An unset window keeps the historical 24 hours.
func jobEnrichmentStaleHours(data JobData) int {
	if data.Enrichment == nil {
		return DefaultEnrichmentStaleHours
	}
	if data.Enrichment.StaleAfterHours <= 0 {
		return DefaultEnrichmentStaleHours
	}
	if data.Enrichment.StaleAfterHours > MaximumEnrichmentStaleHours {
		return MaximumEnrichmentStaleHours
	}

	return data.Enrichment.StaleAfterHours
}

// Validate checks locally enforceable bounds without requiring enrichment to
// be enabled. This keeps older serialized JobData compatible.
func (options JobEnrichmentOptions) Validate() error {
	_, err := (EnrichmentOptions{
		Scope:                 options.Scope,
		MaxPages:              options.MaxPages,
		TimeoutSeconds:        options.TimeoutSeconds,
		MaxBodyBytes:          options.MaxBodyBytes,
		MaxRedirects:          options.MaxRedirects,
		MaxInternalLinkChecks: options.MaxInternalLinkChecks,
		DisableInternalChecks: options.DisableInternalChecks,
		CheckMX:               options.CheckMX,
		Preclassify:           options.Preclassify,
		AdaptiveTimeout:       options.AdaptiveTimeout,
		IncludeURLPatterns:    options.IncludeURLPatterns,
		ExcludeURLPatterns:    options.ExcludeURLPatterns,
	}).normalized()
	if err != nil {
		return err
	}
	if options.StaleAfterHours < 0 || options.StaleAfterHours > MaximumEnrichmentStaleHours {
		return fmt.Errorf("re-audit window must be between 0 and %d hours", MaximumEnrichmentStaleHours)
	}

	return nil
}

// EnrichmentOptions controls one bounded website audit. Zero values receive
// conservative defaults; every network and parsing dimension has a hard cap.
type EnrichmentOptions struct {
	Scope                 string `json:"scope,omitempty"`
	MaxPages              int    `json:"max_pages,omitempty"`
	TimeoutSeconds        int    `json:"timeout_seconds,omitempty"`
	MaxBodyBytes          int64  `json:"max_body_bytes,omitempty"`
	MaxRedirects          int    `json:"max_redirects,omitempty"`
	MaxInternalLinkChecks int    `json:"max_internal_link_checks,omitempty"`
	DisableInternalChecks bool   `json:"disable_internal_link_checks,omitempty"`
	CheckMX               bool   `json:"check_mx,omitempty"`
	CaptureScreenshot     bool   `json:"capture_screenshot,omitempty"`
	Preclassify           bool   `json:"preclassify,omitempty"`
	Force                 bool   `json:"force,omitempty"`
	StaleAfterHours       int    `json:"stale_after_hours,omitempty"`
	// IncludeURLPatterns and ExcludeURLPatterns are the operator's glob-style
	// path filters over the same-origin pages one audit may visit beyond the
	// homepage. Excludes beat includes, an empty include list means "anything
	// not excluded", and both empty keeps exactly the built-in heuristic. They
	// are matched by enrichment.URLPatternSet, which is deliberately a glob
	// rather than a regular expression so no configured pattern can hang a
	// crawl. They round-trip into the immutable per-audit options evidence.
	IncludeURLPatterns []string `json:"include_url_patterns,omitempty"`
	ExcludeURLPatterns []string `json:"exclude_url_patterns,omitempty"`
	// AdaptiveTimeout opts one enrichment run into spending its time budget
	// where it pays: observed per-host latency and failure history shorten the
	// per-request timeout, never lengthen it. Absent (the default) reproduces
	// today's behavior exactly, so TimeoutSeconds is used verbatim.
	AdaptiveTimeout bool `json:"adaptive_timeout,omitempty"`
	// resolvedTimeout is the per-run budget the adaptive policy chose for one
	// claimed task. It is deliberately unexported: it is runtime state, not a
	// requested option, so it never reaches the persisted options JSON, an API
	// response, or a saved job definition. Zero means "use TimeoutSeconds".
	resolvedTimeout time.Duration
}

// requestTimeout returns the per-request budget the analyzer must use: the
// adaptive value when one was resolved for this run, and otherwise exactly the
// configured TimeoutSeconds. The policy is clamped to the configured ceiling,
// so this never returns more than TimeoutSeconds.
func (options EnrichmentOptions) requestTimeout() time.Duration {
	if options.resolvedTimeout > 0 {
		return options.resolvedTimeout
	}

	return time.Duration(options.TimeoutSeconds) * time.Second
}

// PreclassifyProfile returns the coerced lightweight profile used by the
// single-page website pre-classifier probe.
func PreclassifyProfile() EnrichmentOptions {
	options, _ := (EnrichmentOptions{Preclassify: true}).normalized()

	return options
}

func (options EnrichmentOptions) normalized() (EnrichmentOptions, error) {
	if options.Preclassify {
		// The pre-classifier probe coerces the lightweight single-page
		// profile regardless of every other requested value.
		options.Scope = string(enrichment.ScopeHomepage)
		options.MaxPages = 1
		options.DisableInternalChecks = true
		options.MaxInternalLinkChecks = 0
		options.CheckMX = false
		options.CaptureScreenshot = false
		// The probe fetches only the homepage, so it selects no supporting
		// page and checks no internal link: crawl URL patterns have nothing to
		// act on and are cleared rather than recorded as if they applied. See
		// enrichment.preclassifyConfig for the full reasoning.
		options.IncludeURLPatterns = nil
		options.ExcludeURLPatterns = nil

		if options.TimeoutSeconds <= 0 {
			options.TimeoutSeconds = preclassifyDefaultTimeoutSeconds
		}
		if options.TimeoutSeconds > preclassifyMaximumTimeoutSeconds {
			options.TimeoutSeconds = preclassifyMaximumTimeoutSeconds
		}
		if options.MaxBodyBytes <= 0 {
			options.MaxBodyBytes = preclassifyDefaultBodyBytes
		}
		if options.MaxBodyBytes > preclassifyMaximumBodyBytes {
			options.MaxBodyBytes = preclassifyMaximumBodyBytes
		}
		if options.MaxRedirects <= 0 || options.MaxRedirects > preclassifyMaximumRedirects {
			options.MaxRedirects = preclassifyMaximumRedirects
		}
	}

	options.Scope = strings.TrimSpace(strings.ToLower(options.Scope))
	if options.Scope == "" {
		options.Scope = string(enrichment.ScopeContactAbout)
	}
	switch enrichment.CrawlScope(options.Scope) {
	case enrichment.ScopeHomepage, enrichment.ScopeContact, enrichment.ScopeContactAbout:
	default:
		return EnrichmentOptions{}, fmt.Errorf("%w: unsupported crawl scope %q", ErrInvalidEnrichment, options.Scope)
	}

	if options.MaxPages == 0 {
		options.MaxPages = 3
	}
	if options.MaxPages < 1 || options.MaxPages > maximumEnrichmentPages {
		return EnrichmentOptions{}, fmt.Errorf("%w: maximum pages must be between 1 and %d", ErrInvalidEnrichment, maximumEnrichmentPages)
	}
	if options.TimeoutSeconds == 0 {
		options.TimeoutSeconds = 10
	}
	if options.TimeoutSeconds < 1 || options.TimeoutSeconds > maximumEnrichmentTimeout {
		return EnrichmentOptions{}, fmt.Errorf("%w: timeout must be between 1 and %d seconds", ErrInvalidEnrichment, maximumEnrichmentTimeout)
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = 2 << 20
	}
	if options.MaxBodyBytes < 1024 || options.MaxBodyBytes > maximumEnrichmentBodyBytes {
		return EnrichmentOptions{}, fmt.Errorf("%w: maximum body size must be between 1024 and %d bytes", ErrInvalidEnrichment, maximumEnrichmentBodyBytes)
	}
	if options.MaxRedirects == 0 {
		options.MaxRedirects = 10
	}
	if options.MaxRedirects < 1 || options.MaxRedirects > maximumEnrichmentRedirects {
		return EnrichmentOptions{}, fmt.Errorf("%w: maximum redirects must be between 1 and %d", ErrInvalidEnrichment, maximumEnrichmentRedirects)
	}
	if options.MaxInternalLinkChecks == 0 && !options.DisableInternalChecks {
		options.MaxInternalLinkChecks = 10
	}
	if options.MaxInternalLinkChecks < 0 || options.MaxInternalLinkChecks > maximumEnrichmentInternalLinks {
		return EnrichmentOptions{}, fmt.Errorf("%w: internal link checks must be between 0 and %d", ErrInvalidEnrichment, maximumEnrichmentInternalLinks)
	}
	if options.StaleAfterHours == 0 {
		options.StaleAfterHours = 24
	}
	if options.StaleAfterHours < 0 || options.StaleAfterHours > 24*365 {
		return EnrichmentOptions{}, fmt.Errorf("%w: stale age must be between 0 and 8760 hours", ErrInvalidEnrichment)
	}

	patterns, err := options.urlPatterns().Normalized()
	if err != nil {
		return EnrichmentOptions{}, fmt.Errorf("%w: %w", ErrInvalidEnrichment, err)
	}

	options.IncludeURLPatterns = patterns.Include
	options.ExcludeURLPatterns = patterns.Exclude

	return options, nil
}

// urlPatterns bundles the two configured pattern lists into the matcher the
// crawler consumes.
func (options EnrichmentOptions) urlPatterns() enrichment.URLPatternSet {
	return enrichment.URLPatternSet{
		Include: options.IncludeURLPatterns,
		Exclude: options.ExcludeURLPatterns,
	}
}

func (options EnrichmentOptions) crawlerConfig() enrichment.Config {
	return enrichment.Config{
		Timeout:                   options.requestTimeout(),
		MaxPages:                  options.MaxPages,
		MaxBodyBytes:              options.MaxBodyBytes,
		MaxRedirects:              options.MaxRedirects,
		MaxInternalLinkChecks:     options.MaxInternalLinkChecks,
		DisableInternalLinkChecks: options.DisableInternalChecks,
		Scope:                     enrichment.CrawlScope(options.Scope),
		CheckMX:                   options.CheckMX,
		URLPatterns:               options.urlPatterns(),
	}
}

// EnrichmentTask is a durable, restart-safe unit of website work.
type EnrichmentTask struct {
	ID          string            `json:"id"`
	BusinessID  string            `json:"business_id"`
	JobID       string            `json:"job_id,omitempty"`
	WebsiteURL  string            `json:"website_url"`
	State       string            `json:"state"`
	RequestedBy string            `json:"requested_by"`
	Options     EnrichmentOptions `json:"options"`
	Attempts    int               `json:"attempts"`
	AuditID     *int64            `json:"audit_id,omitempty"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// WebsiteAuditView is an immutable persisted website-analysis run.
type WebsiteAuditView struct {
	ID                      int64                      `json:"id"`
	BusinessID              string                     `json:"business_id"`
	TaskID                  string                     `json:"task_id,omitempty"`
	RequestedURL            string                     `json:"requested_url"`
	FinalURL                string                     `json:"final_url,omitempty"`
	Reachable               bool                       `json:"reachable"`
	StatusCode              int                        `json:"status_code,omitempty"`
	HTTPS                   bool                       `json:"https"`
	TLSValid                bool                       `json:"tls_valid"`
	CertificateError        string                     `json:"certificate_error,omitempty"`
	ResponseTimeMS          int64                      `json:"response_time_ms"`
	RedirectChain           []enrichment.Redirect      `json:"redirect_chain,omitempty"`
	InternalLinksChecked    int                        `json:"internal_links_checked"`
	BrokenInternalLinkCount int                        `json:"broken_internal_link_count"`
	BrokenInternalLinks     []enrichment.LinkCheck     `json:"broken_internal_links,omitempty"`
	MixedContent            bool                       `json:"mixed_content"`
	Parked                  bool                       `json:"parked"`
	ComingSoon              bool                       `json:"coming_soon"`
	Placeholder             bool                       `json:"placeholder"`
	TemplateIndicators      []string                   `json:"template_indicators,omitempty"`
	Technologies            []enrichment.Detection     `json:"technologies,omitempty"`
	Trackers                []enrichment.Detection     `json:"trackers,omitempty"`
	Pages                   []enrichment.PageResult    `json:"pages,omitempty"`
	Emails                  []enrichment.Email         `json:"emails,omitempty"`
	Phones                  []enrichment.Phone         `json:"phones,omitempty"`
	Addresses               []enrichment.PostalAddress `json:"addresses,omitempty"`
	SocialProfiles          []enrichment.SocialProfile `json:"social_profiles,omitempty"`
	ContentAudit            enrichment.ContentAudit    `json:"content_audit"`
	// EmailFunnel explains the Emails list: how many candidates the crawl
	// found, how many survived hygiene, and why the rest did not. An audit
	// stored before the funnel existed reports zeros.
	EmailFunnel enrichment.EmailFunnel `json:"email_funnel"`
	// AuditVersion is the extraction ruleset that produced this evidence.
	AuditVersion int `json:"audit_version,omitempty"`
	// Cache is present when this evidence was reused from another business's
	// audit of the same page instead of being crawled again.
	Cache *enrichment.CacheProvenance `json:"cache,omitempty"`
	// URLPatterns reports the crawl URL patterns that were in force for this
	// run and the candidate URLs they kept out. It is absent for a run without
	// patterns and for audits stored before patterns existed.
	URLPatterns         *enrichment.URLPatternEvidence `json:"url_patterns,omitempty"`
	ScreenshotPath      string                         `json:"screenshot_path,omitempty"`
	ErrorScreenshotPath string                         `json:"error_screenshot_path,omitempty"`
	Error               string                         `json:"error,omitempty"`
	StartedAt           time.Time                      `json:"started_at"`
	CompletedAt         time.Time                      `json:"completed_at"`
}

// EnrichmentBatch reports which durable tasks were created or reused.
type EnrichmentBatch struct {
	Tasks   []EnrichmentTask `json:"tasks"`
	Queued  int              `json:"queued"`
	Skipped int              `json:"skipped"`
}

type enrichmentRepository interface {
	QueueBusinessEnrichment(context.Context, []string, EnrichmentOptions, string, string) (EnrichmentBatch, error)
	QueueJobEnrichment(context.Context, string, EnrichmentOptions) (EnrichmentBatch, error)
	RecoverEnrichmentTasks(context.Context) (int, error)
	ClaimEnrichmentTask(context.Context) (EnrichmentTask, bool, error)
	StoreWebsiteAudit(context.Context, EnrichmentTask, enrichment.Result, time.Time, time.Time) (int64, error)
	FinishEnrichmentTask(context.Context, string, *int64, error) error
	ListEnrichmentTasks(context.Context, int) ([]EnrichmentTask, error)
	WebsiteAuditHistory(context.Context, string, int) ([]WebsiteAuditView, error)
	AttachAuditScreenshot(ctx context.Context, auditID int64, relativePath string) error
	RecordScreenshotEvent(ctx context.Context, action string, entityID string, details string) error
}

// enrichmentHistoryRepository is the optional readback used by the adaptive
// timeout policy. It is deliberately separate from enrichmentRepository: a
// repository that cannot serve observed history simply keeps the configured
// timeout instead of failing the task.
type enrichmentHistoryRepository interface {
	WebsiteLatencyHistory(
		ctx context.Context,
		businessID string,
		websiteURL string,
		limit int,
	) (enrichment.SiteHistory, error)
	RecordEnrichmentEvent(ctx context.Context, action string, entityID string, details string) error
}

// adaptiveTimeoutEvidence is the operator-visible record of one adaptation. It
// is written to the audit log, never to an existing API response shape, and
// carries only the public host plus the two budgets.
type adaptiveTimeoutEvidence struct {
	BusinessID   string `json:"business_id,omitempty"`
	Host         string `json:"host,omitempty"`
	LastStatus   string `json:"last_status,omitempty"`
	Observations int    `json:"observations"`
	ConfiguredMS int64  `json:"configured_timeout_ms"`
	AdaptedMS    int64  `json:"adapted_timeout_ms"`
	Preclassify  bool   `json:"preclassify,omitempty"`
}

// RecoverEnrichmentTasks returns tasks left running by an interrupted process
// to the durable FIFO queue. The local worker calls this once during startup.
func (s *Service) RecoverEnrichmentTasks(ctx context.Context) (int, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return 0, err
	}

	return repository.RecoverEnrichmentTasks(ctx)
}

func (s *Service) enrichmentRepository() (enrichmentRepository, error) {
	repository, ok := s.repo.(enrichmentRepository)
	if !ok {
		return nil, ErrEnrichmentUnsupported
	}

	return repository, nil
}

// EnrichmentAvailable reports whether the backing repository supports durable
// enrichment tasks and audit persistence.
func (s *Service) EnrichmentAvailable() bool {
	_, err := s.enrichmentRepository()

	return err == nil
}

// QueueBusinessEnrichment queues a bounded set of explicit business IDs.
func (s *Service) QueueBusinessEnrichment(
	ctx context.Context,
	ids []string,
	options EnrichmentOptions,
	requestedBy string,
) (EnrichmentBatch, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return EnrichmentBatch{}, err
	}
	if len(ids) == 0 || len(ids) > maximumEnrichmentBatch {
		return EnrichmentBatch{}, fmt.Errorf("%w: choose between 1 and %d businesses", ErrInvalidEnrichment, maximumEnrichmentBatch)
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return EnrichmentBatch{}, fmt.Errorf("%w: invalid business ID", ErrInvalidEnrichment)
		}
	}
	options, err = options.normalized()
	if err != nil {
		return EnrichmentBatch{}, err
	}
	requestedBy = strings.TrimSpace(requestedBy)
	if requestedBy == "" {
		requestedBy = "local_api"
	}

	return repository.QueueBusinessEnrichment(ctx, ids, options, requestedBy, "")
}

// QueueJobEnrichment queues website audits for normalized businesses observed
// by one completed scrape job.
func (s *Service) QueueJobEnrichment(
	ctx context.Context,
	jobID string,
	options EnrichmentOptions,
) (EnrichmentBatch, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return EnrichmentBatch{}, err
	}
	options, err = options.normalized()
	if err != nil {
		return EnrichmentBatch{}, err
	}

	return repository.QueueJobEnrichment(ctx, strings.TrimSpace(jobID), options)
}

// ListEnrichmentTasks returns recent task state without exposing response
// bodies or any network credentials.
func (s *Service) ListEnrichmentTasks(ctx context.Context, limit int) ([]EnrichmentTask, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	return repository.ListEnrichmentTasks(ctx, limit)
}

// WebsiteAuditHistory returns bounded immutable analysis evidence.
func (s *Service) WebsiteAuditHistory(ctx context.Context, businessID string, limit int) ([]WebsiteAuditView, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	return repository.WebsiteAuditHistory(ctx, businessID, limit)
}

type websiteAnalyzer interface {
	Analyze(context.Context, string) (enrichment.Result, error)
}

type websiteAnalyzerFactory func(EnrichmentOptions) (websiteAnalyzer, error)

func defaultWebsiteAnalyzer(options EnrichmentOptions) (websiteAnalyzer, error) {
	if options.Preclassify {
		return preclassifyAnalyzer{config: options.crawlerConfig()}, nil
	}

	return enrichment.NewCrawler(options.crawlerConfig())
}

// preclassifyAnalyzer satisfies websiteAnalyzer with the cheap single-page
// DNS/TLS/HTTP probe instead of the full bounded crawl.
type preclassifyAnalyzer struct {
	config enrichment.Config
}

// Analyze runs the single-page pre-classification probe.
func (analyzer preclassifyAnalyzer) Analyze(ctx context.Context, rawURL string) (enrichment.Result, error) {
	return enrichment.Preclassify(ctx, rawURL, analyzer.config)
}

// adaptEnrichmentTimeout resolves the per-request budget for one claimed task
// and returns the options the analyzer should be built from.
//
// It is a strict no-op unless the task explicitly opted in, so an existing
// queued task behaves byte-identically to today. When it does apply, the
// adapted budget is only ever shorter than the configured one: the policy
// clamps to the configured ceiling, and a value that did not shrink is
// discarded so the analyzer keeps the configured timeout verbatim. Neither
// concurrency nor request counts change, so the resource envelope can only
// shrink.
//
// Every failure path here — an unsupported repository, a history read error,
// an unusable policy answer — falls back to the configured timeout. Adaptation
// is an optimization and must never be able to fail a website audit.
func adaptEnrichmentTimeout(
	ctx context.Context,
	repository enrichmentRepository,
	task EnrichmentTask,
) EnrichmentOptions {
	options := task.Options
	if !options.AdaptiveTimeout {
		return options
	}
	history, supported := repository.(enrichmentHistoryRepository)
	if !supported {
		return options
	}
	ceiling := options.requestTimeout()
	if ceiling <= 0 {
		return options
	}
	observed, err := history.WebsiteLatencyHistory(
		ctx, task.BusinessID, task.WebsiteURL, adaptiveTimeoutHistoryWindow,
	)
	if err != nil {
		return options
	}
	adapted := enrichment.AdaptiveTimeout(ceiling, observed)
	if adapted <= 0 || adapted >= ceiling {
		return options
	}
	options.resolvedTimeout = adapted

	evidence, marshalErr := json.Marshal(adaptiveTimeoutEvidence{
		BusinessID:   task.BusinessID,
		Host:         observed.Host,
		LastStatus:   observed.LastStatus,
		Observations: len(observed.Observations),
		ConfiguredMS: ceiling.Milliseconds(),
		AdaptedMS:    adapted.Milliseconds(),
		Preclassify:  options.Preclassify,
	})
	if marshalErr == nil {
		_ = history.RecordEnrichmentEvent(ctx, adaptiveTimeoutEventAction, task.ID, string(evidence))
	}

	return options
}

// ProcessEnrichmentQueue claims and processes up to limit durable tasks. A
// failed task is recorded and does not stop later tasks from running.
//
// The pass itself runs on the bounded website-enrichment pool; see
// enrichment_pipeline.go for its capacity, per-host politeness, and the
// domain-audit cache it consults before crawling.
func (s *Service) ProcessEnrichmentQueue(ctx context.Context, limit int) (int, error) {
	return s.processEnrichmentQueue(ctx, limit, nil)
}
