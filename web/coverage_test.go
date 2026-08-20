package web

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSplitGBPQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		query   string
		synonym string
		zip     string
		ok      bool
	}{
		{"dentist in Springfield IL 62701", "dentist", "62701", true},
		{"walk in clinic in Springfield IL 62701", "walk in clinic", "62701", true},
		{"dentist in New York NY 10001", "dentist", "10001", true},
		{"dentist in IL 62701", "", "", false},
		{"dentist 62701", "", "", false},
		{"dentist in Springfield IL 627", "", "", false},
		{"dentist in Springfield il 62701", "", "", false},
		{"coffee shop", "", "", false},
		{"", "", "", false},
	}

	for _, testCase := range cases {
		synonym, zip, ok := SplitGBPQuery(testCase.query)
		if ok != testCase.ok || synonym != testCase.synonym || zip != testCase.zip {
			t.Errorf("SplitGBPQuery(%q) = %q, %q, %v; want %q, %q, %v",
				testCase.query, synonym, zip, ok, testCase.synonym, testCase.zip, testCase.ok)
		}
	}
}

func TestCoverageWindowRatio(t *testing.T) {
	t.Parallel()

	if ratio := CoverageWindowRatio(nil); ratio != 1 {
		t.Fatalf("empty window ratio = %f, want 1", ratio)
	}

	if ratio := CoverageWindowRatio([]CoverageSample{{RowsAdded: 0, DuplicatesSkipped: 0}}); ratio != 1 {
		t.Fatalf("no-evidence ratio = %f, want 1", ratio)
	}

	samples := []CoverageSample{
		{RowsAdded: 1, DuplicatesSkipped: 3},
		{RowsAdded: 1, DuplicatesSkipped: 3},
	}

	if ratio := CoverageWindowRatio(samples); ratio != 0.25 {
		t.Fatalf("ratio = %f, want 0.25", ratio)
	}
}

func TestCoverageOptionsValidateBounds(t *testing.T) {
	t.Parallel()

	var nilOptions *CoverageOptions
	if err := nilOptions.Validate(); err != nil {
		t.Fatalf("nil options must validate: %v", err)
	}

	valid := &CoverageOptions{AutoStop: true, SaturationWindow: 8, MinNewRatio: 0.1, MaxExpansions: 50, ExpansionMinNew: 10}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}

	zeroDefaults := &CoverageOptions{AutoStop: true}
	if err := zeroDefaults.Validate(); err != nil {
		t.Fatalf("zero values must fall back to defaults: %v", err)
	}

	if zeroDefaults.WindowOrDefault() != DefaultCoverageSaturationWindow ||
		zeroDefaults.MinNewRatioOrDefault() != DefaultCoverageMinNewRatio ||
		zeroDefaults.ExpansionMinNewOrDefault() != DefaultCoverageExpansionMinNew {
		t.Fatal("defaults are not applied")
	}

	for _, invalid := range []*CoverageOptions{
		{SaturationWindow: 2},
		{SaturationWindow: 51},
		{MinNewRatio: 0.005},
		{MinNewRatio: 0.95},
		{MaxExpansions: -1},
		{MaxExpansions: 501},
		{ExpansionMinNew: -1},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("options %#v validated but must not", invalid)
		}
	}
}

