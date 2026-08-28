package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrWebsiteStateUnsupported indicates that the configured repository
	// cannot resolve or sweep canonical website state.
	ErrWebsiteStateUnsupported = errors.New("website state tracking is unavailable")
	// ErrInvalidWebsiteStateRequest identifies an invalid website-state or
	// audit-sweep request.
	ErrInvalidWebsiteStateRequest = errors.New("invalid website state request")
	// ErrWebsiteAuditSweepNotFound indicates that a sweep ID is unknown.
	ErrWebsiteAuditSweepNotFound = errors.New("website audit sweep not found")
)

// websiteAuditSweepRequestedByPrefix marks every enrichment task a sweep
// created. The durable queue is therefore the sweep's own state: progress is
// a count over enrichment_tasks, restart recovery is the queue's existing
// recovery, and no second queue or bookkeeping table exists to fall out of
// step with it.
const websiteAuditSweepRequestedByPrefix = "website_sweep:"

// websiteAuditSweepAction is the audit_logs action that records one sweep's
// creation, its scope, and everything it deliberately skipped.
const websiteAuditSweepAction = "website_audit_sweep_started"

// maximumWebsiteAuditSweep bounds one sweep so a single click can never queue
// an unbounded amount of outbound HTTP work. A larger backlog is drained by
// starting the sweep again once the queue empties.
const maximumWebsiteAuditSweep = 5000

// defaultWebsiteAuditSweep is the batch size used when a request names none.
const defaultWebsiteAuditSweep = 1000

// WebsiteStateCount is one canonical state and how many businesses are in it.
type WebsiteStateCount struct {
	State string `json:"state"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// WebsiteStateSummary is the auditable breakdown of one scope. Every
// website-bearing business is in exactly one bucket, so the counts always add
// up to Total and no row can hide in an implicit "other".
type WebsiteStateSummary struct {
	JobID string `json:"job_id,omitempty"`
	// Counts covers every canonical state in reporting order, including the
	// states with a zero count: an absent bucket reads as "not measured".
	Counts []WebsiteStateCount `json:"counts"`
	Total  int64               `json:"total"`
	// WithWebsite counts listings that carry a URL of their own, which
	// deliberately excludes SOCIAL_ONLY: a rented profile page is not a
	// website the business has.
	WithWebsite int64 `json:"with_website"`
	// NeverChecked is the size of the backlog a sweep would work through.
	NeverChecked int64 `json:"never_checked"`
	// Pending is QUEUED plus CHECKING.
	Pending int64 `json:"pending"`
	// ReusedDomainEvidence counts businesses whose state comes from another
	// business's audit of the same domain.
	ReusedDomainEvidence int64 `json:"reused_domain_evidence"`
}

// WebsiteAuditSweepRequest asks for a durable bulk audit of every business in
// one of the requested canonical states.
type WebsiteAuditSweepRequest struct {
	// JobID limits the sweep to one job's businesses; empty means the whole
	// workspace.
	JobID string `json:"job_id,omitempty"`
	// States selects which canonical states to work on. Empty means
	// NEVER_CHECKED, the backlog the operator actually sees.
	States []string `json:"states,omitempty"`
	// Limit bounds how many tasks this sweep creates.
	Limit int `json:"limit,omitempty"`
	// Options is the bounded audit profile every queued task carries.
	Options EnrichmentOptions `json:"options,omitempty"`
	// RequestedBy records who asked, for the audit log.
	RequestedBy string `json:"requested_by,omitempty"`
}

// WebsiteAuditSweepProgress is derived entirely from the durable queue, so it
// survives a restart and can never claim work the queue does not hold.
type WebsiteAuditSweepProgress struct {
	Total     int64   `json:"total"`
	Queued    int64   `json:"queued"`
	Running   int64   `json:"running"`
	Completed int64   `json:"completed"`
	Failed    int64   `json:"failed"`
	Done      bool    `json:"done"`
	Percent   float64 `json:"percent"`
}

// WebsiteAuditSweep is one bulk audit run: what it targeted, what it queued,
// what it deliberately skipped, and how far the durable queue has got.
type WebsiteAuditSweep struct {
	ID          string    `json:"id"`
	JobID       string    `json:"job_id,omitempty"`
	States      []string  `json:"states"`
	RequestedBy string    `json:"requested_by"`
	CreatedAt   time.Time `json:"created_at"`
	// UniqueDomains is how many distinct domains the sweep found in scope.
	UniqueDomains int `json:"unique_domains"`
	// Queued is how many tasks it created: one per unique domain.
	Queued int `json:"queued"`
	// SkippedDuplicateDomain counts businesses that share a domain with a
	// queued one. They reuse that domain's evidence instead of paying for a
	// second identical probe.
	SkippedDuplicateDomain int `json:"skipped_duplicate_domain"`
	// SkippedFresh counts domains whose last audit is inside the freshness
	// window.
	SkippedFresh int `json:"skipped_fresh"`
	// SkippedAlreadyQueued counts businesses that already had an open task.
	SkippedAlreadyQueued int `json:"skipped_already_queued"`
	// SkippedIneligible counts businesses with nothing auditable: no website,
	// or a social profile standing in for one.
	SkippedIneligible int `json:"skipped_ineligible"`
	// Truncated reports that Limit stopped the sweep before the backlog was
	// exhausted, so running it again will queue more.
	Truncated bool                      `json:"truncated"`
	Progress  WebsiteAuditSweepProgress `json:"progress"`
}

// SocialListingCorrection is one already-stored listing URL that turned out
// to be a social profile rather than an owned website.
type SocialListingCorrection struct {
	BusinessID string `json:"business_id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Platform   string `json:"platform"`
	// PreviousProspectStatus is what the record claimed before the fix.
	PreviousProspectStatus string `json:"previous_prospect_status"`
	// SocialProfileStored reports that the URL is now also recorded in the
	// social_profiles table, where social links belong.
	SocialProfileStored bool `json:"social_profile_stored"`
}

