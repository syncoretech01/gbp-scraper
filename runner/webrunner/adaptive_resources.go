package webrunner

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Adaptive performance measures four local dimensions before it lets a run
// take more capacity: CPU, RAM, free disk, and the number of browser
// processes this application actually has alive. A fifth dimension, the
// block rate, is derived from how recent task attempts failed.
//
// None of the measurements adds network traffic or concurrency: the browser
// census reads the local process table, and the block rate reuses the failure
// classification the pool already computes for every finished attempt.
const (
	// browserCensusInterval spaces out process-table reads. Concurrency
	// adaptation samples every resourceSampleInterval, which is far too hot
	// for a full process enumeration, so the census is cached in between.
	browserCensusInterval = 10 * time.Second
	// browserCensusTimeout bounds one process-table read. A slow or locked
	// table must never delay the supervisor loop.
	browserCensusTimeout = 3 * time.Second
	// browserCensusMaxProcesses bounds how many processes one census will
	// inspect, so an unusually busy host cannot turn the census into a stall.
	browserCensusMaxProcesses = 4096
	// browserAncestorDepth bounds the parent walk used to attribute a browser
	// process to this application.
	browserAncestorDepth = 8

	// recoveryCPUPercent is the highest CPU load at which a run may take
	// concurrency back. Above it the host is already busy, so a clean failure
	// window is not evidence that more parallelism is safe.
	recoveryCPUPercent = 70
	// recoveryMemoryBytes is the available memory a run must still see before
	// it may recover a concurrency step.
	recoveryMemoryBytes uint64 = 2 << 30
	// moderateMemoryBytes and severeMemoryBytes are the RAM-pressure steps at
	// which the browser pool and pages-per-browser budgets shrink.
	moderateMemoryBytes uint64 = 2 << 30
	severeMemoryBytes   uint64 = 1 << 30
	// browserHeadroomSlack tolerates browsers that are still shutting down
	// when the census runs, so a normal teardown does not block recovery.
	browserHeadroomSlack = 2
	// bytesPerMebibyteShift converts a byte count to mebibytes for the
	// operator-facing text of a worker event. Operators configure the memory
	// ceiling in MB, so the evidence has to read back in the same unit.
	bytesPerMebibyteShift = 20
)

// memoryCeilingExceeded reports whether the sampled memory use has reached an
// operator-configured ceiling.
//
// The ceiling is enforced here, at the application level, because that is the
// only place this project controls: the scraping engine builds its own browser
// launch arguments and exposes no hook for a per-process memory limit, and
// forking it is out of scope. What this application does control is how much
// work it asks for, so the ceiling acts on exactly that.
//
// The measurement is MemoryUsedBytes, the same host memory-in-use figure the
// job monitor already reports, so an operator sets the ceiling against the
// number the UI shows them. A zero ceiling is "no ceiling" and reproduces the
// behaviour every run had before the control existed.
func memoryCeilingExceeded(ceiling uint64, sample workerResourceSample) bool {
	return ceiling > 0 && sample.MemoryUsedBytes >= ceiling
}

// browserProcessNames are the executable names a Playwright-driven browser
// runs under. Matching is case-insensitive and ignores a trailing extension.
var browserProcessNames = []string{
	"chrome", "chromium", "headless_shell", "msedge", "firefox",
}

// browserCensus caches the most recent count of browser processes owned by
// this application. The zero value is ready to use.
type browserCensus struct {
	mu        sync.Mutex
	count     int
	sampledAt time.Time
}

// count returns the cached browser-process count, refreshing it at most once
// per browserCensusInterval. A failed census keeps the previous value: the
// measurement is evidence for adaptation, never a reason to fail a run.
func (census *browserCensus) countBrowsers(ctx context.Context, selfPID int32) int {
	census.mu.Lock()
	defer census.mu.Unlock()

	if !census.sampledAt.IsZero() && time.Since(census.sampledAt) < browserCensusInterval {
		return census.count
	}

	censusCtx, cancel := context.WithTimeout(ctx, browserCensusTimeout)
	defer cancel()

	observed, err := countManagedBrowserProcesses(censusCtx, selfPID)
	if err != nil {
		return census.count
	}

	census.count = observed
	census.sampledAt = time.Now()

	return census.count
}

// countManagedBrowserProcesses counts the browser processes whose parent
// chain reaches selfPID. Browsers the operator started themselves therefore
// never inflate the measurement, and an unattributable process is skipped
// rather than guessed at.
func countManagedBrowserProcesses(ctx context.Context, selfPID int32) (int, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return 0, err
	}

	if len(processes) > browserCensusMaxProcesses {
		processes = processes[:browserCensusMaxProcesses]
	}

	parents := make(map[int32]int32, len(processes))
	candidates := make([]int32, 0, 8)

	for _, candidate := range processes {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		parent, parentErr := candidate.PpidWithContext(ctx)
		if parentErr != nil {
			continue
		}

		parents[candidate.Pid] = parent

		name, nameErr := candidate.NameWithContext(ctx)
		if nameErr != nil {
			continue
		}

		if isBrowserProcessName(name) {
			candidates = append(candidates, candidate.Pid)
		}
	}

	owned := 0

	for _, pid := range candidates {
		if hasAncestor(parents, pid, selfPID) {
			owned++
		}
	}

	return owned, nil
}

