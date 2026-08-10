package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embed the IANA timezone database for minimal local and container builds.
	_ "time/tzdata"
)

var (
	ErrScheduleStoreUnsupported = errors.New("schedule storage is unavailable")
	ErrScheduleNotFound         = errors.New("schedule not found")
)

type ScheduleSpec struct {
	Recurrence    string    `json:"recurrence"`
	FirstRunAt    time.Time `json:"first_run_at"`
	Weekdays      []int     `json:"weekdays,omitempty"`
	CustomCron    string    `json:"custom_cron,omitempty"`
	OverlapPolicy string    `json:"overlap_policy"`
	MissedPolicy  string    `json:"missed_policy"`
}

type ScheduleRecord struct {
	ID           string
	Name         string
	TemplateID   string
	TemplateName string
	Timezone     string
	Enabled      bool
	Spec         ScheduleSpec
	NextRunAt    *time.Time
	LastRunAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	Error        string
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

func (s *Service) scheduleRepository() (scheduleRepository, error) {
	repository, ok := s.repo.(scheduleRepository)
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

func (s *Service) StartDueSchedules(ctx context.Context, now time.Time, limit int) ([]Job, error) {
	repository, err := s.scheduleRepository()
	if err != nil {
		return nil, err
	}
	return repository.StartDueSchedules(ctx, now, limit)
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
