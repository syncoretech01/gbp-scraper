package web

import (
	"testing"
	"time"
)

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
