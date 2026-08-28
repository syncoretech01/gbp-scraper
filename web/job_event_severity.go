package web

import (
	"slices"
	"strings"
)

// Honest severity for durable job events.
//
// A completed 180-search run that failed nothing, lost nothing and was blocked
// never still reported 118 warnings, because the worker records "this search
// found no places" at severity "warning". 117 of those 118 were that event.
// An operator reading the monitor sees a run that succeeded completely and a
// counter insisting something went wrong 118 times, and the only rational
// response is to stop trusting the counter.
//
// Severity has to mean one thing:
//
//	information  the run behaved normally and there is nothing to do. A
//	             search whose area held no matching businesses is a fact
//	             about the area, not a fault. So is the adaptive engine
//	             deciding a window has saturated.
//	warning      the run degraded but continued, and an operator could act
//	             on it: a retried attempt, a task truncated at its result
//	             cap, a budget the host forced down, a proxy that dropped.
//	error        a task genuinely failed, or data was at risk: a failed
//	             task after its last attempt, a commit that did not land,
//	             low disk, a run that stopped on a fatal condition.
//
// The table below is the single definition. Every surface that counts or
// colours job events reads it, so the log viewer, the severity totals and the
// "what went wrong" panel can never disagree about what a warning is.

// The three honest severities. They are the values written to
// job_events.severity, so the strings must not change.
const (
	JobEventSeverityInformation = "information"
	JobEventSeverityWarning     = "warning"
	JobEventSeverityError       = "error"
)

// jobEventSeverityByType maps a worker event type onto the severity it
// deserves, overriding whatever the emitter recorded.
//
// Only entries whose recorded severity is wrong need to appear here. An event
// type that is absent keeps the severity its emitter chose.
var jobEventSeverityByType = map[string]string{
	// A cell with no matching businesses is the expected answer to a query
	// over an area that has none. The runner records it so the coverage
	// engine can measure saturation, not because anything went wrong.
	"cell-empty": JobEventSeverityInformation,
	// The adaptive engine stopping because new results dried up is the
	// feature working, and it is already reported as an outcome.
	"coverage-saturated": JobEventSeverityInformation,
	"no-new-results":     JobEventSeverityInformation,
	// Truncation is real degradation: the cell very likely holds businesses
	// the platform never rendered, and the operator can act on it by
	// refining the grid.
	"task-truncated": JobEventSeverityWarning,
	// Losing a commit or a merge is data loss, whatever the emitter said.
	"task-commit-failed": JobEventSeverityError,
	"task-merge-failed":  JobEventSeverityError,
	"low-disk":           JobEventSeverityError,
}

// HonestJobEventSeverity returns the severity an event should be counted and
// displayed under. The recorded severity is the fallback, so an event type the
// policy says nothing about is unchanged.
func HonestJobEventSeverity(eventType, recordedSeverity string) string {
	if severity, known := jobEventSeverityByType[strings.TrimSpace(strings.ToLower(eventType))]; known {
		return severity
	}

	switch strings.TrimSpace(strings.ToLower(recordedSeverity)) {
	case "error", "fatal", "critical":
		return JobEventSeverityError
	case "warning", "warn":
		return JobEventSeverityWarning
	default:
		return JobEventSeverityInformation
	}
}

// InformationalJobEventTypes lists the event types the policy demotes to
// information. It exists so a SQL severity total and a client-side counter can
// be generated from the same list instead of each carrying its own copy.
//
// The order is stable so generated SQL and generated JavaScript are stable.
func InformationalJobEventTypes() []string {
	types := make([]string, 0, len(jobEventSeverityByType))
	for eventType, severity := range jobEventSeverityByType {
		if severity == JobEventSeverityInformation {
			types = append(types, eventType)
		}
	}
	slices.Sort(types)

	return types
}
