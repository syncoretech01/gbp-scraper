package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embed the IANA timezone database for minimal local and container builds.
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var (
	ErrScheduleStoreUnsupported = errors.New("schedule storage is unavailable")
	ErrScheduleNotFound         = errors.New("schedule not found")
)

// Schedule overlap policies decide what happens when a schedule fires while
// its previous run's job is still active.
const (
	ScheduleOverlapQueue   = "queue"
	ScheduleOverlapSkip    = "skip"
	ScheduleOverlapReplace = "replace"
)

// Bounds for the schedule-level automation options. Retry delays grow
// linearly: retry_backoff_seconds multiplied by the attempt that just failed.
const (
	MinScheduleRetryBackoffSeconds     = 10
	MaxScheduleRetryBackoffSeconds     = 3600
	MaxScheduleRetryCount              = 10
	MaxScheduleRunsRetentionDays       = 3650
	DefaultScheduleRetryBackoffSeconds = 60
)

type ScheduleSpec struct {
	Recurrence    string    `json:"recurrence"`
	FirstRunAt    time.Time `json:"first_run_at"`
	Weekdays      []int     `json:"weekdays,omitempty"`
	CustomCron    string    `json:"custom_cron,omitempty"`
	OverlapPolicy string    `json:"overlap_policy"`
	MissedPolicy  string    `json:"missed_policy"`
	// IncrementalMode is the JobData.IncrementalMode every run this schedule
	// creates is stamped with, overriding whatever the template stored. An
	// empty value keeps the template's own mode, which is exactly what every
	// schedule did before this option existed. It travels inside the
	// schedules.configuration JSON the spec already occupies, so no schema
	// change is involved.
	IncrementalMode string `json:"incremental_mode,omitempty"`
}

