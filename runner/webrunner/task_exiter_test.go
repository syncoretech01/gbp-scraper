package webrunner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/scrapemate"
)

const (
	// exitSignalTimeout is a failure guard, not a timing assertion: a correct
	// implementation cancels immediately and never reaches it.
	exitSignalTimeout = 5 * time.Second
	// stillRunningWindow bounds how long a negative assertion waits before
	// accepting that no cancel is pending.
	stillRunningWindow = 200 * time.Millisecond
)

func waitCancelled(t *testing.T, ctx context.Context, what string) {
	t.Helper()

	select {
	case <-ctx.Done():
	case <-time.After(exitSignalTimeout):
		t.Fatalf("%s was not cancelled once its own work finished", what)
	}
}

func requireStillRunning(t *testing.T, ctx context.Context, what string) {
	t.Helper()

	select {
	case <-ctx.Done():
		t.Fatalf("%s was cancelled while it still had work in flight", what)
	case <-time.After(stillRunningWindow):
	}
}

// TestTaskExiterFinishesTaskOneOfAMultiTaskPlan pins the production defect: a
// pool of several tasks shares one run-level monitor whose seed count is the
// task count, so its done condition cannot be met until the last task. Task
// one must still exit the moment its own seed and listings are done.
func TestTaskExiterFinishesTaskOneOfAMultiTaskPlan(t *testing.T) {
	t.Parallel()

	const (
		pendingTasks = 3
		placesFound  = 2
	)

	runMonitor := exiter.New()
	runMonitor.SetSeedCount(pendingTasks)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	runMonitor.SetCancelFunc(runCancel)

	go runMonitor.Run(runCtx)

	taskCtx, taskCancel := context.WithCancel(runCtx)
	defer taskCancel()

	monitor := newTaskExiter(runMonitor, taskSeedCount)
	monitor.SetCancelFunc(taskCancel)

	go monitor.Run(taskCtx)

	monitor.IncrPlacesFound(placesFound)
	monitor.IncrSeedCompleted(1)

	for range placesFound {
		monitor.IncrPlacesCompleted(1)
	}

	waitCancelled(t, taskCtx, "task one of a three task plan")

	// The run-level monitor is still two tasks short of its own done
	// condition, which is exactly why the shared monitor could not do this.
	if runCtx.Err() != nil {
		t.Fatal("the whole run was cancelled after only one task finished")
	}

	snapshot := exiter.SnapshotOf(runMonitor)
	if snapshot.SeedsTotal != pendingTasks || snapshot.SeedsCompleted != 1 ||
		snapshot.PlacesFound != placesFound || snapshot.PlacesCompleted != placesFound {
		t.Fatalf("run-level snapshot = %+v", snapshot)
	}
}

// TestTaskExiterWaitsForItsOwnPlaceDetails proves the completion condition is
// both halves: a seed that found listings may not end the task until every one
// of those listings has been processed.
func TestTaskExiterWaitsForItsOwnPlaceDetails(t *testing.T) {
	t.Parallel()

	runMonitor := exiter.New()
	runMonitor.SetSeedCount(2)

	taskCtx, taskCancel := context.WithCancel(context.Background())
	defer taskCancel()

	monitor := newTaskExiter(runMonitor, taskSeedCount)
	monitor.SetCancelFunc(taskCancel)

	go monitor.Run(taskCtx)

	monitor.IncrPlacesFound(2)
	monitor.IncrSeedCompleted(1)
	monitor.IncrPlacesCompleted(1)

	requireStillRunning(t, taskCtx, "a task with one place detail outstanding")

	monitor.IncrPlacesCompleted(1)

	waitCancelled(t, taskCtx, "a task whose place details all finished")
}

// TestTaskExiterKeepsTheRunLevelRecordBudget checks that per-task exits do not
// cost the run its global MaxRecords budget or its accumulated snapshot.
func TestTaskExiterKeepsTheRunLevelRecordBudget(t *testing.T) {
	t.Parallel()

	const (
		pendingTasks   = 3
		recordLimit    = 3
		placesPerTask  = 1
		expectedPlaces = pendingTasks * placesPerTask
	)

	runMonitor := exiter.New(exiter.WithMaximumPlaces(recordLimit))
	runMonitor.SetSeedCount(pendingTasks)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	runMonitor.SetCancelFunc(runCancel)

	for index := range pendingTasks {
		taskCtx, taskCancel := context.WithCancel(runCtx)

		monitor := newTaskExiter(runMonitor, taskSeedCount)
		monitor.SetCancelFunc(taskCancel)

		go monitor.Run(taskCtx)

		monitor.IncrPlacesFound(placesPerTask)
		monitor.IncrSeedCompleted(1)
		monitor.IncrPlacesCompleted(placesPerTask)

		waitCancelled(t, taskCtx, fmt.Sprintf("task %d", index+1))
		taskCancel()
	}

	if !exiter.LimitReached(runMonitor) {
		t.Fatal("the run-level record limit was not reached")
	}

	waitCancelled(t, runCtx, "the run once its record limit was reached")

	snapshot := exiter.SnapshotOf(runMonitor)
	if snapshot.SeedsCompleted != pendingTasks || snapshot.PlacesFound != expectedPlaces ||
		snapshot.PlacesCompleted != expectedPlaces || !snapshot.LimitReached {
		t.Fatalf("run-level snapshot = %+v", snapshot)
	}
}