// isBrowserProcessName reports whether an executable name belongs to a
// browser the scraper can launch.
func isBrowserProcessName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".exe")

	for _, candidate := range browserProcessNames {
		if name == candidate {
			return true
		}
	}

	return false
}

// hasAncestor walks a bounded parent chain looking for one process ID.
func hasAncestor(parents map[int32]int32, pid, ancestor int32) bool {
	for depth := 0; depth < browserAncestorDepth; depth++ {
		parent, found := parents[pid]
		if !found || parent <= 0 || parent == pid {
			return false
		}

		if parent == ancestor {
			return true
		}

		pid = parent
	}

	return false
}

// blockedFailureMarkers are the substrings that identify a refusal by the
// platform rather than a local fault: an explicit rate limit, a consent or
// interstitial redirect, or a challenge page. They are matched against the
// attempt error, which is the only block evidence available without changing
// the scraping engine.
var blockedFailureMarkers = []string{
	"429", "too many requests", "rate limit", "rate-limit", "ratelimit",
	"captcha", "recaptcha", "unusual traffic", "/sorry/", "sorry/index",
	"consent.google", "http 403", "status 403", "status code 403",
	"access denied", "temporarily blocked", "blocked by",
}

// isBlockedFailure reports whether an attempt error looks like a platform
// block or rate limit. It is deliberately conservative: an unrecognised error
// stays an ordinary failure, so the block rate never overstates itself.
func isBlockedFailure(message string) bool {
	for _, marker := range blockedFailureMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}

	return false
}

// decideBlockBudget is the pure block-rate rule. Blocks are treated more
// harshly than ordinary failures because continuing at the same concurrency
// makes them worse:
//
//   - any window containing a block halves the budget (never below one);
//   - a window with at least three attempts and no block recovers one step;
//   - a quiet window leaves the budget unchanged.
func decideBlockBudget(current, desired, blocks, attempts int) int {
	if current < 1 {
		current = 1
	}

	if current > desired {
		current = desired
	}

	switch {
	case blocks > 0:
		return max(1, current/2)
	case attempts >= 3 && current < desired:
		return current + 1
	default:
		return current
	}
}

// recoveryHasHeadroom reports whether the four measured local dimensions plus
// the block rate all allow a run to take a concurrency step back. Every
// condition must hold: a clean failure window on its own is not enough.
//
// allowedBrowsers is the browser count the current plan expects; a census
// above it (beyond a small teardown slack) means browsers from the previous
// budget are still alive, so more parallelism would overshoot the host.
//
// memoryCeiling is the operator's optional memory ceiling. While it is
// exceeded there is no head-room by definition, so no budget may recover.
func recoveryHasHeadroom(sample workerResourceSample, blocks, allowedBrowsers int, memoryCeiling uint64) bool {
	if blocks > 0 {
		return false
	}

	if memoryCeilingExceeded(memoryCeiling, sample) {
		return false
	}

	if sample.CPUPercent >= recoveryCPUPercent {
		return false
	}

	if sample.MemoryAvailableBytes > 0 && sample.MemoryAvailableBytes < recoveryMemoryBytes {
		return false
	}

	if allowedBrowsers > 0 && sample.BrowserProcesses > allowedBrowsers+browserHeadroomSlack {
		return false
	}

	return true
}

// adaptiveBrowserBudget reduces the per-task browser pool and pages-per-browser
// budgets under RAM pressure. Zero desired values mean "engine default" and are
// only replaced when memory pressure makes an explicit, smaller budget safer.
//
// The result never exceeds the configured budget, so this can only ever lower
// the memory the run asks for.
//
// memoryCeiling is the operator's optional ceiling. Crossing it applies the
// severe step directly — one browser, one page — regardless of how much memory
// the host still reports as available, because the ceiling is a statement
// about how much this run may use rather than about how loaded the host is.
func adaptiveBrowserBudget(
	desiredPool, desiredPages int,
	sample workerResourceSample,
	memoryCeiling uint64,
) (pool, pages int) {
	pool, pages = desiredPool, desiredPages

	if memoryCeilingExceeded(memoryCeiling, sample) {
		return 1, 1
	}

	available := sample.MemoryAvailableBytes
	if available == 0 {
		return pool, pages
	}

	switch {
	case available < severeMemoryBytes:
		pool, pages = 1, 1
	case available < moderateMemoryBytes:
		if desiredPool > 0 {
			pool = max(1, desiredPool/2)
		} else {
			pool = 1
		}

		if desiredPages > 0 {
			pages = max(1, desiredPages/2)
		} else {
			pages = 1
		}
	}

	if desiredPool > 0 && pool > desiredPool {
		pool = desiredPool
	}

	if desiredPages > 0 && pages > desiredPages {
		pages = desiredPages
	}

	return pool, pages
}
