package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var (
	// ErrCheckpointUnsupported indicates that the repository cannot persist
	// worker tasks and safe resume points.
	ErrCheckpointUnsupported = errors.New("job checkpoint storage is unavailable")
	// ErrCheckpointTaskNotFound indicates that a deterministic task key is not
	// part of the persisted job plan.
	ErrCheckpointTaskNotFound = errors.New("job checkpoint task not found")
	// ErrCheckpointLeaseLost indicates that another worker reclaimed a task
	// lease. The previous owner must abandon the task without finishing it.
	ErrCheckpointLeaseLost = errors.New("job checkpoint task lease was reclaimed")
)

// JobTaskDefinition is one deterministic, resumable unit of Maps work. Key
// must remain stable for an unchanged job configuration.
type JobTaskDefinition struct {
	Key        string          `json:"key"`
	Kind       string          `json:"kind"`
	Sequence   int             `json:"sequence"`
	Query      string          `json:"query,omitempty"`
	SourceCell string          `json:"source_cell,omitempty"`
	InputID    string          `json:"input_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	// Origin records how the task entered the plan: empty for the up-front
	// plan, "expansion:<parentZip>" for tasks the coverage engine appended.
	Origin string `json:"origin,omitempty"`
	// Priority orders claims (higher first); zero keeps the historical
	// FIFO order.
	Priority int `json:"priority,omitempty"`
}

// JobTask is the durable execution state for one definition.
type JobTask struct {
	ID          string          `json:"id"`
	JobID       string          `json:"job_id"`
	Key         string          `json:"key"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Sequence    int             `json:"sequence"`
	Query       string          `json:"query,omitempty"`
	SourceCell  string          `json:"source_cell,omitempty"`
	InputID     string          `json:"input_id,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Checkpoint  json.RawMessage `json:"checkpoint,omitempty"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	LastError   string          `json:"last_error,omitempty"`
	Origin      string          `json:"origin,omitempty"`
	Priority    int             `json:"priority,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// JobTaskSummary is an aggregate of the durable task plan.
type JobTaskSummary struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
	Retries   int64 `json:"retries"`
}

// Remaining reports work which may still be attempted.
func (summary JobTaskSummary) Remaining() int64 {
	return summary.Pending + summary.Running
}

// JobCheckpoint is the most recently committed safe resume boundary.
type JobCheckpoint struct {
	ID        int64            `json:"id"`
	JobID     string           `json:"job_id"`
	TaskID    string           `json:"task_id,omitempty"`
	TaskKey   string           `json:"task_key,omitempty"`
	Stage     jobruntime.Stage `json:"stage"`
	Payload   json.RawMessage  `json:"payload"`
	CreatedAt time.Time        `json:"created_at"`
}

// JobWorkerProgress contains inexpensive live worker/resource evidence.
type JobWorkerProgress struct {
	Stage            jobruntime.Stage `json:"stage"`
	ActiveTasks      int64            `json:"active_tasks"`
	Retries          int64            `json:"retries"`
	PlacesPerMinute  float64          `json:"places_per_minute"`
	ETASeconds       *int64           `json:"eta_seconds,omitempty"`
	CurrentQuery     string           `json:"current_query,omitempty"`
	CurrentCell      string           `json:"current_cell,omitempty"`
	CurrentDomain    string           `json:"current_domain,omitempty"`
	BrowserCount     int64            `json:"browser_count"`
	ActivePages      int64            `json:"active_pages"`
	CPUPercent       float64          `json:"cpu_percent"`
	MemoryBytes      uint64           `json:"memory_bytes"`
	DiskFreeBytes    uint64           `json:"disk_free_bytes"`
	DatabaseWrites   int64            `json:"database_writes"`
	WebsiteQueue     int64            `json:"website_queue"`
	DesiredWorkers   int64            `json:"desired_workers"`
	EffectiveWorkers int64            `json:"effective_workers"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// JobExecutionSnapshot joins task, checkpoint, resource, and recovery state
// for the local API and monitor UI.
type JobExecutionSnapshot struct {
	Tasks            JobTaskSummary    `json:"tasks"`
	Checkpoint       *JobCheckpoint    `json:"checkpoint,omitempty"`
	Progress         JobWorkerProgress `json:"progress"`
	RecoveryRequired bool              `json:"recovery_required"`
}

// JobTaskCheckpoint is the bounded metadata written after a task's result
// file has been durably merged into the compatible job CSV.
//
// The payload is stored as JSON, so new fields are additive: a checkpoint
// written by an older build simply reports their zero values.
type JobTaskCheckpoint struct {
	State             string `json:"state"`
	RowsAdded         int64  `json:"rows_added,omitempty"`
	RowsReplaced      int64  `json:"rows_replaced,omitempty"`
	DuplicatesSkipped int64  `json:"duplicates_skipped,omitempty"`
	DiskFreeBytes     uint64 `json:"disk_free_bytes,omitempty"`
	// Truncated reports that this task's own result set reached the
	// effective per-query cap for the job's configured depth, so the cell
	// is very likely missing businesses the platform never rendered. It is
	// evidence, not proof: see TruncationCap for the yardstick used.
	Truncated bool `json:"truncated,omitempty"`
	// TruncationCap is the effective per-query result cap the yield was
	// compared against, recorded so the signal stays auditable after the
	// depth or the cap model changes.
	TruncationCap int `json:"truncation_cap,omitempty"`
}

type checkpointRepository interface {
	PrepareJobTasks(context.Context, string, []JobTaskDefinition, int) ([]JobTask, error)
	StartJobTask(context.Context, string, string) (JobTask, error)
	ClaimNextJobTask(context.Context, string, string, time.Duration) (JobTask, bool, error)
	HeartbeatJobTask(context.Context, string, string, string, time.Duration) error
	ReleaseJobTask(context.Context, string, string, string, string) error
	ReclaimExpiredJobTasks(context.Context, string) (int, error)
	ReclaimStaleJobTasks(context.Context) (int, error)
	CompleteJobTask(context.Context, string, string, JobTaskCheckpoint) error
	CompleteJobTaskAs(context.Context, string, string, string, JobTaskCheckpoint) error
	FailJobTask(context.Context, string, string, error, bool, JobTaskCheckpoint) error
	FailJobTaskAs(context.Context, string, string, string, error, bool, JobTaskCheckpoint) error
	UpdateJobWorkerProgress(context.Context, string, JobWorkerProgress) error
	RecordJobWorkerEvent(context.Context, string, string, string, string, map[string]any) error
	GetJobExecution(context.Context, string) (JobExecutionSnapshot, error)
	RecoverAbandonedJobs(context.Context) (int, error)
}

// SupportsJobCheckpoints reports whether the active repository implements
// the durable worker protocol.
func (s *Service) SupportsJobCheckpoints() bool {
	_, ok := s.repo.(checkpointRepository)

	return ok
}

// PrepareJobTasks persists a deterministic task plan and returns unfinished
// tasks in sequence order.
func (s *Service) PrepareJobTasks(
	ctx context.Context,
	jobID string,
	definitions []JobTaskDefinition,
	maxAttempts int,
) ([]JobTask, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return nil, ErrCheckpointUnsupported
	}
	if strings.TrimSpace(jobID) == "" {
		return nil, errors.New("job ID is required")
	}
	if len(definitions) == 0 {
		return []JobTask{}, nil
	}
	if maxAttempts < 1 || maxAttempts > 100 {
		return nil, errors.New("maximum task attempts must be between 1 and 100")
	}
	seen := make(map[string]struct{}, len(definitions))
	for index := range definitions {
		definition := &definitions[index]
		definition.Key = strings.TrimSpace(definition.Key)
		definition.Kind = strings.TrimSpace(definition.Kind)
		if definition.Key == "" || len(definition.Key) > 256 {
			return nil, fmt.Errorf("task %d has an invalid key", index+1)
		}
		if definition.Kind == "" || len(definition.Kind) > 64 {
			return nil, fmt.Errorf("task %d has an invalid kind", index+1)
		}
		if _, duplicate := seen[definition.Key]; duplicate {
			return nil, fmt.Errorf("duplicate task key %q", definition.Key)
		}
		seen[definition.Key] = struct{}{}
		if definition.Sequence < 0 {
			return nil, fmt.Errorf("task %q has a negative sequence", definition.Key)
		}
		if len(definition.Payload) == 0 {
			definition.Payload = json.RawMessage("{}")
		}
		if !json.Valid(definition.Payload) || len(definition.Payload) > 64*1024 {
			return nil, fmt.Errorf("task %q has an invalid payload", definition.Key)
		}
	}

	return repository.PrepareJobTasks(ctx, jobID, definitions, maxAttempts)
}

