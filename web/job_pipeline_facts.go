package web

import (
	"context"
	"errors"
)

// ErrJobPipelineFactsUnsupported indicates that the active repository cannot
// serve the durable per-stage evidence the live monitor shows.
var ErrJobPipelineFactsUnsupported = errors.New("job pipeline facts are unavailable")

// JobPipelineFacts is the durable, per-job evidence behind the live monitor's
// eight pipeline stages and its job-detail counters.
//
// Every field is a number the workspace already stores: the task plan, the
// redacted worker event log, and the businesses linked to this job through
// business_sources. Nothing here is estimated, so a stage that has not run yet
// reports zero rather than a guess.
type JobPipelineFacts struct {
	// QueriesPlanned and CellsPlanned describe the deterministic task plan:
	// how many distinct searches were generated and how many grid cells they
	// were spread across.
	QueriesPlanned int64 `json:"queries_planned"`
	CellsPlanned   int64 `json:"cells_planned"`

	TasksTotal     int64 `json:"tasks_total"`
	TasksCompleted int64 `json:"tasks_completed"`
	TasksFailed    int64 `json:"tasks_failed"`
	TasksSkipped   int64 `json:"tasks_skipped"`
	TasksRunning   int64 `json:"tasks_running"`
	TasksPending   int64 `json:"tasks_pending"`
	// Attempts is the sum of every attempt across the plan; Retries is the
	// part of it that was a second or later try.
	Attempts int64 `json:"attempts"`
	Retries  int64 `json:"retries"`

	// EventsByType counts the durable worker events for this job, keyed by the
	// worker's own event type ("proxy-failure", "browser-failure", …). It is
	// what makes per-stage health a measurement rather than an impression.
	EventsByType map[string]int64 `json:"events_by_type"`
	// Warnings and Errors are the severity totals across the same events.
	Warnings int64 `json:"warnings"`
	Errors   int64 `json:"errors"`

	// Businesses linked to this job through business_sources.
	UniqueBusinesses int64 `json:"unique_businesses"`
	WithWebsite      int64 `json:"with_website"`
	WithEmail        int64 `json:"with_email"`
	WithPhone        int64 `json:"with_phone"`
	WithSocial       int64 `json:"with_social"`
	// Merged counts businesses this job contributed that deduplication folded
	// into another record.
	Merged int64 `json:"merged"`

	// Website crawl evidence for this job's businesses.
	WebsitesChecked   int64   `json:"websites_checked"`
	WebsitesActive    int64   `json:"websites_active"`
	WebsitesInactive  int64   `json:"websites_inactive"`
	PagesChecked      int64   `json:"pages_checked"`
	AverageResponseMS float64 `json:"average_response_ms"`
	// LastHTTPStatus is the most recently recorded HTTP status for one of this
	// job's websites; zero means no fetch has been recorded.
	LastHTTPStatus int64 `json:"last_http_status"`
	// DomainsChecked is how many distinct registrable-host domains those
	// websites represent. It is never larger than WebsitesChecked, and the gap
	// between the two is exactly the crawling the domain cache can avoid.
	DomainsChecked int64 `json:"domains_checked"`

	// EmailAddresses is how many distinct addresses this job's businesses now
	// hold. It is the number an export can actually deliver, which is why the
	// monitor must never report a different one as "emails discovered".
	EmailAddresses int64 `json:"email_addresses"`
	// EmailCandidates, EmailsAccepted, EmailsRejected, and EmailsRepaired are
	// the extraction funnel summed across this job's website audits. They exist
	// so a run that found candidates but stored none can say why instead of
	// looking broken.
	EmailCandidates int64 `json:"email_candidates"`
	EmailsAccepted  int64 `json:"emails_accepted"`
	EmailsRejected  int64 `json:"emails_rejected"`
	EmailsRepaired  int64 `json:"emails_repaired"`
	// EmailRejectionReasons counts the rejected candidates by named reason,
	// using the enrichment package's rejection constants.
	EmailRejectionReasons map[string]int64 `json:"email_rejection_reasons,omitempty"`

	// Enrichment is the second pipeline stage: the website audits queued after
	// discovery committed its normalized businesses.
	EnrichmentTasksTotal int64 `json:"enrichment_tasks_total"`
	EnrichmentQueued     int64 `json:"enrichment_queued"`
	EnrichmentRunning    int64 `json:"enrichment_running"`
	EnrichmentCompleted  int64 `json:"enrichment_completed"`
	EnrichmentFailed     int64 `json:"enrichment_failed"`
	// EnrichmentReused counts audits served from the domain cache instead of a
	// fresh crawl.
	EnrichmentReused int64 `json:"enrichment_reused"`
	// EnrichmentComplete reports whether every queued audit reached a durable
	// terminal state. A job is only genuinely finished when this is true.
	EnrichmentComplete bool `json:"enrichment_complete"`

	// Unix timestamps for the two stages; zero means "not recorded".
	DiscoveryStartedAt   int64 `json:"discovery_started_at"`
	DiscoveryFinishedAt  int64 `json:"discovery_finished_at"`
	EnrichmentStartedAt  int64 `json:"enrichment_started_at"`
	EnrichmentFinishedAt int64 `json:"enrichment_finished_at"`
	// DiscoveryDurationMS, EnrichmentDurationMS, and TotalDurationMS separate
	// the listing walk from the website audits, and report the end-to-end span
	// the operator actually waited. Before these existed a Fast run reported
	// six seconds for work that took nearly three minutes, because the job
	// clock stopped before enrichment was even queued.
	DiscoveryDurationMS  int64 `json:"discovery_duration_ms"`
	EnrichmentDurationMS int64 `json:"enrichment_duration_ms"`
	TotalDurationMS      int64 `json:"total_duration_ms"`
}

