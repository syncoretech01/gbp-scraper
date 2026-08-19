package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// scheduleAutomationFake implements the job, schedule, automation, and
// lifecycle repository capabilities so the service orchestration and the
// automation API handlers can be exercised without SQLite.
type scheduleAutomationFake struct {
	schedule       ScheduleRecord
	scheduleExists bool
	saved          []ScheduleRecord
	runs           []ScheduleRunRecord
	runsLimit      int
	replaceJobIDs  []string
	completions    []ScheduleRunCompletion
	retryJobs      []Job
	dueJobs        []Job
	calls          []string
	cancelled      []string
	pruned         bool
}

func (f *scheduleAutomationFake) Get(context.Context, string) (Job, error) { return Job{}, nil }
func (f *scheduleAutomationFake) Create(context.Context, *Job) error       { return nil }
func (f *scheduleAutomationFake) Delete(context.Context, string) error     { return nil }
func (f *scheduleAutomationFake) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}
func (f *scheduleAutomationFake) Update(context.Context, *Job) error { return nil }

func (f *scheduleAutomationFake) ListSchedules(context.Context) ([]ScheduleRecord, error) {
	return []ScheduleRecord{f.schedule}, nil
}

func (f *scheduleAutomationFake) ListScheduleRuns(context.Context, int) ([]ScheduleRunRecord, error) {
	return f.runs, nil
}

func (f *scheduleAutomationFake) SaveSchedule(_ context.Context, schedule ScheduleRecord) error {
	f.saved = append(f.saved, schedule)
	f.schedule = schedule
	return nil
}

func (f *scheduleAutomationFake) SetScheduleEnabled(context.Context, string, bool) error {
	return nil
}
func (f *scheduleAutomationFake) DeleteSchedule(context.Context, string) error { return nil }

func (f *scheduleAutomationFake) RunScheduleNow(context.Context, string, time.Time) (Job, error) {
	return Job{}, nil
}

func (f *scheduleAutomationFake) StartDueSchedules(context.Context, time.Time, int) ([]Job, error) {
	f.calls = append(f.calls, "due")
	return f.dueJobs, nil
}

func (f *scheduleAutomationFake) GetSchedule(_ context.Context, id string) (ScheduleRecord, error) {
	if !f.scheduleExists || id != f.schedule.ID {
		return ScheduleRecord{}, ErrScheduleNotFound
	}
	return f.schedule, nil
}

func (f *scheduleAutomationFake) ListScheduleRunsForSchedule(
	_ context.Context,
	_ string,
	limit int,
) ([]ScheduleRunRecord, error) {
	f.runsLimit = limit
	return f.runs, nil
}

func (f *scheduleAutomationFake) CompleteScheduleRuns(
	context.Context,
	time.Time,
) ([]ScheduleRunCompletion, error) {
	f.calls = append(f.calls, "complete")
	return f.completions, nil
}

func (f *scheduleAutomationFake) StartDueScheduleRetries(
	context.Context,
	time.Time,
	int,
) ([]Job, error) {
	f.calls = append(f.calls, "retries")
	return f.retryJobs, nil
}

func (f *scheduleAutomationFake) PruneScheduleRuns(context.Context, time.Time) (int64, error) {
	f.calls = append(f.calls, "prune")
	f.pruned = true
	return 0, nil
}

func (f *scheduleAutomationFake) ListDueReplaceableScheduleJobs(
	context.Context,
	time.Time,
) ([]string, error) {
	f.calls = append(f.calls, "replace")
	return f.replaceJobIDs, nil
}

func (f *scheduleAutomationFake) CreateWithState(context.Context, *Job, jobruntime.State) error {
	return nil
}

func (f *scheduleAutomationFake) GetRuntime(context.Context, string) (JobRuntime, error) {
	return JobRuntime{}, nil
}

func (f *scheduleAutomationFake) ApplyControl(
	_ context.Context,
	id string,
	control jobruntime.Control,
) (JobRuntime, jobruntime.ControlDecision, error) {
	if control == jobruntime.ControlCancel {
		f.cancelled = append(f.cancelled, id)
	}
	return JobRuntime{}, jobruntime.ControlDecision{Disposition: jobruntime.ControlApplied}, nil
}

func (f *scheduleAutomationFake) SetOutcome(
	context.Context,
	string,
	jobruntime.Outcome,
	string,
) (JobRuntime, error) {
	return JobRuntime{}, nil
}

func (f *scheduleAutomationFake) EventsAfter(context.Context, string, int64, int) ([]JobEvent, error) {
	return nil, nil
}

