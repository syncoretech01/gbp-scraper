package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ErrListingStateUnsupported indicates that the configured repository cannot
// persist listing identities or interval checkpoints.
var ErrListingStateUnsupported = errors.New("durable listing state is unavailable")

const (
	// MaximumListingKeysPerJob bounds how many listing identities one job may
	// keep. It is generous enough for any single local plan and stops a
	// runaway run from growing the workspace without limit.
	MaximumListingKeysPerJob = 500_000
	// maximumListingKeyLength bounds one stored identity. Callers supply a
	// fixed-width digest, so anything longer is a programming error.
	maximumListingKeyLength = 128
)

// listingStateRepository is the durable store for the deduplication state and
// the interval checkpoints. It is an optional capability: a repository without
// it keeps exactly the historical in-memory behaviour.
type listingStateRepository interface {
	RememberJobListingKeys(ctx context.Context, jobID string, keys []string) (int, error)
	JobListingKeys(ctx context.Context, jobID string, limit int) ([]string, error)
	CountJobListingKeys(ctx context.Context, jobID string) (int64, error)
	RecordJobIntervalCheckpoint(ctx context.Context, jobID string, payload string) error
}

func (s *Service) listingStateRepository() (listingStateRepository, error) {
	repository, ok := s.repo.(listingStateRepository)
	if !ok {
		return nil, ErrListingStateUnsupported
	}

	return repository, nil
}

// SupportsListingState reports whether deduplication state and interval
// checkpoints survive a restart for this repository.
func (s *Service) SupportsListingState() bool {
	_, err := s.listingStateRepository()

	return err == nil
}

// RememberJobListingKeys records listing identities a job has discovered. The
// write is idempotent, so a retried task or a restarted run converges on the
// same durable set. It returns how many keys were genuinely new.
func (s *Service) RememberJobListingKeys(ctx context.Context, jobID string, keys []string) (int, error) {
	repository, err := s.listingStateRepository()
	if err != nil {
		return 0, err
	}

	accepted := make([]string, 0, len(keys))

	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || len(key) > maximumListingKeyLength {
			continue
		}

		accepted = append(accepted, key)
	}

	if len(accepted) == 0 {
		return 0, nil
	}

	return repository.RememberJobListingKeys(ctx, jobID, accepted)
}

// JobListingKeys returns the recorded listing identities so a resumed run can
// rebuild its deduplication state before it claims any task.
func (s *Service) JobListingKeys(ctx context.Context, jobID string, limit int) ([]string, error) {
	repository, err := s.listingStateRepository()
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > MaximumListingKeysPerJob {
		limit = MaximumListingKeysPerJob
	}

	return repository.JobListingKeys(ctx, jobID, limit)
}

// CountJobListingKeys reports how many listing identities a job has recorded.
func (s *Service) CountJobListingKeys(ctx context.Context, jobID string) (int64, error) {
	repository, err := s.listingStateRepository()
	if err != nil {
		return 0, err
	}

	return repository.CountJobListingKeys(ctx, jobID)
}

// JobIntervalCheckpoint is the bounded payload of a time-based resume
// boundary. It records where the plan stood, not what it collected.
type JobIntervalCheckpoint struct {
	Reason           string `json:"reason"`
	IntervalSeconds  int    `json:"interval_seconds"`
	TasksCompleted   int64  `json:"tasks_completed"`
	TasksRunning     int64  `json:"tasks_running"`
	TasksPending     int64  `json:"tasks_pending"`
	TasksFailed      int64  `json:"tasks_failed"`
	ListingKeys      int64  `json:"listing_keys"`
	CommittedMerges  int64  `json:"committed_merges"`
	EffectiveWorkers int64  `json:"effective_workers"`
}

// RecordJobIntervalCheckpoint appends a time-based safe resume boundary
// between task completions, so an interrupted run reports how recently it was
// still making progress.
func (s *Service) RecordJobIntervalCheckpoint(
	ctx context.Context,
	jobID string,
	checkpoint JobIntervalCheckpoint,
) error {
	repository, err := s.listingStateRepository()
	if err != nil {
		return err
	}

	payload, marshalErr := json.Marshal(checkpoint)
	if marshalErr != nil {
		payload = []byte("{}")
	}

	return repository.RecordJobIntervalCheckpoint(ctx, jobID, string(payload))
}
