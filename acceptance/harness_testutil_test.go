package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// eventSpec is one canned worker event the fake server streams. Context is a
// structured map the fake encodes into the context_json string, exactly as the
// real API stores it.
type eventSpec struct {
	ID       int64
	Type     string
	Severity string
	Message  string
	Context  map[string]any
}

// scenario holds the canned numbers a fake server renders across every
// endpoint. Its JSON is written with the real API's field names, so a test
// that passes proves the harness parses the genuine wire format.
type scenario struct {
	jobID string

	// progressStates is the sequence of lifecycle states the progress endpoint
	// returns on successive polls; the last state is returned thereafter.
	progressStates []string
	stopReason     string

	// task plan counters reported by the progress execution snapshot.
	tasksTotal     int64
	tasksCompleted int64
	tasksFailed    int64
	tasksSkipped   int64
	tasksPending   int64
	tasksRunning   int64
	tasksRetries   int64

	checkpointTaskKey string
	checkpointPresent bool
	recoveryRequired  bool
	desiredWorkers    int64
	effectiveWorkers  int64

	// file-backed result summary embedded in progress.
	resultRows      int
	resultUnique    int
	resultDuplicate int

	// benchmark totals.
	rowsAdded          int64
	rowsReplaced       int64
	duplicatesSkipped  int64
	duplicateRate      float64
	uniqueBusinesses   int64
	totalDiscovered    int64
	benchRetries       int64
	newPerMinute       float64
	benchTasksComplete int64
	benchTasksFailed   int64
	benchTasksSkipped  int64

	// benchmark runtime.
	createdAt        int64
	startedAt        int64
	finishedAt       int64
	wallSeconds      float64
	rawRecords       int64
	uniqueRecords    int64
	duplicateRecords int64

	failures []failureClass

	// coverage.
	coverageStopped bool
	coverageReason  string

	events       []eventSpec
	resultsTotal int64

	// system metrics.
	cpuPercent     float64
	memUsedBytes   uint64
	memUsedPercent float64
	memTotalBytes  uint64
	logicalCPUs    int
	activeBrowsers int64
	activePages    int64
	runningJobs    int64

	// availability toggles: when true the endpoint answers 501.
	benchmarkUnavailable bool
	coverageUnavailable  bool
	logsUnavailable      bool
	eventsUnavailable    bool
	resultsUnavailable   bool
}

// defaultScenario is a fully-populated, internally consistent run used as the
// happy path. Its numbers are chosen so derived metrics are easy to assert.
func defaultScenario() scenario {
	return scenario{
		jobID:          "11111111-1111-1111-1111-111111111111",
		progressStates: []string{"partial"},
		stopReason:     "runtime_limit",

		tasksTotal:     48,
		tasksCompleted: 40,
		tasksFailed:    6,
		tasksSkipped:   2,
		tasksPending:   0,
		tasksRunning:   0,
		tasksRetries:   9,

		checkpointTaskKey: "task-40",
		checkpointPresent: true,
		recoveryRequired:  false,
		desiredWorkers:    4,
		effectiveWorkers:  2,

		resultRows:      120,
		resultUnique:    100,
		resultDuplicate: 20,

		rowsAdded:          100,
		rowsReplaced:       10,
		duplicatesSkipped:  20,
		duplicateRate:      0.1538,
		uniqueBusinesses:   100,
		totalDiscovered:    130,
		benchRetries:       9,
		newPerMinute:       50,
		benchTasksComplete: 40,
		benchTasksFailed:   6,
		benchTasksSkipped:  2,

		createdAt:        1_700_000_000,
		startedAt:        1_700_000_060,
		finishedAt:       1_700_000_180,
		wallSeconds:      120,
		rawRecords:       130,
		uniqueRecords:    100,
		duplicateRecords: 20,

		failures: []failureClass{
			{Class: "browser", Count: 4, Retries: 3, Sample: "browser crashed"},
			{Class: "blocked", Count: 2, Retries: 1, Sample: "429 too many requests"},
		},

		coverageStopped: false,
		coverageReason:  "",

		events: []eventSpec{
			{ID: 1, Type: "task-pool", Severity: "information", Message: "Running 4 task(s) in parallel with 2 worker concurrency each",
				Context: map[string]any{
					"task_workers": 4, "per_task_concurrency": 2, "per_task_browser_pool": 1,
					"desired_concurrency": 8, "effective_concurrency": 8, "pending_tasks": 48,
				}},
			{ID: 2, Type: "adaptive-performance", Severity: "information", Message: "Adaptive performance changed the concurrency budget from 8 to 4 (platform block rate)",
				Context: map[string]any{"previous_concurrency": 8, "effective_concurrency": 4, "desired_concurrency": 8}},
			{ID: 3, Type: "adaptive-performance", Severity: "information", Message: "Adaptive performance changed the concurrency budget from 4 to 2 (task failure rate)",
				Context: map[string]any{"previous_concurrency": 4, "effective_concurrency": 2, "desired_concurrency": 8}},
			{ID: 4, Type: "browser-failure", Severity: "warning", Message: "Task attempt failed (browser-failure)", Context: map[string]any{"task_key": "task-5"}},
			{ID: 5, Type: "browser-failure", Severity: "warning", Message: "Task attempt failed (browser-failure)", Context: map[string]any{"task_key": "task-6"}},
			{ID: 6, Type: "browser-failure", Severity: "warning", Message: "Task attempt failed (browser-failure)", Context: map[string]any{"task_key": "task-7"}},
			{ID: 7, Type: "blocked", Severity: "warning", Message: "Task attempt failed (blocked)", Context: map[string]any{"task_key": "task-8"}},
			{ID: 8, Type: "blocked", Severity: "warning", Message: "Task attempt failed (blocked)", Context: map[string]any{"task_key": "task-9"}},
		},
		resultsTotal: 100,

		cpuPercent:     73.5,
		memUsedBytes:   6 << 30,
		memUsedPercent: 61.2,
		memTotalBytes:  16 << 30,
		logicalCPUs:    8,
		activeBrowsers: 4,
		activePages:    8,
		runningJobs:    1,
	}
}

