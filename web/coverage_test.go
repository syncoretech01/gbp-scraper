package web

import (
	"encoding/json"
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

	// 11 new rows against 11 duplicates over both completed tasks.
	if report.Saturation.CurrentNewRatio != 0.5 {
		t.Fatalf("current new ratio = %f, want 0.5", report.Saturation.CurrentNewRatio)
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
