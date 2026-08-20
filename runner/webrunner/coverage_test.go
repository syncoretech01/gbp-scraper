package webrunner

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/prospect"
	"github.com/gosom/google-maps-scraper/web/resultimport"
	"github.com/gosom/scrapemate"
)

// coverageScrapeJob is a plain multi-query job (one task per query), which is
// the shape the coverage engine reasons about.
func coverageScrapeJob(id string, queries []string) web.Job {
	return web.Job{
		ID:     id,
		Name:   "coverage",
		Date:   time.Now().UTC(),
		Status: web.StatusPending,
		Data: web.JobData{
			Keywords:    queries,
			Lang:        "en",
			Zoom:        15,
			Lat:         "39.7817",
			Lon:         "-89.6501",
			FastMode:    false,
			Radius:      10000,
			Depth:       5,
			MaxTime:     2 * time.Minute,
			Concurrency: 2,
			TaskWorkers: 1,
		},
	}
}

// duplicateHeavyMate writes the same business plus one in-run duplicate for
// every task, so each completed task reports rows_added=1 and
// duplicates_skipped=1 (a 0.5 new-row ratio).
type duplicateHeavyMate struct {
	output io.Writer
}

func (mate *duplicateHeavyMate) Start(_ context.Context, jobs ...scrapemate.IJob) error {
	_ = jobs

	header := resultimport.LegacyHeaders()
	row := make([]string, len(header))

	for index, name := range header {
		switch name {
		case "place_id":
			row[index] = "place-shared"
		case "title":
			row[index] = "Shared Business"
		case "address":
			row[index] = "1 Market Street, Springfield"
		case "latitude":
			row[index] = "39.7817"
		case "longitude":
			row[index] = "-89.6501"
		}
	}

	writer := csv.NewWriter(mate.output)
	if err := writer.Write(header); err != nil {
		return err
	}

	// The same row twice: the second is an in-run duplicate.
	if err := writer.Write(row); err != nil {
		return err
	}

	if err := writer.Write(row); err != nil {
		return err
	}

	writer.Flush()

	return writer.Error()
}

func (*duplicateHeavyMate) Close() error { return nil }

func TestCoverageSaturationSkipsRemainderAndJobCompletesOK(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	queries := make([]string, 0, 8)
	for index := range 8 {
		queries = append(queries, fmt.Sprintf("coffee shop %d", index+1))
	}

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555551", queries)
	job.Data.Coverage = &web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.6,
	}

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &duplicateHeavyMate{output: output}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// Each completed task reports a 0.5 new-row ratio, so the third
	// completion fills the window below the 0.6 threshold and the remaining
	// five tasks are skipped, not run.
	if execution.Tasks.Completed != 3 || execution.Tasks.Skipped != 5 ||
		execution.Tasks.Pending != 0 || execution.Tasks.Failed != 0 {
		t.Fatalf("task summary = %#v, want 3 completed and 5 skipped", execution.Tasks)
	}

	// The legacy status contract: an adaptive stop still finishes as 'ok'.
	if job.Status != web.StatusOK {
		t.Fatalf("job status = %q, want %q", job.Status, web.StatusOK)
	}

	report, err := service.JobCoverage(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read coverage: %v", err)
	}

	if !report.Saturation.Enabled || !report.Saturation.Stopped {
		t.Fatalf("saturation = %#v, want enabled and stopped", report.Saturation)
	}

	if report.Saturation.CurrentNewRatio >= 0.6 {
		t.Fatalf("current new ratio = %f, want below the threshold", report.Saturation.CurrentNewRatio)
	}

	if report.Totals.TasksSkipped != 5 || report.Totals.TasksDone != 3 {
		t.Fatalf("totals = %#v", report.Totals)
	}

	if len(report.Trend) != 3 {
		t.Fatalf("trend has %d points, want 3", len(report.Trend))
	}
}

func TestNilCoverageRunsEveryTaskDespiteDuplicates(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	queries := make([]string, 0, 6)
	for index := range 6 {
		queries = append(queries, fmt.Sprintf("coffee shop %d", index+1))
	}

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555552", queries)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &duplicateHeavyMate{output: output}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// Without a coverage block the duplicate-heavy window changes nothing:
	// exactly today's behaviour.
	if execution.Tasks.Completed != 6 || execution.Tasks.Skipped != 0 || execution.Tasks.Pending != 0 {
		t.Fatalf("task summary = %#v, want all 6 completed", execution.Tasks)
	}

	if job.Status != web.StatusOK {
		t.Fatalf("job status = %q, want %q", job.Status, web.StatusOK)
	}
}

