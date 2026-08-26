package webrunner

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// engineContainment tracks scrape engines the pool has abandoned because their
// shutdown never returned (see awaitEngine). Abandonment bounds the JOB's
// exposure to the upstream teardown wedge; this registry bounds the PROCESS's.
//
// Vocabulary, kept distinct on purpose:
//
//   - engine-shutdown-timeout (event, recorded by the pool): the UPSTREAM
//     failure — playwright's browser teardown blocked past the grace period.
//   - adopted (this registry): OUR containment — the wedged goroutine, its
//     Chromium process tree, its Playwright node driver and its CSV file
//     handle are intentionally left behind, but registered and watched.
//   - engine-reclaimed (event): the abandoned engine eventually returned —
//     usually because the janitor killed its driver and the transport abort
//     unwedged it — and every Go-side resource was then released for real.
//
// Nothing here waits on a wedged engine with a deadline: the detached monitor
// is the ONE place an unbounded wait is correct, because it replaces a silent
// leak with an observed one.
type engineContainment struct {
	mu   sync.Mutex
	next uint64
	live map[uint64]*abandonedEngine

	// activeEngines counts engines currently executing legitimately. The
	// janitor only sweeps when this is zero: at that moment every surviving
	// scraper-owned browser or driver process belongs to an abandoned engine.
	activeEngines  atomic.Int64
	abandonedTotal atomic.Int64
	reclaimedTotal atomic.Int64
	// lastSweepUnix throttles janitor process-table scans while a kill keeps
	// failing; stored as Unix seconds for lock-free access.
	lastSweepUnix atomic.Int64
}

type abandonedEngine struct {
	id      uint64
	jobID   string
	taskKey string
	since   time.Time
	runPath string
	outfile *os.File
	done    <-chan error
}

// engineStarted marks one engine as legitimately running. Every runCheckpointTask
// engine lifecycle brackets itself with engineStarted/engineFinished so the
// janitor knows when a process-table sweep is safe.
func (c *engineContainment) engineStarted() {
	c.activeEngines.Add(1)
}

func (c *engineContainment) engineFinished() {
	c.activeEngines.Add(-1)
}

// adopt registers an abandoned engine and spawns its detached monitor. The
// monitor waits — without any bound — for the engine's Start call to finally
// return, then closes the CSV handle, deletes the orphaned run file, and
// reports through reclaimed. The adopted engine no longer counts as active.
func (c *engineContainment) adopt(
	jobID, taskKey, runPath string,
	outfile *os.File,
	done <-chan error,
	reclaimed func(engine abandonedEngine, wedgedFor time.Duration),
) {
	c.mu.Lock()
	c.next++
	engine := &abandonedEngine{
		id: c.next, jobID: jobID, taskKey: taskKey,
		since: time.Now().UTC(), runPath: runPath, outfile: outfile, done: done,
	}
	c.live[engine.id] = engine
	c.mu.Unlock()

	c.abandonedTotal.Add(1)
	c.engineFinished()

	go func() {
		<-done

		c.mu.Lock()
		delete(c.live, engine.id)
		c.mu.Unlock()

		// The engine returned, so its errgroup — including the CSV writer
		// goroutine that shared this handle — has exited; closing is safe now
		// and only now.
		if engine.outfile != nil {
			_ = engine.outfile.Close()
		}

		if engine.runPath != "" {
			_ = os.Remove(engine.runPath)
		}

		c.reclaimedTotal.Add(1)

		if reclaimed != nil {
			reclaimed(*engine, time.Since(engine.since))
		}
	}()
}

// AbandonedNow reports how many engines are currently abandoned and unreclaimed.
func (c *engineContainment) AbandonedNow() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.live)
}

// snapshotAbandoned lists the currently abandoned engines for reporting.
func (c *engineContainment) snapshotAbandoned() []abandonedEngine {
	c.mu.Lock()
	defer c.mu.Unlock()

	engines := make([]abandonedEngine, 0, len(c.live))
	for _, engine := range c.live {
		engines = append(engines, *engine)
	}

	return engines
}

func newEngineContainment() *engineContainment {
	return &engineContainment{live: make(map[uint64]*abandonedEngine)}
}