// StartJobTask records an outer, fresh-worker attempt.
func (s *Service) StartJobTask(ctx context.Context, jobID, taskKey string) (JobTask, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return JobTask{}, ErrCheckpointUnsupported
	}

	return repository.StartJobTask(ctx, jobID, taskKey)
}

// ClaimNextJobTask leases the next runnable task for one worker. found is false
// when the plan has no runnable task left.
func (s *Service) ClaimNextJobTask(
	ctx context.Context,
	jobID, owner string,
	lease time.Duration,
) (JobTask, bool, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return JobTask{}, false, ErrCheckpointUnsupported
	}

	return repository.ClaimNextJobTask(ctx, jobID, owner, lease)
}

// HeartbeatJobTask extends a lease the caller still owns. It returns
// ErrCheckpointLeaseLost once another worker has reclaimed the task.
func (s *Service) HeartbeatJobTask(
	ctx context.Context,
	jobID, taskKey, owner string,
	lease time.Duration,
) error {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return ErrCheckpointUnsupported
	}

	return repository.HeartbeatJobTask(ctx, jobID, taskKey, owner, lease)
}

// ReleaseJobTask returns an interrupted task to the queue without consuming a
// further attempt, so a restart resumes it exactly.
func (s *Service) ReleaseJobTask(ctx context.Context, jobID, taskKey, owner, reason string) error {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return ErrCheckpointUnsupported
	}

	return repository.ReleaseJobTask(ctx, jobID, taskKey, owner, reason)
}