// coverageParentArea picks a real embedded ZIP whose state has enough
// neighbours to expand into.
func coverageParentArea(t *testing.T) prospect.ZIPArea {
	t.Helper()

	areas := prospect.EmbeddedZIPAreas()
	if len(areas) == 0 {
		t.Skip("embedded ZIP dataset unavailable")
	}

	counts := make(map[string]int)
	for _, area := range areas {
		counts[area.State]++
	}

	for _, area := range areas {
		if counts[area.State] >= 4 {
			return area
		}
	}

	t.Fatal("no state with enough ZIP areas")

	return prospect.ZIPArea{}
}

func TestCoverageExpansionAppendsNearbyZIPsAndResumesAfterRestart(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	parent := coverageParentArea(t)
	parentQuery := strings.Join(strings.Fields(fmt.Sprintf(
		"dentist in %s %s %s", parent.City, strings.ToUpper(parent.State), parent.ZIP,
	)), " ")

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555553", []string{parentQuery})
	job.Data.Coverage = &web.CoverageOptions{
		MaxExpansions:   2,
		ExpansionMinNew: 1,
	}

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	// First run: the parent task completes and earns an expansion, then the
	// process "dies" the moment an expansion task starts.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	firstTracker := &poolTracker{}
	firstWorker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: firstTracker, onStart: func(taskCtx context.Context, seed string) error {
				if strings.HasPrefix(seed, "exp-") {
					cancelFirst()
					<-taskCtx.Done()

					return taskCtx.Err()
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := firstWorker.scrapeJob(firstCtx, &job); err != nil {
		t.Fatalf("first (interrupted) scrape: %v", err)
	}

	seedState, err := service.JobCoverageSeedState(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read seed state: %v", err)
	}

	if seedState.ExpansionTasks != 2 {
		t.Fatalf("expansion tasks after first run = %d, want 2", seedState.ExpansionTasks)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution after interrupt: %v", err)
	}

	if execution.Tasks.Completed != 1 || execution.Tasks.Pending != 2 || execution.Tasks.Failed != 0 {
		t.Fatalf("task summary after interrupt = %#v, want 1 completed and 2 resumable", execution.Tasks)
	}

	// Resume the paused job, then run it in a fresh worker process. The
	// expansion tasks are not part of the seeded plan, so completing them
	// proves the seeds were rebuilt from their durable payloads.
	if _, _, err := service.ApplyControl(context.Background(), job.ID, jobruntime.ControlResume); err != nil {
		t.Fatalf("resume job: %v", err)
	}

	secondTracker := &poolTracker{}
	secondWorker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: secondTracker}, nil
		},
		sampleResources: healthyResources,
	}

	if err := secondWorker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("resumed scrape: %v", err)
	}

	execution, err = service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution after resume: %v", err)
	}

	// The budget of two was spent in run one; the resumed expansion tasks
	// (each productive) must not expand further.
	if execution.Tasks.Total != 3 || execution.Tasks.Completed != 3 || execution.Tasks.Pending != 0 {
		t.Fatalf("task summary after resume = %#v, want exactly 3 completed", execution.Tasks)
	}

	if job.Status != web.StatusOK {
		t.Fatalf("job status = %q, want %q", job.Status, web.StatusOK)
	}

	expansionSeeds := 0

	for _, seed := range secondTracker.startedSeeds() {
		if strings.HasPrefix(seed, "exp-") {
			expansionSeeds++
		}
	}

	if expansionSeeds != 2 {
		t.Fatalf("resumed run started %d expansion seed(s), want 2", expansionSeeds)
	}

	// Every task contributed its own distinct committed row.
	ids := readResultPlaceIDs(t, filepath.Join(dataFolder, job.ID+".csv"))
	if len(ids) != 3 {
		t.Fatalf("committed rows = %d, want 3", len(ids))
	}

	report, err := service.JobCoverage(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read coverage: %v", err)
	}

	if report.Totals.ExpansionsAdded != 2 {
		t.Fatalf("coverage expansions = %d, want 2", report.Totals.ExpansionsAdded)
	}

	expansionRows := 0

	for _, row := range report.ByQuery {
		if strings.HasPrefix(row.Origin, web.CoverageExpansionOriginPrefix) {
			expansionRows++

			if row.Origin != web.CoverageExpansionOriginPrefix+parent.ZIP {
				t.Fatalf("expansion origin = %q, want parent ZIP %s", row.Origin, parent.ZIP)
			}

			if row.ZIP == "" || row.ZIP == parent.ZIP {
				t.Fatalf("expansion row ZIP = %q, want a different ZIP than the parent", row.ZIP)
			}
		}
	}

	if expansionRows != 2 {
		t.Fatalf("coverage rows show %d expansion task(s), want 2", expansionRows)
	}
}

