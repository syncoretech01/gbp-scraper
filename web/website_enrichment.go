package web

import (
	"context"
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
}

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
		StaleAfterHours:       24,
	}).normalized()

	return options, true, err
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
	}).normalized()

	return err
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
	Force                 bool   `json:"force,omitempty"`
	StaleAfterHours       int    `json:"stale_after_hours,omitempty"`
}

func (options EnrichmentOptions) normalized() (EnrichmentOptions, error) {
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

	return options, nil
}

func (options EnrichmentOptions) crawlerConfig() enrichment.Config {
	return enrichment.Config{
		Timeout:                   time.Duration(options.TimeoutSeconds) * time.Second,
		MaxPages:                  options.MaxPages,
		MaxBodyBytes:              options.MaxBodyBytes,
		MaxRedirects:              options.MaxRedirects,
		MaxInternalLinkChecks:     options.MaxInternalLinkChecks,
		DisableInternalLinkChecks: options.DisableInternalChecks,
		Scope:                     enrichment.CrawlScope(options.Scope),
		CheckMX:                   options.CheckMX,
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
	SocialProfiles          []enrichment.SocialProfile `json:"social_profiles,omitempty"`
	ScreenshotPath          string                     `json:"screenshot_path,omitempty"`
	Error                   string                     `json:"error,omitempty"`
	StartedAt               time.Time                  `json:"started_at"`
	CompletedAt             time.Time                  `json:"completed_at"`
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
	return enrichment.NewCrawler(options.crawlerConfig())
}

// ProcessEnrichmentQueue claims and processes up to limit durable tasks. A
// failed task is recorded and does not stop later tasks from running.
func (s *Service) ProcessEnrichmentQueue(ctx context.Context, limit int) (int, error) {
	return s.processEnrichmentQueue(ctx, limit, defaultWebsiteAnalyzer)
}

func (s *Service) processEnrichmentQueue(
	ctx context.Context,
	limit int,
	factory websiteAnalyzerFactory,
) (int, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 25 {
		limit = 25
	}

	processed := 0
	failures := make([]error, 0)
	// Homepage screenshots share one lazily started browser per queue pass,
	// and a missing driver is reported at most once per pass. Screenshot
	// problems are recorded as audit-log events and never fail a task.
	var capturer screenshotCapturer
	missingDriverLogged := false
	defer func() {
		if closer, ok := capturer.(interface{ Close() }); ok {
			closer.Close()
		}
	}()
	for processed < limit {
		if err := ctx.Err(); err != nil {
			return processed, errors.Join(append(failures, err)...)
		}
		task, found, claimErr := repository.ClaimEnrichmentTask(ctx)
		if claimErr != nil {
			return processed, errors.Join(append(failures, claimErr)...)
		}
		if !found {
			break
		}
		processed++

		analyzer, analyzerErr := factory(task.Options)
		if analyzerErr != nil {
			_ = repository.FinishEnrichmentTask(ctx, task.ID, nil, analyzerErr)
			failures = append(failures, fmt.Errorf("enrichment task %s: %w", task.ID, analyzerErr))
			continue
		}

		startedAt := time.Now().UTC()
		requestBudget := time.Duration(task.Options.TimeoutSeconds) * time.Second *
			time.Duration(task.Options.MaxPages+task.Options.MaxInternalLinkChecks+2)
		if requestBudget < 30*time.Second {
			requestBudget = 30 * time.Second
		}
		if requestBudget > 30*time.Minute {
			requestBudget = 30 * time.Minute
		}
		taskContext, cancelTask := context.WithTimeout(ctx, requestBudget)
		result, analyzeErr := analyzer.Analyze(taskContext, task.WebsiteURL)
		cancelTask()
		completedAt := time.Now().UTC()
		if analyzeErr != nil {
			// A cancelled worker context means the application is shutting down, not
			// that the website failed. Leave the claimed task in its running state so
			// RecoverEnrichmentTasks requeues it on the next start; recording a
			// permanent failure here would silently discard the work instead.
			if ctx.Err() != nil {
				failures = append(failures, fmt.Errorf("enrichment task %s: %w", task.ID, ctx.Err()))

				break
			}
			if errors.Is(analyzeErr, enrichment.ErrUnsafeURL) ||
				errors.Is(analyzeErr, enrichment.ErrUnsupportedScheme) {
				_ = repository.FinishEnrichmentTask(context.WithoutCancel(ctx), task.ID, nil, analyzeErr)
				failures = append(failures, fmt.Errorf("enrichment task %s: %w", task.ID, analyzeErr))

				continue
			}
			// DNS, timeout, TLS, and transport failures are useful website-status
			// evidence. Persist them as an inaccessible audit rather than losing
			// the observation in a task error alone.
			result = enrichment.Result{RequestedURL: task.WebsiteURL, Error: analyzeErr.Error()}
		}

		auditID, storeErr := repository.StoreWebsiteAudit(context.WithoutCancel(ctx), task, result, startedAt, completedAt)
		if storeErr != nil {
			_ = repository.FinishEnrichmentTask(context.WithoutCancel(ctx), task.ID, nil, storeErr)
			failures = append(failures, fmt.Errorf("enrichment task %s: %w", task.ID, storeErr))
			continue
		}
		if finishErr := repository.FinishEnrichmentTask(context.WithoutCancel(ctx), task.ID, &auditID, nil); finishErr != nil {
			failures = append(failures, fmt.Errorf("finish enrichment task %s: %w", task.ID, finishErr))
			continue
		}
		// The homepage screenshot is best-effort extra evidence captured only
		// for genuinely reachable audits. It runs after the task is durably
		// completed so a slow browser can never hold an audit hostage.
		if task.Options.CaptureScreenshot && analyzeErr == nil {
			if !screenshotDriverAvailable() {
				if !missingDriverLogged {
					missingDriverLogged = true
					_ = repository.RecordScreenshotEvent(
						context.WithoutCancel(ctx),
						"screenshot_skipped_no_driver",
						task.ID,
						`{"reason":"the Playwright browser driver is not installed on this host"}`,
					)
				}
			} else {
				if capturer == nil {
					capturer = newScreenshotCapturer()
				}
				s.captureAuditScreenshot(ctx, repository, capturer, task, auditID, result.FinalURL)
			}
		}
		if _, qualityErr := s.RecalculateQuality(context.WithoutCancel(ctx), []string{task.BusinessID}); qualityErr != nil &&
			!errors.Is(qualityErr, ErrQualityScoringUnsupported) {
			failures = append(failures, fmt.Errorf("refresh quality for %s: %w", task.BusinessID, qualityErr))
		}
	}

	return processed, errors.Join(failures...)
}