// TestTaskExiterCancelsATaskStartedAfterTheLimit covers the resume case where
// the budget was already spent before this task registered its cancel.
func TestTaskExiterCancelsATaskStartedAfterTheLimit(t *testing.T) {
	t.Parallel()

	runMonitor := exiter.New(exiter.WithMaximumPlaces(1))
	runMonitor.SetSeedCount(2)
	runMonitor.IncrPlacesFound(1)
	runMonitor.IncrPlacesCompleted(1)

	if !exiter.LimitReached(runMonitor) {
		t.Fatal("the record limit was not reached by the setup")
	}

	taskCtx, taskCancel := context.WithCancel(context.Background())
	defer taskCancel()

	monitor := newTaskExiter(runMonitor, taskSeedCount)
	monitor.SetCancelFunc(taskCancel)

	waitCancelled(t, taskCtx, "a task started after the record limit was reached")
}

// TestTaskExiterLeavesTheRunLevelSeedCountAlone guards the coverage engine and
// the progress snapshot: only the pool may change the run-level plan size.
func TestTaskExiterLeavesTheRunLevelSeedCountAlone(t *testing.T) {
	t.Parallel()

	const (
		pendingTasks = 4
		taskSeeds    = 7
	)

	runMonitor := exiter.New()
	runMonitor.SetSeedCount(pendingTasks)

	monitor := newTaskExiter(runMonitor, taskSeedCount)
	monitor.SetSeedCount(taskSeeds)

	if got := exiter.SnapshotOf(runMonitor).SeedsTotal; got != pendingTasks {
		t.Fatalf("run-level seed count = %d, want %d", got, pendingTasks)
	}

	if got := monitor.ownSnapshot().SeedsTotal; got != taskSeeds {
		t.Fatalf("task seed count = %d, want %d", got, taskSeeds)
	}
}

// TestTaskExiterForwardsCountersRaceFree runs the forwarding path from many
// goroutines the way scrapemate workers do.
func TestTaskExiterForwardsCountersRaceFree(t *testing.T) {
	t.Parallel()

	const (
		goroutines   = 8
		perGoroutine = 50
		total        = goroutines * perGoroutine
	)

	runMonitor := exiter.New()
	// A seed count neither monitor can reach keeps the run alive for the whole
	// test, so the assertion is about forwarding rather than about exiting.
	runMonitor.SetSeedCount(total * 2)

	monitor := newTaskExiter(runMonitor, total*2)

	var group sync.WaitGroup

	for range goroutines {
		group.Add(1)

		go func() {
			defer group.Done()

			for range perGoroutine {
				monitor.IncrPlacesFound(1)
				monitor.IncrSeedCompleted(1)
				monitor.IncrPlacesCompleted(1)
			}
		}()
	}

	group.Wait()

	runSnapshot := exiter.SnapshotOf(runMonitor)
	if runSnapshot.SeedsCompleted != total || runSnapshot.PlacesFound != total ||
		runSnapshot.PlacesCompleted != total {
		t.Fatalf("run-level snapshot = %+v", runSnapshot)
	}

	ownSnapshot := monitor.ownSnapshot()
	if ownSnapshot.SeedsCompleted != total || ownSnapshot.PlacesFound != total ||
		ownSnapshot.PlacesCompleted != total {
		t.Fatalf("task snapshot = %+v", ownSnapshot)
	}
}

// TestTaskExiterToleratesAMissingRunMonitor keeps the composite usable when a
// caller runs a task without run-level accounting.
func TestTaskExiterToleratesAMissingRunMonitor(t *testing.T) {
	t.Parallel()

	taskCtx, taskCancel := context.WithCancel(context.Background())
	defer taskCancel()

	monitor := newTaskExiter(nil, taskSeedCount)
	monitor.SetCancelFunc(taskCancel)

	go monitor.Run(taskCtx)

	monitor.IncrPlacesFound(1)
	monitor.IncrSeedCompleted(1)
	monitor.IncrPlacesCompleted(1)

	waitCancelled(t, taskCtx, "a task without a run-level monitor")
}

func TestSeedWithExitMonitorCopiesInsteadOfMutatingThePlan(t *testing.T) {
	t.Parallel()

	runMonitor := exiter.New()
	seed := gmaps.NewGmapJob(
		"seed-1", "en", "coffee", 10, false, "37.7749,-122.4194", 15,
		gmaps.WithExitMonitor(runMonitor),
	)

	monitor := newTaskExiter(runMonitor, taskSeedCount)

	rebound, ok := seedWithExitMonitor(seed, monitor).(*gmaps.GmapJob)
	if !ok {
		t.Fatal("rebound seed is not a Maps seed")
	}

	if rebound == seed {
		t.Fatal("the plan's own seed value was reused instead of copied")
	}

	if rebound.ExitMonitor != exiter.Exiter(monitor) {
		t.Fatal("the copied seed does not report to the task monitor")
	}

	if seed.ExitMonitor != runMonitor {
		t.Fatal("the plan's seed was mutated")
	}

	if rebound.GetID() != seed.GetID() || rebound.GetFullURL() != seed.GetFullURL() {
		t.Fatal("the copied seed is not the same unit of work")
	}

	// Search seeds take the same path; an unknown seed type is passed through.
	search := gmaps.NewSearchJob(
		&gmaps.MapSearchParams{Query: "coffee", Hl: "en"},
		gmaps.WithSearchJobExitMonitor(runMonitor),
	)

	reboundSearch, ok := seedWithExitMonitor(search, monitor).(*gmaps.SearchJob)
	if !ok {
		t.Fatal("rebound search seed is not a search seed")
	}

	if reboundSearch.ExitMonitor != exiter.Exiter(monitor) || search.ExitMonitor != runMonitor {
		t.Fatal("the search seed was not rebound onto a copy")
	}

	var plain scrapemate.IJob = &scrapemate.Job{ID: "plain"}
	if seedWithExitMonitor(plain, monitor) != plain {
		t.Fatal("an unsupported seed should be passed through untouched")
	}
}