// fakeServer is an httptest server that answers the local API endpoints the
// harness drives from a scenario.
type fakeServer struct {
	*httptest.Server
	scenario scenario

	progressCalls atomic.Int64
	createCalls   atomic.Int64
	mu            sync.Mutex
	lastJobBody   map[string]any
}

func newFakeServer(t *testing.T, sc scenario) *fakeServer {
	t.Helper()

	fake := &fakeServer{scenario: sc}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		fake.createCalls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.lastJobBody = body
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": sc.jobID})
	})

	mux.HandleFunc("GET /api/v1/jobs/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		index := int(fake.progressCalls.Add(1) - 1)
		writeEnvelope(w, fake.scenario.progressPayload(index))
	})
	mux.HandleFunc("GET /api/v1/jobs/{id}/benchmark", func(w http.ResponseWriter, r *http.Request) {
		if sc.benchmarkUnavailable {
			writeUnavailable(w, "benchmark_unavailable")
			return
		}
		writeEnvelope(w, fake.scenario.benchmarkPayload())
	})
	mux.HandleFunc("GET /api/v1/jobs/{id}/coverage", func(w http.ResponseWriter, r *http.Request) {
		if sc.coverageUnavailable {
			writeUnavailable(w, "coverage_unavailable")
			return
		}
		writeEnvelope(w, fake.scenario.coveragePayload())
	})
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		if sc.logsUnavailable {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(fake.scenario.logsText()))
	})
	mux.HandleFunc("GET /api/v1/jobs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if sc.eventsUnavailable {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(fake.scenario.eventsSSE()))
	})
	mux.HandleFunc("GET /api/v1/results", func(w http.ResponseWriter, r *http.Request) {
		if sc.resultsUnavailable {
			writeUnavailable(w, "result_store_unavailable")
			return
		}
		writeMetaEnvelope(w, []any{}, map[string]any{"total": sc.resultsTotal, "limit": 500, "offset": 0})
	})
	mux.HandleFunc("GET /api/v1/system/metrics", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, fake.scenario.metricsPayload())
	})

	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Server.Close)

	return fake
}

func (s scenario) progressPayload(pollIndex int) map[string]any {
	state := s.progressStates[len(s.progressStates)-1]
	if pollIndex < len(s.progressStates) {
		state = s.progressStates[pollIndex]
	}

	execution := map[string]any{
		"tasks": map[string]any{
			"total": s.tasksTotal, "pending": s.tasksPending, "running": s.tasksRunning,
			"completed": s.tasksCompleted, "failed": s.tasksFailed, "skipped": s.tasksSkipped,
			"retries": s.tasksRetries,
		},
		"progress": map[string]any{
			"desired_workers": s.desiredWorkers, "effective_workers": s.effectiveWorkers,
			"browser_count": s.activeBrowsers, "active_pages": s.activePages,
		},
		"recovery_required": s.recoveryRequired,
	}
	if s.checkpointPresent {
		execution["checkpoint"] = map[string]any{
			"id": 7, "task_key": s.checkpointTaskKey, "stage": "collect",
			"created_at": time.Unix(s.finishedAt, 0).UTC().Format(time.RFC3339),
		}
	}

	return map[string]any{
		"job_id": s.jobID, "name": "acceptance", "state": state, "legacy_status": "ok",
		"stage": "collect", "percent": 100.0, "stop_reason": s.stopReason,
		"config": map[string]any{
			"keywords": []string{"plumber in Austin TX 78701"}, "language": "en", "zoom": 15,
			"depth": 10, "fast_mode": false, "email_crawl": false, "runtime_limit_seconds": 3600,
			"grid_bounding_box": "30.250,-97.760,30.285,-97.720", "grid_cell_km": 1.0,
			"estimated_grid_cells": 16, "estimated_seed_tasks": 48,
		},
		"results": map[string]any{
			"rows": s.resultRows, "unique_businesses": s.resultUnique, "duplicates": s.resultDuplicate,
		},
		"execution": execution,
	}
}