func TestBuildCoverageReportContract(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	finishedEarly := started.Add(30 * time.Second)
	finishedLate := started.Add(90 * time.Second)

	rows := []CoverageTaskRow{
		{
			TaskKey: "t-1", Query: "dentist in Springfield IL 62701", State: "completed",
			Attempts: 1, Sequence: 0, RowsAdded: 10, RowsReplaced: 1, DuplicatesSkipped: 2,
			StartedAt: &started, FinishedAt: &finishedLate,
		},
		{
			TaskKey: "t-2", Query: "dentist in Chatham IL 62629", State: "completed",
			Attempts: 1, Sequence: 1, RowsAdded: 1, DuplicatesSkipped: 9,
			Origin:    CoverageExpansionOriginPrefix + "62701",
			StartedAt: &started, FinishedAt: &finishedEarly,
		},
		{
			TaskKey: "t-3", Query: "plain query", State: "skipped",
			LastError: CoverageSkipReason, Sequence: 2,
		},
		{
			TaskKey: "t-4", Query: "dentist in Rochester IL 62563", State: "failed",
			Attempts: 3, Sequence: 3,
		},
	}

	options := &CoverageOptions{AutoStop: true, SaturationWindow: 5, MinNewRatio: 0.2}
	report := buildCoverageReport(options, rows)

	if report.Totals.TasksTotal != 4 || report.Totals.TasksDone != 2 ||
		report.Totals.TasksFailed != 1 || report.Totals.TasksSkipped != 1 {
		t.Fatalf("totals = %#v", report.Totals)
	}

	if report.Totals.RowsAdded != 11 || report.Totals.RowsReplaced != 1 ||
		report.Totals.DuplicatesSkipped != 11 || report.Totals.ExpansionsAdded != 1 {
		t.Fatalf("totals = %#v", report.Totals)
	}

	if !report.Saturation.Enabled || !report.Saturation.Stopped ||
		report.Saturation.Window != 5 || report.Saturation.MinNewRatio != 0.2 {
		t.Fatalf("saturation = %#v", report.Saturation)
	}

	// Net-new is what counts: the two completed tasks added 11 rows, but one
	// of those superseded a stored business, so 10 businesses were genuinely
	// new against 1 re-found and 11 duplicates.
	const wantRatio = 10.0 / 22.0

	if math.Abs(report.Saturation.CurrentNewRatio-wantRatio) > 1e-9 {
		t.Fatalf("current new ratio = %f, want %f", report.Saturation.CurrentNewRatio, wantRatio)
	}

	if len(report.ByQuery) != 4 {
		t.Fatalf("by_query rows = %d", len(report.ByQuery))
	}

	if report.ByQuery[0].ZIP != "62701" || report.ByQuery[0].Seconds != 90 {
		t.Fatalf("first by_query row = %#v", report.ByQuery[0])
	}

	if report.ByQuery[2].ZIP != "" {
		t.Fatalf("non-GBP row parsed a ZIP: %#v", report.ByQuery[2])
	}

	// Trend is in completion order: t-2 finished before t-1.
	if len(report.Trend) != 2 || report.Trend[0].Seq != 1 ||
		report.Trend[0].DuplicatesSkipped != 9 || report.Trend[1].RowsAdded != 10 {
		t.Fatalf("trend = %#v", report.Trend)
	}

	// The serialized envelope is a UI contract: assert the exact key names.
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	for _, key := range []string{
		`"totals"`, `"tasks_total"`, `"tasks_done"`, `"tasks_failed"`, `"tasks_skipped"`,
		`"rows_added"`, `"rows_replaced"`, `"duplicates_skipped"`, `"expansions_added"`,
		`"saturation"`, `"enabled"`, `"window"`, `"min_new_ratio"`, `"current_new_ratio"`, `"stopped"`,
		`"by_query"`, `"task_key"`, `"query"`, `"zip"`, `"origin"`, `"state"`, `"attempts"`, `"seconds"`,
		`"trend"`, `"seq"`, `"finished_at"`,
	} {
		if !strings.Contains(string(payload), key) {
			t.Errorf("serialized report lacks %s: %s", key, payload)
		}
	}
}

func coverageForm(t *testing.T, values url.Values) (*CoverageOptions, error) {
	t.Helper()

	request := httptest.NewRequest("POST", "/app/scrapes", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if err := request.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}

	return wizardCoverageOptions(request)
}

