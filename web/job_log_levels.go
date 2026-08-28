package web

import (
	"encoding/json"
	"net/url"
	"strings"
)

// The ten log levels the local monitor filters on. They are event classes, not
// syslog priorities: an operator triaging a run wants "which proxies failed",
// not "how severe was it". Severity is still the fallback for an event whose
// type carries no class of its own.
const (
	JobLogLevelInformation    = "information"
	JobLogLevelWarning        = "warning"
	JobLogLevelRateLimit      = "rate-limit"
	JobLogLevelProxyFailure   = "proxy-failure"
	JobLogLevelBrowserFailure = "browser-failure"
	JobLogLevelWebsiteTimeout = "website-timeout"
	JobLogLevelParsingFailure = "parsing-failure"
	JobLogLevelDuplicate      = "duplicate"
	JobLogLevelMaximumRuntime = "maximum-runtime"
	JobLogLevelSystemError    = "system-error"
)

// JobLogLevels lists every level in the order the severity filter offers them.
// The monitor template and the API both read this list, so a level can never
// appear in one and be missing from the other.
var JobLogLevels = []string{
	JobLogLevelInformation,
	JobLogLevelWarning,
	JobLogLevelRateLimit,
	JobLogLevelProxyFailure,
	JobLogLevelBrowserFailure,
	JobLogLevelWebsiteTimeout,
	JobLogLevelParsingFailure,
	JobLogLevelDuplicate,
	JobLogLevelMaximumRuntime,
	JobLogLevelSystemError,
}

// jobLogLevelByEventType maps the worker's own event types onto the levels an
// operator filters by. Every key here is written by runner/webrunner or by the
// lifecycle repository, so the filter can never offer a class that no event
// can ever carry.
var jobLogLevelByEventType = map[string]string{
	"proxy-failure":        JobLogLevelProxyFailure,
	"browser-failure":      JobLogLevelBrowserFailure,
	"website-timeout":      JobLogLevelWebsiteTimeout,
	"parsing-failure":      JobLogLevelParsingFailure,
	"rate-limit":           JobLogLevelRateLimit,
	"blocked":              JobLogLevelRateLimit,
	"captcha":              JobLogLevelRateLimit,
	"coverage-saturated":   JobLogLevelDuplicate,
	"duplicate":            JobLogLevelDuplicate,
	"task-claim-failed":    JobLogLevelSystemError,
	"task-commit-failed":   JobLogLevelSystemError,
	"task-merge-failed":    JobLogLevelSystemError,
	"low-disk":             JobLogLevelSystemError,
	"adaptive-performance": JobLogLevelInformation,
}

// jobLogLevelByStopReason classifies the terminal "outcome" event, whose class
// lives in its recorded stop reason rather than in its type.
var jobLogLevelByStopReason = map[string]string{
	"runtime_limit":       JobLogLevelMaximumRuntime,
	"maximum_records":     JobLogLevelInformation,
	"fatal_error":         JobLogLevelSystemError,
	"low_disk":            JobLogLevelSystemError,
	"proxies_unavailable": JobLogLevelProxyFailure,
	"task_failures":       JobLogLevelWarning,
	"tasks_incomplete":    JobLogLevelWarning,
}

// jobLogRateLimitPhrases catch a refusal the worker recorded as free text.
// classifyTaskFailure collapses an HTTP 429 or an interstitial challenge into
// a generic task failure, so the phrase is the only surviving evidence that
// the run was rate limited rather than merely slow.
var jobLogRateLimitPhrases = []string{"rate limit", "rate-limit", "429", "captcha", "too many requests"}

// classifyJobLogLevel reduces one durable event to exactly one of the ten
// levels. Order matters: an explicit event type wins over a recorded stop
// reason, which wins over the free-text phrases, which win over the raw
// severity. Nothing here inspects anything but stored evidence.
func classifyJobLogLevel(event JobEvent) string {
	if level, known := jobLogLevelByEventType[strings.TrimSpace(event.Type)]; known {
		return level
	}

	fields := decodeJobEventContext(event.Context)
	if reason, ok := fields["reason"].(string); ok {
		if level, known := jobLogLevelByStopReason[reason]; known {
			return level
		}
	}

	message := strings.ToLower(event.Message)
	for _, phrase := range jobLogRateLimitPhrases {
		if strings.Contains(message, phrase) {
			return JobLogLevelRateLimit
		}
	}

	switch HonestJobEventSeverity(event.Type, event.Severity) {
	case JobEventSeverityError:
		return JobLogLevelSystemError
	case JobEventSeverityWarning:
		return JobLogLevelWarning
	default:
		return JobLogLevelInformation
	}
}

// jobLogTarget links one event to the thing it happened to, so an operator can
// go straight from an error line to the affected query, cell, or record.
// It returns an empty string when the event names nothing addressable, because
// a link that lands on an unfiltered list is worse than no link.
func jobLogTarget(jobID string, event JobEvent) string {
	fields := decodeJobEventContext(event.Context)

	if business, ok := stringField(fields, "business_id"); ok {
		return "/app/results/" + url.PathEscape(business)
	}

	if query, ok := stringField(fields, "query"); ok {
		return "/app/results?job_id=" + url.QueryEscape(jobID) + "&q=" + url.QueryEscape(query)
	}

	for _, key := range []string{"source_cell", "cell", "cell_id"} {
		if cell, ok := stringField(fields, key); ok {
			return "/app/map?mode=results&job_id=" + url.QueryEscape(jobID) + "&q=" + url.QueryEscape(cell)
		}
	}

	if _, ok := stringField(fields, "task_key"); ok {
		// A task has no page of its own, but the coverage table on this job's
		// monitor lists every task with its outcome.
		return "/app/jobs/" + url.PathEscape(jobID) + "#job-coverage"
	}

	return ""
}

func decodeJobEventContext(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}

	fields := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil
	}

	return fields
}

func stringField(fields map[string]any, key string) (string, bool) {
	value, ok := fields[key].(string)
	if !ok {
		return "", false
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	return value, true
}