func TestPoolWaitsThroughFailureBackoffInsteadOfExiting(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555554", []string{"coffee shop"})
	job.Data.RetryCount = 1
	job.Data.RetryConfigured = true

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var attempts atomic.Int32

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(context.Context, string) error {
				if attempts.Add(1) == 1 {
					// classify as parsing-failure => 5s backoff.
					return fmt.Errorf("unexpected parse failure in listing")
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	startedAt := time.Now()

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	// The single task failed once, was deferred by the parsing-failure
	// backoff, and the pool waited through it instead of concluding the
	// plan was drained.
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}

	if elapsed := time.Since(startedAt); elapsed < 4*time.Second {
		t.Fatalf("run finished in %s; the 5s parsing backoff was not honoured", elapsed)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if execution.Tasks.Completed != 1 || execution.Tasks.Failed != 0 || execution.Tasks.Pending != 0 {
		t.Fatalf("task summary = %#v, want the retried task completed", execution.Tasks)
	}

	if job.Status != web.StatusOK {
		t.Fatalf("job status = %q, want %q", job.Status, web.StatusOK)
	}
}

func TestTaskFailureBackoffTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind     string
		attempts int
		want     time.Duration
	}{
		{"browser-failure", 1, 20 * time.Second},
		{"browser-failure", 2, 40 * time.Second},
		{"proxy-failure", 1, 30 * time.Second},
		{"proxy-failure", 3, 90 * time.Second},
		{"website-timeout", 2, 20 * time.Second},
		{"parsing-failure", 1, 5 * time.Second},
		{"parsing-failure", 7, 5 * time.Second},
		{"task-failed", 2, 20 * time.Second},
		{"task-failed", 0, 10 * time.Second},
		{"browser-failure", 40, 300 * time.Second},
		{"proxy-failure", 100, 300 * time.Second},
	}

	for _, testCase := range cases {
		if got := taskFailureBackoff(testCase.kind, testCase.attempts); got != testCase.want {
			t.Errorf("taskFailureBackoff(%q, %d) = %s, want %s",
				testCase.kind, testCase.attempts, got, testCase.want)
		}
	}
}

func TestCoverageEngineSaturationAndBudget(t *testing.T) {
	t.Parallel()

	engine := newCoverageEngine("job-1", web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.5,
	}, web.CoverageSeedState{MaxSequence: 0})

	saturatedAt := -1

	for index := range 4 {
		decision := engine.recordCompletion(
			web.JobTask{Key: fmt.Sprintf("t-%d", index), Query: "anything"},
			web.JobTaskCheckpoint{RowsAdded: 1, DuplicatesSkipped: 9},
		)

		if decision.saturatedNow {
			saturatedAt = index

			break
		}
	}

	// The window holds three samples; the third completion is the first
	// that can trigger, and it must trigger exactly once.
	if saturatedAt != 2 {
		t.Fatalf("saturated at completion %d, want 2", saturatedAt)
	}

	if decision := engine.recordCompletion(
		web.JobTask{Key: "t-late", Query: "anything"},
		web.JobTaskCheckpoint{RowsAdded: 0, DuplicatesSkipped: 5},
	); decision.saturatedNow {
		t.Fatal("saturation triggered twice")
	}
}

