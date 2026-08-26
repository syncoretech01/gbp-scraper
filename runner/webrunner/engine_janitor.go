package webrunner

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// The janitor turns abandonment into reclamation. An abandoned engine's
// goroutine is parked inside playwright-go waiting for a browser that will
// never answer — but killing that engine's Node driver closes the driver
// transport, playwright-go's receive loop aborts every pending protocol call,
// the teardown chain finally returns, and the containment monitor releases the
// Go-side resources. Killing the browser processes afterwards releases the
// ~300MB-per-browser memory and corrects the browser census that would
// otherwise veto adaptive recovery for every later job.
//
// Safety rests on one invariant: the sweep runs ONLY while no engine is
// legitimately active (containment.activeEngines == 0, i.e. between jobs). At
// that moment every scraper-owned run-driver or browser process is, by
// construction, orphaned — clean teardowns take their processes with them.
const (
	// janitorReclaimWait is how long the janitor gives the driver-kill to
	// propagate (transport abort -> teardown returns -> monitor reclaims)
	// before it falls through to killing browser processes directly.
	janitorReclaimWait = 5 * time.Second
	// janitorScanTimeout bounds one process-table scan.
	janitorScanTimeout = 10 * time.Second
	// janitorSweepCooldown spaces out sweeps: if a kill keeps failing the
	// janitor must not rescan the process table on every loop tick.
	janitorSweepCooldown = 30 * time.Second
)

// janitorProcess is the minimal process-table row the selection logic needs;
// a plain struct so the selection is a pure, deterministically testable
// function.
type janitorProcess struct {
	PID     int32
	PPID    int32
	Name    string
	Cmdline string
}

// selectOrphanEngineProcesses picks the processes an abandoned engine can have
// left behind: Playwright Node drivers (node ... run-driver) and browser
// processes, in both cases only when the parent chain reaches selfPID so a
// browser or Node process the operator runs themselves is never touched.
func selectOrphanEngineProcesses(procs []janitorProcess, selfPID int32) (drivers, browsers []int32) {
	parents := make(map[int32]int32, len(procs))
	for _, p := range procs {
		parents[p.PID] = p.PPID
	}

	for _, p := range procs {
		if p.PID == selfPID || !hasAncestor(parents, p.PID, selfPID) {
			continue
		}

		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.Name)), ".exe")

		switch {
		case name == "node" && strings.Contains(p.Cmdline, "run-driver"):
			drivers = append(drivers, p.PID)
		case isBrowserProcessName(name):
			browsers = append(browsers, p.PID)
		}
	}

	return drivers, browsers
}

// sweepAbandonedEngines reclaims the OS processes of abandoned engines. Called
// at safe points only — after a job finishes, when no engine is active. It is
// best-effort by design: a failed kill leaves the registry entry in place and
// the next safe point tries again.
func (w *webrunner) sweepAbandonedEngines(ctx context.Context) {
	if w.containment == nil || w.containment.AbandonedNow() == 0 || w.containment.activeEngines.Load() != 0 {
		return
	}

	now := time.Now().Unix()
	last := w.containment.lastSweepUnix.Load()

	if now-last < int64(janitorSweepCooldown/time.Second) ||
		!w.containment.lastSweepUnix.CompareAndSwap(last, now) {
		return
	}

	scanCtx, cancel := context.WithTimeout(ctx, janitorScanTimeout)
	defer cancel()

	procs, err := enumerateJanitorProcesses(scanCtx)
	if err != nil {
		log.Printf("engine janitor: process scan failed, will retry at the next safe point: %v", err)

		return
	}

	drivers, browsers := selectOrphanEngineProcesses(procs, int32(os_Getpid()))
	if len(drivers) == 0 && len(browsers) == 0 {
		return
	}

	abandoned := w.containment.snapshotAbandoned()

	// Drivers first: killing the driver is what UNWEDGES the parked goroutine,
	// letting the containment monitor reclaim the Go-side resources too.
	for _, pid := range drivers {
		killProcess(pid)
	}

	// Give the transport aborts a bounded window to propagate before falling
	// through to the browsers, then take those down regardless: the memory is
	// needed whether or not the goroutine unwedged.
	deadline := time.Now().Add(janitorReclaimWait)
	for time.Now().Before(deadline) && w.containment.AbandonedNow() > 0 {
		time.Sleep(200 * time.Millisecond)
	}

	for _, pid := range browsers {
		killProcess(pid)
		reapOrphanProcess(pid)
	}

	for _, engine := range abandoned {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), engine.jobID, "engine-killed", "information",
			"The janitor terminated the wedged engine's driver and browser processes; leaked resources are being reclaimed",
			map[string]any{
				"task_key":          engine.taskKey,
				"wedged_for":        time.Since(engine.since).Round(time.Second).String(),
				"driver_processes":  len(drivers),
				"browser_processes": len(browsers),
			},
		)
	}

	log.Printf("engine janitor: killed %d driver(s) and %d browser(s) left by %d abandoned engine(s)",
		len(drivers), len(browsers), len(abandoned))
}

// enumerateJanitorProcesses reads the live process table into plain rows.
func enumerateJanitorProcesses(ctx context.Context) ([]janitorProcess, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}

	if len(processes) > browserCensusMaxProcesses {
		processes = processes[:browserCensusMaxProcesses]
	}

	rows := make([]janitorProcess, 0, len(processes))

	for _, candidate := range processes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		row := janitorProcess{PID: candidate.Pid}
		if ppid, ppidErr := candidate.PpidWithContext(ctx); ppidErr == nil {
			row.PPID = ppid
		}
		if name, nameErr := candidate.NameWithContext(ctx); nameErr == nil {
			row.Name = name
		}
		if cmdline, cmdErr := candidate.CmdlineWithContext(ctx); cmdErr == nil {
			row.Cmdline = cmdline
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// killProcess terminates one PID, best effort.
func killProcess(pid int32) {
	proc, err := process.NewProcess(pid)
	if err != nil {
		return
	}

	_ = proc.Kill()
}
