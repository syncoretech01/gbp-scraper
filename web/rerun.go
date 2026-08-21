package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var (
	// ErrCampaignUnsupported indicates that the active repository cannot
	// store campaign lineage, so rescans cannot be linked to their source.
	ErrCampaignUnsupported = errors.New("campaign lineage storage is unavailable")
	// ErrCampaignNotFound indicates that a job carries no campaign lineage.
	ErrCampaignNotFound = errors.New("campaign lineage not found")
	// ErrInvalidRerun identifies a rejected rescan request.
	ErrInvalidRerun = errors.New("invalid rescan request")
)

// Rescan modes accepted by POST /api/v1/jobs/{id}/rerun. They are the
// operator-facing vocabulary; each maps onto the JobData.IncrementalMode the
// engine already understands, so the scrape itself is unchanged.
const (
	// RerunModeFull re-runs the whole plan and keeps every observation, the
	// same collection an original run performs.
	RerunModeFull = "full"
	// RerunModeNewOnly keeps only businesses the workspace has never seen.
	RerunModeNewOnly = "new-only"
	// RerunModeChangedOnly keeps businesses that are new or whose stored
	// record changed, which is the engine's new_changed collection.
	RerunModeChangedOnly = "changed-only"
)

// MaximumCampaignIdempotencyKeyLength bounds the optional client-supplied
// key that makes a repeated rescan request resolve to the run the first
// attempt created instead of starting a second one.
const MaximumCampaignIdempotencyKeyLength = 128

// maximumRerunNameLength keeps generated rescan names inside the job-name
// bound the wizard enforces.
const maximumRerunNameLength = 120