func TestCoverageEngineExpandsNearestUnexploredSameStateZIPs(t *testing.T) {
	t.Parallel()

	areas := []prospect.ZIPArea{
		{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.01, Longitude: -89.0},
		{ZIP: "60003", City: "Gamma", State: "IL", Latitude: 40.5, Longitude: -89.0},
		{ZIP: "60004", City: "Delta", State: "IL", Latitude: 41.5, Longitude: -89.0},
		{ZIP: "70001", City: "Elsewhere", State: "MO", Latitude: 40.0, Longitude: -89.001},
	}

	engine := newCoverageEngine("job-2", web.CoverageOptions{
		MaxExpansions:   2,
		ExpansionMinNew: 1,
	}, web.CoverageSeedState{
		Queries:     []string{"dentist in Alpha IL 60001", "dentist in Gamma IL 60003"},
		MaxSequence: 1,
	})
	engine.zipAreas = func() []prospect.ZIPArea { return areas }

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 5},
	)

	if len(decision.expansions) != 2 {
		t.Fatalf("expansions = %d, want 2 (budget-capped)", len(decision.expansions))
	}

	// Nearest same-state unexplored first: 60002 (nearest), then 60004
	// (60003 is already in the plan and MO is another state).
	if decision.expansions[0].Query != "dentist in Beta IL 60002" {
		t.Fatalf("first expansion = %q", decision.expansions[0].Query)
	}

	if decision.expansions[1].Query != "dentist in Delta IL 60004" {
		t.Fatalf("second expansion = %q", decision.expansions[1].Query)
	}

	if decision.expansions[0].Origin != web.CoverageExpansionOriginPrefix+"60001" ||
		decision.expansions[0].Priority != 1 || decision.expansions[0].Sequence != 2 {
		t.Fatalf("first expansion definition = %#v", decision.expansions[0])
	}

	if decision.expansions[1].Sequence != 3 {
		t.Fatalf("second expansion sequence = %d, want 3", decision.expansions[1].Sequence)
	}

	// The budget is spent: another productive completion adds nothing.
	if next := engine.recordCompletion(
		web.JobTask{Key: "t-next", Query: "dentist in Beta IL 60002"},
		web.JobTaskCheckpoint{RowsAdded: 5},
	); len(next.expansions) != 0 {
		t.Fatalf("expansions after budget spent = %d, want 0", len(next.expansions))
	}
}

func TestNonStickyProxyAssignmentPrefersHealthyProxies(t *testing.T) {
	t.Parallel()

	state := newLiveRunState(time.Now().Add(time.Hour))
	state.setProxyPlan(&web.ProxyPlan{
		PoolID:   "pool-1",
		Strategy: "round_robin",
		Proxies:  []string{"http://p0", "http://p1", "http://p2"},
	}, false)

	// Fresh pool: no health difference, so the historical rotation applies.
	first, err := state.assignTaskProxies(web.JobTask{Key: "t-1"})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	if first.proxies[0] != "http://p0" {
		t.Fatalf("fresh pool head = %q, want the strategy order preserved", first.proxies[0])
	}

	second, err := state.assignTaskProxies(web.JobTask{Key: "t-2"})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	if second.proxies[0] != "http://p1" {
		t.Fatalf("fresh pool rotation head = %q, want http://p1", second.proxies[0])
	}

	// Recorded task history reorders the candidates: fewest consecutive
	// failures first, then the lower failure rate. Nothing is excluded.
	state.applyProxyHealth(map[string]web.ProxyTaskHealth{
		"http://p0": {ProxyID: "id-0", ConsecutiveFailures: 2, Successes: 1, Failures: 3},
		"http://p1": {ProxyID: "id-1", ConsecutiveFailures: 0, Successes: 4, Failures: 0},
		"http://p2": {ProxyID: "id-2", ConsecutiveFailures: 0, Successes: 1, Failures: 1},
	})

	assignment, err := state.assignTaskProxies(web.JobTask{Key: "t-3"})
	if err != nil {
		t.Fatalf("assign with health: %v", err)
	}

	want := []string{"http://p1", "http://p2", "http://p0"}
	for index, proxy := range want {
		if assignment.proxies[index] != proxy {
			t.Fatalf("health-ordered proxies = %v, want %v", assignment.proxies, want)
		}
	}

	if assignment.statsProxyID != "id-1" {
		t.Fatalf("stats proxy = %q, want the list head id-1", assignment.statsProxyID)
	}

	if assignment.index != -1 {
		t.Fatalf("failure-attribution index = %d, want -1 for non-sticky pools", assignment.index)
	}

	// In-memory outcomes shift the ordering: two failures on p1 sink it
	// behind p2 (and behind p0's rate on the streak key only).
	state.recordProxyOutcome(assignment.statsIndex, false)
	state.recordProxyOutcome(assignment.statsIndex, false)

	next, err := state.assignTaskProxies(web.JobTask{Key: "t-4"})
	if err != nil {
		t.Fatalf("assign after failures: %v", err)
	}

	if next.proxies[0] != "http://p2" {
		t.Fatalf("head after p1 failures = %q, want http://p2", next.proxies[0])
	}

	// Sticky strategies keep their hashed precedence and gain attribution.
	sticky := newLiveRunState(time.Now().Add(time.Hour))
	sticky.setProxyPlan(&web.ProxyPlan{
		PoolID:   "pool-2",
		Strategy: "sticky_query",
		Proxies:  []string{"http://s0", "http://s1"},
	}, false)
	sticky.applyProxyHealth(map[string]web.ProxyTaskHealth{
		"http://s0": {ProxyID: "sid-0", ConsecutiveFailures: 9, Successes: 0, Failures: 9},
		"http://s1": {ProxyID: "sid-1"},
	})

	stickyFirst, err := sticky.assignTaskProxies(web.JobTask{Key: "t-5", Query: "dentist"})
	if err != nil {
		t.Fatalf("sticky assign: %v", err)
	}

	stickyAgain, err := sticky.assignTaskProxies(web.JobTask{Key: "t-6", Query: "dentist"})
	if err != nil {
		t.Fatalf("sticky reassign: %v", err)
	}

	if stickyFirst.proxies[0] != stickyAgain.proxies[0] {
		t.Fatalf("sticky assignment moved from %q to %q despite health", stickyFirst.proxies[0], stickyAgain.proxies[0])
	}

	if stickyFirst.statsProxyID == "" || stickyFirst.statsIndex != stickyFirst.index {
		t.Fatalf("sticky attribution = %#v", stickyFirst)
	}
}

