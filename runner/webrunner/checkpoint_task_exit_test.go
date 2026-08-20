package webrunner

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/resultimport"
	"github.com/gosom/scrapemate"
)

// taskExitWait bounds how long a simulated task waits for its own exit signal
// before reporting that it never arrived. The real code cancels immediately,
// so a passing run never spends this long; only the pre-fix behaviour, which
// waits for scrapemate's inactivity timeout, reaches it.
const taskExitWait = 3 * time.Second

// taskExitReport records, per seed, whether that task's context was cancelled
// once the task finished its own work.
type taskExitReport struct {
	mu        sync.Mutex
	cancelled map[string]bool
}

func (report *taskExitReport) record(seed string, cancelled bool) {
	report.mu.Lock()
	defer report.mu.Unlock()

	if report.cancelled == nil {
		report.cancelled = make(map[string]bool)
	}

	report.cancelled[seed] = cancelled
}

func (report *taskExitReport) stalled() []string {
	report.mu.Lock()
	defer report.mu.Unlock()

	stalled := make([]string, 0, len(report.cancelled))

	for seed, cancelled := range report.cancelled {
		if !cancelled {
			stalled = append(stalled, seed)
		}
	}

	return stalled
}

func (report *taskExitReport) size() int {
	report.mu.Lock()
	defer report.mu.Unlock()

	return len(report.cancelled)
}

// seedExitMonitor reads the exit monitor a seed reports to.
func seedExitMonitor(seed scrapemate.IJob) exiter.Exiter {
	switch typed := seed.(type) {
	case *gmaps.GmapJob:
		return typed.ExitMonitor
	case *gmaps.SearchJob:
		return typed.ExitMonitor
	default:
		return nil
	}
}

// exitSignalingMate imitates a real scrapemate run: it drives the exit monitor
// its seed carries exactly the way gmaps jobs do, writes the task's row, and
// then waits for its context to be cancelled. Waiting is what the real runner
// does too — it is how the inactivity timeout used to end every task.
type exitSignalingMate struct {
	output io.Writer
	places int
	report *taskExitReport
	wait   time.Duration
}

func (mate *exitSignalingMate) Start(ctx context.Context, jobs ...scrapemate.IJob) error {
	if len(jobs) != 1 {
		return errors.New("a checkpoint task must run exactly one seed")
	}

	seed := jobs[0].GetID()

	monitor := seedExitMonitor(jobs[0])
	if monitor == nil {
		return errors.New("the seed does not report to an exit monitor")
	}

	monitor.IncrPlacesFound(mate.places)
	monitor.IncrSeedCompleted(1)

	for range mate.places {
		monitor.IncrPlacesCompleted(1)
	}

	if err := writeTaskResultRow(mate.output, seed); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		mate.report.record(seed, true)
	case <-time.After(mate.wait):
		mate.report.record(seed, false)
	}

	return nil
}

func (*exitSignalingMate) Close() error { return nil }

// writeTaskResultRow writes the header and one distinct business row for a
// task, so a duplicated execution or a lost merge is visible in the final CSV.
// The merge treats a shared address as the same business, which is why the row
// is keyed on the seed.
func writeTaskResultRow(output io.Writer, seed string) error {
	const (
		latitude  = "37.7749"
		longitude = "-122.4194"
	)

	header := resultimport.LegacyHeaders()
	row := make([]string, len(header))

	for index, name := range header {
		switch name {
		case "place_id":
			row[index] = "place-" + seed
		case "title":
			row[index] = "Business " + seed
		case "address":
			row[index] = seed + " Market Street, San Francisco"
		case "latitude":
			row[index] = latitude
		case "longitude":
			row[index] = longitude
		}
	}

	writer := csv.NewWriter(output)
	if err := writer.Write(header); err != nil {
		return err
	}

	if err := writer.Write(row); err != nil {
		return err
	}

	writer.Flush()

	return writer.Error()
}

// TestCheckpointPoolCancelsEveryTaskOnItsOwnCompletion is the pool-level proof
// of the fix. Before it, the pool's single exit monitor had the task count as
// its seed count, so only the last task of the plan could ever satisfy the
// done condition; every earlier task sat in its scrapemate run until the
// three-minute inactivity timeout fired. Each task must now be cancelled by
// its own completion.
func TestCheckpointPoolCancelsEveryTaskOnItsOwnCompletion(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("33333333-3333-4333-8333-333333333333", 2)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	report := &taskExitReport{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 4},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &exitSignalingMate{output: output, places: 2, report: report, wait: taskExitWait}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("checkpointed scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if execution.Tasks.Total < 2 {
		t.Fatalf("expected a multi-task plan, got %#v", execution.Tasks)
	}

	if report.size() != int(execution.Tasks.Total) {
		t.Fatalf("ran %d task(s) of a %d task plan", report.size(), execution.Tasks.Total)
	}

	if stalled := report.stalled(); len(stalled) > 0 {
		t.Fatalf("%d of %d task(s) waited for a timeout instead of their own completion: %v",
			len(stalled), execution.Tasks.Total, stalled)
	}

	// Ending a task early must not cost it its checkpoint: the plan still has
	// to finish completed, resumable, and merged.
	if execution.Tasks.Completed != execution.Tasks.Total || execution.Tasks.Pending != 0 ||
		execution.Tasks.Failed != 0 {
		t.Fatalf("tasks = %#v, want all completed", execution.Tasks)
	}

	rows := readResultPlaceIDs(t, dataFolder+"/"+job.ID+".csv")
	if len(rows) != int(execution.Tasks.Total) {
		t.Fatalf("merged %d row(s) for %d task(s): %v", len(rows), execution.Tasks.Total, rows)
	}
}

// TestCheckpointPoolStopsTheWholeRunAtTheRecordLimit keeps the run-level
// budget honest now that tasks end on their own monitors.
func TestCheckpointPoolStopsTheWholeRunAtTheRecordLimit(t *testing.T) {
	t.Parallel()

	const (
		placesPerTask = 2
		recordLimit   = 2
	)

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("44444444-4444-4444-8444-444444444444", 1)
	job.Data.MaxRecords = recordLimit

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	report := &taskExitReport{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 1},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &exitSignalingMate{
				output: output, places: placesPerTask, report: report, wait: taskExitWait,
			}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("checkpointed scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// The very first task spends the whole budget, so the run must stop with
	// the rest of its plan untouched rather than draining it.
	if execution.Tasks.Total < 2 {
		t.Fatalf("expected a multi-task plan, got %#v", execution.Tasks)
	}

	if report.size() != 1 {
		t.Fatalf("ran %d task(s) after the record limit was reached on the first", report.size())
	}

	// The plan is left exactly resumable: the interrupted task goes back to
	// pending along with everything the run never reached.
	if execution.Tasks.Pending != execution.Tasks.Total {
		t.Fatalf("tasks = %#v, want the whole remaining plan left pending", execution.Tasks)
	}

	if execution.Tasks.Failed != 0 {
		t.Fatalf("tasks = %#v, want no failed task from a budget stop", execution.Tasks)
	}
}