type ScheduleRecord struct {
	ID           string
	Name         string
	TemplateID   string
	TemplateName string
	Timezone     string
	Enabled      bool
	Spec         ScheduleSpec
	// Automation options stored alongside the recurrence.
	RetryCount          int
	RetryBackoffSeconds int
	AutoExportFormat    string
	RunsRetentionDays   int
	NextRunAt           *time.Time
	LastRunAt           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ScheduleRunRecord struct {
	ID           int64
	ScheduleID   string
	ScheduleName string
	JobID        string
	State        string
	ScheduledFor time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	Attempt      int
	Error        string
}

// ScheduleRunCompletion reports one scheduled run whose job just reached a
// terminal state, together with the schedule's automation settings so the
// service can queue retries and build automatic exports exactly once.
type ScheduleRunCompletion struct {
	RunID            int64
	ScheduleID       string
	ScheduleName     string
	JobID            string
	State            string
	Attempt          int
	AutoExportFormat string
	RetryQueued      bool
	NextRetryAt      *time.Time
}

type scheduleRepository interface {
	ListSchedules(context.Context) ([]ScheduleRecord, error)
	ListScheduleRuns(context.Context, int) ([]ScheduleRunRecord, error)
	SaveSchedule(context.Context, ScheduleRecord) error
	SetScheduleEnabled(context.Context, string, bool) error
	DeleteSchedule(context.Context, string) error
	RunScheduleNow(context.Context, string, time.Time) (Job, error)
	StartDueSchedules(context.Context, time.Time, int) ([]Job, error)
}

// scheduleAutomationRepository is deliberately additive so embedders which
// implement the original scheduleRepository keep compiling unchanged. The
// bundled SQLite repository implements it and enables the replace overlap
// policy, bounded retries, per-schedule run history, and retention.
type scheduleAutomationRepository interface {
	GetSchedule(context.Context, string) (ScheduleRecord, error)
	ListScheduleRunsForSchedule(context.Context, string, int) ([]ScheduleRunRecord, error)
	CompleteScheduleRuns(context.Context, time.Time) ([]ScheduleRunCompletion, error)
	StartDueScheduleRetries(context.Context, time.Time, int) ([]Job, error)
	PruneScheduleRuns(context.Context, time.Time) (int64, error)
	ListDueReplaceableScheduleJobs(context.Context, time.Time) ([]string, error)
}

func (s *Service) scheduleRepository() (scheduleRepository, error) {
	repository, ok := s.repo.(scheduleRepository)
	if !ok {
		return nil, ErrScheduleStoreUnsupported
	}
	return repository, nil
}

func (s *Service) scheduleAutomationRepository() (scheduleAutomationRepository, error) {
	repository, ok := s.repo.(scheduleAutomationRepository)
	if !ok {
		return nil, ErrScheduleStoreUnsupported
	}
	return repository, nil
}

func (s *Service) ListSchedules(ctx context.Context) ([]ScheduleRecord, error) {
	repository, err := s.scheduleRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListSchedules(ctx)
}

func (s *Service) ListScheduleRuns(ctx context.Context, limit int) ([]ScheduleRunRecord, error) {
	repository, err := s.scheduleRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListScheduleRuns(ctx, limit)
}

func (s *Service) SaveSchedule(ctx context.Context, schedule ScheduleRecord) error {
	repository, err := s.scheduleRepository()
	if err != nil {
		return err
	}
	return repository.SaveSchedule(ctx, schedule)
}

func (s *Service) SetScheduleEnabled(ctx context.Context, id string, enabled bool) error {
	repository, err := s.scheduleRepository()
	if err != nil {
		return err
	}
	return repository.SetScheduleEnabled(ctx, id, enabled)
}

func (s *Service) DeleteSchedule(ctx context.Context, id string) error {
	repository, err := s.scheduleRepository()
	if err != nil {
		return err
	}
	return repository.DeleteSchedule(ctx, id)
}

func (s *Service) RunScheduleNow(ctx context.Context, id string, now time.Time) (Job, error) {
	repository, err := s.scheduleRepository()
	if err != nil {
		return Job{}, err
	}
	return repository.RunScheduleNow(ctx, id, now)
}

func (s *Service) GetSchedule(ctx context.Context, id string) (ScheduleRecord, error) {
	repository, err := s.scheduleAutomationRepository()
	if err != nil {
		return ScheduleRecord{}, err
	}
	return repository.GetSchedule(ctx, id)
}

func (s *Service) ListScheduleRunsForSchedule(ctx context.Context, id string, limit int) ([]ScheduleRunRecord, error) {
	repository, err := s.scheduleAutomationRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListScheduleRunsForSchedule(ctx, id, limit)
}

// StartDueSchedules is the scheduler's poll pass. Besides queueing due
// schedules it settles finished runs (recording retries and automatic
// exports), cancels jobs replaced by the replace overlap policy, starts due
// retry attempts, and prunes run history past each schedule's retention.
// Automation problems never block queueing; they are joined into the returned
// error so the polling worker can log and retry on the next tick.
func (s *Service) StartDueSchedules(ctx context.Context, now time.Time, limit int) ([]Job, error) {
	repository, err := s.scheduleRepository()
	if err != nil {
		return nil, err
	}
	automation, hasAutomation := s.repo.(scheduleAutomationRepository)

	var automationErrs []error
	if hasAutomation {
		if err := s.settleFinishedScheduleRuns(ctx, automation, now); err != nil {
			automationErrs = append(automationErrs, err)
		}
		if err := s.cancelReplacedScheduleJobs(ctx, automation, now); err != nil {
			automationErrs = append(automationErrs, err)
		}
	}

	jobs, err := repository.StartDueSchedules(ctx, now, limit)
	if err != nil {
		return jobs, err
	}

	if hasAutomation {
		retries, retryErr := automation.StartDueScheduleRetries(ctx, now, limit)
		if retryErr != nil {
			automationErrs = append(automationErrs, retryErr)
		}
		jobs = append(jobs, retries...)
		if err := s.applyScheduleRetention(ctx, automation, now); err != nil {
			automationErrs = append(automationErrs, err)
		}
	}
	return jobs, errors.Join(automationErrs...)
}

// applyScheduleRetention enforces each schedule's own retention window on the
// artifacts one run leaves behind: the run-history row, the operational log,
// and any completed export produced from the run's job.
//
// Exports are deleted through the ordinary export-deletion path so the file on
// disk and the row go together. Collected data — the job, its normalized
// results, and its per-job CSV — is never a retention candidate. Screenshots
// are attached to businesses rather than runs, so they are governed by the
// workspace storage cap and the System maintenance cleanup instead.
func (s *Service) applyScheduleRetention(
	ctx context.Context,
	automation scheduleAutomationRepository,
	now time.Time,
) error {
	var retentionErrs []error
	if source, ok := automation.(scheduleExportRetentionRepository); ok {
		exportIDs, err := source.ExpiredScheduleRunExports(ctx, now)
		if err != nil {
			retentionErrs = append(retentionErrs, err)
		}
		for _, exportID := range exportIDs {
			if err := s.DeleteExport(ctx, exportID); err != nil && !errors.Is(err, ErrExportNotFound) {
				retentionErrs = append(retentionErrs, fmt.Errorf("prune scheduled export %s: %w", exportID, err))
			}
		}
	}
	if _, err := automation.PruneScheduleRuns(ctx, now); err != nil {
		retentionErrs = append(retentionErrs, err)
	}

	return errors.Join(retentionErrs...)
}

// scheduleExportRetentionRepository optionally reports the completed exports
// belonging to expired schedule runs. A repository without it simply keeps
// exports, which is the historical behaviour.
type scheduleExportRetentionRepository interface {
	ExpiredScheduleRunExports(context.Context, time.Time) ([]string, error)
}

// settleFinishedScheduleRuns records terminal outcomes on schedule_runs rows
// exactly once. The repository queues failed-run retries transactionally; the
// service builds the configured automatic export for completed runs here.
func (s *Service) settleFinishedScheduleRuns(
	ctx context.Context,
	automation scheduleAutomationRepository,
	now time.Time,
) error {
	completions, err := automation.CompleteScheduleRuns(ctx, now)
	if err != nil {
		return err
	}
	var exportErrs []error
	for _, completion := range completions {
		if completion.State != string(jobruntime.StateCompleted) || completion.AutoExportFormat == "" {
			continue
		}
		if err := s.buildScheduleAutoExport(ctx, completion, now); err != nil {
			exportErrs = append(exportErrs,
				fmt.Errorf("automatic export for schedule %s: %w", completion.ScheduleID, err))
		}
	}
	return errors.Join(exportErrs...)
}

// cancelReplacedScheduleJobs cancels still-active jobs of due schedules whose
// overlap policy is replace, through the ordinary lifecycle control path, so
// the poll pass can queue the new run in their place.
func (s *Service) cancelReplacedScheduleJobs(
	ctx context.Context,
	automation scheduleAutomationRepository,
	now time.Time,
) error {
	jobIDs, err := automation.ListDueReplaceableScheduleJobs(ctx, now)
	if err != nil {
		return err
	}
	var cancelErrs []error
	for _, jobID := range jobIDs {
		if _, _, err := s.ApplyControl(ctx, jobID, jobruntime.ControlCancel); err != nil &&
			!errors.Is(err, jobruntime.ErrControlRejected) && !errors.Is(err, ErrLifecycleNotFound) {
			cancelErrs = append(cancelErrs, fmt.Errorf("replace scheduled job %s: %w", jobID, err))
		}
	}
	return errors.Join(cancelErrs...)
}

// buildScheduleAutoExport creates a verified export of all businesses in the
// schedule's configured format after a completed scheduled run. It reuses the
// interactive export builder; Server and Service live in the same package and
// the builder only needs the service, so a transient handle is sufficient.
func (s *Service) buildScheduleAutoExport(ctx context.Context, completion ScheduleRunCompletion, now time.Time) error {
	format := strings.ToLower(strings.TrimSpace(completion.AutoExportFormat))
	if !validScheduleAutoExportFormat(format) || format == "" {
		return fmt.Errorf("unsupported automatic export format %q", completion.AutoExportFormat)
	}
	search := ResultSearch{}
	columns := defaultExportColumns()
	options := ExportBuildOptions{SplitBy: "none", Deduplicate: true, LegacyShape: true}
	filterJSON, err := json.Marshal(search)
	if err != nil {
		return fmt.Errorf("encode automatic export filter: %w", err)
	}
	columnJSON, optionJSON, err := encodeExportConfiguration(columns, options)
	if err != nil {
		return err
	}
	record := ExportRecord{
		ID:         uuid.NewString(),
		Name:       completion.ScheduleName + " auto export " + now.UTC().Format("2006-01-02 15:04"),
		Format:     format,
		State:      "running",
		SourceType: "schedule",
		SourceID:   completion.ScheduleID,
		Filters:    string(filterJSON),
		Columns:    columnJSON,
		Options:    optionJSON,
		CreatedAt:  now.UTC(),
		StartedAt:  &now,
	}
	if err := s.CreateExport(ctx, record); err != nil {
		return err
	}
	builder := &Server{svc: s}
	artifact, err := builder.generateConfiguredExport(ctx, record, search, columns, options)
	finished := time.Now().UTC()
	record.FinishedAt = &finished
	if err != nil {
		record.State = "failed"
		record.Error = publicExportError(err)
		_ = s.UpdateExport(ctx, record)
		return err
	}
	if err := s.ReplaceExportParts(ctx, record.ID, artifact.Parts); err != nil {
		record.State = "failed"
		record.Error = "could not register generated parts"
		removeExportArtifact(s.dataFolder, artifact)
		_ = s.UpdateExport(ctx, record)
		return err
	}
	record.State = "completed"
	record.RelativePath = artifact.RelativePath
	record.RecordCount = artifact.RecordCount
	record.FileSize = artifact.FileSize
	record.Checksum = artifact.Checksum
	if err := s.UpdateExport(ctx, record); err != nil {
		removeExportArtifact(s.dataFolder, artifact)
		return err
	}
	return nil
}

// validScheduleOverlapPolicy accepts the three supported overlap policies.
// Every place that parses a policy must use it so stored values stay bounded.
func validScheduleOverlapPolicy(policy string) bool {
	switch policy {
	case ScheduleOverlapQueue, ScheduleOverlapSkip, ScheduleOverlapReplace:
		return true
	default:
		return false
	}
}

func validScheduleMissedPolicy(policy string) bool {
	return policy == "skip" || policy == "run_once"
}

func validateScheduleRetryCount(count int) error {
	if count < 0 || count > MaxScheduleRetryCount {
		return fmt.Errorf("retry count must be between 0 and %d", MaxScheduleRetryCount)
	}
	return nil
}

func validateScheduleRetryBackoff(seconds int) error {
	if seconds < MinScheduleRetryBackoffSeconds || seconds > MaxScheduleRetryBackoffSeconds {
		return fmt.Errorf("retry backoff must be between %d and %d seconds",
			MinScheduleRetryBackoffSeconds, MaxScheduleRetryBackoffSeconds)
	}
	return nil
}

func validateScheduleRetentionDays(days int) error {
	if days < 0 || days > MaxScheduleRunsRetentionDays {
		return fmt.Errorf("run retention must be between 0 (keep all) and %d days", MaxScheduleRunsRetentionDays)
	}
	return nil
}

// normalizeScheduleAutoExportFormat maps the UI's "off" choice to the stored
// empty string which disables the automatic export.
func normalizeScheduleAutoExportFormat(raw string) string {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "off" || format == "none" {
		return ""
	}
	return format
}

// validScheduleAutoExportFormat accepts the advertised export formats or the
// empty string, which keeps the automatic export disabled.
func validScheduleAutoExportFormat(format string) bool {
	if format == "" {
		return true
	}
	_, ok := exportExtension(format)
	return ok
}

// ScheduleRetryAllowed reports whether a failed run at the given attempt may
// schedule another automatic attempt. Attempt one is the original run and a
// schedule allows retry_count extra attempts, so attempts run 1..retry_count+1.
func ScheduleRetryAllowed(retryCount, failedAttempt int) bool {
	if retryCount > MaxScheduleRetryCount {
		retryCount = MaxScheduleRetryCount
	}
	if failedAttempt < 1 {
		failedAttempt = 1
	}
	return retryCount > 0 && failedAttempt <= retryCount
}

// ScheduleRetryDelay returns the wait before the next automatic attempt:
// retry_backoff_seconds multiplied by the attempt number that just failed,
// with the backoff clamped into its documented bounds.
func ScheduleRetryDelay(backoffSeconds, failedAttempt int) time.Duration {
	if backoffSeconds < MinScheduleRetryBackoffSeconds {
		backoffSeconds = MinScheduleRetryBackoffSeconds
	}
	if backoffSeconds > MaxScheduleRetryBackoffSeconds {
		backoffSeconds = MaxScheduleRetryBackoffSeconds
	}
	if failedAttempt < 1 {
		failedAttempt = 1
	}
	if failedAttempt > MaxScheduleRetryCount {
		failedAttempt = MaxScheduleRetryCount
	}
	return time.Duration(backoffSeconds*failedAttempt) * time.Second
}

func NextScheduleTime(spec ScheduleSpec, timezone string, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("unknown timezone %q", timezone)
	}
	first := spec.FirstRunAt.In(location)
	after = after.In(location)
	if after.Before(first) {
		return first, nil
	}

	switch spec.Recurrence {
	case "once":
		return time.Time{}, nil
	case "hourly":
		candidate := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), first.Minute(), 0, 0, location)
		if !candidate.After(after) {
			candidate = candidate.Add(time.Hour)
		}
		return candidate, nil
	case "daily":
		candidate := time.Date(after.Year(), after.Month(), after.Day(), first.Hour(), first.Minute(), 0, 0, location)
		if !candidate.After(after) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate, nil
	case "weekly":
		weekdays := make(map[time.Weekday]struct{})
		for _, day := range spec.Weekdays {
			if day >= 0 && day <= 6 {
				weekdays[time.Weekday(day)] = struct{}{}
			}
		}
		if len(weekdays) == 0 {
			weekdays[first.Weekday()] = struct{}{}
		}
		for offset := 0; offset <= 7; offset++ {
			day := after.AddDate(0, 0, offset)
			if _, ok := weekdays[day.Weekday()]; !ok {
				continue
			}
			candidate := time.Date(day.Year(), day.Month(), day.Day(), first.Hour(), first.Minute(), 0, 0, location)
			if candidate.After(after) {
				return candidate, nil
			}
		}
		return time.Time{}, fmt.Errorf("could not calculate weekly schedule")
	case "monthly":
		for offset := 0; offset <= 14; offset++ {
			base := time.Date(after.Year(), after.Month()+time.Month(offset), 1, first.Hour(), first.Minute(), 0, 0, location)
			day := min(first.Day(), daysInMonth(base.Year(), base.Month(), location))
			candidate := time.Date(base.Year(), base.Month(), day, first.Hour(), first.Minute(), 0, 0, location)
			if candidate.After(after) {
				return candidate, nil
			}
		}
		return time.Time{}, fmt.Errorf("could not calculate monthly schedule")
	case "cron":
		return nextCronTime(spec.CustomCron, location, after)
	default:
		return time.Time{}, fmt.Errorf("unsupported recurrence %q", spec.Recurrence)
	}
}