func TestBuildExpansionSeedRebuildsFromPayloadOnly(t *testing.T) {
	t.Parallel()

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555556", []string{"dentist in Alpha IL 60001"})

	definition := expansionTaskDefinition(job.ID, "dentist", "60001", prospect.ZIPArea{
		ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.01, Longitude: -89.0,
	}, 7)

	task := web.JobTask{
		Key:     definition.Key,
		Query:   definition.Query,
		Origin:  definition.Origin,
		Payload: definition.Payload,
	}

	seed, err := buildExpansionSeed(&job, task, nil, nil, false)
	if err != nil {
		t.Fatalf("build expansion seed: %v", err)
	}

	if seed == nil || seed.GetID() != definition.Key {
		t.Fatalf("seed = %#v, want ID %q", seed, definition.Key)
	}

	// A non-expansion task yields no seed and no error.
	if other, otherErr := buildExpansionSeed(&job, web.JobTask{Key: "plain", Origin: ""}, nil, nil, false); other != nil || otherErr != nil {
		t.Fatalf("non-expansion seed = %#v, %v; want nil, nil", other, otherErr)
	}
}

// coverageBool is a local helper for the tri-state StopOnEmptyWindow knob.
func coverageBool(value bool) *bool { return &value }

// silentMate is a search that genuinely returns nothing: a valid result file
// with headers and no listings, so every task completes with zero new rows
// and zero duplicates.
type silentMate struct {
	output  io.Writer
	tracker *poolTracker
	onStart func(ctx context.Context, seedID string) error
}

func (mate *silentMate) Start(ctx context.Context, jobs ...scrapemate.IJob) error {
	seed := "unknown"
	if len(jobs) > 0 {
		seed = jobs[0].GetID()
	}

	if mate.tracker != nil {
		mate.tracker.enter(seed)
		defer mate.tracker.exit()
	}

	if mate.onStart != nil {
		if err := mate.onStart(ctx, seed); err != nil {
			return err
		}
	}

	writer := csv.NewWriter(mate.output)
	if err := writer.Write(resultimport.LegacyHeaders()); err != nil {
		return err
	}

	writer.Flush()

	return writer.Error()
}

func (*silentMate) Close() error { return nil }

// emptyCheckpoint is what a successful but completely silent task commits.
var emptyCheckpoint = web.JobTaskCheckpoint{RowsAdded: 0, DuplicatesSkipped: 0}