func TestWizardCoverageOptionsMapping(t *testing.T) {
	t.Parallel()

	// Absent fields: nil, meaning exactly today's behaviour.
	options, err := coverageForm(t, url.Values{"name": {"unrelated"}})
	if err != nil || options != nil {
		t.Fatalf("absent fields = %#v, %v; want nil, nil", options, err)
	}

	options, err = coverageForm(t, url.Values{
		"coverage_auto_stop":         {"on"},
		"coverage_saturation_window": {"12"},
		"coverage_min_new_ratio":     {"0.25"},
		"coverage_max_expansions":    {"40"},
		"coverage_expansion_min_new": {"5"},
	})
	if err != nil {
		t.Fatalf("full mapping: %v", err)
	}

	want := CoverageOptions{AutoStop: true, SaturationWindow: 12, MinNewRatio: 0.25, MaxExpansions: 40, ExpansionMinNew: 5}
	if options == nil || *options != want {
		t.Fatalf("mapped options = %#v, want %#v", options, want)
	}

	// A single field is enough to enable the block.
	options, err = coverageForm(t, url.Values{"coverage_max_expansions": {"3"}})
	if err != nil || options == nil || options.MaxExpansions != 3 || options.AutoStop {
		t.Fatalf("partial mapping = %#v, %v", options, err)
	}

	for name, invalid := range map[string]url.Values{
		"window low":     {"coverage_saturation_window": {"2"}},
		"window high":    {"coverage_saturation_window": {"51"}},
		"window word":    {"coverage_saturation_window": {"many"}},
		"ratio low":      {"coverage_min_new_ratio": {"0.001"}},
		"ratio high":     {"coverage_min_new_ratio": {"0.91"}},
		"expansions":     {"coverage_max_expansions": {"501"}},
		"expansion word": {"coverage_expansion_min_new": {"lots"}},
	} {
		if _, err := coverageForm(t, invalid); err == nil {
			t.Errorf("%s: invalid values %v were accepted", name, invalid)
		}
	}
}

// boolPointer is a local helper for the tri-state StopOnEmptyWindow knob.
func boolPointer(value bool) *bool { return &value }

func TestNewCoverageSampleDerivesEvidenceFlags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		rowsAdded         int64
		duplicatesSkipped int64
		succeeded         bool
		wantEmpty         bool
	}{
		{"successful and silent", 0, 0, true, true},
		{"successful with new rows", 3, 0, true, false},
		{"successful with duplicates only", 0, 4, true, false},
		{"failed and silent is never empty evidence", 0, 0, false, false},
		{"failed with partial rows is never empty evidence", 2, 1, false, false},
	}

	for _, testCase := range cases {
		sample := NewCoverageSample(testCase.rowsAdded, 0, testCase.duplicatesSkipped, testCase.succeeded)

		if sample.Succeeded != testCase.succeeded || sample.Empty != testCase.wantEmpty {
			t.Errorf("%s: sample = %#v, want succeeded=%v empty=%v",
				testCase.name, sample, testCase.succeeded, testCase.wantEmpty)
		}

		if sample.RowsAdded != testCase.rowsAdded || sample.DuplicatesSkipped != testCase.duplicatesSkipped {
			t.Errorf("%s: counters were altered: %#v", testCase.name, sample)
		}
	}

	// The historical zero value keeps meaning "quality unknown", which is
	// the conservative case everywhere.
	var legacy CoverageSample
	if legacy.Succeeded || legacy.Empty {
		t.Fatalf("zero-value sample = %#v, want no evidence claims", legacy)
	}
}