func daysInMonth(year int, month time.Month, location *time.Location) int {
	return time.Date(year, month+1, 0, 12, 0, 0, 0, location).Day()
}

type cronField struct {
	any     bool
	allowed map[int]struct{}
}

func nextCronTime(expression string, location *time.Location, after time.Time) (time.Time, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron must contain minute, hour, day-of-month, month, and weekday")
	}
	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron minute: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron hour: %w", err)
	}
	monthDay, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron day: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron month: %w", err)
	}
	weekday, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron weekday: %w", err)
	}

	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	deadline := candidate.AddDate(5, 0, 0)
	for candidate.Before(deadline) {
		_, minuteOK := minute.allowed[candidate.Minute()]
		_, hourOK := hour.allowed[candidate.Hour()]
		_, monthOK := month.allowed[int(candidate.Month())]
		_, monthDayOK := monthDay.allowed[candidate.Day()]
		_, weekdayOK := weekday.allowed[int(candidate.Weekday())]
		dayOK := monthDayOK && weekdayOK
		if !monthDay.any && !weekday.any {
			dayOK = monthDayOK || weekdayOK
		}
		if minuteOK && hourOK && monthOK && dayOK {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron has no occurrence within five years")
}

func parseCronField(value string, minimum, maximum int) (cronField, error) {
	field := cronField{any: value == "*", allowed: make(map[int]struct{})}
	if field.any {
		for current := minimum; current <= maximum; current++ {
			field.allowed[current] = struct{}{}
		}
		return field, nil
	}
	for _, part := range strings.Split(value, ",") {
		number, err := strconv.Atoi(part)
		if err != nil || number < minimum || number > maximum {
			return cronField{}, fmt.Errorf("value %q must be between %d and %d", part, minimum, maximum)
		}
		field.allowed[number] = struct{}{}
	}
	if len(field.allowed) == 0 {
		return cronField{}, fmt.Errorf("field is empty")
	}
	return field, nil
}