func TestCoverageEngineStopsOnSustainedSuccessfulZeroYield(t *testing.T) {
	t.Parallel()

	// auto_stop alone turns the zero-yield rule on.
	engine := newCoverageEngine("job-empty", web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.5,
	}, web.CoverageSeedState{MaxSequence: 0})

	// A partial window is never evidence, however silent it is.
	for index := range 2 {
		decision := engine.recordCompletion(
			web.JobTask{Key: fmt.Sprintf("t-%d", index), Query: "anything"}, emptyCheckpoint,
		)

		if !decision.recorded {
			t.Fatalf("completion %d was not recorded as evidence", index)
		}

		if decision.saturatedNow {
			t.Fatalf("saturated on completion %d with a partial window", index)
		}

		if decision.windowSize != index+1 {
			t.Fatalf("window size after completion %d = %d", index, decision.windowSize)
		}
	}

	decision := engine.recordCompletion(web.JobTask{Key: "t-2", Query: "anything"}, emptyCheckpoint)

	if !decision.saturatedNow || decision.reason != web.CoverageSaturationReasonEmpty {
		t.Fatalf("full silent window decision = %#v, want an empty-area stop", decision)
	}

	if decision.evidence.Samples != 3 || decision.evidence.Successful != 3 || decision.evidence.Empty != 3 {
		t.Fatalf("evidence = %#v, want three clean empty samples", decision.evidence)
	}

	// The duplicate rule could never have fired here: with no rows and no
	// duplicates the window scores a perfect ratio.
	if decision.ratio != 1 {
		t.Fatalf("zero-yield window ratio = %f, want 1", decision.ratio)
	}

	if again := engine.recordCompletion(
		web.JobTask{Key: "t-late", Query: "anything"}, emptyCheckpoint,
	); again.saturatedNow {
		t.Fatal("zero-yield saturation triggered twice")
	}
}

func TestCoverageEngineRejectsFailedAttemptsAsEvidence(t *testing.T) {
	t.Parallel()

	engine := newCoverageEngine("job-failures", web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.5,
	}, web.CoverageSeedState{MaxSequence: 0})

	engine.recordCompletion(web.JobTask{Key: "t-0", Query: "anything"}, emptyCheckpoint)
	engine.recordCompletion(web.JobTask{Key: "t-1", Query: "anything"}, emptyCheckpoint)

	// A browser crash, proxy error or timeout says nothing about the area:
	// it must not complete the window, however empty its checkpoint looks.
	failed := engine.recordFailedAttempt(web.JobTask{Key: "t-2", Query: "anything"}, emptyCheckpoint)

	if failed.recorded || failed.saturatedNow || failed.reason != "" {
		t.Fatalf("failed attempt decision = %#v, want no evidence and no stop", failed)
	}

	if failed.windowSize != 2 || failed.evidence.Samples != 2 {
		t.Fatalf("failed attempt changed the window: %#v", failed)
	}

	// The third genuine success still closes the window.
	final := engine.recordCompletion(web.JobTask{Key: "t-3", Query: "anything"}, emptyCheckpoint)
	if !final.saturatedNow || final.reason != web.CoverageSaturationReasonEmpty {
		t.Fatalf("decision after the failure = %#v, want an empty-area stop", final)
	}

	// A failure must not evict older good evidence either.
	productive := newCoverageEngine("job-productive", web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.5,
	}, web.CoverageSeedState{MaxSequence: 0})

	var full coverageDecision

	for index := range 3 {
		full = productive.recordCompletion(
			web.JobTask{Key: fmt.Sprintf("p-%d", index), Query: "anything"},
			web.JobTaskCheckpoint{RowsAdded: 10, DuplicatesSkipped: 1},
		)
	}

	after := productive.recordFailedAttempt(
		web.JobTask{Key: "p-failed", Query: "anything"},
		web.JobTaskCheckpoint{RowsAdded: 0, DuplicatesSkipped: 0},
	)

	if after.windowSize != full.windowSize || after.ratio != full.ratio {
		t.Fatalf("failure changed the window: %#v then %#v", full, after)
	}

	if after.evidence.Successful != 3 || after.evidence.Empty != 0 {
		t.Fatalf("failure evicted good evidence: %#v", after.evidence)
	}
}

