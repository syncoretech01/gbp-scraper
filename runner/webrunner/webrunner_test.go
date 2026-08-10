//nolint:testpackage // This test needs unexported hooks to avoid running a browser.
package webrunner

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/scrapemate"
)

func TestScrapeJobMarksOKBeforeClosingMate(t *testing.T) {
	t.Parallel()

	repo := &memoryJobRepo{}
	svc := web.NewService(repo, t.TempDir())
	job := web.Job{
		ID:     "job-1",
		Name:   "coffee",
		Date:   time.Now().UTC(),
		Status: web.StatusPending,
		Data: web.JobData{
			Keywords: []string{"coffee"},
			Lang:     "en",
			Zoom:     15,
			Lat:      "37.7749",
			Lon:      "-122.4194",
			FastMode: true,
			Radius:   1000,
			Depth:    10,
			MaxTime:  time.Minute,
		},
	}

	if err := svc.Create(context.Background(), &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := &webrunner{
		svc: svc,
		cfg: &runner.Config{DataFolder: t.TempDir(), Concurrency: 1},
		setupMate: func(_ context.Context, _ io.Writer, _ *web.Job) (mateRunner, error) {
			return fakeMate{
				onClose: func() {
					got, err := svc.Get(context.Background(), job.ID)
					if err != nil {
						t.Fatalf("get job during close: %v", err)
					}
					if got.Status != web.StatusOK {
						t.Fatalf("status during close = %q, want %q", got.Status, web.StatusOK)
					}
				},
			}, nil
		},
	}

	if err := w.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape job: %v", err)
	}
}

func TestScrapeJobCreatesOneSeedPerGridCellAndQuery(t *testing.T) {
	t.Parallel()

	repo := &memoryJobRepo{}
	svc := web.NewService(repo, t.TempDir())
	job := web.Job{
		ID:     "job-grid",
		Name:   "San Francisco dentists",
		Date:   time.Now().UTC(),
		Status: web.StatusPending,
		Data: web.JobData{
			Keywords:   []string{"dentists", "dental clinics"},
			Lang:       "en",
			Zoom:       14,
			Depth:      5,
			MaxTime:    time.Minute,
			GridBBox:   "37.750,-122.450,37.795,-122.390",
			GridCellKM: 5,
		},
	}

	if err := svc.Create(context.Background(), &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var seedCount int

	w := &webrunner{
		svc: svc,
		cfg: &runner.Config{DataFolder: t.TempDir(), Concurrency: 1},
		setupMate: func(_ context.Context, _ io.Writer, _ *web.Job) (mateRunner, error) {
			return fakeMate{
				onStart: func(jobs []scrapemate.IJob) {
					seedCount = len(jobs)
				},
			}, nil
		},
	}

	if err := w.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape job: %v", err)
	}

	// The box is approximately one 5 km cell, multiplied by two queries.
	if seedCount != 2 {
		t.Fatalf("seed count = %d, want 2", seedCount)
	}
}

func TestScrapeJobClassifiesDeadlineAsPartial(t *testing.T) {
	t.Parallel()

	repo := &lifecycleMemoryRepo{}
	svc := web.NewService(repo, t.TempDir())
	job := testScrapeJob("job-time-limit")

	if err := svc.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := &webrunner{
		svc: svc,
		cfg: &runner.Config{DataFolder: t.TempDir(), Concurrency: 1},
		setupMate: func(_ context.Context, _ io.Writer, _ *web.Job) (mateRunner, error) {
			return fakeMate{startErr: context.DeadlineExceeded}, nil
		},
	}

	if err := w.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape job: %v", err)
	}

	runtime, err := svc.GetRuntime(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}

	if runtime.State != jobruntime.StatePartial {
		t.Fatalf("state = %q, want %q", runtime.State, jobruntime.StatePartial)
	}

	if runtime.OutcomeReason != jobruntime.StopReasonRuntimeLimit {
		t.Fatalf("reason = %q, want %q", runtime.OutcomeReason, jobruntime.StopReasonRuntimeLimit)
	}

	got, err := svc.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	if got.Status != web.StatusOK {
		t.Fatalf("legacy status = %q, want %q", got.Status, web.StatusOK)
	}
}

func TestScrapeJobPersistsFatalOutcome(t *testing.T) {
	t.Parallel()

	repo := &lifecycleMemoryRepo{}
	svc := web.NewService(repo, t.TempDir())
	job := testScrapeJob("job-failed")

	if err := svc.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := &webrunner{
		svc: svc,
		cfg: &runner.Config{DataFolder: t.TempDir(), Concurrency: 1},
		setupMate: func(_ context.Context, _ io.Writer, _ *web.Job) (mateRunner, error) {
			return fakeMate{startErr: errors.New("browser crashed")}, nil
		},
	}

	err := w.scrapeJob(context.Background(), &job)
	if err == nil || err.Error() != "browser crashed" {
		t.Fatalf("scrape error = %v, want browser crashed", err)
	}

	runtime, getErr := svc.GetRuntime(context.Background(), job.ID)
	if getErr != nil {
		t.Fatalf("get runtime: %v", getErr)
	}

	if runtime.State != jobruntime.StateFailed {
		t.Fatalf("state = %q, want %q", runtime.State, jobruntime.StateFailed)
	}
}