func TestCoverageWindowEvidenceZeroYieldRules(t *testing.T) {
	t.Parallel()

	empty := NewCoverageSample(0, 0, 0, true)
	productive := NewCoverageSample(5, 0, 1, true)
	failed := NewCoverageSample(0, 0, 0, false)

	cases := []struct {
		name    string
		samples []CoverageSample
		window  int
		want    bool
	}{
		{"full window of successful empties", []CoverageSample{empty, empty, empty}, 3, true},
		{"partial window never saturates", []CoverageSample{empty, empty}, 3, false},
		{"one failed entry disqualifies the window", []CoverageSample{empty, failed, empty}, 3, false},
		{"one productive entry disqualifies the window", []CoverageSample{empty, productive, empty}, 3, false},
		{"empty window", nil, 3, false},
		{"zero window size", []CoverageSample{empty, empty, empty}, 0, false},
		{"legacy zero-value samples are not evidence", make([]CoverageSample, 3), 3, false},
	}

	for _, testCase := range cases {
		evidence := CoverageWindowEvidenceOf(testCase.samples)

		if got := evidence.ZeroYield(testCase.window); got != testCase.want {
			t.Errorf("%s: ZeroYield(%d) = %v with evidence %#v, want %v",
				testCase.name, testCase.window, got, evidence, testCase.want)
		}
	}

	// The counts themselves are what the API reports.
	evidence := CoverageWindowEvidenceOf([]CoverageSample{empty, productive, failed})
	if evidence.Samples != 3 || evidence.Successful != 2 || evidence.Empty != 1 {
		t.Fatalf("evidence = %#v, want 3 samples, 2 successful, 1 empty", evidence)
	}

	// A zero-yield window scores a perfect ratio, which is exactly why the
	// duplicate rule can never detect it.
	if ratio := CoverageWindowRatio([]CoverageSample{empty, empty, empty}); ratio != 1 {
		t.Fatalf("zero-yield window ratio = %f, want 1", ratio)
	}
}

func TestStopOnEmptyWindowDefaultsAndJSONBackCompat(t *testing.T) {
	t.Parallel()

	var nilOptions *CoverageOptions
	if nilOptions.StopOnEmptyWindowOrDefault() {
		t.Fatal("a nil coverage block must keep exactly the historical behaviour")
	}

	cases := []struct {
		name    string
		options CoverageOptions
		want    bool
	}{
		{"absent knob follows auto_stop", CoverageOptions{AutoStop: true}, true},
		{"absent knob without auto_stop stays off", CoverageOptions{}, false},
		{
			"explicit off wins over auto_stop",
			CoverageOptions{AutoStop: true, StopOnEmptyWindow: boolPointer(false)},
			false,
		},
		{"explicit on wins without auto_stop", CoverageOptions{StopOnEmptyWindow: boolPointer(true)}, true},
	}

	for _, testCase := range cases {
		options := testCase.options
		if got := options.StopOnEmptyWindowOrDefault(); got != testCase.want {
			t.Errorf("%s: StopOnEmptyWindowOrDefault() = %v, want %v", testCase.name, got, testCase.want)
		}

		if err := options.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", testCase.name, err)
		}
	}

	// A configuration persisted before the knob existed decodes to the
	// absent state, not to an explicit false.
	var legacy CoverageOptions
	if err := json.Unmarshal(
		[]byte(`{"auto_stop":true,"saturation_window":6,"min_new_ratio":0.2,"max_expansions":3,"expansion_min_new":4}`),
		&legacy,
	); err != nil {
		t.Fatalf("decode legacy options: %v", err)
	}

	if legacy.StopOnEmptyWindow != nil {
		t.Fatalf("legacy options decoded an explicit knob: %#v", legacy.StopOnEmptyWindow)
	}

	if !legacy.StopOnEmptyWindowOrDefault() || legacy.SaturationWindow != 6 || legacy.MaxExpansions != 3 {
		t.Fatalf("legacy options = %#v", legacy)
	}

	// An absent knob does not appear in the serialized form either, so
	// re-persisting an untouched configuration is byte-stable.
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}

	if strings.Contains(string(encoded), "stop_on_empty_window") {
		t.Fatalf("absent knob was serialized: %s", encoded)
	}

	explicit, err := json.Marshal(CoverageOptions{AutoStop: true, StopOnEmptyWindow: boolPointer(false)})
	if err != nil {
		t.Fatalf("encode explicit options: %v", err)
	}

	if !strings.Contains(string(explicit), `"stop_on_empty_window":false`) {
		t.Fatalf("explicit knob was not serialized: %s", explicit)
	}
}

