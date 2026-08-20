package webrunner

import (
	"context"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/scrapemate"
)

// taskSeedCount is how many seeds one checkpoint task owns. The durable plan
// stores exactly one seed per task entry, so a task is finished once that seed
// and every listing it found have been processed.
const taskSeedCount = 1

// taskExiter is the exit monitor a single checkpoint task runs behind.
//
// A pool run shares one run-level monitor. That monitor owns the job's record
// budget and feeds progress reporting, so its seed count is the number of
// tasks in the plan. The exiter's done condition is
//
//	seedCompleted >= seedCount && placesCompleted >= placesFound
//
// which, with a task-count seed count, can only become true while the last
// task of the pool is running. Every earlier task therefore finished its work
// in seconds and then sat in its own scrapemate instance until the inactivity
// timeout fired.
//
// taskExiter removes that wait without weakening run-level accounting. It owns
// a private monitor whose seed count is the task's own seed count, and it
// forwards every counter update to both monitors:
//
//   - the private monitor cancels this task's context the moment this task's
//     seeds and places are complete;
//   - the run-level monitor keeps accumulating across tasks, so its snapshot,
//     its MaxRecords budget, and the cancel it already holds for the whole run
//     behave exactly as before.
//
// The inactivity timeout stays configured on every task; it is now the safety
// net for a genuinely stalled task rather than the normal way a task ends.
type taskExiter struct {
	// own counts only this task's seeds and places.
	own exiter.Exiter
	// run is the shared pool monitor; nil is tolerated so a caller may run a
	// task without run-level accounting.
	run exiter.Exiter
}

// newTaskExiter builds the composite monitor for one task. seedCount is the
// number of seeds handed to that task, not the size of the pool plan.
func newTaskExiter(run exiter.Exiter, seedCount int) *taskExiter {
	own := exiter.New()
	own.SetSeedCount(seedCount)

	return &taskExiter{own: own, run: run}
}

// SetSeedCount retargets this task's own completion condition. It deliberately
// does not touch the run-level monitor: that count is the pool plan's size and
// is owned by the pool supervisor and the coverage engine.
func (t *taskExiter) SetSeedCount(val int) {
	t.own.SetSeedCount(val)
}

// SetCancelFunc registers the cancel for this task's context. The run-level
// monitor keeps the run-wide cancel it was given when the pool started, so a
// record limit reached by any task still stops the whole run; a limit that was
// already reached before this task registered cancels it immediately.
func (t *taskExiter) SetCancelFunc(fn context.CancelFunc) {
	t.own.SetCancelFunc(fn)

	if fn != nil && t.run != nil && exiter.LimitReached(t.run) {
		fn()
	}
}

// IncrSeedCompleted forwards to the run-level monitor first so the global
// budget and progress snapshot never lag behind a task-local exit decision.
func (t *taskExiter) IncrSeedCompleted(val int) {
	if t.run != nil {
		t.run.IncrSeedCompleted(val)
	}

	t.own.IncrSeedCompleted(val)
}

// IncrPlacesFound forwards a batch of discovered listings to both monitors.
func (t *taskExiter) IncrPlacesFound(val int) {
	if t.run != nil {
		t.run.IncrPlacesFound(val)
	}

	t.own.IncrPlacesFound(val)
}

// IncrPlacesCompleted forwards to the run-level monitor first, so a completion
// that crosses the run's MaxRecords budget cancels the run before this task
// concludes it is merely done.
func (t *taskExiter) IncrPlacesCompleted(val int) {
	if t.run != nil {
		t.run.IncrPlacesCompleted(val)
	}

	t.own.IncrPlacesCompleted(val)
}

// Run blocks until this task is complete or ctx ends. The run-level monitor
// has its own Run started by the pool and must not be started again here.
func (t *taskExiter) Run(ctx context.Context) {
	t.own.Run(ctx)
}

// ownSnapshot reports this task's own counters. The run-level snapshot mixes in
// every other task in the pool, so it cannot answer whether this task's seed
// completed.
func (t *taskExiter) ownSnapshot() exiter.Snapshot {
	return exiter.SnapshotOf(t.own)
}

// seedWithExitMonitor returns a copy of seed that reports to monitor.
//
// Seeds are built once for the whole plan with the run-level monitor embedded,
// and the same seed value is reused across retries and across workers. Copying
// keeps the plan entry untouched while every other field, including the shared
// deduper, is carried over unchanged. A seed of an unrecognised type is
// returned as-is, which simply leaves it on the run-level monitor.
func seedWithExitMonitor(seed scrapemate.IJob, monitor exiter.Exiter) scrapemate.IJob {
	switch typed := seed.(type) {
	case *gmaps.GmapJob:
		copied := *typed
		copied.ExitMonitor = monitor

		return &copied
	case *gmaps.SearchJob:
		copied := *typed
		copied.ExitMonitor = monitor

		return &copied
	default:
		return seed
	}
}
