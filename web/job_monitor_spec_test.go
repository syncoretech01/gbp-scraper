package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// monitorSpecRepository is a lifecycle repository that also serves per-stage
// evidence and a recorded scraper version, which together are what the live
// monitor needs to render anything beyond the job's own configuration.
type monitorSpecRepository struct {
	fakeLifecycleRepository

	facts        JobPipelineFacts
	version      string
	execution    JobExecutionSnapshot
	labels       JobLabels
	organisation JobOrganisation
}

func (r *monitorSpecRepository) JobPipelineFacts(context.Context, string) (JobPipelineFacts, error) {
	return r.facts, nil
}

func (r *monitorSpecRepository) RecordJobScraperVersion(_ context.Context, _, version string) error {
	if r.version == "" {
		r.version = version
	}

	return nil
}

func (r *monitorSpecRepository) JobScraperVersion(context.Context, string) (string, error) {
	return r.version, nil
}

// The worker protocol below is stubbed only as far as the monitor reads it:
// GetJobExecution supplies the live frame, and the mutating calls exist so the
// repository satisfies the capability interface the service type-asserts.
func (r *monitorSpecRepository) GetJobExecution(context.Context, string) (JobExecutionSnapshot, error) {
	return r.execution, nil
}

func (r *monitorSpecRepository) PrepareJobTasks(
	context.Context, string, []JobTaskDefinition, int,
) ([]JobTask, error) {
	return nil, nil
}

func (r *monitorSpecRepository) StartJobTask(context.Context, string, string) (JobTask, error) {
	return JobTask{}, nil
}

func (r *monitorSpecRepository) ClaimNextJobTask(
	context.Context, string, string, time.Duration,
) (JobTask, bool, error) {
	return JobTask{}, false, nil
}

func (r *monitorSpecRepository) HeartbeatJobTask(
	context.Context, string, string, string, time.Duration,
) error {
	return nil
}

func (r *monitorSpecRepository) ReleaseJobTask(context.Context, string, string, string, string) error {
	return nil
}

func (r *monitorSpecRepository) ReclaimExpiredJobTasks(context.Context, string) (int, error) {
	return 0, nil
}

func (r *monitorSpecRepository) ReclaimStaleJobTasks(context.Context) (int, error) {
	return 0, nil
}

func (r *monitorSpecRepository) CompleteJobTask(
	context.Context, string, string, JobTaskCheckpoint,
) error {
	return nil
}

func (r *monitorSpecRepository) CompleteJobTaskAs(
	context.Context, string, string, string, JobTaskCheckpoint,
) error {
	return nil
}

func (r *monitorSpecRepository) FailJobTask(
	context.Context, string, string, error, bool, JobTaskCheckpoint,
) error {
	return nil
}

func (r *monitorSpecRepository) FailJobTaskAs(
	context.Context, string, string, string, error, bool, JobTaskCheckpoint,
) error {
	return nil
}

func (r *monitorSpecRepository) UpdateJobWorkerProgress(context.Context, string, JobWorkerProgress) error {
	return nil
}

func (r *monitorSpecRepository) RecordJobWorkerEvent(
	context.Context, string, string, string, string, map[string]any,
) error {
	return nil
}

func (r *monitorSpecRepository) RecoverAbandonedJobs(context.Context) (int, error) {
	return 0, nil
}

const monitorSpecJobID = "77777777-7777-7777-7777-777777777777"