func TestCoverageEngineMixedWindowUsesRatioRuleOnly(t *testing.T) {
	t.Parallel()

	newEngine := func() *coverageEngine {
		return newCoverageEngine("job-mixed", web.CoverageOptions{
			AutoStop:         true,
			SaturationWindow: 3,
			MinNewRatio:      0.5,
		}, web.CoverageSeedState{MaxSequence: 0})
	}

	// Empty, duplicate-heavy, empty: the window is not zero-yield, so the
	// historical ratio rule decides and reports the historical reason.
	engine := newEngine()
	engine.recordCompletion(web.JobTask{Key: "m-0", Query: "anything"}, emptyCheckpoint)
	engine.recordCompletion(
		web.JobTask{Key: "m-1", Query: "anything"},
		web.JobTaskCheckpoint{RowsAdded: 1, DuplicatesSkipped: 9},
	)

	decision := engine.recordCompletion(web.JobTask{Key: "m-2", Query: "anything"}, emptyCheckpoint)
	if !decision.saturatedNow || decision.reason != web.CoverageSaturationReasonDuplicates {
		t.Fatalf("mixed duplicate-heavy window = %#v, want the ratio stop", decision)
	}

	if decision.evidence.Empty != 2 || decision.evidence.Successful != 3 {
		t.Fatalf("mixed window evidence = %#v", decision.evidence)
	}

	// Empty, productive, empty: the ratio is healthy, so nothing stops. A
	// couple of silent cells inside a productive area are not exhaustion.
	engine = newEngine()
	engine.recordCompletion(web.JobTask{Key: "p-0", Query: "anything"}, emptyCheckpoint)
	engine.recordCompletion(
		web.JobTask{Key: "p-1", Query: "anything"},
		web.JobTaskCheckpoint{RowsAdded: 10, DuplicatesSkipped: 0},
	)

	decision = engine.recordCompletion(web.JobTask{Key: "p-2", Query: "anything"}, emptyCheckpoint)
	if decision.saturatedNow {
		t.Fatalf("productive mixed window stopped the run: %#v", decision)
	}

	if decision.ratio != 1 || decision.evidence.Empty != 2 {
		t.Fatalf("productive mixed window = %#v", decision)
	}
}

func TestCoverageEngineZeroYieldKnobRespectsExplicitChoice(t *testing.T) {
	t.Parallel()

	feedSilence := func(options web.CoverageOptions, completions int) coverageDecision {
		engine := newCoverageEngine("job-knob", options, web.CoverageSeedState{MaxSequence: 0})

		var decision coverageDecision

		for index := range completions {
			decision = engine.recordCompletion(
				web.JobTask{Key: fmt.Sprintf("k-%d", index), Query: "anything"}, emptyCheckpoint,
			)
			if decision.saturatedNow {
				return decision
			}
		}

		return decision
	}

	// Explicitly off keeps exactly the legacy behaviour: a silent run burns
	// its whole plan.
	off := feedSilence(web.CoverageOptions{
		AutoStop:          true,
		SaturationWindow:  3,
		StopOnEmptyWindow: coverageBool(false),
	}, 8)
	if off.saturatedNow {
		t.Fatalf("explicit opt-out still stopped the run: %#v", off)
	}

	// Without a coverage stop configured at all, silence changes nothing.
	unset := feedSilence(web.CoverageOptions{SaturationWindow: 3}, 8)
	if unset.saturatedNow {
		t.Fatalf("a job without auto_stop stopped on silence: %#v", unset)
	}

	// Explicitly on works without auto_stop for operators who want the
	// exhaustion stop but not the duplicate stop.
	on := feedSilence(web.CoverageOptions{
		SaturationWindow:  3,
		StopOnEmptyWindow: coverageBool(true),
	}, 8)
	if !on.saturatedNow || on.reason != web.CoverageSaturationReasonEmpty {
		t.Fatalf("explicit opt-in decision = %#v, want an empty-area stop", on)
	}
}

