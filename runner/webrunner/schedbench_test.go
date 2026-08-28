package webrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/scrapemate"
)

// # The offline scheduler benchmark
//
// This harness answers the one question the acceptance harness cannot answer
// without spending live Google traffic: how much of a run's wall time is the
// SCHEDULER's, and how does that change as the pool gets wider.
//
// It drives the REAL pool — the real durable plan, the real lease/claim
// protocol, the real per-task CSV merge, the real SQLite writes, the real
// supervisor and the real adaptive controller — and stubs exactly one thing:
// the scraping engine, which sleeps for a measured duration instead of driving
// a browser. Everything it reports about scheduling, contention, duplicate work
// and write latency is therefore measured, not modelled. Everything it reports
// about how long a TASK takes is replayed from the acceptance run and is
// modelled.
//
// What it can prove:  scheduler overhead per task, achieved parallelism,
//                     duplicate execution (must be zero), SQLite write latency
//                     against pool width, and where widening stops paying.
// What it cannot prove: the platform's block rate at width. That is a live
//                     property and belongs to the acceptance harness and the
//                     lead's coordinated runs. A knee found here is the LOCAL
//                     knee only.
//
// Run it with:
//
//	GMS_SCHEDBENCH=1 go test -run TestSchedulerThroughputBenchmark \
//	    -timeout 30m ./runner/webrunner/
//
// Optional environment:
//
//	GMS_SCHEDBENCH_SCALE=100   divide every replayed task duration by this
//	GMS_SCHEDBENCH_WIDTHS=1,2,4,6,8
//	GMS_SCHEDBENCH_OUT=<dir>   write records.json there as well as to the log

// acceptanceTaskSeconds is the measured per-task duration, in seconds, of every
// one of the 180 tasks in acceptance job 7100e95b-28f9-4979-9e85-8cd2294f0173
// ("Tattoo Artists - Los Angeles Metro - Thorough Test 01"), read from
// job_tasks.finished_at - started_at on a read-only copy of the live workspace.
//
// Summary of the source run: 180 tasks, 6262 task-seconds, 3147s wall,
// 555 rows, 0 failures, 0 retries, 0 blocks, average parallelism 1.99.
var acceptanceTaskSeconds = []int{
	174, 70, 19, 21, 58, 185, 109, 16, 60, 63, 86, 105,
	36, 58, 49, 15, 63, 50, 22, 15, 109, 41, 67, 64,
	23, 55, 39, 14, 14, 132, 91, 23, 40, 15, 14, 14,
	21, 56, 48, 50, 15, 19, 46, 40, 14, 38, 14, 15,
	39, 20, 44, 13, 15, 38, 39, 76, 40, 15, 40, 44,
	14, 14, 39, 15, 44, 21, 15, 38, 40, 42, 63, 26,
	25, 46, 14, 14, 18, 20, 40, 39, 46, 39, 15, 15,
	39, 15, 38, 40, 39, 39, 14, 15, 40, 15, 15, 38,
	15, 13, 13, 14, 14, 45, 44, 15, 13, 39, 14, 77,
	15, 44, 44, 15, 44, 14, 40, 15, 14, 14, 39, 40,
	14, 15, 39, 39, 15, 14, 40, 15, 45, 49, 50, 15,
	40, 14, 15, 14, 39, 15, 15, 62, 55, 41, 15, 16,
	21, 56, 40, 16, 17, 19, 47, 41, 41, 15, 46, 15,
	25, 16, 41, 40, 20, 17, 24, 15, 16, 16, 41, 16,
	16, 69, 17, 40, 17, 18, 17, 55, 41, 17, 55, 17,
}

// schedBenchRecord is one measured configuration.
type schedBenchRecord struct {
	Workers            int     `json:"workers"`
	Tasks              int     `json:"tasks"`
	WallSeconds        float64 `json:"wall_seconds"`
	SimulatedWorkSecs  float64 `json:"simulated_work_seconds"`
	AchievedOverlapAvg float64 `json:"achieved_parallelism_mean"`
	AchievedOverlapMax int     `json:"achieved_parallelism_peak"`
	SchedulerOverhead  float64 `json:"scheduler_overhead_seconds"`
	OverheadPerTaskMS  float64 `json:"scheduler_overhead_per_task_ms"`
	Efficiency         float64 `json:"parallel_efficiency"`
	TasksPerMinute     float64 `json:"tasks_per_minute"`
	DuplicateStarts    int     `json:"duplicate_task_starts"`
	CompletedTasks     int64   `json:"completed_tasks"`
	FailedTasks        int64   `json:"failed_tasks"`
	WriteMeanMS        float64 `json:"durable_write_mean_ms"`
	WriteP95MS         float64 `json:"durable_write_p95_ms"`
	ProjectedRunMins   float64 `json:"projected_full_run_minutes"`
}