// SocialListingBackfill reports what a social reclassification pass found and
// changed.
type SocialListingBackfill struct {
	// Applied is false for a dry run.
	Applied bool `json:"applied"`
	// Examined is how many listings carried a URL at all.
	Examined int64 `json:"examined"`
	// Social is how many of those are really social profiles.
	Social int64 `json:"social"`
	// ProfilesInserted counts social_profiles rows created by this pass.
	ProfilesInserted int64 `json:"profiles_inserted"`
	// StatusCorrected counts businesses whose stored prospect_status was
	// something other than SOCIAL_ONLY and is now correct.
	StatusCorrected int64 `json:"status_corrected"`
	// QualityRescored counts businesses whose quality score was recalculated
	// because the social correction changed its inputs.
	QualityRescored int64 `json:"quality_rescored"`
	// ByPlatform breaks the social listings down by network.
	ByPlatform map[string]int64 `json:"by_platform"`
	// Samples is a bounded, human-readable sample of the corrections.
	Samples []SocialListingCorrection `json:"samples"`
}

// websiteStateRepository is the persistence surface the website-state service
// type-asserts from the configured repository.
type websiteStateRepository interface {
	WebsiteStateSummary(ctx context.Context, jobID string) (WebsiteStateSummary, error)
	BusinessWebsiteState(ctx context.Context, businessID string) (WebsiteStateResolution, error)
	BusinessWebsiteHealth(ctx context.Context, businessID string) (WebsiteHealthReport, error)
	StartWebsiteAuditSweep(ctx context.Context, request WebsiteAuditSweepRequest) (WebsiteAuditSweep, error)
	WebsiteAuditSweepByID(ctx context.Context, sweepID string) (WebsiteAuditSweep, error)
	RecentWebsiteAuditSweeps(ctx context.Context, limit int) ([]WebsiteAuditSweep, error)
	BackfillSocialListings(ctx context.Context, apply bool, limit int) (SocialListingBackfill, error)
	UnauditedBusinessIDs(ctx context.Context, ids []string) ([]string, error)
	DomainSiblingBusinessIDs(ctx context.Context, businessID string) ([]string, error)
}

// RefreshDomainSiblingScores rescores the other businesses that share one
// audited business's domain.
//
// One domain is one site, so an audit answers for every listing on it. Without
// this the duplicate listings keep the scores they had while their site was
// unknown, and the workspace shows two different verdicts for one website.
// It returns how many sibling rows were refreshed.
func (s *Service) RefreshDomainSiblingScores(ctx context.Context, businessID string) (int, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		if errors.Is(err, ErrWebsiteStateUnsupported) {
			return 0, nil
		}

		return 0, err
	}
	siblings, err := repository.DomainSiblingBusinessIDs(ctx, businessID)
	if err != nil {
		return 0, err
	}
	if len(siblings) == 0 {
		return 0, nil
	}
	if _, err := s.RecomputeProspects(ctx, siblings); err != nil && !errors.Is(err, ErrProspectsUnsupported) {
		return 0, err
	}
	if _, err := s.RecalculateQuality(ctx, siblings); err != nil &&
		!errors.Is(err, ErrQualityScoringUnsupported) {
		return 0, err
	}

	return len(siblings), nil
}