func TestScrapeJobRetryMergesWithoutTruncatingPartialCSV(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	repo := &memoryJobRepo{}
	svc := web.NewService(repo, dataFolder)
	job := testScrapeJob("job-preserve-partial")
	if err := svc.Create(context.Background(), &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	header := []string{"place_id", "title", "website", "phone", "address"}
	writeMergeFixture(t, filepath.Join(dataFolder, job.ID+".csv"), header,
		[]string{"place-1", "Already Saved", "https://saved.example", "+1 415 555 0101", "1 Main St"},
	)
	w := &webrunner{
		svc: svc,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 1},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			writer := csv.NewWriter(output)
			if err := writer.Write(header); err != nil {
				return nil, err
			}
			if err := writer.Write([]string{"place-2", "Collected On Retry", "https://retry.example", "+1 415 555 0102", "2 Main St"}); err != nil {
				return nil, err
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				return nil, err
			}

			return fakeMate{}, nil
		},
	}

	if err := w.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape job: %v", err)
	}
	rows := readMergeFixture(t, filepath.Join(dataFolder, job.ID+".csv"))
	if len(rows) != 3 || rows[1][1] != "Already Saved" || rows[2][1] != "Collected On Retry" {
		t.Fatalf("merged retry rows = %v", rows)
	}
}

func TestStoppedBecausePrefersOperatorRequest(t *testing.T) {
	t.Parallel()

	requested := make(chan jobruntime.StopReason, 1)
	requested <- jobruntime.StopReasonPauseRequested

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := stoppedBecause(ctx, context.DeadlineExceeded, requested); got != jobruntime.StopReasonPauseRequested {
		t.Fatalf("reason = %q, want %q", got, jobruntime.StopReasonPauseRequested)
	}
}

func testScrapeJob(id string) web.Job {
	return web.Job{
		ID:     id,
		Name:   "coffee",
		Date:   time.Now().UTC(),
		Status: web.StatusPending,
		Data: web.JobData{
			Keywords: []string{"coffee"},
			Lang:     "en",
			Zoom:     15,
			Lat:      "37.7749",
			Lon:      "-122.4194",
			FastMode: true,
			Radius:   1000,
			Depth:    10,
			MaxTime:  time.Minute,
		},
	}
}

type fakeMate struct {
	onClose  func()
	onStart  func([]scrapemate.IJob)
	startErr error
}

func (m fakeMate) Start(_ context.Context, jobs ...scrapemate.IJob) error {
	if m.onStart != nil {
		m.onStart(jobs)
	}

	return m.startErr
}

func (m fakeMate) Close() error {
	if m.onClose != nil {
		m.onClose()
	}

	return nil
}

type memoryJobRepo struct {
	mu   sync.Mutex
	jobs map[string]web.Job
}

func (r *memoryJobRepo) Get(_ context.Context, id string) (web.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.jobs[id], nil
}

func (r *memoryJobRepo) Create(_ context.Context, job *web.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.jobs == nil {
		r.jobs = make(map[string]web.Job)
	}

	r.jobs[job.ID] = *job

	return nil
}

func (r *memoryJobRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.jobs, id)
	return nil
}

func (r *memoryJobRepo) Select(_ context.Context, params web.SelectParams) ([]web.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	jobs := make([]web.Job, 0, len(r.jobs))

	for id := range r.jobs {
		job := r.jobs[id]
		if params.Status == "" || job.Status == params.Status {
			jobs = append(jobs, job)
		}
	}

	return jobs, nil
}

func (r *memoryJobRepo) Update(_ context.Context, job *web.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.jobs[job.ID] = *job
	return nil
}

type lifecycleMemoryRepo struct {
	memoryJobRepo
	runtimes map[string]web.JobRuntime
}

func (r *lifecycleMemoryRepo) CreateWithState(
	_ context.Context,
	job *web.Job,
	state jobruntime.State,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.jobs == nil {
		r.jobs = make(map[string]web.Job)
	}
	if r.runtimes == nil {
		r.runtimes = make(map[string]web.JobRuntime)
	}

	r.jobs[job.ID] = *job
	r.runtimes[job.ID] = web.JobRuntime{
		JobID:     job.ID,
		State:     state,
		Stage:     jobruntime.StagePreparingQueries,
		UpdatedAt: time.Now().UTC(),
	}

	return nil
}

func (r *lifecycleMemoryRepo) GetRuntime(_ context.Context, id string) (web.JobRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	runtime, ok := r.runtimes[id]
	if !ok {
		return web.JobRuntime{}, web.ErrLifecycleNotFound
	}

	return runtime, nil
}

func (r *lifecycleMemoryRepo) ApplyControl(
	_ context.Context,
	_ string,
	_ jobruntime.Control,
) (web.JobRuntime, jobruntime.ControlDecision, error) {
	return web.JobRuntime{}, jobruntime.ControlDecision{}, web.ErrLifecycleUnsupported
}

func (r *lifecycleMemoryRepo) SetOutcome(
	_ context.Context,
	id string,
	outcome jobruntime.Outcome,
	message string,
) (web.JobRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	runtime, ok := r.runtimes[id]
	if !ok {
		return web.JobRuntime{}, web.ErrLifecycleNotFound
	}

	legacyStatus, err := jobruntime.LegacyStatusForState(outcome.State)
	if err != nil {
		return web.JobRuntime{}, err
	}

	runtime.State = outcome.State
	runtime.OutcomeReason = outcome.Reason
	runtime.Message = message
	runtime.UpdatedAt = time.Now().UTC()
	r.runtimes[id] = runtime

	job := r.jobs[id]
	job.Status = string(legacyStatus)
	r.jobs[id] = job

	return runtime, nil
}

func (r *lifecycleMemoryRepo) EventsAfter(
	context.Context,
	string,
	int64,
	int,
) ([]web.JobEvent, error) {
	return nil, nil
}
