package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maximumScheduleUpdateBytes = 16 << 10
	maximumScheduleRunHistory  = 200
	defaultScheduleRunHistory  = 50
)

// registerScheduleAutomationRoutes exposes the schedule automation API. The
// main router calls this once during construction. The extra POST route lets
// the plain HTML edit form reach the same handler without JavaScript.
func (s *Server) registerScheduleAutomationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/schedules/{id}", s.updateScheduleAPI)
	mux.HandleFunc("POST /api/v1/schedules/{id}/update", s.updateScheduleAPI)
	mux.HandleFunc("GET /api/v1/schedules/{id}/runs", s.listScheduleRunsAPI)
}

// scheduleUpdateRequest is a partial update. Pointer fields distinguish
// "leave unchanged" from an explicit new value.
type scheduleUpdateRequest struct {
	Name                *string `json:"name,omitempty"`
	Cron                *string `json:"cron,omitempty"`
	Timezone            *string `json:"timezone,omitempty"`
	Enabled             *bool   `json:"enabled,omitempty"`
	OverlapPolicy       *string `json:"overlap_policy,omitempty"`
	MissedPolicy        *string `json:"missed_policy,omitempty"`
	RetryCount          *int    `json:"retry_count,omitempty"`
	RetryBackoffSeconds *int    `json:"retry_backoff_seconds,omitempty"`
	AutoExportFormat    *string `json:"auto_export_format,omitempty"`
	RunsRetentionDays   *int    `json:"runs_retention_days,omitempty"`
	// IncrementalMode stamps every run this schedule creates with one
	// JobData.IncrementalMode. An explicit empty string clears it, which
	// returns the schedule to using the template's own mode.
	IncrementalMode *string `json:"incremental_mode,omitempty"`
}