func (s *Service) websiteStateRepository() (websiteStateRepository, error) {
	repository, ok := s.repo.(websiteStateRepository)
	if !ok {
		return nil, ErrWebsiteStateUnsupported
	}

	return repository, nil
}

// WebsiteStateAvailable reports whether canonical website state can be
// resolved for the configured repository.
func (s *Service) WebsiteStateAvailable() bool {
	_, err := s.websiteStateRepository()

	return err == nil
}

// WebsiteStateSummary returns the canonical state breakdown for one job, or
// for the whole workspace when jobID is empty.
func (s *Service) WebsiteStateSummary(ctx context.Context, jobID string) (WebsiteStateSummary, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return WebsiteStateSummary{}, err
	}

	return repository.WebsiteStateSummary(ctx, strings.TrimSpace(jobID))
}

// BusinessWebsiteState resolves one business's canonical state with the
// evidence trail behind it.
func (s *Service) BusinessWebsiteState(ctx context.Context, businessID string) (WebsiteStateResolution, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return WebsiteStateResolution{}, err
	}
	businessID = strings.TrimSpace(businessID)
	if businessID == "" {
		return WebsiteStateResolution{}, fmt.Errorf("%w: business ID is required", ErrInvalidWebsiteStateRequest)
	}

	return repository.BusinessWebsiteState(ctx, businessID)
}

// BusinessWebsiteHealth returns the website health grade for one business, or
// an explicitly unavailable report when no audit reached an owned site.
func (s *Service) BusinessWebsiteHealth(ctx context.Context, businessID string) (WebsiteHealthReport, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return WebsiteHealthReport{}, err
	}
	businessID = strings.TrimSpace(businessID)
	if businessID == "" {
		return WebsiteHealthReport{}, fmt.Errorf("%w: business ID is required", ErrInvalidWebsiteStateRequest)
	}

	return repository.BusinessWebsiteHealth(ctx, businessID)
}

// StartWebsiteAuditSweep queues a durable bulk audit of every business in the
// requested canonical states.
//
// The sweep is durable by construction: it only ever creates rows in the
// existing enrichment_tasks queue, which the local worker already drains one
// task at a time and already returns to 'queued' after an interrupted run. A
// restart therefore resumes the sweep with no extra machinery, and progress
// is a count over that queue rather than a second record that could disagree
// with it.
//
// Work is deduplicated per unique domain: two businesses on one domain share
// one site, so one probe answers for both and the second business reuses the
// stored evidence.
func (s *Service) StartWebsiteAuditSweep(
	ctx context.Context,
	request WebsiteAuditSweepRequest,
) (WebsiteAuditSweep, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return WebsiteAuditSweep{}, err
	}
	request.JobID = strings.TrimSpace(request.JobID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if request.RequestedBy == "" {
		request.RequestedBy = "local_api"
	}

	states, err := normalizeWebsiteSweepStates(request.States)
	if err != nil {
		return WebsiteAuditSweep{}, err
	}
	request.States = states

	if request.Limit <= 0 {
		request.Limit = defaultWebsiteAuditSweep
	}
	if request.Limit > maximumWebsiteAuditSweep {
		return WebsiteAuditSweep{}, fmt.Errorf("%w: a sweep may queue at most %d audits at a time",
			ErrInvalidWebsiteStateRequest, maximumWebsiteAuditSweep)
	}

	options, err := request.Options.normalized()
	if err != nil {
		return WebsiteAuditSweep{}, err
	}
	request.Options = options

	return repository.StartWebsiteAuditSweep(ctx, request)
}

// WebsiteAuditSweepStatus returns one sweep with progress read back from the
// durable queue.
func (s *Service) WebsiteAuditSweepStatus(ctx context.Context, sweepID string) (WebsiteAuditSweep, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return WebsiteAuditSweep{}, err
	}
	sweepID = strings.TrimSpace(sweepID)
	if sweepID == "" {
		return WebsiteAuditSweep{}, fmt.Errorf("%w: sweep ID is required", ErrInvalidWebsiteStateRequest)
	}

	return repository.WebsiteAuditSweepByID(ctx, sweepID)
}

// RecentWebsiteAuditSweeps lists recent sweeps, newest first.
func (s *Service) RecentWebsiteAuditSweeps(ctx context.Context, limit int) ([]WebsiteAuditSweep, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	return repository.RecentWebsiteAuditSweeps(ctx, limit)
}