func newMonitorSpecServer(t *testing.T) (*Server, *monitorSpecRepository) {
	t.Helper()

	dir := t.TempDir()
	writeCSV(t, dir, monitorSpecJobID, strings.Join([]string{
		"place_id,title,website,phone,emails",
		"one,Alpha,https://alpha.test,+1 555,[hello@alpha.test]",
		"two,Beta,,+1 556,[]",
	}, "\n"))

	etaSeconds := int64(420)
	repository := &monitorSpecRepository{
		version: "v1.17.3",
		execution: JobExecutionSnapshot{
			Tasks: JobTaskSummary{Total: 10, Completed: 7, Failed: 2, Skipped: 1, Retries: 4},
			Progress: JobWorkerProgress{
				Stage:            jobruntime.StageSearchingMaps,
				PlacesPerMinute:  18.5,
				ETASeconds:       &etaSeconds,
				CurrentQuery:     "plumber in Austin TX",
				CurrentCell:      "cell-12",
				CurrentDomain:    "alpha.test",
				BrowserCount:     3,
				ActivePages:      6,
				CPUPercent:       41.5,
				MemoryBytes:      512 << 20,
				DiskFreeBytes:    64 << 30,
				DatabaseWrites:   1200,
				WebsiteQueue:     8,
				DesiredWorkers:   4,
				EffectiveWorkers: 3,
			},
		},
		facts: JobPipelineFacts{
			QueriesPlanned: 6,
			CellsPlanned:   4,
			TasksTotal:     10,
			TasksCompleted: 7,
			TasksFailed:    2,
			TasksSkipped:   1,
			Attempts:       14,
			Retries:        4,
			EventsByType: map[string]int64{
				"proxy-failure":   3,
				"browser-failure": 1,
				"parsing-failure": 2,
			},
			Warnings:          5,
			Errors:            2,
			UniqueBusinesses:  2,
			WithWebsite:       1,
			WithEmail:         1,
			WithPhone:         2,
			WithSocial:        1,
			Merged:            1,
			WebsitesChecked:   1,
			WebsitesActive:    1,
			PagesChecked:      9,
			AverageResponseMS: 415,
			LastHTTPStatus:    200,
		},
	}
	repository.job = Job{
		ID:     monitorSpecJobID,
		Name:   "Austin plumbers",
		Date:   time.Now().UTC(),
		Status: StatusWorking,
		Data: JobData{
			Keywords: []string{"plumber", "Plumber", "  ", "roofer"},
			Lang:     "en",
			Lat:      "30.2672",
			Lon:      "-97.7431",
			Zoom:     14,
			Depth:    10,
			MaxTime:  30 * time.Minute,
		},
	}
	repository.runtime = JobRuntime{
		JobID:      monitorSpecJobID,
		State:      jobruntime.StateRunning,
		Stage:      jobruntime.StageSearchingMaps,
		TotalTasks: 10,
		Completed:  7,
		Failed:     2,
		RawRecords: 42,
		Warnings:   5,
		Errors:     2,
	}

	srv, err := New(NewService(repository, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return srv, repository
}

func renderMonitor(t *testing.T, srv *Server, query string) string {
	t.Helper()

	// The handler resolves its ID through requestWithID, which reads the route
	// pattern value first and the "id" query parameter second. A synthetic
	// request has no pattern, so the parameter is how a test addresses a job.
	target := "/app/jobs/" + monitorSpecJobID + "?id=" + monitorSpecJobID
	if query != "" {
		target += "&" + strings.TrimPrefix(query, "?")
	}
	request := requestWithID(httptest.NewRequest(http.MethodGet, target, http.NoBody))
	recorder := httptest.NewRecorder()
	srv.jobMonitorPage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("monitor status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	return recorder.Body.String()
}

// Every one of the eight stages must carry the metrics the specification names
// for it. A stage rendered as a bare label was the previous behaviour and is
// exactly what this guards against.
func TestJobMonitorPipelineRendersEveryStageWithItsNamedMetrics(t *testing.T) {
	t.Parallel()

	srv, _ := newMonitorSpecServer(t)
	page, err := srv.buildJobMonitorPage(
		requestWithID(httptest.NewRequest(http.MethodGet, "/app/jobs/"+monitorSpecJobID, http.NoBody)),
		monitorSpecJobID,
	)
	if err != nil {
		t.Fatalf("buildJobMonitorPage: %v", err)
	}

	want := map[string][]string{
		"Preparing queries":   {"Keywords configured", "Passed validation", "Duplicates removed", "Searches generated"},
		"Generating grid":     {"Cells created", "Cells excluded", "Geographic coverage", "Task estimate"},
		"Searching Maps":      {"Current query", "Coordinates", "Grid cell", "Results found", "Speed", "Block rate"},
		"Extracting details":  {"Listings opened", "Detail rows parsed", "Retries", "Browser health"},
		"Crawling websites":   {"Current domain", "Pages visited", "Last HTTP status", "Average response time"},
		"Extracting contacts": {"Businesses with an email", "Phones discovered", "Social links discovered"},
		// The counting vocabulary is fixed in web/run_metrics.go. The stage used
		// to report "Raw records / Duplicate matches / Merged records", three
		// labels for three different quantities that no operator could
		// reconcile against the coverage strip on the same page.
		"Deduplicating": {
			"Maps observations", "Repeated observations", "Unique businesses",
			"Entity merges", "Unresolved duplicate candidates",
		},
		"Saving/exporting": {"Rows committed", "Output file", "Storage used"},
	}

	if len(page.Pipeline) != len(want) {
		t.Fatalf("pipeline stages = %d, want %d", len(page.Pipeline), len(want))
	}

	for _, step := range page.Pipeline {
		labels, known := want[step.Label]
		if !known {
			t.Fatalf("unexpected pipeline stage %q", step.Label)
		}
		present := map[string]string{}
		for _, metric := range step.Metrics {
			if strings.TrimSpace(metric.Value) == "" {
				t.Fatalf("stage %q metric %q rendered an empty value", step.Label, metric.Label)
			}
			present[metric.Label] = metric.Value
		}
		for _, label := range labels {
			if _, ok := present[label]; !ok {
				t.Fatalf("stage %q is missing metric %q (has %#v)", step.Label, label, present)
			}
		}
	}
}

func TestJobMonitorPipelineMetricsUseDurableEvidence(t *testing.T) {
	t.Parallel()

	srv, _ := newMonitorSpecServer(t)
	page, err := srv.buildJobMonitorPage(
		requestWithID(httptest.NewRequest(http.MethodGet, "/app/jobs/"+monitorSpecJobID, http.NoBody)),
		monitorSpecJobID,
	)
	if err != nil {
		t.Fatalf("buildJobMonitorPage: %v", err)
	}

	metric := func(stage, label string) string {
		t.Helper()
		for _, step := range page.Pipeline {
			if step.Label != stage {
				continue
			}
			for _, entry := range step.Metrics {
				if entry.Label == label {
					return entry.Value
				}
			}
		}
		t.Fatalf("stage %q has no metric %q", stage, label)

		return ""
	}

	// "plumber", "Plumber", "  ", "roofer": four configured, three valid, one
	// case-insensitive duplicate removed.
	if got := metric("Preparing queries", "Keywords configured"); got != "4" {
		t.Fatalf("keywords configured = %q, want 4", got)
	}
	if got := metric("Preparing queries", "Passed validation"); got != "3 of 4" {
		t.Fatalf("validation = %q, want \"3 of 4\"", got)
	}
	if got := metric("Preparing queries", "Duplicates removed"); got != "1" {
		t.Fatalf("duplicates removed = %q, want 1", got)
	}
	if got := metric("Preparing queries", "Searches generated"); got != "6" {
		t.Fatalf("searches generated = %q, want 6", got)
	}
	if got := metric("Generating grid", "Cells created"); got != "4" {
		t.Fatalf("cells created = %q, want 4", got)
	}
	if got := metric("Searching Maps", "Coordinates"); got != "30.2672, -97.7431" {
		t.Fatalf("coordinates = %q", got)
	}
	// Three proxy failures against ten finished tasks: 3 / (3 + 10) = 23.1%.
	if got := metric("Searching Maps", "Block rate"); !strings.HasPrefix(got, "23.1%") {
		t.Fatalf("block rate = %q, want 23.1%%", got)
	}
	if got := metric("Extracting details", "Browser health"); !strings.Contains(got, "1 browser failure") {
		t.Fatalf("browser health = %q", got)
	}
	if got := metric("Extracting contacts", "Social links discovered"); got != "1" {
		t.Fatalf("social links = %q, want 1", got)
	}
	// One stored record folded into another is the only thing the console may
	// call a merge. See web/run_metrics.go for the rest of the vocabulary.
	if got := metric("Deduplicating", "Entity merges"); got != "1" {
		t.Fatalf("entity merges = %q, want 1", got)
	}
	if got := metric("Saving/exporting", "Output file"); got != monitorSpecJobID+".csv" {
		t.Fatalf("output file = %q", got)
	}
}

// A repository without per-stage evidence must still render eight stages, and
// must say so rather than printing zeros that read as measurements.
func TestJobMonitorPipelineDegradesToNotReportedWithoutEvidence(t *testing.T) {
	t.Parallel()

	steps := buildPipeline(jobPipelineInput{
		Job:     Job{ID: "bare", Data: JobData{Keywords: []string{"dentist"}}},
		Runtime: JobRuntime{State: jobruntime.StateQueued, Stage: jobruntime.StagePreparingQueries},
	})

	if len(steps) != 8 {
		t.Fatalf("pipeline stages = %d, want 8", len(steps))
	}
	found := false
	for _, step := range steps {
		for _, metric := range step.Metrics {
			if metric.Value == notReported {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("a pipeline with no evidence reported no unknown values at all")
	}
}

func TestJobMonitorRendersRealTimeDiagnosticsAndRecordedVersion(t *testing.T) {
	t.Parallel()

	srv, _ := newMonitorSpecServer(t)
	body := renderMonitor(t, srv, "")

	for _, want := range []string{
		"Pipeline stages",
		`data-pipeline-detail`,
		`data-job-scraper-version`,
		"v1.17.3",
		"Local build serving this page",
		// The diagnostics line the specification names, field by field.
		`data-progress-field="current-query"`,
		`data-progress-field="current-cell"`,
		`data-progress-field="rate"`,
		`data-progress-field="eta"`,
		`data-progress-field="cpu"`,
		"Memory",
		"Database writes",
		"Website queue",
		"Browsers",
		"Active pages",
		"Active proxy",
		// Job-detail counters.
		`data-job-warnings`,
		`data-job-errors`,
		`data-job-queries`,
		`data-job-cells`,
		`data-job-block-rate`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("job monitor is missing %q", want)
		}
	}
}

func TestJobMonitorLogToolbarOffersEverySeverityAndControl(t *testing.T) {
	t.Parallel()

	srv, _ := newMonitorSpecServer(t)
	body := renderMonitor(t, srv, "")

	for _, level := range JobLogLevels {
		if !strings.Contains(body, `<option value="`+level+`"`) {
			t.Fatalf("severity filter is missing level %q", level)
		}
	}
	for _, want := range []string{
		`id="log-search"`,
		`data-log-autoscroll`,
		`data-log-copy`,
		`/api/v1/jobs/` + monitorSpecJobID + `/logs?format=text`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("log toolbar is missing %q", want)
		}
	}
}

func TestJobLogLevelClassificationCoversEveryDeclaredLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event JobEvent
		want  string
	}{
		{name: "worker proxy failure", event: JobEvent{Type: "proxy-failure", Severity: "warning"}, want: JobLogLevelProxyFailure},
		{name: "worker browser failure", event: JobEvent{Type: "browser-failure", Severity: "warning"}, want: JobLogLevelBrowserFailure},
		{name: "worker website timeout", event: JobEvent{Type: "website-timeout", Severity: "warning"}, want: JobLogLevelWebsiteTimeout},
		{name: "worker parsing failure", event: JobEvent{Type: "parsing-failure", Severity: "warning"}, want: JobLogLevelParsingFailure},
		{name: "coverage saturation is a duplicate signal", event: JobEvent{Type: "coverage-saturated", Severity: "information"}, want: JobLogLevelDuplicate},
		{
			name:  "runtime limit outcome",
			event: JobEvent{Type: "outcome", Severity: "warning", Context: `{"reason":"runtime_limit"}`},
			want:  JobLogLevelMaximumRuntime,
		},
		{
			name:  "fatal outcome",
			event: JobEvent{Type: "outcome", Severity: "warning", Context: `{"reason":"fatal_error"}`},
			want:  JobLogLevelSystemError,
		},
		{
			name:  "rate limit recorded only in the message",
			event: JobEvent{Type: "task-failed", Severity: "warning", Message: "Task attempt failed: HTTP 429 too many requests"},
			want:  JobLogLevelRateLimit,
		},
		{name: "unclassified warning", event: JobEvent{Type: "task-failed", Severity: "warning"}, want: JobLogLevelWarning},
		{name: "unclassified information", event: JobEvent{Type: "task-pool", Severity: "information"}, want: JobLogLevelInformation},
	}

	seen := map[string]bool{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := classifyJobLogLevel(test.event); got != test.want {
				t.Fatalf("classifyJobLogLevel() = %q, want %q", got, test.want)
			}
		})
		seen[test.want] = true
	}

	for _, level := range JobLogLevels {
		if !seen[level] {
			t.Fatalf("no event shape maps to declared log level %q", level)
		}
	}
}