// EnrichmentPending reports how many of this job's website audits have not
// reached a durable terminal state yet.
func (f JobPipelineFacts) EnrichmentPending() int64 {
	return f.EnrichmentQueued + f.EnrichmentRunning
}

// EmailsUnexportable reports how many discovered candidates never became a
// deliverable address. It is the number that has to be explained whenever a
// monitor shows candidates and an export shows nothing.
func (f JobPipelineFacts) EmailsUnexportable() int64 {
	if f.EmailCandidates <= f.EmailsAccepted {
		return 0
	}

	return f.EmailCandidates - f.EmailsAccepted
}

// BlockEvents totals the worker events that mean a request was refused rather
// than merely degraded.
func (f JobPipelineFacts) BlockEvents() int64 {
	var total int64
	for _, name := range []string{"proxy-failure", "rate-limit", "blocked", "captcha"} {
		total += f.EventsByType[name]
	}

	return total
}

// BlockRatePercent expresses BlockEvents as a share of this job's block events
// plus its finished tasks. It returns zero when neither exists, because a run
// with no evidence has no measured block rate.
func (f JobPipelineFacts) BlockRatePercent() float64 {
	blocks := f.BlockEvents()
	finished := f.TasksCompleted + f.TasksFailed + f.TasksSkipped
	denominator := blocks + finished
	if denominator == 0 {
		return 0
	}

	return float64(blocks) / float64(denominator) * 100
}

type jobPipelineFactsRepository interface {
	JobPipelineFacts(context.Context, string) (JobPipelineFacts, error)
}

// SupportsJobPipelineFacts reports whether per-stage evidence can be read.
func (s *Service) SupportsJobPipelineFacts() bool {
	_, ok := s.repo.(jobPipelineFactsRepository)

	return ok
}

// JobPipelineFacts returns the durable per-stage evidence for one job.
func (s *Service) JobPipelineFacts(ctx context.Context, jobID string) (JobPipelineFacts, error) {
	repository, ok := s.repo.(jobPipelineFactsRepository)
	if !ok {
		return JobPipelineFacts{}, ErrJobPipelineFactsUnsupported
	}

	return repository.JobPipelineFacts(ctx, jobID)
}
