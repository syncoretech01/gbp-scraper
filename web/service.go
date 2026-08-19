package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// ErrLifecycleUnsupported is returned when a non-queued state is requested
// from a repository that only implements the legacy four-state job contract.
var ErrLifecycleUnsupported = errors.New("job lifecycle storage is unavailable")

type lifecycleJobCreator interface {
	CreateWithState(context.Context, *Job, jobruntime.State) error
}

type Service struct {
	repo               JobRepository
	dataFolder         string
	startedAt          time.Time
	schedulerHeartbeat atomic.Int64
}

func NewService(repo JobRepository, dataFolder string) *Service {
	return &Service{
		repo:       repo,
		dataFolder: dataFolder,
		startedAt:  time.Now().UTC(),
	}
}

func (s *Service) Create(ctx context.Context, job *Job) error {
	return s.repo.Create(ctx, job)
}

// CreateWithState persists a job and its canonical lifecycle state atomically
// when the backing repository supports the upgraded local schema.
func (s *Service) CreateWithState(ctx context.Context, job *Job, state jobruntime.State) error {
	if !state.Valid() {
		return fmt.Errorf("%w: %s", jobruntime.ErrInvalidState, state)
	}

	if creator, ok := s.repo.(lifecycleJobCreator); ok {
		return creator.CreateWithState(ctx, job, state)
	}

	if state != jobruntime.StateQueued {
		return ErrLifecycleUnsupported
	}

	return s.repo.Create(ctx, job)
}

func (s *Service) All(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{})
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	datapath, err := s.csvPath(id)
	if err != nil {
		return err
	}

	if _, err := os.Stat(datapath); err == nil {
		if err := os.Remove(datapath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, job *Job) error {
	return s.repo.Update(ctx, job)
}

func (s *Service) SelectPending(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{Status: StatusPending, Limit: 1})
}

// csvPath returns the on-disk path of a job's CSV output, rejecting ids that
// could escape the data folder.
func (s *Service) csvPath(id string) (string, error) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid file name")
	}

	return filepath.Join(s.dataFolder, id+".csv"), nil
}

func (s *Service) GetCSV(_ context.Context, id string) (string, error) {
	datapath, err := s.csvPath(id)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(datapath); os.IsNotExist(err) {
		return "", fmt.Errorf("%w for job %s", ErrPlacesNotFound, id)
	}

	return datapath, nil
}
