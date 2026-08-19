package webrunner

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// liveRunState carries everything a run reconfigures between tasks: the
// deadline (extendable while running), the proxy plan (switchable and, for
// sticky strategies, per-proxy attributed), and retry-current signalling.
type liveRunState struct {
	// deadlineUnixNano is the current absolute run deadline. The supervisor
	// extends it when the operator adds runtime.
	deadlineUnixNano atomic.Int64
	// appliedExtension tracks how many extension seconds are already folded
	// into the deadline, so a re-read of the durable counter is idempotent.
	appliedExtension atomic.Int64

	proxyMu sync.Mutex
	// proxyPlan is the active plan; nil means the job's own proxies apply
	// unchanged (the pre-pool behaviour). direct means no proxies at all.
	proxyPlan *web.ProxyPlan
	direct    bool
	perProxy  []proxyRunState
	rotation  int

	cancelMu    sync.Mutex
	taskCancels map[string]context.CancelFunc
	retryFlags  map[string]bool
}

type proxyRunState struct {
	tasks  int
	failed bool
}

func newLiveRunState(deadline time.Time) *liveRunState {
	state := &liveRunState{
		taskCancels: make(map[string]context.CancelFunc),
		retryFlags:  make(map[string]bool),
	}
	state.deadlineUnixNano.Store(deadline.UnixNano())

	return state
}

func (state *liveRunState) deadline() time.Time {
	return time.Unix(0, state.deadlineUnixNano.Load())
}

// applyExtension folds newly observed extension seconds into the deadline and
// reports how many seconds were newly applied.
func (state *liveRunState) applyExtension(totalExtensionSeconds int64) int64 {
	applied := state.appliedExtension.Load()
	if totalExtensionSeconds <= applied {
		return 0
	}

	delta := totalExtensionSeconds - applied

	if !state.appliedExtension.CompareAndSwap(applied, totalExtensionSeconds) {
		return 0
	}

	state.deadlineUnixNano.Add(delta * int64(time.Second))

	return delta
}

// setProxyPlan installs (or clears) the active plan. Passing direct=true drops
// proxies entirely for new tasks.
func (state *liveRunState) setProxyPlan(plan *web.ProxyPlan, direct bool) {
	state.proxyMu.Lock()
	defer state.proxyMu.Unlock()

	state.direct = direct
	state.proxyPlan = plan
	state.rotation = 0

	if plan != nil {
		state.perProxy = make([]proxyRunState, len(plan.Proxies))
	} else {
		state.perProxy = nil
	}
}

func (state *liveRunState) currentPoolID() string {
	state.proxyMu.Lock()
	defer state.proxyMu.Unlock()

	if state.direct {
		return web.DirectConnectionPool
	}

	if state.proxyPlan == nil {
		return ""
	}

	return state.proxyPlan.PoolID
}

// taskProxyAssignment is what one task runs with.
type taskProxyAssignment struct {
	// override reports whether the task's proxies must be replaced at all.
	override bool
	proxies  []string
	// index is the attributed proxy for sticky strategies, -1 otherwise.
	index int
}

var errNoUsableProxies = errors.New("every proxy in the pool is failed or at its task cap")