// BackfillSocialListings finds listing URLs that are really social profiles,
// records them in social_profiles where social links belong, and corrects the
// stored classification. apply=false makes it a dry run that changes nothing.
func (s *Service) BackfillSocialListings(ctx context.Context, apply bool, limit int) (SocialListingBackfill, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		return SocialListingBackfill{}, err
	}
	if limit <= 0 {
		limit = 100_000
	}

	return repository.BackfillSocialListings(ctx, apply, limit)
}

// WebsiteScoringPrerequisite reports the audits that have to run before a
// website-dependent score would mean anything.
type WebsiteScoringPrerequisite struct {
	// Unaudited is how many of the requested businesses have an auditable
	// website that has never been checked.
	Unaudited int `json:"unaudited"`
	// Sweep is the durable sweep that was queued to satisfy the prerequisite.
	Sweep *WebsiteAuditSweep `json:"sweep,omitempty"`
	// Message explains what happened, for an operator reading the response.
	Message string `json:"message,omitempty"`
}

// EnsureWebsiteAuditPrerequisite queues the audits a website-dependent score
// needs before it can be honest.
//
// Scoring a business whose website state is unknown produces a number that
// looks like a measurement and is not one, so the prerequisite is queued
// first and the caller is told to come back. It is a no-op - Unaudited zero,
// no sweep - when every named business is already resolved, which is the
// common case and keeps existing callers unchanged.
func (s *Service) EnsureWebsiteAuditPrerequisite(
	ctx context.Context,
	ids []string,
	requestedBy string,
) (WebsiteScoringPrerequisite, error) {
	repository, err := s.websiteStateRepository()
	if err != nil {
		if errors.Is(err, ErrWebsiteStateUnsupported) {
			return WebsiteScoringPrerequisite{}, nil
		}

		return WebsiteScoringPrerequisite{}, err
	}

	pending, err := repository.UnauditedBusinessIDs(ctx, ids)
	if err != nil {
		return WebsiteScoringPrerequisite{}, err
	}
	if len(pending) == 0 {
		return WebsiteScoringPrerequisite{}, nil
	}

	sweep, err := s.StartWebsiteAuditSweep(ctx, WebsiteAuditSweepRequest{
		States:      []string{WebsiteStateNeverChecked},
		Limit:       min(len(pending), maximumWebsiteAuditSweep),
		RequestedBy: requestedBy,
	})
	if err != nil {
		return WebsiteScoringPrerequisite{}, err
	}

	return WebsiteScoringPrerequisite{
		Unaudited: len(pending),
		Sweep:     &sweep,
		Message: fmt.Sprintf(
			"%d of the selected businesses have a website that has never been audited. "+
				"%d website audits were queued first; website-dependent scores stay "+
				"unset for those rows until the audits finish.",
			len(pending), sweep.Queued),
	}, nil
}

// normalizeWebsiteSweepStates validates the requested states and defaults to
// the never-checked backlog.
func normalizeWebsiteSweepStates(states []string) ([]string, error) {
	cleaned := make([]string, 0, len(states))
	seen := make(map[string]struct{}, len(states))
	for _, state := range states {
		state = strings.ToUpper(strings.TrimSpace(state))
		if state == "" {
			continue
		}
		if !ValidWebsiteState(state) {
			return nil, fmt.Errorf("%w: %q is not a website state", ErrInvalidWebsiteStateRequest, state)
		}
		if state == WebsiteStateNoWebsite || state == WebsiteStateSocialOnly {
			return nil, fmt.Errorf("%w: %s has no owned site to audit", ErrInvalidWebsiteStateRequest, state)
		}
		if _, duplicate := seen[state]; duplicate {
			continue
		}
		seen[state] = struct{}{}
		cleaned = append(cleaned, state)
	}
	if len(cleaned) == 0 {
		cleaned = append(cleaned, WebsiteStateNeverChecked)
	}

	return cleaned, nil
}

// WebsiteAuditSweepRequestedBy builds the queue marker for one sweep. It is
// the only link between a sweep and its tasks, so it lives next to the
// constant that defines it.
func WebsiteAuditSweepRequestedBy(sweepID string) string {
	return websiteAuditSweepRequestedByPrefix + sweepID
}

// WebsiteAuditSweepIDFromRequestedBy recovers a sweep ID from a queue marker,
// returning "" for a task that no sweep created.
func WebsiteAuditSweepIDFromRequestedBy(requestedBy string) string {
	if !strings.HasPrefix(requestedBy, websiteAuditSweepRequestedByPrefix) {
		return ""
	}

	return strings.TrimPrefix(requestedBy, websiteAuditSweepRequestedByPrefix)
}