func (s scenario) benchmarkPayload() map[string]any {
	failures := make([]map[string]any, 0, len(s.failures))
	for _, class := range s.failures {
		failures = append(failures, map[string]any{
			"class": class.Class, "count": class.Count, "retries": class.Retries, "sample": class.Sample,
		})
	}

	return map[string]any{
		"job_id": s.jobID, "job_name": "acceptance", "engine_version": "test", "schema_version": 18,
		"totals": map[string]any{
			"tasks_planned": 48, "tasks_expanded": 0, "tasks_completed": s.benchTasksComplete,
			"tasks_failed": s.benchTasksFailed, "tasks_skipped": s.benchTasksSkipped,
			"attempts": s.benchRetries + s.benchTasksComplete, "retries": s.benchRetries,
			"rows_added": s.rowsAdded, "rows_replaced": s.rowsReplaced, "duplicates_skipped": s.duplicatesSkipped,
			"duplicate_rate": s.duplicateRate, "unique_businesses": s.uniqueBusinesses,
			"total_discovered_rows": s.totalDiscovered, "new_businesses_per_minute": s.newPerMinute,
		},
		"failures": failures,
		"runtime": map[string]any{
			"created_at": s.createdAt, "started_at": s.startedAt, "finished_at": s.finishedAt,
			"wall_seconds": s.wallSeconds, "tasks_per_minute": 20.0, "raw_records": s.rawRecords,
			"unique_records": s.uniqueRecords, "duplicate_records": s.duplicateRecords,
		},
	}
}

func (s scenario) coveragePayload() map[string]any {
	return map[string]any{
		"totals": map[string]any{
			"tasks_total": s.tasksTotal, "tasks_done": s.tasksCompleted, "tasks_failed": s.tasksFailed,
			"tasks_skipped": s.tasksSkipped, "rows_added": s.rowsAdded, "rows_replaced": s.rowsReplaced,
			"duplicates_skipped": s.duplicatesSkipped, "expansions_added": 0, "refinements_added": 0,
			"tasks_truncated": 0,
		},
		"saturation": map[string]any{
			"enabled": false, "window": 8, "current_new_ratio": 0.8, "stopped": s.coverageStopped,
			"reason": s.coverageReason, "window_samples": 8, "empty_samples": 0,
		},
	}
}

func (s scenario) logsText() string {
	lines := ""
	for _, event := range s.events {
		lines += fmt.Sprintf("%s\t%s\t%s\n",
			time.Unix(s.startedAt, 0).UTC().Format(time.RFC3339), event.Severity, event.Message)
	}

	return lines
}

func (s scenario) eventsSSE() string {
	body := ""
	for _, event := range s.events {
		contextJSON, _ := json.Marshal(event.Context)
		payload, _ := json.Marshal(map[string]any{
			"id": event.ID, "job_id": s.jobID, "type": event.Type, "severity": event.Severity,
			"message": event.Message, "context_json": string(contextJSON),
			"occurred_at": time.Unix(s.startedAt, 0).UTC().Format(time.RFC3339),
		})
		body += fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, payload)
	}

	return body
}

func (s scenario) metricsPayload() map[string]any {
	return map[string]any{
		"status": "ok", "collected_at": time.Unix(s.finishedAt, 0).UTC().Format(time.RFC3339),
		"resources": map[string]any{
			"cpu_percent": s.cpuPercent, "logical_cpus": s.logicalCPUs,
			"memory_total_bytes": s.memTotalBytes, "memory_used_bytes": s.memUsedBytes,
			"memory_used_percent": s.memUsedPercent,
		},
		"database": map[string]any{
			"running_jobs": s.runningJobs, "queued_jobs": 0,
			"active_browsers": s.activeBrowsers, "active_pages": s.activePages,
		},
	}
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeMetaEnvelope(w http.ResponseWriter, data, meta any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": meta})
}

func writeUnavailable(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "unavailable"}})
}

// runFast runs an experiment against the fake with a tiny poll interval and an
// injected clock so tests never sleep.
func runFast(client *Client, config ExperimentConfig) (ExperimentRecord, error) {
	options := RunOptions{
		PollInterval:    time.Millisecond,
		MaxWait:         time.Minute,
		SampleResources: true,
		sleep:           func(context.Context, time.Duration) {},
	}

	return RunExperiment(context.Background(), client, config, options)
}