// TestSchedulerThroughputBenchmark measures the pool at a ladder of widths.
func TestSchedulerThroughputBenchmark(t *testing.T) {
	if os.Getenv("GMS_SCHEDBENCH") == "" {
		t.Skip("set GMS_SCHEDBENCH=1 to run the offline scheduler benchmark")
	}

	scale := schedBenchEnvInt(t, "GMS_SCHEDBENCH_SCALE", 100)
	if scale < 1 {
		scale = 1
	}

	widths := schedBenchWidths(t)

	records := make([]schedBenchRecord, 0, len(widths))

	for _, width := range widths {
		record := runSchedBench(t, width, scale)
		records = append(records, record)

		t.Logf(
			"workers=%d wall=%.2fs overlap(mean/peak)=%.2f/%d efficiency=%.2f "+
				"overhead=%.1fms/task writes(mean/p95)=%.2f/%.2fms duplicates=%d projected=%.1fmin",
			record.Workers, record.WallSeconds, record.AchievedOverlapAvg, record.AchievedOverlapMax,
			record.Efficiency, record.OverheadPerTaskMS, record.WriteMeanMS, record.WriteP95MS,
			record.DuplicateStarts, record.ProjectedRunMins,
		)
	}

	report, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("encode report: %v", err)
	}

	t.Logf("scheduler benchmark records (time scale 1/%d):\n%s", scale, report)

	if outDir := os.Getenv("GMS_SCHEDBENCH_OUT"); outDir != "" {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			t.Fatalf("create report directory: %v", err)
		}

		path := filepath.Join(outDir, "schedbench.json")
		if err := os.WriteFile(path, report, 0o600); err != nil {
			t.Fatalf("write report: %v", err)
		}

		t.Logf("report written to %s", path)
	}

	// The benchmark is also an assertion: widening the pool must never make a
	// task run twice, and must never lose one.
	for _, record := range records {
		if record.DuplicateStarts != 0 {
			t.Fatalf("width %d executed %d task(s) twice", record.Workers, record.DuplicateStarts)
		}

		if int(record.CompletedTasks) != record.Tasks {
			t.Fatalf("width %d completed %d of %d tasks",
				record.Workers, record.CompletedTasks, record.Tasks)
		}
	}
}

// runSchedBench executes the replayed plan once at one pool width.
func runSchedBench(t *testing.T, workers, scale int) schedBenchRecord {
	t.Helper()

	service, dataFolder := newPoolTestService(t)

	job := gridScrapeJob(fmt.Sprintf("bench-%02d-0000-4000-8000-000000000000", workers), workers)
	// Fast mode keeps the browser budget out of the measurement: this harness
	// measures the SCHEDULER, and a browser budget derived from whatever host
	// runs the benchmark would silently change the width under test.
	job.Data.FastMode = true
	job.Data.Adaptive = false
	job.Data.Concurrency = workers
	job.Data.MaxTime = time.Hour
	job.Data.GridBBox = ""
	job.Data.GridCellKM = 0
	job.Data.Keywords = make([]string, 0, len(acceptanceTaskSeconds))

	for index := range acceptanceTaskSeconds {
		job.Data.Keywords = append(job.Data.Keywords, fmt.Sprintf("bench query %03d", index))
	}

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	replay := newSchedBenchEngine(scale)

	worker := &webrunner{
		svc:                service,
		cfg:                &runner.Config{DataFolder: dataFolder, Concurrency: workers},
		sampleResources:    healthyResources,
		setupMate:          replay.factory(),
		supervisorInterval: 200 * time.Millisecond,
	}

	startedAt := time.Now()

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("width %d: scrape: %v", workers, err)
	}

	wall := time.Since(startedAt)

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	simulated := replay.simulatedWork()
	overlapMean, overlapPeak := replay.overlap()
	writeMean, writeP95 := replay.writeLatency()

	record := schedBenchRecord{
		Workers:            workers,
		Tasks:              len(acceptanceTaskSeconds),
		WallSeconds:        wall.Seconds(),
		SimulatedWorkSecs:  simulated.Seconds(),
		AchievedOverlapAvg: overlapMean,
		AchievedOverlapMax: overlapPeak,
		SchedulerOverhead:  wall.Seconds() - simulated.Seconds()/float64(workers),
		Efficiency:         (simulated.Seconds() / float64(workers)) / wall.Seconds(),
		DuplicateStarts:    replay.duplicates(),
		CompletedTasks:     execution.Tasks.Completed,
		FailedTasks:        execution.Tasks.Failed,
		WriteMeanMS:        writeMean,
		WriteP95MS:         writeP95,
	}

	record.OverheadPerTaskMS = record.SchedulerOverhead * 1000 / float64(record.Tasks)
	record.TasksPerMinute = float64(record.Tasks) / (wall.Seconds() / 60)
	// Projected back to real time: the same scheduler overhead per task plus
	// the unscaled measured work, divided by the width.
	record.ProjectedRunMins = (float64(schedBenchTotalSeconds())/float64(workers) +
		record.SchedulerOverhead) / 60

	return record
}

