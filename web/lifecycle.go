package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var (
	// ErrLifecycleNotFound indicates that no canonical runtime row exists for a
	// requested job.
	ErrLifecycleNotFound = errors.New("job lifecycle not found")
	// ErrLifecycleConflict indicates that another actor changed the lifecycle
	// between reading and applying a compare-and-swap update.
	ErrLifecycleConflict = errors.New("job lifecycle conflict")
)

// JobRuntime is the durable lifecycle projection used by the upgraded local
// UI and API. Legacy Job.Status remains unchanged for compatibility.
type JobRuntime struct {
	JobID         string                `json:"job_id"`
	State         jobruntime.State      `json:"state"`
	StateVersion  int64                 `json:"state_version"`
	Stage         jobruntime.Stage      `json:"stage"`
	RequestedStop jobruntime.StopReason `json:"requested_stop,omitempty"`
	OutcomeReason jobruntime.StopReason `json:"outcome_reason,omitempty"`
	Progress      float64               `json:"progress"`
	Message       string                `json:"message"`
	TotalTasks    int64                 `json:"total_tasks"`
	Completed     int64                 `json:"completed_tasks"`
	Failed        int64                 `json:"failed_tasks"`
	RawRecords    int64                 `json:"raw_records"`
	UniqueRecords int64                 `json:"unique_records"`
	Emails        int64                 `json:"emails"`
	Warnings      int64                 `json:"warnings"`
	Errors        int64                 `json:"errors"`
	StartedAt     *time.Time            `json:"started_at,omitempty"`
	FinishedAt    *time.Time            `json:"finished_at,omitempty"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

// JobEvent is one redacted, ordered lifecycle/log event. Its integer ID is a
// durable cursor for polling and Server-Sent Events replay.
type JobEvent struct {
	ID         int64            `json:"id"`
	JobID      string           `json:"job_id"`
	Type       string           `json:"type"`
	Severity   string           `json:"severity"`
	Stage      jobruntime.Stage `json:"stage,omitempty"`
	Message    string           `json:"message"`
	Context    string           `json:"context_json,omitempty"`
	OccurredAt time.Time        `json:"occurred_at"`
}

// LifecycleRepository is an additive capability implemented by the upgraded
// SQLite repository. It is intentionally separate from JobRepository so old
// embedders and tests keep compiling unchanged.
type LifecycleRepository interface {
	CreateWithState(context.Context, *Job, jobruntime.State) error
	GetRuntime(context.Context, string) (JobRuntime, error)
	ApplyControl(context.Context, string, jobruntime.Control) (JobRuntime, jobruntime.ControlDecision, error)
	SetOutcome(context.Context, string, jobruntime.Outcome, string) (JobRuntime, error)
	EventsAfter(context.Context, string, int64, int) ([]JobEvent, error)
}

// GetRuntime returns the canonical state when available and otherwise derives
// a read-only compatibility projection from the legacy job row.
func (s *Service) GetRuntime(ctx context.Context, id string) (JobRuntime, error) {
	if repository, ok := s.repo.(LifecycleRepository); ok {
		return repository.GetRuntime(ctx, id)
	}

	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return JobRuntime{}, err
	}

	state, err := stateFromLegacyStatus(job.Status)
	if err != nil {
		return JobRuntime{}, err
	}

	return JobRuntime{
		JobID:     job.ID,
		State:     state,
		Stage:     stageForState(state),
		Progress:  progressForState(state),
		UpdatedAt: job.Date,
	}, nil
}

// ApplyControl evaluates and atomically persists an operator command.
func (s *Service) ApplyControl(
	ctx context.Context,
	id string,
	control jobruntime.Control,
) (JobRuntime, jobruntime.ControlDecision, error) {
	repository, ok := s.repo.(LifecycleRepository)
	if !ok {
		return JobRuntime{}, jobruntime.ControlDecision{}, ErrLifecycleUnsupported
	}

	return repository.ApplyControl(ctx, id, control)
}

// SetOutcome stores deterministic terminal evidence while maintaining the
// legacy four-value status projection.
func (s *Service) SetOutcome(
	ctx context.Context,
	id string,
	outcome jobruntime.Outcome,
	message string,
) (JobRuntime, error) {
	repository, ok := s.repo.(LifecycleRepository)
	if !ok {
		return JobRuntime{}, ErrLifecycleUnsupported
	}

	return repository.SetOutcome(ctx, id, outcome, message)
}

// EventsAfter returns durable redacted events after an exclusive cursor.
func (s *Service) EventsAfter(ctx context.Context, id string, after int64, limit int) ([]JobEvent, error) {
	repository, ok := s.repo.(LifecycleRepository)
	if !ok {
		return nil, ErrLifecycleUnsupported
	}

	return repository.EventsAfter(ctx, id, after, limit)
}

func stateFromLegacyStatus(status string) (jobruntime.State, error) {
	switch status {
	case StatusPending:
		return jobruntime.StateQueued, nil
	case StatusWorking:
		return jobruntime.StateRunning, nil
	case StatusOK:
		return jobruntime.StateCompleted, nil
	case StatusFailed:
		return jobruntime.StateFailed, nil
	default:
		return "", fmt.Errorf("unknown legacy status %q", status)
	}
}

func stageForState(state jobruntime.State) jobruntime.Stage {
	switch state {
	case jobruntime.StateCompleted, jobruntime.StatePartial, jobruntime.StateCancelled:
		return jobruntime.StageSavingExporting
	case jobruntime.StateStarting, jobruntime.StateRunning, jobruntime.StateCancelling:
		return jobruntime.StageSearchingMaps
	default:
		return jobruntime.StagePreparingQueries
	}
}

func progressForState(state jobruntime.State) float64 {
	if state.Terminal() {
		return 100
	}

	return 0
}