// coverageCompletedRow is one finished plan task with the given counters.
func coverageCompletedRow(key string, sequence int, rowsAdded, duplicates int64) CoverageTaskRow {
	started := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(time.Duration(sequence+1) * time.Minute)

	return CoverageTaskRow{
		TaskKey: key, Query: "dentist in Springfield IL 62701", State: "completed",
		Attempts: 1, Sequence: sequence, RowsAdded: rowsAdded, DuplicatesSkipped: duplicates,
		StartedAt: &started, FinishedAt: &finished,
	}
}

// coverageStoppedRows appends one skipped task carrying the given adaptive
// skip reason to a completed plan.
func coverageStoppedRows(skipReason string, completed []CoverageTaskRow) []CoverageTaskRow {
	rows := append([]CoverageTaskRow{}, completed...)

	return append(rows, CoverageTaskRow{
		TaskKey: "t-skipped", Query: "dentist in Rochester IL 62563", State: "skipped",
		LastError: skipReason, Sequence: len(rows),
	})
}

func TestCoverageReportDistinguishesSaturationReasons(t *testing.T) {
	t.Parallel()

	silent := []CoverageTaskRow{
		coverageCompletedRow("t-1", 0, 0, 0),
		coverageCompletedRow("t-2", 1, 0, 0),
		coverageCompletedRow("t-3", 2, 0, 0),
	}

	options := &CoverageOptions{AutoStop: true, SaturationWindow: 3, MinNewRatio: 0.2}

	report := buildCoverageReport(options, coverageStoppedRows(CoverageEmptySkipReason, silent))
	if !report.Saturation.Stopped || report.Saturation.Reason != CoverageSaturationReasonEmpty {
		t.Fatalf("empty stop saturation = %#v", report.Saturation)
	}

	if report.Saturation.WindowSamples != 3 || report.Saturation.SuccessfulSamples != 3 ||
		report.Saturation.EmptySamples != 3 {
		t.Fatalf("empty stop evidence = %#v", report.Saturation)
	}

	if !report.Saturation.StopOnEmptyWindow {
		t.Fatal("auto_stop must enable the zero-yield rule by default")
	}

	// The historical stop keeps its historical reason and its ratio.
	duplicateHeavy := []CoverageTaskRow{
		coverageCompletedRow("t-1", 0, 1, 9),
		coverageCompletedRow("t-2", 1, 1, 9),
		coverageCompletedRow("t-3", 2, 1, 9),
	}

	report = buildCoverageReport(options, coverageStoppedRows(CoverageSkipReason, duplicateHeavy))
	if !report.Saturation.Stopped || report.Saturation.Reason != CoverageSaturationReasonDuplicates {
		t.Fatalf("duplicate stop saturation = %#v", report.Saturation)
	}

	if report.Saturation.CurrentNewRatio != 0.1 || report.Saturation.EmptySamples != 0 ||
		report.Saturation.SuccessfulSamples != 3 {
		t.Fatalf("duplicate stop evidence = %#v", report.Saturation)
	}

	// A skip that is not an adaptive stop keeps the report unstopped and
	// reasonless.
	report = buildCoverageReport(options, coverageStoppedRows("operator-cancelled", duplicateHeavy))
	if report.Saturation.Stopped || report.Saturation.Reason != "" {
		t.Fatalf("unrelated skip = %#v", report.Saturation)
	}

	// A nil coverage block reports the legacy defaults and never claims the
	// zero-yield rule is on.
	report = buildCoverageReport(nil, silent)
	if report.Saturation.Enabled || report.Saturation.StopOnEmptyWindow || report.Saturation.Reason != "" {
		t.Fatalf("nil options saturation = %#v", report.Saturation)
	}

	if report.Saturation.Window != DefaultCoverageSaturationWindow ||
		report.Saturation.MinNewRatio != DefaultCoverageMinNewRatio {
		t.Fatalf("nil options defaults = %#v", report.Saturation)
	}
}