// ReclaimExpiredJobTasks recovers tasks whose worker stopped reporting.
func (s *Service) ReclaimExpiredJobTasks(ctx context.Context, jobID string) (int, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return 0, ErrCheckpointUnsupported
	}

	return repository.ReclaimExpiredJobTasks(ctx, jobID)
}

// ReclaimStaleJobTasks returns every running task whose lease has expired to
// the pending queue, across all jobs regardless of their lifecycle state. It
// complements RecoverAbandonedJobs, which only resets tasks of jobs left in an
// active state, and is intended to be called once at process startup next to
// it so a stale lease on a paused, partial, or cancelled job cannot linger.
func (s *Service) ReclaimStaleJobTasks(ctx context.Context) (int, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return 0, ErrCheckpointUnsupported
	}

	return repository.ReclaimStaleJobTasks(ctx)
}

// CompleteJobTask commits a safe resume boundary for a task finished outside
// the lease protocol. It delegates to CompleteJobTaskAs with an empty owner,
// which matches only lease-less rows: StartJobTask stores an empty
// lease_owner, while ClaimNextJobTask always records a non-empty worker owner.
// A worker that claimed its task by lease must finish through the As variant.
func (s *Service) CompleteJobTask(
	ctx context.Context,
	jobID, taskKey string,
	checkpoint JobTaskCheckpoint,
) error {
	return s.CompleteJobTaskAs(ctx, jobID, taskKey, "", checkpoint)
}

// CompleteJobTaskAs commits a safe resume boundary after verifying that owner
// still holds the task. It returns ErrCheckpointLeaseLost, and persists
// nothing, once another worker has reclaimed the task's lease.
func (s *Service) CompleteJobTaskAs(
	ctx context.Context,
	jobID, taskKey, owner string,
	checkpoint JobTaskCheckpoint,
) error {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return ErrCheckpointUnsupported
	}

	return repository.CompleteJobTaskAs(ctx, jobID, taskKey, owner, checkpoint)
}

// FailJobTask records a failed or interrupted attempt for a task finished
// outside the lease protocol. It delegates to FailJobTaskAs with an empty
// owner, which matches only lease-less rows (see CompleteJobTask).
func (s *Service) FailJobTask(
	ctx context.Context,
	jobID, taskKey string,
	runErr error,
	retryable bool,
	checkpoint JobTaskCheckpoint,
) error {
	return s.FailJobTaskAs(ctx, jobID, taskKey, "", runErr, retryable, checkpoint)
}

// FailJobTaskAs records a failed or interrupted attempt after verifying that
// owner still holds the task. Retryable tasks return to pending so a restart
// can safely execute them again. It returns ErrCheckpointLeaseLost, and
// persists nothing, once another worker has reclaimed the task's lease.
func (s *Service) FailJobTaskAs(
	ctx context.Context,
	jobID, taskKey, owner string,
	runErr error,
	retryable bool,
	checkpoint JobTaskCheckpoint,
) error {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return ErrCheckpointUnsupported
	}

	return repository.FailJobTaskAs(ctx, jobID, taskKey, owner, runErr, retryable, checkpoint)
}

// UpdateJobWorkerProgress persists a replaceable live resource sample.
func (s *Service) UpdateJobWorkerProgress(ctx context.Context, jobID string, progress JobWorkerProgress) error {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return ErrCheckpointUnsupported
	}

	return repository.UpdateJobWorkerProgress(ctx, jobID, progress)
}

// RecordJobWorkerEvent appends redacted operational/adaptive evidence.
func (s *Service) RecordJobWorkerEvent(
	ctx context.Context,
	jobID, eventType, severity, message string,
	fields map[string]any,
) error {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return ErrCheckpointUnsupported
	}

	return repository.RecordJobWorkerEvent(ctx, jobID, eventType, severity, message, fields)
}

// GetJobExecution returns checkpoint and worker evidence for a job.
func (s *Service) GetJobExecution(ctx context.Context, jobID string) (JobExecutionSnapshot, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return JobExecutionSnapshot{}, ErrCheckpointUnsupported
	}

	return repository.GetJobExecution(ctx, jobID)
}

// RecoverAbandonedJobs safely pauses active work left by an earlier process
// and returns its in-flight tasks to the pending state.
func (s *Service) RecoverAbandonedJobs(ctx context.Context) (int, error) {
	repository, ok := s.repo.(checkpointRepository)
	if !ok {
		return 0, ErrCheckpointUnsupported
	}

	return repository.RecoverAbandonedJobs(ctx)
}