// JobCampaignLink is the durable lineage of one job inside a rescan
// campaign. A job that was never part of a campaign has no link at all,
// which is exactly the historical behaviour.
type JobCampaignLink struct {
	JobID string `json:"job_id"`
	// CampaignID groups every generation of one campaign. The first job of
	// a campaign is its own root and campaign identity.
	CampaignID string `json:"campaign_id"`
	// RootJobID is the original job the campaign descends from.
	RootJobID string `json:"root_job_id"`
	// SourceJobID is the job this one was re-run from; empty for the root.
	SourceJobID string `json:"source_job_id,omitempty"`
	// Mode is the RerunMode* this generation was created with; empty for
	// the root.
	Mode string `json:"mode,omitempty"`
	// Generation counts rescans from the root, which is generation 0.
	Generation int `json:"generation"`
	// IdempotencyKey is the optional client key that created this link.
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// JobCampaign is one campaign's lineage in generation order.
type JobCampaign struct {
	CampaignID string            `json:"campaign_id"`
	RootJobID  string            `json:"root_job_id"`
	Jobs       []JobCampaignLink `json:"jobs"`
}

// JobRerun is the outcome of a rescan request.
type JobRerun struct {
	// Job is the newly created rescan run.
	Job Job `json:"job"`
	// Link is the lineage row recorded for it.
	Link JobCampaignLink `json:"link"`
	// State is the lifecycle state the rescan was created in.
	State string `json:"state"`
	// Reused reports that an earlier request with the same idempotency key
	// already created this run, so nothing new was started.
	Reused bool `json:"reused"`
}

type campaignRepository interface {
	SaveJobCampaignLink(context.Context, JobCampaignLink) error
	GetJobCampaignLink(context.Context, string) (JobCampaignLink, error)
	CampaignLinks(context.Context, string) ([]JobCampaignLink, error)
	FindCampaignIdempotencyKey(context.Context, string, string) (JobCampaignLink, error)
}

// SupportsCampaigns reports whether rescan lineage can be stored durably.
func (s *Service) SupportsCampaigns() bool {
	_, ok := s.repo.(campaignRepository)

	return ok
}

func (s *Service) campaignRepository() (campaignRepository, error) {
	repository, ok := s.repo.(campaignRepository)
	if !ok {
		return nil, ErrCampaignUnsupported
	}

	return repository, nil
}

// ValidRerunMode reports whether a mode string is one of the three accepted
// rescan modes.
func ValidRerunMode(mode string) bool {
	switch mode {
	case RerunModeFull, RerunModeNewOnly, RerunModeChangedOnly:
		return true
	default:
		return false
	}
}

// incrementalModeForRerun maps an operator-facing rescan mode onto the
// JobData.IncrementalMode the scrape engine already understands.
func incrementalModeForRerun(mode string) (string, error) {
	switch mode {
	case RerunModeFull:
		return "", nil
	case RerunModeNewOnly:
		return IncrementalModeNewOnly, nil
	case RerunModeChangedOnly:
		return IncrementalModeNewChanged, nil
	default:
		return "", fmt.Errorf("%w: mode must be %q, %q or %q",
			ErrInvalidRerun, RerunModeFull, RerunModeNewOnly, RerunModeChangedOnly)
	}
}

// RerunRequest describes one rescan of an existing job's plan against the
// current workspace.
type RerunRequest struct {
	// SourceJobID is the job whose plan and configuration are re-used.
	SourceJobID string `json:"source_job_id"`
	// Mode is one of the RerunMode* values.
	Mode string `json:"mode"`
	// Name overrides the generated rescan name.
	Name string `json:"name,omitempty"`
	// IdempotencyKey makes a repeated request resolve to the run the first
	// attempt created rather than starting another one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Draft creates the rescan as a draft instead of queueing it.
	Draft bool `json:"draft,omitempty"`
}

// RerunJob creates a NEW job that re-runs the source job's plan against the
// current workspace and records the campaign lineage linking the two.
//
// The source job is never touched: its configuration is copied, not moved,
// and its per-job CSV is left exactly where it is because the rescan writes
// to its own UUID-named file. Repeating a request that carries an
// IdempotencyKey returns the run the first attempt created, so a retried or
// double-submitted rescan can never start the same plan twice.
func (s *Service) RerunJob(ctx context.Context, request RerunRequest) (JobRerun, error) {
	repository, err := s.campaignRepository()
	if err != nil {
		return JobRerun{}, err
	}

	incremental, err := incrementalModeForRerun(request.Mode)
	if err != nil {
		return JobRerun{}, err
	}

	key, err := validCampaignIdempotencyKey(request.IdempotencyKey)
	if err != nil {
		return JobRerun{}, err
	}

	source, err := s.Get(ctx, strings.TrimSpace(request.SourceJobID))
	if err != nil {
		return JobRerun{}, err
	}

	if source.ID == "" {
		return JobRerun{}, ErrNotFound
	}

	lineage, err := s.campaignLineageOf(ctx, repository, source)
	if err != nil {
		return JobRerun{}, err
	}

	if key != "" {
		existing, findErr := repository.FindCampaignIdempotencyKey(ctx, lineage.CampaignID, key)
		switch {
		case findErr == nil:
			job, jobErr := s.Get(ctx, existing.JobID)
			if jobErr != nil {
				return JobRerun{}, jobErr
			}

			return JobRerun{Job: job, Link: existing, State: string(jobruntime.StateQueued), Reused: true}, nil
		case !errors.Is(findErr, ErrCampaignNotFound):
			return JobRerun{}, findErr
		}
	}

	name, err := rerunName(request.Name, source.Name, lineage.Generation+1)
	if err != nil {
		return JobRerun{}, err
	}

	now := time.Now().UTC()
	rescan := Job{
		ID:     uuid.NewString(),
		Name:   name,
		Date:   now,
		Status: StatusPending,
		Data:   source.Data,
	}
	rescan.Data.IncrementalMode = incremental

	if err := rescan.Validate(); err != nil {
		return JobRerun{}, fmt.Errorf("%w: %s", ErrInvalidRerun, err)
	}

	state := jobruntime.StateQueued
	if request.Draft {
		state = jobruntime.StateDraft
	}

	if err := s.CreateWithState(ctx, &rescan, state); err != nil {
		return JobRerun{}, err
	}

	link := JobCampaignLink{
		JobID:          rescan.ID,
		CampaignID:     lineage.CampaignID,
		RootJobID:      lineage.RootJobID,
		SourceJobID:    source.ID,
		Mode:           request.Mode,
		Generation:     lineage.Generation + 1,
		IdempotencyKey: key,
		CreatedAt:      now,
	}

	if err := repository.SaveJobCampaignLink(ctx, link); err != nil {
		return JobRerun{}, err
	}

	return JobRerun{Job: rescan, Link: link, State: string(state)}, nil
}

// campaignLineageOf returns the source job's lineage, recording the job as
// its own campaign root the first time it is rescanned. Recording the root
// is what makes the whole family discoverable from any of its members.
func (s *Service) campaignLineageOf(
	ctx context.Context,
	repository campaignRepository,
	source Job,
) (JobCampaignLink, error) {
	link, err := repository.GetJobCampaignLink(ctx, source.ID)
	if err == nil {
		return link, nil
	}

	if !errors.Is(err, ErrCampaignNotFound) {
		return JobCampaignLink{}, err
	}

	root := JobCampaignLink{
		JobID:      source.ID,
		CampaignID: source.ID,
		RootJobID:  source.ID,
		Generation: 0,
		CreatedAt:  time.Now().UTC(),
	}

	if err := repository.SaveJobCampaignLink(ctx, root); err != nil {
		return JobCampaignLink{}, err
	}

	return root, nil
}

// JobCampaignOf returns the full lineage of the campaign the job belongs to.
// A job that has never been part of a campaign reports itself as a
// single-generation campaign, so a caller never has to special-case it.
func (s *Service) JobCampaignOf(ctx context.Context, jobID string) (JobCampaign, error) {
	repository, err := s.campaignRepository()
	if err != nil {
		return JobCampaign{}, err
	}

	link, err := repository.GetJobCampaignLink(ctx, jobID)
	if errors.Is(err, ErrCampaignNotFound) {
		return JobCampaign{
			CampaignID: jobID,
			RootJobID:  jobID,
			Jobs: []JobCampaignLink{{
				JobID: jobID, CampaignID: jobID, RootJobID: jobID, Generation: 0,
			}},
		}, nil
	}

	if err != nil {
		return JobCampaign{}, err
	}

	links, err := repository.CampaignLinks(ctx, link.CampaignID)
	if err != nil {
		return JobCampaign{}, err
	}

	return JobCampaign{CampaignID: link.CampaignID, RootJobID: link.RootJobID, Jobs: links}, nil
}

// CampaignJobIDs returns every job ID of one campaign in generation order.
func (s *Service) CampaignJobIDs(ctx context.Context, campaignID string) ([]string, error) {
	repository, err := s.campaignRepository()
	if err != nil {
		return nil, err
	}

	links, err := repository.CampaignLinks(ctx, strings.TrimSpace(campaignID))
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.JobID)
	}

	return ids, nil
}