func weeklyAutomationSchedule(now time.Time) ScheduleRecord {
	return ScheduleRecord{
		ID: "schedule-automation", Name: "Weekly dentists", TemplateID: "template-1",
		Timezone: "UTC", Enabled: true,
		Spec: ScheduleSpec{
			Recurrence: "weekly", FirstRunAt: now.Add(time.Hour), Weekdays: []int{1},
			OverlapPolicy: ScheduleOverlapQueue, MissedPolicy: "skip",
		},
		RetryCount: 1, RetryBackoffSeconds: 60, CreatedAt: now, UpdatedAt: now,
	}
}

func newScheduleAutomationServer(t *testing.T, fake *scheduleAutomationFake) (*Server, *http.ServeMux) {
	t.Helper()
	server, err := New(NewService(fake, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	server.registerScheduleAutomationRoutes(mux)
	return server, mux
}

func TestStartDueSchedulesRunsAutomationPass(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	fake := &scheduleAutomationFake{
		replaceJobIDs: []string{"job-replaced-1", "job-replaced-2"},
		dueJobs:       []Job{{ID: "job-due"}},
		retryJobs:     []Job{{ID: "job-retry"}},
		completions: []ScheduleRunCompletion{
			{RunID: 1, ScheduleID: "schedule-automation", State: "failed", Attempt: 1},
		},
	}
	svc := NewService(fake, t.TempDir())

	jobs, err := svc.StartDueSchedules(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("StartDueSchedules() error = %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != "job-due" || jobs[1].ID != "job-retry" {
		t.Fatalf("jobs = %+v", jobs)
	}
	if len(fake.cancelled) != 2 || fake.cancelled[0] != "job-replaced-1" || fake.cancelled[1] != "job-replaced-2" {
		t.Fatalf("cancelled = %+v", fake.cancelled)
	}
	if !fake.pruned {
		t.Fatal("retention pruning did not run")
	}
	wantOrder := []string{"complete", "replace", "due", "retries", "prune"}
	if len(fake.calls) != len(wantOrder) {
		t.Fatalf("calls = %+v", fake.calls)
	}
	for index, want := range wantOrder {
		if fake.calls[index] != want {
			t.Fatalf("calls = %+v, want %+v", fake.calls, wantOrder)
		}
	}
}

func TestScheduleRetryHelpersHonourBounds(t *testing.T) {
	t.Parallel()

	if !ScheduleRetryAllowed(2, 1) || !ScheduleRetryAllowed(2, 2) {
		t.Fatal("attempts inside the retry budget must be allowed")
	}
	if ScheduleRetryAllowed(2, 3) || ScheduleRetryAllowed(0, 1) {
		t.Fatal("attempts outside the retry budget must be rejected")
	}
	if ScheduleRetryAllowed(99, 11) {
		t.Fatal("retry count above the maximum must be clamped")
	}

	if got := ScheduleRetryDelay(30, 1); got != 30*time.Second {
		t.Fatalf("ScheduleRetryDelay(30, 1) = %s", got)
	}
	if got := ScheduleRetryDelay(30, 3); got != 90*time.Second {
		t.Fatalf("ScheduleRetryDelay(30, 3) = %s", got)
	}
	if got := ScheduleRetryDelay(1, 1); got != MinScheduleRetryBackoffSeconds*time.Second {
		t.Fatalf("delay below the minimum backoff = %s", got)
	}
	if got := ScheduleRetryDelay(1_000_000, 1); got != MaxScheduleRetryBackoffSeconds*time.Second {
		t.Fatalf("delay above the maximum backoff = %s", got)
	}

	for policy, want := range map[string]bool{
		"queue": true, "skip": true, "replace": true, "": false, "sometimes": false,
	} {
		if got := validScheduleOverlapPolicy(policy); got != want {
			t.Fatalf("validScheduleOverlapPolicy(%q) = %v, want %v", policy, got, want)
		}
	}
}

func TestUpdateScheduleAPIValidatesAndPersists(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	fake := &scheduleAutomationFake{schedule: weeklyAutomationSchedule(now), scheduleExists: true}
	server, mux := newScheduleAutomationServer(t, fake)

	putJSON := func(target, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", server.csrfToken)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	valid := `{"name":"Nightly dentists","cron":"0 3 * * *","timezone":"UTC","enabled":true,` +
		`"overlap_policy":"replace","missed_policy":"run_once","retry_count":3,` +
		`"retry_backoff_seconds":45,"auto_export_format":"csv","runs_retention_days":14}`
	response := putJSON("/api/v1/schedules/schedule-automation", valid)
	if response.Code != http.StatusOK {
		t.Fatalf("valid update status = %d body=%s", response.Code, response.Body.String())
	}
	if len(fake.saved) != 1 {
		t.Fatalf("saved schedules = %d", len(fake.saved))
	}
	saved := fake.saved[0]
	if saved.Name != "Nightly dentists" || saved.Spec.Recurrence != "cron" || saved.Spec.CustomCron != "0 3 * * *" ||
		saved.Spec.OverlapPolicy != ScheduleOverlapReplace || saved.Spec.MissedPolicy != "run_once" ||
		saved.RetryCount != 3 || saved.RetryBackoffSeconds != 45 || saved.AutoExportFormat != "csv" ||
		saved.RunsRetentionDays != 14 || !saved.Enabled {
		t.Fatalf("saved schedule = %+v", saved)
	}
	if saved.NextRunAt == nil || saved.NextRunAt.Hour() != 3 || saved.NextRunAt.Minute() != 0 {
		t.Fatalf("recomputed next run = %v", saved.NextRunAt)
	}

	invalidBodies := map[string]string{
		"overlap":        `{"overlap_policy":"sometimes"}`,
		"missed":         `{"missed_policy":"later"}`,
		"retry_count":    `{"retry_count":11}`,
		"backoff_low":    `{"retry_backoff_seconds":5}`,
		"backoff_high":   `{"retry_backoff_seconds":3601}`,
		"export_format":  `{"auto_export_format":"docx"}`,
		"retention":      `{"runs_retention_days":-1}`,
		"cron":           `{"cron":"not a cron"}`,
		"timezone":       `{"timezone":"Mars/Olympus"}`,
		"unknown_field":  `{"nome":"typo"}`,
		"second_object":  `{"name":"a"}{"name":"b"}`,
		"empty_name":     `{"name":"   "}`,
		"empty_timezone": `{"timezone":"  "}`,
	}
	for label, body := range invalidBodies {
		if response := putJSON("/api/v1/schedules/schedule-automation", body); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d body=%s", label, response.Code, response.Body.String())
		}
	}
	if len(fake.saved) != 1 {
		t.Fatalf("invalid updates persisted: %d saves", len(fake.saved))
	}

	if response := putJSON("/api/v1/schedules/missing-schedule", `{"name":"x"}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing schedule status = %d", response.Code)
	}

	forbidden := httptest.NewRequest(http.MethodPut, "/api/v1/schedules/schedule-automation",
		strings.NewReader(`{"name":"x"}`))
	forbidden.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, forbidden)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", recorder.Code)
	}
}

func TestUpdateScheduleFormPostRedirects(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	fake := &scheduleAutomationFake{schedule: weeklyAutomationSchedule(now), scheduleExists: true}
	server, mux := newScheduleAutomationServer(t, fake)

	values := url.Values{
		"name": {"Edited dentists"}, "timezone": {"UTC"}, "cron": {""}, "enabled": {"false"},
		"overlap_policy": {"skip"}, "missed_policy": {"skip"},
		"retry_count": {"2"}, "retry_backoff_seconds": {"120"},
		"auto_export_format": {"off"}, "runs_retention_days": {"30"},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/schedules/schedule-automation/update",
		strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("form update status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(fake.saved) != 1 {
		t.Fatalf("saved schedules = %d", len(fake.saved))
	}
	saved := fake.saved[0]
	if saved.Name != "Edited dentists" || saved.Enabled || saved.Spec.OverlapPolicy != "skip" ||
		saved.RetryCount != 2 || saved.RetryBackoffSeconds != 120 || saved.AutoExportFormat != "" ||
		saved.RunsRetentionDays != 30 || saved.NextRunAt != nil {
		t.Fatalf("saved schedule = %+v", saved)
	}
	// The empty cron field must not switch a weekly schedule to cron.
	if saved.Spec.Recurrence != "weekly" {
		t.Fatalf("recurrence = %q", saved.Spec.Recurrence)
	}
}

func TestScheduleRunsAPIReturnsHistoryAndBoundsLimit(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	started := now.Add(time.Minute)
	fake := &scheduleAutomationFake{
		schedule:       weeklyAutomationSchedule(now),
		scheduleExists: true,
		runs: []ScheduleRunRecord{
			{
				ID: 2, ScheduleID: "schedule-automation", ScheduleName: "Weekly dentists",
				JobID: "job-2", State: "failed", ScheduledFor: now, StartedAt: &started,
				Attempt: 2, Error: "fatal scrape error",
			},
			{ID: 1, ScheduleID: "schedule-automation", State: "completed", ScheduledFor: now.Add(-time.Hour), Attempt: 1},
		},
	}
	_, mux := newScheduleAutomationServer(t, fake)

	get := func(target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		return recorder
	}

	response := get("/api/v1/schedules/schedule-automation/runs")
	if response.Code != http.StatusOK {
		t.Fatalf("runs status = %d body=%s", response.Code, response.Body.String())
	}
	if fake.runsLimit != defaultScheduleRunHistory {
		t.Fatalf("default limit = %d", fake.runsLimit)
	}
	var envelope struct {
		Data []scheduleRunAPIView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode runs response: %v", err)
	}
	if len(envelope.Data) != 2 || envelope.Data[0].Attempt != 2 || envelope.Data[0].JobID != "job-2" ||
		envelope.Data[0].Error != "fatal scrape error" || envelope.Data[0].StartedAt == nil ||
		envelope.Data[1].State != "completed" {
		t.Fatalf("runs payload = %+v", envelope.Data)
	}

	if response := get("/api/v1/schedules/schedule-automation/runs?limit=200"); response.Code != http.StatusOK {
		t.Fatalf("limit=200 status = %d", response.Code)
	}
	if fake.runsLimit != 200 {
		t.Fatalf("explicit limit = %d", fake.runsLimit)
	}
	for _, target := range []string{
		"/api/v1/schedules/schedule-automation/runs?limit=0",
		"/api/v1/schedules/schedule-automation/runs?limit=201",
		"/api/v1/schedules/schedule-automation/runs?limit=abc",
	} {
		if response := get(target); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s status = %d", target, response.Code)
		}
	}
	if response := get("/api/v1/schedules/other/runs"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown schedule status = %d", response.Code)
	}
}

// The main router already owns "POST /api/v1/schedules/{id}/{action}"; the
// automation routes must be strictly more specific so both can share one mux.
func TestScheduleAutomationRoutesCoexistWithActionRoute(t *testing.T) {
	t.Parallel()

	fake := &scheduleAutomationFake{}
	server, err := New(NewService(fake, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/schedules/{id}/{action}", func(http.ResponseWriter, *http.Request) {})
	server.registerScheduleAutomationRoutes(mux)
}

func TestNextScheduleTimeUsesConfiguredTimezoneAcrossDST(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	spec := ScheduleSpec{
		Recurrence: "daily", FirstRunAt: time.Date(2026, time.March, 7, 9, 30, 0, 0, location),
	}
	after := time.Date(2026, time.March, 8, 9, 0, 0, 0, location)
	next, err := NextScheduleTime(spec, "America/Los_Angeles", after)
	if err != nil {
		t.Fatalf("NextScheduleTime() error = %v", err)
	}
	want := time.Date(2026, time.March, 8, 9, 30, 0, 0, location)
	if !next.Equal(want) || next.Hour() != 9 {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextScheduleTimeSupportsMonthEndsAndStandardCronDayOR(t *testing.T) {
	t.Parallel()

	monthly := ScheduleSpec{
		Recurrence: "monthly", FirstRunAt: time.Date(2026, time.January, 31, 8, 15, 0, 0, time.UTC),
	}
	next, err := NextScheduleTime(monthly, "UTC", time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("monthly NextScheduleTime() error = %v", err)
	}
	want := time.Date(2026, time.February, 28, 8, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("monthly next = %s, want %s", next, want)
	}

	cron := ScheduleSpec{
		Recurrence: "cron", FirstRunAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		CustomCron: "0 9 15 * 1",
	}
	next, err = NextScheduleTime(cron, "UTC", time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("cron NextScheduleTime() error = %v", err)
	}
	// Both day-of-month and weekday are constrained, so standard cron treats
	// them as OR. Monday September 7 occurs before the 15th.
	want = time.Date(2026, time.September, 7, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("cron next = %s, want %s", next, want)
	}
}

func TestNextScheduleTimeRejectsInvalidTimezoneAndCron(t *testing.T) {
	t.Parallel()

	spec := ScheduleSpec{Recurrence: "cron", FirstRunAt: time.Now(), CustomCron: "61 9 * * *"}
	if _, err := NextScheduleTime(spec, "UTC", time.Now()); err == nil {
		t.Fatal("invalid cron minute was accepted")
	}
	if _, err := NextScheduleTime(spec, "Mars/Olympus_Mons", time.Now()); err == nil {
		t.Fatal("invalid timezone was accepted")
	}
}