func TestJobLogTargetLinksErrorsToTheAffectedItem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event JobEvent
		want  string
	}{{
		name:  "business",
		event: JobEvent{Context: `{"business_id":"biz-1"}`},
		want:  "/app/results/biz-1",
	}, {
		name:  "query",
		event: JobEvent{Context: `{"query":"plumber in Austin"}`},
		want:  "/app/results?job_id=job-1&q=plumber+in+Austin",
	}, {
		name:  "cell",
		event: JobEvent{Context: `{"source_cell":"c-12"}`},
		want:  "/app/map?mode=results&job_id=job-1&q=c-12",
	}, {
		name:  "task",
		event: JobEvent{Context: `{"task_key":"t-9"}`},
		want:  "/app/jobs/job-1#job-coverage",
	}, {
		name:  "nothing addressable",
		event: JobEvent{Context: `{"workers":4}`},
		want:  "",
	}, {
		name:  "unparsable context",
		event: JobEvent{Context: `not json`},
		want:  "",
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := jobLogTarget("job-1", test.event); got != test.want {
				t.Fatalf("jobLogTarget() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMonitorLogsFilterBySeverityClassAndCarryTargets(t *testing.T) {
	t.Parallel()

	srv, repository := newMonitorSpecServer(t)
	repository.events = []JobEvent{
		{ID: 1, JobID: monitorSpecJobID, Type: "task-pool", Severity: "information", Message: "Running 2 tasks"},
		{
			ID: 2, JobID: monitorSpecJobID, Type: "proxy-failure", Severity: "warning",
			Message: "Task attempt failed (proxy-failure)", Context: `{"task_key":"t-2"}`,
		},
		{
			ID: 3, JobID: monitorSpecJobID, Type: "outcome", Severity: "warning",
			Message: "Stopped at the runtime limit", Context: `{"reason":"runtime_limit"}`,
		},
	}

	all, err := srv.monitorLogs(context.Background(), monitorSpecJobID, "", "")
	if err != nil {
		t.Fatalf("monitorLogs: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered logs = %d, want 3", len(all))
	}

	proxy, err := srv.monitorLogs(context.Background(), monitorSpecJobID, "", JobLogLevelProxyFailure)
	if err != nil {
		t.Fatalf("monitorLogs(proxy-failure): %v", err)
	}
	if len(proxy) != 1 || proxy[0].Severity != JobLogLevelProxyFailure {
		t.Fatalf("proxy-failure filter = %#v", proxy)
	}
	if proxy[0].TargetURL == "" {
		t.Fatal("a proxy failure carrying a task key produced no target link")
	}

	runtimeLimit, err := srv.monitorLogs(context.Background(), monitorSpecJobID, "", JobLogLevelMaximumRuntime)
	if err != nil {
		t.Fatalf("monitorLogs(maximum-runtime): %v", err)
	}
	if len(runtimeLimit) != 1 {
		t.Fatalf("maximum-runtime filter = %#v", runtimeLimit)
	}

	// Search covers the redacted context, so an operator can pull every line
	// about one task key.
	byContext, err := srv.monitorLogs(context.Background(), monitorSpecJobID, "t-2", "")
	if err != nil {
		t.Fatalf("monitorLogs(search): %v", err)
	}
	if len(byContext) != 1 {
		t.Fatalf("context search = %#v", byContext)
	}
}

func TestStreamedJobEventCarriesLevelAndTarget(t *testing.T) {
	t.Parallel()

	dto := newJobEventDTO("job-1", JobEvent{
		ID: 4, Type: "proxy-failure", Severity: "warning",
		Message: "Task attempt failed", Context: `{"query":"plumber"}`,
	})

	if dto.Level != JobLogLevelProxyFailure {
		t.Fatalf("streamed level = %q", dto.Level)
	}
	if dto.TargetURL != "/app/results?job_id=job-1&q=plumber" {
		t.Fatalf("streamed target = %q", dto.TargetURL)
	}
	// The embedded event keeps every historical field, so an existing consumer
	// is unaffected by the addition.
	if dto.ID != 4 || dto.Type != "proxy-failure" || dto.Severity != "warning" {
		t.Fatalf("streamed event lost a historical field: %#v", dto)
	}
}