type scheduleAPIView struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	TemplateID          string     `json:"template_id,omitempty"`
	TemplateName        string     `json:"template_name,omitempty"`
	Timezone            string     `json:"timezone"`
	Enabled             bool       `json:"enabled"`
	Recurrence          string     `json:"recurrence"`
	Cron                string     `json:"cron,omitempty"`
	OverlapPolicy       string     `json:"overlap_policy"`
	MissedPolicy        string     `json:"missed_policy"`
	RetryCount          int        `json:"retry_count"`
	RetryBackoffSeconds int        `json:"retry_backoff_seconds"`
	AutoExportFormat    string     `json:"auto_export_format"`
	RunsRetentionDays   int        `json:"runs_retention_days"`
	IncrementalMode     string     `json:"incremental_mode,omitempty"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	LastRunAt           *time.Time `json:"last_run_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type scheduleRunAPIView struct {
	ID           int64      `json:"id"`
	ScheduleID   string     `json:"schedule_id"`
	JobID        string     `json:"job_id,omitempty"`
	State        string     `json:"state"`
	ScheduledFor time.Time  `json:"scheduled_for"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Attempt      int        `json:"attempt"`
	Error        string     `json:"error,omitempty"`
}

func (s *Server) updateScheduleAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	schedule, err := s.svc.GetSchedule(r.Context(), id)
	if err != nil {
		renderScheduleAPIError(w, err)
		return
	}
	update, err := decodeScheduleUpdate(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_schedule_update", err.Error())
		return
	}
	if err := applyScheduleUpdate(&schedule, update, time.Now().UTC()); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_schedule_update", err.Error())
		return
	}
	if err := s.svc.SaveSchedule(r.Context(), schedule); err != nil {
		renderScheduleAPIError(w, err)
		return
	}
	if !scheduleRequestWantsJSON(r) {
		http.Redirect(w, r, "/app/schedules?notice=Schedule+updated", http.StatusSeeOther)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: scheduleAPIViewFrom(schedule)})
}

func (s *Server) listScheduleRunsAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	limit := defaultScheduleRunHistory
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumScheduleRunHistory {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit",
				fmt.Sprintf("Limit must be between 1 and %d", maximumScheduleRunHistory))
			return
		}
		limit = value
	}
	if _, err := s.svc.GetSchedule(r.Context(), id); err != nil {
		renderScheduleAPIError(w, err)
		return
	}
	runs, err := s.svc.ListScheduleRunsForSchedule(r.Context(), id, limit)
	if err != nil {
		renderScheduleAPIError(w, err)
		return
	}
	views := make([]scheduleRunAPIView, 0, len(runs))
	for _, run := range runs {
		views = append(views, scheduleRunAPIView{
			ID: run.ID, ScheduleID: run.ScheduleID, JobID: run.JobID, State: run.State,
			ScheduledFor: run.ScheduledFor, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
			Attempt: run.Attempt, Error: run.Error,
		})
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: views})
}

func decodeScheduleUpdate(w http.ResponseWriter, r *http.Request) (scheduleUpdateRequest, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumScheduleUpdateBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var update scheduleUpdateRequest
		if err := decoder.Decode(&update); err != nil {
			return scheduleUpdateRequest{}, fmt.Errorf("invalid JSON")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return scheduleUpdateRequest{}, fmt.Errorf("request must contain exactly one JSON object")
		}
		return update, nil
	}

	if err := parseBoundedRequestForm(w, r, maximumScheduleUpdateBytes); err != nil {
		return scheduleUpdateRequest{}, fmt.Errorf("invalid form")
	}
	return scheduleUpdateFromForm(r)
}

// scheduleUpdateFromForm treats a submitted field as a change request. Empty
// optional text fields mean "leave unchanged" so one shared edit form works
// for every recurrence type.
func scheduleUpdateFromForm(r *http.Request) (scheduleUpdateRequest, error) {
	var update scheduleUpdateRequest
	setString := func(name string, target **string) {
		if values, ok := r.Form[name]; ok && len(values) > 0 {
			value := values[0]
			*target = &value
		}
	}
	if values, ok := r.Form["name"]; ok && strings.TrimSpace(values[0]) != "" {
		value := values[0]
		update.Name = &value
	}
	if values, ok := r.Form["cron"]; ok && strings.TrimSpace(values[0]) != "" {
		value := values[0]
		update.Cron = &value
	}
	if values, ok := r.Form["timezone"]; ok && strings.TrimSpace(values[0]) != "" {
		value := values[0]
		update.Timezone = &value
	}
	setString("overlap_policy", &update.OverlapPolicy)
	setString("missed_policy", &update.MissedPolicy)
	setString("auto_export_format", &update.AutoExportFormat)
	// An empty incremental_mode is meaningful — it clears the override — so it
	// is read directly rather than through setString, which skips blanks.
	if values, ok := r.Form["incremental_mode"]; ok && len(values) > 0 {
		value := strings.TrimSpace(values[0])
		update.IncrementalMode = &value
	}
	if values, ok := r.Form["enabled"]; ok && len(values) > 0 {
		enabled, err := parseScheduleFormBool(values[0])
		if err != nil {
			return scheduleUpdateRequest{}, err
		}
		update.Enabled = &enabled
	}
	intFields := []struct {
		name   string
		target **int
	}{
		{"retry_count", &update.RetryCount},
		{"retry_backoff_seconds", &update.RetryBackoffSeconds},
		{"runs_retention_days", &update.RunsRetentionDays},
	}
	for _, field := range intFields {
		values, ok := r.Form[field.name]
		if !ok || len(values) == 0 || strings.TrimSpace(values[0]) == "" {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(values[0]))
		if err != nil {
			return scheduleUpdateRequest{}, fmt.Errorf("%s must be a whole number", field.name)
		}
		*field.target = &value
	}
	return update, nil
}

func parseScheduleFormBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "on", "yes":
		return true, nil
	case "false", "0", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("enabled must be true or false")
	}
}

// applyScheduleUpdate validates each supplied field with the same rules as
// schedule creation, then recomputes the next occurrence when the recurrence,
// timezone, or enablement changed.
func applyScheduleUpdate(schedule *ScheduleRecord, update scheduleUpdateRequest, now time.Time) error {
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if name == "" || len(name) > 120 {
			return fmt.Errorf("schedule name is required and must be at most 120 characters")
		}
		schedule.Name = name
	}
	if update.Timezone != nil {
		timezone := strings.TrimSpace(*update.Timezone)
		if _, err := time.LoadLocation(timezone); err != nil || timezone == "" {
			return fmt.Errorf("unknown IANA timezone")
		}
		schedule.Timezone = timezone
	}
	if update.Cron != nil {
		cron := strings.TrimSpace(*update.Cron)
		if cron == "" {
			return fmt.Errorf("cron expression cannot be empty")
		}
		schedule.Spec.Recurrence = "cron"
		schedule.Spec.CustomCron = cron
	}
	if update.OverlapPolicy != nil {
		policy := strings.TrimSpace(*update.OverlapPolicy)
		if !validScheduleOverlapPolicy(policy) {
			return fmt.Errorf("overlap policy must be queue, skip, or replace")
		}
		schedule.Spec.OverlapPolicy = policy
	}
	if update.MissedPolicy != nil {
		policy := strings.TrimSpace(*update.MissedPolicy)
		if !validScheduleMissedPolicy(policy) {
			return fmt.Errorf("missed-run policy must be skip or run_once")
		}
		schedule.Spec.MissedPolicy = policy
	}
	if update.RetryCount != nil {
		if err := validateScheduleRetryCount(*update.RetryCount); err != nil {
			return err
		}
		schedule.RetryCount = *update.RetryCount
	}
	if update.RetryBackoffSeconds != nil {
		if err := validateScheduleRetryBackoff(*update.RetryBackoffSeconds); err != nil {
			return err
		}
		schedule.RetryBackoffSeconds = *update.RetryBackoffSeconds
	}
	if update.AutoExportFormat != nil {
		format := normalizeScheduleAutoExportFormat(*update.AutoExportFormat)
		if !validScheduleAutoExportFormat(format) {
			return fmt.Errorf("unsupported automatic export format %q", *update.AutoExportFormat)
		}
		schedule.AutoExportFormat = format
	}
	if update.RunsRetentionDays != nil {
		if err := validateScheduleRetentionDays(*update.RunsRetentionDays); err != nil {
			return err
		}
		schedule.RunsRetentionDays = *update.RunsRetentionDays
	}
	if update.IncrementalMode != nil {
		mode := strings.TrimSpace(*update.IncrementalMode)
		if !ValidIncrementalMode(mode) {
			return fmt.Errorf("unsupported incremental mode %q", *update.IncrementalMode)
		}
		schedule.Spec.IncrementalMode = mode
	}
	if update.Enabled != nil {
		schedule.Enabled = *update.Enabled
	}

	// Reuse the create-time recurrence validator, and recompute the stored
	// next occurrence whenever a field that shapes it changed.
	next, err := NextScheduleTime(schedule.Spec, schedule.Timezone, now)
	if err != nil {
		return err
	}
	if update.Cron != nil || update.Timezone != nil || update.Enabled != nil {
		schedule.NextRunAt = nil
		if schedule.Enabled {
			if next.IsZero() {
				return fmt.Errorf("schedule has no future occurrence")
			}
			nextUTC := next.UTC()
			schedule.NextRunAt = &nextUTC
		}
	}
	schedule.UpdatedAt = now
	return nil
}

func scheduleAPIViewFrom(schedule ScheduleRecord) scheduleAPIView {
	return scheduleAPIView{
		ID: schedule.ID, Name: schedule.Name, TemplateID: schedule.TemplateID,
		TemplateName: schedule.TemplateName, Timezone: schedule.Timezone, Enabled: schedule.Enabled,
		Recurrence: schedule.Spec.Recurrence, Cron: schedule.Spec.CustomCron,
		OverlapPolicy: schedule.Spec.OverlapPolicy, MissedPolicy: schedule.Spec.MissedPolicy,
		RetryCount: schedule.RetryCount, RetryBackoffSeconds: schedule.RetryBackoffSeconds,
		AutoExportFormat: schedule.AutoExportFormat, RunsRetentionDays: schedule.RunsRetentionDays,
		IncrementalMode: schedule.Spec.IncrementalMode,
		NextRunAt:       schedule.NextRunAt, LastRunAt: schedule.LastRunAt, UpdatedAt: schedule.UpdatedAt,
	}
}

func scheduleRequestWantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") ||
		r.Method == http.MethodPut
}

func renderScheduleAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrScheduleNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "schedule_not_found", "Schedule was not found")
	case errors.Is(err, ErrScheduleStoreUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "schedules_unavailable", "Schedule storage is unavailable")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "schedule_failed", "Could not process the schedule request")
	}
}
