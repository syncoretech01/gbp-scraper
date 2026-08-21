package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrLiveControlsUnsupported indicates the repository cannot store live
	// job controls.
	ErrLiveControlsUnsupported = errors.New("live job controls are unavailable")
	// ErrInvalidLiveControl indicates a rejected live control request.
	ErrInvalidLiveControl = errors.New("invalid live control")
)

// DirectConnectionPool is the proxy-pool override sentinel meaning "drop the
// proxies and connect directly". An empty override means "no override".
const DirectConnectionPool = "direct"

// JobLiveControls is the durable control state one worker polls between tasks.
type JobLiveControls struct {
	JobID                 string `json:"job_id"`
	ExtendedSeconds       int64  `json:"extended_seconds"`
	ConcurrencyOverride   int    `json:"concurrency_override"`
	ProxyPoolOverride     string `json:"proxy_pool_override"`
	RetryCurrentRequested bool   `json:"retry_current_requested"`
}

type liveControlRepository interface {
	JobLiveControls(context.Context, string) (JobLiveControls, error)
	ExtendJobRuntime(context.Context, string, int64) error
	SetJobConcurrencyOverride(context.Context, string, int) error
	SetJobProxyPoolOverride(context.Context, string, string) error
	RequestJobRetryCurrent(context.Context, string) error
	ConsumeJobRetryCurrent(context.Context, string) (bool, error)
	ResetJobLiveControls(context.Context, string) error
}

// SupportsLiveControls reports whether live job controls can be stored.
func (s *Service) SupportsLiveControls() bool {
	_, ok := s.repo.(liveControlRepository)

	return ok
}

func (s *Service) liveControlRepository() (liveControlRepository, error) {
	repository, ok := s.repo.(liveControlRepository)
	if !ok {
		return nil, ErrLiveControlsUnsupported
	}

	return repository, nil
}

// JobLiveControls reads the pending control state for a job.
func (s *Service) JobLiveControls(ctx context.Context, jobID string) (JobLiveControls, error) {
	repository, err := s.liveControlRepository()
	if err != nil {
		return JobLiveControls{}, err
	}

	return repository.JobLiveControls(ctx, jobID)
}

// ExtendJobRuntime adds between one minute and six hours to the current run.
func (s *Service) ExtendJobRuntime(ctx context.Context, jobID string, extra time.Duration) error {
	repository, err := s.liveControlRepository()
	if err != nil {
		return err
	}

	if extra < time.Minute || extra > 6*time.Hour {
		return fmt.Errorf("%w: runtime extension must be between 1m and 6h", ErrInvalidLiveControl)
	}

	return repository.ExtendJobRuntime(ctx, jobID, int64(extra.Seconds()))
}

// SetJobConcurrencyOverride sets a live concurrency target; 0 clears it.
func (s *Service) SetJobConcurrencyOverride(ctx context.Context, jobID string, concurrency int) error {
	repository, err := s.liveControlRepository()
	if err != nil {
		return err
	}

	if concurrency < 0 || concurrency > 64 {
		return fmt.Errorf("%w: concurrency must be between 1 and 64", ErrInvalidLiveControl)
	}

	return repository.SetJobConcurrencyOverride(ctx, jobID, concurrency)
}

// SetJobProxyPoolOverride switches the pool new tasks resolve their proxies
// from. "direct" drops proxies entirely; "" clears the override.
func (s *Service) SetJobProxyPoolOverride(ctx context.Context, jobID, poolID string) error {
	repository, err := s.liveControlRepository()
	if err != nil {
		return err
	}

	poolID = strings.TrimSpace(poolID)
	if len(poolID) > 64 || strings.ContainsAny(poolID, "\x00\r\n") {
		return fmt.Errorf("%w: invalid proxy pool", ErrInvalidLiveControl)
	}

	// An explicit pool must exist and have usable proxies before it is stored,
	// so a typo cannot silently strand new tasks.
	if poolID != "" && poolID != DirectConnectionPool {
		proxies, resolveErr := s.ResolveProxyPool(ctx, poolID)
		if resolveErr != nil {
			return fmt.Errorf("%w: %s", ErrInvalidLiveControl, "the proxy pool could not be resolved")
		}

		if len(proxies) == 0 {
			return fmt.Errorf("%w: the proxy pool has no usable proxies", ErrInvalidLiveControl)
		}
	}

	return repository.SetJobProxyPoolOverride(ctx, jobID, poolID)
}

// RequestJobRetryCurrent asks the worker to abandon and requeue in-flight
// tasks without consuming their attempts.
func (s *Service) RequestJobRetryCurrent(ctx context.Context, jobID string) error {
	repository, err := s.liveControlRepository()
	if err != nil {
		return err
	}

	return repository.RequestJobRetryCurrent(ctx, jobID)
}

// ConsumeJobRetryCurrent claims a pending retry-current request (worker side).
func (s *Service) ConsumeJobRetryCurrent(ctx context.Context, jobID string) (bool, error) {
	repository, err := s.liveControlRepository()
	if err != nil {
		return false, err
	}

	return repository.ConsumeJobRetryCurrent(ctx, jobID)
}

// ResetJobLiveControls clears every pending control at the start of a run.
//
// A run start is also the only moment the workspace can know which build of
// the binary is about to execute the job, so the scraper version is stamped
// here. The stamp is idempotent and best-effort: a repository without version
// storage, or a job whose version was already recorded, leaves the durable
// row untouched and never fails the run.
func (s *Service) ResetJobLiveControls(ctx context.Context, jobID string) error {
	repository, err := s.liveControlRepository()
	if err != nil {
		return err
	}

	if versionErr := s.RecordJobScraperVersion(ctx, jobID, ScraperVersion()); versionErr != nil &&
		!errors.Is(versionErr, ErrScraperVersionUnsupported) {
		return versionErr
	}

	return repository.ResetJobLiveControls(ctx, jobID)
}