// schedBenchTotalSeconds is the acceptance run's total measured task-seconds.
func schedBenchTotalSeconds() int {
	total := 0
	for _, seconds := range acceptanceTaskSeconds {
		total += seconds
	}

	return total
}

// schedBenchEngine is the replay engine: it sleeps for the measured duration of
// the next task, records overlap, and writes one row so the real CSV merge and
// the real durable finish write are exercised.
type schedBenchEngine struct {
	scale int

	mu        sync.Mutex
	next      int
	active    int
	peak      int
	started   map[string]int
	simulated time.Duration
	overlaps  []overlapSample
	writes    []float64
}

type overlapSample struct {
	at    time.Time
	width int
}

func newSchedBenchEngine(scale int) *schedBenchEngine {
	return &schedBenchEngine{scale: scale, started: map[string]int{}}
}

func (engine *schedBenchEngine) factory() func(context.Context, io.Writer, *web.Job) (mateRunner, error) {
	return func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
		return &schedBenchMate{engine: engine, output: output}, nil
	}
}

func (engine *schedBenchEngine) begin(seed string) time.Duration {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	engine.started[seed]++
	engine.active++

	if engine.active > engine.peak {
		engine.peak = engine.active
	}

	engine.overlaps = append(engine.overlaps, overlapSample{at: time.Now(), width: engine.active})

	seconds := acceptanceTaskSeconds[engine.next%len(acceptanceTaskSeconds)]
	engine.next++

	duration := time.Duration(seconds) * time.Second / time.Duration(engine.scale)
	engine.simulated += duration

	return duration
}

func (engine *schedBenchEngine) end() {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	engine.active--
	engine.overlaps = append(engine.overlaps, overlapSample{at: time.Now(), width: engine.active})
}

func (engine *schedBenchEngine) observeWrite(elapsed time.Duration) {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	engine.writes = append(engine.writes, float64(elapsed)/float64(time.Millisecond))
}

func (engine *schedBenchEngine) simulatedWork() time.Duration {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	return engine.simulated
}

func (engine *schedBenchEngine) duplicates() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	total := 0

	for _, starts := range engine.started {
		if starts > 1 {
			total += starts - 1
		}
	}

	return total
}

// overlap reports the time-weighted mean and the peak number of tasks that were
// genuinely executing at the same moment.
func (engine *schedBenchEngine) overlap() (float64, int) {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	if len(engine.overlaps) < 2 {
		return 0, engine.peak
	}

	var (
		weighted float64
		span     float64
	)

	for index := 1; index < len(engine.overlaps); index++ {
		delta := engine.overlaps[index].at.Sub(engine.overlaps[index-1].at).Seconds()
		weighted += delta * float64(engine.overlaps[index-1].width)
		span += delta
	}

	if span == 0 {
		return 0, engine.peak
	}

	return weighted / span, engine.peak
}

func (engine *schedBenchEngine) writeLatency() (mean, p95 float64) {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	if len(engine.writes) == 0 {
		return 0, 0
	}

	sorted := append([]float64(nil), engine.writes...)
	sort.Float64s(sorted)

	total := 0.0
	for _, value := range sorted {
		total += value
	}

	index := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}

	return total / float64(len(sorted)), sorted[index]
}

// schedBenchMate is one replayed task: it sleeps, writes a row, and times how
// long the surrounding durable write takes once the pool merges it.
type schedBenchMate struct {
	engine *schedBenchEngine
	output io.Writer
}

func (mate *schedBenchMate) Start(ctx context.Context, jobs ...scrapemate.IJob) error {
	seed := "unknown"
	if len(jobs) > 0 {
		seed = jobs[0].GetID()
	}

	duration := mate.engine.begin(seed)
	defer mate.engine.end()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
	}

	writeStart := time.Now()
	_ = writeTaskResultRow(mate.output, seed)
	mate.engine.observeWrite(time.Since(writeStart))

	return nil
}

func (mate *schedBenchMate) Close() error { return nil }

func schedBenchEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()

	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s = %q: %v", name, raw, err)
	}

	return value
}

func schedBenchWidths(t *testing.T) []int {
	t.Helper()

	raw := os.Getenv("GMS_SCHEDBENCH_WIDTHS")
	if raw == "" {
		return []int{1, 2, 4, 6, 8}
	}

	widths := make([]int, 0, 8)
	current := ""

	for _, symbol := range raw + "," {
		if symbol == ',' {
			if current == "" {
				continue
			}

			value, err := strconv.Atoi(current)
			if err != nil {
				t.Fatalf("GMS_SCHEDBENCH_WIDTHS = %q: %v", raw, err)
			}

			widths = append(widths, value)
			current = ""

			continue
		}

		current += string(symbol)
	}

	return widths
}