// assignTaskProxies picks the proxies one task runs with. Sticky strategies
// pin a task to one proxy chosen by a stable hash of its query or cell, so
// retries and resumes see the same exit; other strategies keep the pool's
// ordered list, rotated so load spreads across tasks.
func (state *liveRunState) assignTaskProxies(task web.JobTask) (taskProxyAssignment, error) {
	state.proxyMu.Lock()
	defer state.proxyMu.Unlock()

	if state.direct {
		return taskProxyAssignment{override: true, proxies: nil, index: -1}, nil
	}

	if state.proxyPlan == nil {
		return taskProxyAssignment{override: false, index: -1}, nil
	}

	plan := state.proxyPlan

	usable := make([]int, 0, len(plan.Proxies))

	for index := range plan.Proxies {
		record := state.perProxy[index]
		if record.failed {
			continue
		}

		if plan.MaxTasksPerProxy > 0 && record.tasks >= plan.MaxTasksPerProxy {
			continue
		}

		usable = append(usable, index)
	}

	if len(usable) == 0 {
		return taskProxyAssignment{}, errNoUsableProxies
	}

	sticky := plan.Strategy == "sticky_query" || plan.Strategy == "sticky_cell"

	if !sticky {
		// Non-sticky pools keep their strategy-ordered list so the browser
		// layer rotates exactly as before; the rotation offset spreads the
		// preferred first proxy across tasks. Per-proxy attribution is not
		// possible here, so caps count the task against the first proxy.
		ordered := make([]string, 0, len(usable))

		offset := state.rotation % len(usable)
		state.rotation++

		for position := range usable {
			ordered = append(ordered, plan.Proxies[usable[(position+offset)%len(usable)]])
		}

		first := usable[offset]
		state.perProxy[first].tasks++

		return taskProxyAssignment{override: true, proxies: ordered, index: -1}, nil
	}

	key := task.Query
	if plan.Strategy == "sticky_cell" && strings.TrimSpace(task.SourceCell) != "" {
		key = task.SourceCell
	}

	if strings.TrimSpace(key) == "" {
		key = task.Key
	}

	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	chosen := usable[int(hasher.Sum32())%len(usable)]

	state.perProxy[chosen].tasks++

	return taskProxyAssignment{
		override: true,
		proxies:  []string{plan.Proxies[chosen]},
		index:    chosen,
	}, nil
}

// markProxyFailed records a proxy-classified failure against an attributed
// proxy. It reports whether the whole pool is now unusable.
func (state *liveRunState) markProxyFailed(index int) bool {
	state.proxyMu.Lock()
	defer state.proxyMu.Unlock()

	if state.proxyPlan == nil || index < 0 || index >= len(state.perProxy) {
		return false
	}

	state.perProxy[index].failed = true

	for position := range state.perProxy {
		record := state.perProxy[position]
		if record.failed {
			continue
		}

		if state.proxyPlan.MaxTasksPerProxy > 0 && record.tasks >= state.proxyPlan.MaxTasksPerProxy {
			continue
		}

		return false
	}

	return true
}

// registerTaskCancel exposes an in-flight task to retry-current.
func (state *liveRunState) registerTaskCancel(taskKey string, cancel context.CancelFunc) {
	state.cancelMu.Lock()
	defer state.cancelMu.Unlock()

	state.taskCancels[taskKey] = cancel
}

func (state *liveRunState) unregisterTaskCancel(taskKey string) {
	state.cancelMu.Lock()
	defer state.cancelMu.Unlock()

	delete(state.taskCancels, taskKey)
}

// retryCurrentTasks cancels every in-flight task and marks each one so its
// worker releases it (keeping committed rows, consuming no attempt) and then
// keeps claiming.
func (state *liveRunState) retryCurrentTasks() int {
	state.cancelMu.Lock()
	defer state.cancelMu.Unlock()

	for taskKey, cancel := range state.taskCancels {
		state.retryFlags[taskKey] = true

		cancel()
	}

	return len(state.taskCancels)
}

// consumeRetryFlag reports and clears a task's retry-current mark.
func (state *liveRunState) consumeRetryFlag(taskKey string) bool {
	state.cancelMu.Lock()
	defer state.cancelMu.Unlock()

	if !state.retryFlags[taskKey] {
		return false
	}

	delete(state.retryFlags, taskKey)

	return true
}