func TestCoverageZeroYieldSkipsRemainderWithDistinctEvent(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	queries := make([]string, 0, 8)
	for index := range 8 {
		queries = append(queries, fmt.Sprintf("coffee shop %d", index+1))
	}

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555561", queries)
	job.Data.Coverage = &web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.6,
	}

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &silentMate{output: output}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// Three silent successes fill the window; the remaining five tasks are
	// skipped instead of burning the plan on an empty area.
	if execution.Tasks.Completed != 3 || execution.Tasks.Skipped != 5 ||
		execution.Tasks.Pending != 0 || execution.Tasks.Failed != 0 {
		t.Fatalf("task summary = %#v, want 3 completed and 5 skipped", execution.Tasks)
	}

	// The legacy status contract: an adaptive stop still finishes as 'ok'.
	if job.Status != web.StatusOK {
		t.Fatalf("job status = %q, want %q", job.Status, web.StatusOK)
	}

	report, err := service.JobCoverage(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read coverage: %v", err)
	}

	if !report.Saturation.Stopped || report.Saturation.Reason != web.CoverageSaturationReasonEmpty {
		t.Fatalf("saturation = %#v, want an empty-area stop", report.Saturation)
	}

	if report.Saturation.EmptySamples != 3 || report.Saturation.SuccessfulSamples != 3 {
		t.Fatalf("saturation evidence = %#v", report.Saturation)
	}

	// The ratio rule was structurally blind to this run: a perfect 1.0.
	if report.Saturation.CurrentNewRatio != 1 {
		t.Fatalf("current new ratio = %f, want 1", report.Saturation.CurrentNewRatio)
	}

	events, err := service.EventsAfter(context.Background(), job.ID, 0, 200)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	var sawExhausted, sawDuplicateSaturation bool

	for _, event := range events {
		switch event.Type {
		case "coverage-exhausted":
			sawExhausted = true

			if !strings.Contains(event.Message, "no new and no duplicate rows") {
				t.Fatalf("exhaustion event message = %q", event.Message)
			}
		case "coverage-saturated":
			sawDuplicateSaturation = true
		}
	}

	if !sawExhausted {
		t.Fatal("no coverage-exhausted event was recorded")
	}

	if sawDuplicateSaturation {
		t.Fatal("an empty area was reported as duplicate saturation")
	}

	// A restart rebuilds the engine from the durable plan: the skipped tasks
	// are terminal, nothing is pending, and the seed state still describes
	// the whole plan.
	seedState, err := service.JobCoverageSeedState(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read seed state: %v", err)
	}

	if len(seedState.Queries) != 8 || seedState.MaxSequence != 7 || seedState.ExpansionTasks != 0 {
		t.Fatalf("seed state after the stop = %#v", seedState)
	}

	resumed := newCoverageEngine(job.ID, *job.Data.Coverage, seedState)
	if fresh := resumed.recordCompletion(
		web.JobTask{Key: "restart-1", Query: "coffee shop 1"}, emptyCheckpoint,
	); fresh.saturatedNow || fresh.windowSize != 1 {
		t.Fatalf("restarted engine decision = %#v, want a fresh, unsaturated window", fresh)
	}
}

func TestCoverageFailedTaskIsNotExhaustionEvidence(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555562", []string{
		"coffee shop 1", "coffee shop 2", "coffee shop 3",
	})
	job.Data.Coverage = &web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.6,
	}

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var attempts atomic.Int32

	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &silentMate{output: output, onStart: func(context.Context, string) error {
				// The middle task fails transiently; the other two return
				// nothing at all.
				if attempts.Add(1) == 2 {
					return errors.New("chromium target closed unexpectedly")
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// Two silent successes plus one failure: the window never fills with
	// clean evidence, so nothing is skipped and the plan runs out honestly.
	if execution.Tasks.Completed != 2 || execution.Tasks.Failed != 1 ||
		execution.Tasks.Skipped != 0 || execution.Tasks.Pending != 0 {
		t.Fatalf("task summary = %#v, want 2 completed, 1 failed and nothing skipped", execution.Tasks)
	}

	report, err := service.JobCoverage(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read coverage: %v", err)
	}

	if report.Saturation.Stopped || report.Saturation.Reason != "" {
		t.Fatalf("a transient failure was read as exhaustion: %#v", report.Saturation)
	}

	// The failed task is not in the window: only the two clean successes are.
	if report.Saturation.WindowSamples != 2 || report.Saturation.SuccessfulSamples != 2 ||
		report.Saturation.EmptySamples != 2 {
		t.Fatalf("window evidence = %#v, want only the two successes", report.Saturation)
	}

	events, err := service.EventsAfter(context.Background(), job.ID, 0, 200)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	var sawFailure bool

	for _, event := range events {
		switch event.Type {
		case "coverage-exhausted", "coverage-saturated":
			t.Fatalf("a failed attempt produced a %q event", event.Type)
		case "browser-failure":
			sawFailure = true
		}
	}

	if !sawFailure {
		t.Fatal("the transient failure was not reported as a browser failure")
	}
}