// coverageAPIRepository serves one job and one fixed plan to the coverage
// route. The real SQLite implementation cannot be imported from a
// package-internal web test (import cycle through web/sqlite); its queries
// are proven by the repository tests in web/sqlite.
type coverageAPIRepository struct {
	*fixedJobRepository

	rows []CoverageTaskRow
}

func (r *coverageAPIRepository) JobCoverageTasks(context.Context, string) ([]CoverageTaskRow, error) {
	return r.rows, nil
}

func (r *coverageAPIRepository) JobCoverageSeedState(context.Context, string) (CoverageSeedState, error) {
	return CoverageSeedState{MaxSequence: -1}, nil
}

func (r *coverageAPIRepository) SkipPendingJobTasks(context.Context, string, string) (int, error) {
	return 0, nil
}

func (r *coverageAPIRepository) AppendJobTasks(
	context.Context, string, []JobTaskDefinition, int,
) ([]JobTask, error) {
	return nil, nil
}

func (r *coverageAPIRepository) DeferJobTask(context.Context, string, string, time.Time) error {
	return nil
}

func (r *coverageAPIRepository) UpsertProxyTaskStat(context.Context, ProxyTaskStatInput) error {
	return nil
}

func (r *coverageAPIRepository) ProxyTaskHealthByURL(
	context.Context, string,
) (map[string]ProxyTaskHealth, error) {
	return nil, nil
}

func TestCoverageAPIReportsEvidenceAdditively(t *testing.T) {
	t.Parallel()

	const jobID = "44444444-4444-4444-8444-444444444444"

	silent := []CoverageTaskRow{
		coverageCompletedRow("t-1", 0, 0, 0),
		coverageCompletedRow("t-2", 1, 0, 0),
		coverageCompletedRow("t-3", 2, 0, 0),
	}

	job := Job{ID: jobID, Name: "coverage", Status: StatusOK}
	job.Data.Coverage = &CoverageOptions{AutoStop: true, SaturationWindow: 3, MinNewRatio: 0.2}

	repository := &coverageAPIRepository{
		fixedJobRepository: &fixedJobRepository{job: job},
		rows:               coverageStoppedRows(CoverageEmptySkipReason, silent),
	}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	server.registerCoverageRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/coverage", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("coverage = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()

	// Every historical key the UI reads keeps its exact name.
	for _, key := range []string{
		`"totals"`, `"tasks_total"`, `"tasks_done"`, `"tasks_failed"`, `"tasks_skipped"`,
		`"rows_added"`, `"rows_replaced"`, `"duplicates_skipped"`, `"expansions_added"`,
		`"saturation"`, `"enabled"`, `"window"`, `"min_new_ratio"`, `"current_new_ratio"`, `"stopped"`,
		`"by_query"`, `"trend"`,
	} {
		if !strings.Contains(body, key) {
			t.Fatalf("coverage payload lacks the existing key %s: %s", key, body)
		}
	}

	// The additive evidence sits inside the same "saturation" object.
	for _, key := range []string{
		`"reason":"` + CoverageSaturationReasonEmpty + `"`,
		`"stop_on_empty_window":true`,
		`"window_samples":3`, `"successful_samples":3`, `"empty_samples":3`,
	} {
		if !strings.Contains(body, key) {
			t.Fatalf("coverage payload lacks %s: %s", key, body)
		}
	}

	// The duplicate stop reports the other reason through the same route.
	repository.rows = coverageStoppedRows(CoverageSkipReason, []CoverageTaskRow{
		coverageCompletedRow("t-1", 0, 1, 9),
		coverageCompletedRow("t-2", 1, 1, 9),
		coverageCompletedRow("t-3", 2, 1, 9),
	})

	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/coverage", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("coverage = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body = recorder.Body.String()
	if !strings.Contains(body, `"reason":"`+CoverageSaturationReasonDuplicates+`"`) ||
		!strings.Contains(body, `"empty_samples":0`) {
		t.Fatalf("duplicate stop payload = %s", body)
	}
}