// classifyTaskFailure maps an attempt error to the human-readable event type
// the specification's log severities call for. Classification is heuristic
// and only shapes reporting, never control flow beyond proxy attribution.
func classifyTaskFailure(err error) string {
	if err == nil {
		return "task-failed"
	}

	message := strings.ToLower(err.Error())

	switch {
	case strings.Contains(message, "browser"), strings.Contains(message, "playwright"),
		strings.Contains(message, "target closed"), strings.Contains(message, "driver"),
		strings.Contains(message, "chromium"):
		return "browser-failure"
	case strings.Contains(message, "proxy"), strings.Contains(message, "socks"),
		strings.Contains(message, "tunnel"), strings.Contains(message, "407"):
		return "proxy-failure"
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "website-timeout"
	case strings.Contains(message, "parse"), strings.Contains(message, "unmarshal"),
		strings.Contains(message, "unexpected"):
		return "parsing-failure"
	default:
		return "task-failed"
	}
}

// pollLiveControls applies pending operator requests at the supervisor tick.
func (w *webrunner) pollLiveControls(run *taskPoolRun, runCancel context.CancelFunc) {
	job := run.job

	controls, err := w.svc.JobLiveControls(context.Background(), job.ID)
	if err != nil {
		return
	}

	if delta := run.live.applyExtension(controls.ExtendedSeconds); delta > 0 {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "runtime-extended", "information",
			fmt.Sprintf("Added %d minute(s) of runtime at the operator's request", delta/60),
			map[string]any{"added_seconds": delta, "deadline": run.live.deadline().UTC().Format(time.RFC3339)},
		)
	}

	if controls.ConcurrencyOverride > 0 &&
		int64(controls.ConcurrencyOverride) != run.desiredConcurrency.Load() {
		previous := run.desiredConcurrency.Load()
		run.desiredConcurrency.Store(int64(controls.ConcurrencyOverride))

		// Without adaptive sampling nothing else recomputes the effective
		// budget, so the override must reach it directly; with adaptation on,
		// the next tick folds it in through the usual caps.
		if !job.Data.Adaptive {
			run.effectiveConcurrency.Store(int64(controls.ConcurrencyOverride))
		}

		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "concurrency-changed", "information",
			fmt.Sprintf("Operator changed the concurrency target from %d to %d; new tasks pick it up", previous, controls.ConcurrencyOverride),
			map[string]any{"previous": previous, "requested": controls.ConcurrencyOverride},
		)
	}

	if controls.ProxyPoolOverride != "" && controls.ProxyPoolOverride != run.live.currentPoolID() {
		w.switchRunProxyPool(run, controls.ProxyPoolOverride)
	}

	if controls.RetryCurrentRequested {
		if consumed, consumeErr := w.svc.ConsumeJobRetryCurrent(context.Background(), job.ID); consumeErr == nil && consumed {
			cancelled := run.live.retryCurrentTasks()
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "retry-current", "information",
				fmt.Sprintf("Operator asked to retry the current task(s); %d in-flight task(s) were requeued without consuming an attempt", cancelled),
				map[string]any{"cancelled_tasks": cancelled},
			)
		}
	}

	if time.Now().After(run.live.deadline()) {
		if run.requestStop(jobruntime.StopReasonRuntimeLimit) {
			runCancel()
		}
	}
}

func (w *webrunner) switchRunProxyPool(run *taskPoolRun, poolID string) {
	job := run.job

	if poolID == web.DirectConnectionPool {
		run.live.setProxyPlan(nil, true)
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "proxy-pool-changed", "information",
			"Operator switched new tasks to a direct connection",
			map[string]any{"pool_id": poolID},
		)

		return
	}

	plan, err := w.svc.ResolveProxyPlan(context.Background(), poolID)
	if err != nil || len(plan.Proxies) == 0 {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "proxy-pool-changed", "warning",
			"Operator asked to switch proxy pools but the pool could not be resolved; keeping the current proxies",
			map[string]any{"pool_id": poolID},
		)

		return
	}

	run.live.setProxyPlan(&plan, false)
	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), job.ID, "proxy-pool-changed", "information",
		fmt.Sprintf("Operator switched new tasks to proxy pool %s (%d usable proxies)", plan.PoolID, len(plan.Proxies)),
		map[string]any{"pool_id": plan.PoolID, "proxies": len(plan.Proxies), "strategy": plan.Strategy},
	)
}
