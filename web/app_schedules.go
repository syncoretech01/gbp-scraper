package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type schedulesPageData struct {
	Templates     []namedAppOption
	Schedules     []scheduleCardView
	Runs          []scheduleRunView
	ExportFormats []string
	Notice        string
}

type scheduleCardView struct {
	ID                  string
	Name                string
	TemplateName        string
	Enabled             bool
	Recurrence          string
	CustomCron          string
	Timezone            string
	NextRunAt           string
	LastRunAt           string
	OverlapPolicy       string
	MissedPolicy        string
	RetryCount          int
	RetryBackoffSeconds int
	AutoExportFormat    string
	RunsRetentionDays   int
	IncrementalMode     string
	Runs                []scheduleRunView
}

type scheduleRunView struct {
	ScheduleName string
	State        string
	DueAt        string
	StartedAt    string
	FinishedAt   string
	Attempt      int
	JobID        string
	Message      string
}

func (s *Server) schedulesPage(w http.ResponseWriter, r *http.Request) {
	templates, err := s.svc.ListScrapeTemplates(r.Context(), "")
	if err != nil {
		http.Error(w, "could not load templates", http.StatusInternalServerError)
		return
	}
	schedules, err := s.svc.ListSchedules(r.Context())
	if err != nil {
		http.Error(w, "could not load schedules", http.StatusInternalServerError)
		return
	}
	runs, err := s.svc.ListScheduleRuns(r.Context(), 100)
	if err != nil {
		http.Error(w, "could not load schedule history", http.StatusInternalServerError)
		return
	}
	page := schedulesPageData{
		ExportFormats: scheduleAutoExportFormats(),
		Notice:        strings.TrimSpace(r.URL.Query().Get("notice")),
	}
	for _, template := range templates {
		page.Templates = append(page.Templates, namedAppOption{ID: template.ID, Name: template.Name})
	}
	runsBySchedule := make(map[string][]scheduleRunView, len(schedules))
	for _, run := range runs {
		view := scheduleRunView{
			ScheduleName: run.ScheduleName, State: run.State, DueAt: run.ScheduledFor.Format(time.RFC3339),
			StartedAt: optionalTimeLabel(run.StartedAt), FinishedAt: optionalTimeLabel(run.FinishedAt),
			Attempt: run.Attempt, JobID: run.JobID, Message: run.Error,
		}
		page.Runs = append(page.Runs, view)
		if len(runsBySchedule[run.ScheduleID]) < 5 {
			runsBySchedule[run.ScheduleID] = append(runsBySchedule[run.ScheduleID], view)
		}
	}
	for _, schedule := range schedules {
		page.Schedules = append(page.Schedules, scheduleCardView{
			ID: schedule.ID, Name: schedule.Name, TemplateName: schedule.TemplateName,
			Enabled: schedule.Enabled, Recurrence: scheduleRecurrenceLabel(schedule.Spec),
			CustomCron: schedule.Spec.CustomCron,
			Timezone:   schedule.Timezone, NextRunAt: optionalTimeLabel(schedule.NextRunAt),
			LastRunAt: optionalTimeLabel(schedule.LastRunAt), OverlapPolicy: schedule.Spec.OverlapPolicy,
			MissedPolicy: schedule.Spec.MissedPolicy, RetryCount: schedule.RetryCount,
			RetryBackoffSeconds: schedule.RetryBackoffSeconds, AutoExportFormat: schedule.AutoExportFormat,
			RunsRetentionDays: schedule.RunsRetentionDays, IncrementalMode: schedule.Spec.IncrementalMode,
			Runs: runsBySchedule[schedule.ID],
		})
	}
	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "schedules", appPageData{
		Title:     "Schedules",
		Subtitle:  "Queue saved templates on a durable local clock while this application is running.",
		ActiveNav: "schedules",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func scheduleRecurrenceLabel(spec ScheduleSpec) string {
	if spec.Recurrence == "cron" {
		return "cron " + spec.CustomCron
	}
	return spec.Recurrence
}

// scheduleAutoExportFormats lists the advertised export formats offered for
// the per-schedule automatic export. Every entry must be accepted by
// exportExtension so the automation path can build the file.
func scheduleAutoExportFormats() []string {
	return []string{
		"csv", "xlsx", "json", "jsonl", "geojson", "kml",
		"vcard", "txt", "postgresql_sql", "mysql_sql", "sqlite",
	}
}

func parseScheduleFormInt(raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func optionalTimeLabel(value *time.Time) string {
	if value == nil {
		return "never"
	}
	return value.Format(time.RFC3339)
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 120 {
		http.Error(w, "schedule name is required and must be at most 120 characters", http.StatusUnprocessableEntity)
		return
	}
	templateID := strings.TrimSpace(r.FormValue("template_id"))
	if _, err := s.svc.GetScrapeTemplate(r.Context(), templateID); err != nil {
		http.Error(w, "a valid scrape template is required", http.StatusUnprocessableEntity)
		return
	}
	timezone := strings.TrimSpace(r.FormValue("timezone"))
	location, err := time.LoadLocation(timezone)
	if err != nil {
		http.Error(w, "unknown IANA timezone", http.StatusUnprocessableEntity)
		return
	}
	firstRun, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(r.FormValue("first_run_at")), location)
	if err != nil {
		http.Error(w, "first run must include a local date and time", http.StatusUnprocessableEntity)
		return
	}
	recurrence := strings.TrimSpace(r.FormValue("recurrence"))
	if recurrence != "once" && recurrence != "hourly" && recurrence != "daily" &&
		recurrence != "weekly" && recurrence != "monthly" && recurrence != "cron" {
		http.Error(w, "invalid recurrence", http.StatusUnprocessableEntity)
		return
	}
	overlapPolicy := strings.TrimSpace(r.FormValue("overlap_policy"))
	if !validScheduleOverlapPolicy(overlapPolicy) {
		http.Error(w, "overlap policy must be queue, skip, or replace", http.StatusUnprocessableEntity)
		return
	}
	missedPolicy := strings.TrimSpace(r.FormValue("missed_policy"))
	if !validScheduleMissedPolicy(missedPolicy) {
		http.Error(w, "missed-run policy must be skip or run once", http.StatusUnprocessableEntity)
		return
	}
	retryCount, err := parseScheduleFormInt(r.FormValue("retry_count"), 0)
	if err != nil || validateScheduleRetryCount(retryCount) != nil {
		http.Error(w, fmt.Sprintf("retry count must be between 0 and %d", MaxScheduleRetryCount),
			http.StatusUnprocessableEntity)
		return
	}
	retryBackoff, err := parseScheduleFormInt(r.FormValue("retry_backoff_seconds"), DefaultScheduleRetryBackoffSeconds)
	if err != nil || validateScheduleRetryBackoff(retryBackoff) != nil {
		http.Error(w, fmt.Sprintf("retry backoff must be between %d and %d seconds",
			MinScheduleRetryBackoffSeconds, MaxScheduleRetryBackoffSeconds), http.StatusUnprocessableEntity)
		return
	}
	autoExportFormat := normalizeScheduleAutoExportFormat(r.FormValue("auto_export_format"))
	if !validScheduleAutoExportFormat(autoExportFormat) {
		http.Error(w, "unsupported automatic export format", http.StatusUnprocessableEntity)
		return
	}
	retentionDays, err := parseScheduleFormInt(r.FormValue("runs_retention_days"), 0)
	if err != nil || validateScheduleRetentionDays(retentionDays) != nil {
		http.Error(w, fmt.Sprintf("run retention must be between 0 (keep all) and %d days",
			MaxScheduleRunsRetentionDays), http.StatusUnprocessableEntity)
		return
	}
	incrementalMode := strings.TrimSpace(r.FormValue("incremental_mode"))
	if !ValidIncrementalMode(incrementalMode) {
		http.Error(w, "unsupported incremental mode", http.StatusUnprocessableEntity)
		return
	}
	spec := ScheduleSpec{
		Recurrence: recurrence, FirstRunAt: firstRun, CustomCron: strings.TrimSpace(r.FormValue("cron")),
		OverlapPolicy: overlapPolicy, MissedPolicy: missedPolicy, IncrementalMode: incrementalMode,
	}
	for _, value := range r.Form["weekdays"] {
		day, parseErr := strconv.Atoi(value)
		if parseErr != nil || day < 0 || day > 6 {
			http.Error(w, "invalid weekly day", http.StatusUnprocessableEntity)
			return
		}
		spec.Weekdays = append(spec.Weekdays, day)
	}
	now := time.Now().UTC()
	next, err := NextScheduleTime(spec, timezone, now)
	if recurrence == "once" && firstRun.Before(now.In(location)) {
		if missedPolicy == "run_once" {
			next = now
			err = nil
		} else {
			err = fmt.Errorf("one-time run is in the past")
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	enabled := r.FormValue("enabled") == "on"
	var nextPointer *time.Time
	if enabled && !next.IsZero() {
		nextUTC := next.UTC()
		nextPointer = &nextUTC
	}
	schedule := ScheduleRecord{
		ID: uuid.NewString(), Name: name, TemplateID: templateID, Timezone: timezone,
		Enabled: enabled, Spec: spec, RetryCount: retryCount, RetryBackoffSeconds: retryBackoff,
		AutoExportFormat: autoExportFormat, RunsRetentionDays: retentionDays,
		NextRunAt: nextPointer, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.svc.SaveSchedule(r.Context(), schedule); err != nil {
		http.Error(w, "could not save schedule", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/schedules?notice=Schedule+created", http.StatusSeeOther)
}

func (s *Server) toggleSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	enabled := r.PathValue("action") == "enable"
	if !enabled && r.PathValue("action") != "disable" {
		http.Error(w, "invalid schedule action", http.StatusUnprocessableEntity)
		return
	}
	if err := s.svc.SetScheduleEnabled(r.Context(), strings.TrimSpace(r.PathValue("id")), enabled); err != nil {
		if errors.Is(err, ErrScheduleNotFound) {
			http.Error(w, "schedule not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not update schedule", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/schedules?notice=Schedule+updated", http.StatusSeeOther)
}

func (s *Server) runScheduleNow(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	job, err := s.svc.RunScheduleNow(r.Context(), strings.TrimSpace(r.PathValue("id")), time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrScheduleNotFound) {
			http.Error(w, "schedule not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not queue scheduled job", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/jobs/"+job.ID, http.StatusSeeOther)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.svc.DeleteSchedule(r.Context(), strings.TrimSpace(r.PathValue("id"))); err != nil {
		if errors.Is(err, ErrScheduleNotFound) {
			http.Error(w, "schedule not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not delete schedule", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/schedules?notice=Schedule+deleted", http.StatusSeeOther)
}