// rerunName builds the display name of a rescan: the operator's override
// when given, otherwise the source name with the generation appended.
func rerunName(override, sourceName string, generation int) (string, error) {
	name := strings.TrimSpace(override)
	if name == "" {
		suffix := fmt.Sprintf(" (rescan %d)", generation)
		base := strings.TrimSpace(sourceName)

		if trimmed := strings.Index(base, " (rescan "); trimmed > 0 {
			base = strings.TrimSpace(base[:trimmed])
		}

		if len(base)+len(suffix) > maximumRerunNameLength {
			base = strings.TrimSpace(base[:maximumRerunNameLength-len(suffix)])
		}

		name = base + suffix
	}

	if name == "" || len(name) > maximumRerunNameLength {
		return "", fmt.Errorf("%w: name must be 1 to %d characters", ErrInvalidRerun, maximumRerunNameLength)
	}

	if strings.ContainsFunc(name, unicode.IsControl) {
		return "", fmt.Errorf("%w: name contains control characters", ErrInvalidRerun)
	}

	return name, nil
}

// validCampaignIdempotencyKey bounds the optional client key; an empty key
// simply disables idempotent replay for that request.
func validCampaignIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	if len(value) > MaximumCampaignIdempotencyKeyLength {
		return "", fmt.Errorf("%w: idempotency key must be at most %d characters",
			ErrInvalidRerun, MaximumCampaignIdempotencyKeyLength)
	}

	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) ||
			character == '-' || character == '_' || character == ':' || character == '.' {
			continue
		}

		return "", fmt.Errorf("%w: idempotency key contains unsupported characters", ErrInvalidRerun)
	}

	return value, nil
}
