package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// eventsHardCap bounds the total time the harness spends draining the SSE
	// event stream, regardless of idleness. Terminal jobs deliver their durable
	// events immediately, so this only guards against a stuck stream.
	eventsHardCap = 45 * time.Second
	// eventsIdleTimeout ends the drain once no new event has arrived for this
	// long. The server flushes all available events on connect, so a short idle
	// gap means the durable log has been fully replayed.
	eventsIdleTimeout = 4 * time.Second
	// eventsChannelBuffer sizes the parser-to-collector channel.
	eventsChannelBuffer = 256
)

// workerEvent is one durable lifecycle/worker event as delivered by the SSE
// event stream. Context is the raw JSON string the server stored under
// context_json; the harness decodes it only for the fields it needs.
type workerEvent struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"`
	Severity   string    `json:"severity"`
	Message    string    `json:"message"`
	Context    string    `json:"context_json"`
	OccurredAt time.Time `json:"occurred_at"`
}

// concurrencyContext decodes the fields the task-pool and adaptive-performance
// events carry inside context_json. Absent fields decode to zero.
type concurrencyContext struct {
	TaskWorkers          int `json:"task_workers"`
	PerTaskConcurrency   int `json:"per_task_concurrency"`
	DesiredConcurrency   int `json:"desired_concurrency"`
	EffectiveConcurrency int `json:"effective_concurrency"`
	PreviousConcurrency  int `json:"previous_concurrency"`
}

// concurrencyEvidence is the harness's reconstruction of how many simultaneous
// browser operations the run actually ran with. PlannedEffective is the budget
// the task pool started at; FinalEffective is the last value adaptive
// performance settled on. AdaptiveReductions counts the adaptation events that
// lowered the budget.
type concurrencyEvidence struct {
	Desired            int    `json:"desired"`
	PlannedWorkers     int    `json:"planned_workers"`
	PerTaskConcurrency int    `json:"per_task_concurrency"`
	PlannedEffective   int    `json:"planned_effective"`
	FinalEffective     int    `json:"final_effective"`
	AdaptiveReductions int    `json:"adaptive_reductions"`
	Source             string `json:"source"`
}

// taskPoolLogPattern matches the human task-pool log line, so effective
// concurrency can still be recovered from the plain-text log when the
// structured event context is unavailable.
var taskPoolLogPattern = regexp.MustCompile(`Running (\d+) task\(s\) in parallel with (\d+) worker concurrency each`)

// adaptiveLogPattern matches the adaptive-performance budget-change log line.
var adaptiveLogPattern = regexp.MustCompile(`changed the concurrency budget from (\d+) to (\d+)`)

// failureEventTypes are the worker event types that name a task failure cause.
// The harness counts them into the fine failure-kind breakdown. This set is a
// conservative default: any future kind the classification specialist emits is
// still counted, because failureKindsFromEvents also folds in every warning or
// error event whose type is not a known non-failure event.
var failureEventTypes = map[string]struct{}{
	"blocked":         {},
	"rate-limit":      {},
	"captcha":         {},
	"browser-failure": {},
	"proxy-failure":   {},
	"timeout":         {},
	"network-failure": {},
	"task-failure":    {},
}

// nonFailureEventTypes are worker event types that are operational information
// rather than a failure, so they are excluded from the failure-kind counts.
var nonFailureEventTypes = map[string]struct{}{
	"task-pool":            {},
	"adaptive-performance": {},
	"coverage-disabled":    {},
	"coverage-stop":        {},
	"coverage-expansion":   {},
	"coverage-refine":      {},
}

// Events drains the durable SSE event log for a job and returns the events in
// delivery order. It is bounded by eventsHardCap and stops early once the
// stream has been idle for eventsIdleTimeout, so it is safe to call against a
// still-open stream of a terminal job.
func (c *Client) Events(ctx context.Context, jobID string) ([]workerEvent, error) {
	streamCtx, cancel := context.WithTimeout(ctx, eventsHardCap)
	defer cancel()

	request, err := c.newRequest(streamCtx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/events?after=0", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")

	response, err := c.httpClient.Do(request)
	if err != nil {
		// A cancelled stream after we have what we need is not a failure here;
		// callers treat a nil slice with error as "events unavailable".
		return nil, fmt.Errorf("acceptance: open event stream: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotImplemented {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, &ErrAPIStatus{Method: http.MethodGet, Path: "/events", Status: response.StatusCode}
	}

	frames := make(chan workerEvent, eventsChannelBuffer)
	go parseSSE(streamCtx, response.Body, frames)

	events := make([]workerEvent, 0, eventsChannelBuffer)
	idle := time.NewTimer(eventsIdleTimeout)
	defer idle.Stop()

	for {
		select {
		case event, ok := <-frames:
			if !ok {
				return events, nil
			}
			events = append(events, event)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(eventsIdleTimeout)
		case <-idle.C:
			cancel()

			return events, nil
		case <-streamCtx.Done():
			return events, nil
		}
	}
}

// parseSSE reads Server-Sent Event frames from body and emits one workerEvent
// per frame that carries a JSON data payload. It closes frames when the stream
// ends or ctx is cancelled, and stops sending once ctx is done so it can never
// leak while blocked on a full channel. Comment lines (": heartbeat") and
// id/event fields are ignored; only the data payload is decoded, which already
// contains the event type.
func parseSSE(ctx context.Context, body io.Reader, frames chan<- workerEvent) {
	defer close(frames)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var data strings.Builder
	flush := func() bool {
		if data.Len() == 0 {
			return true
		}
		payload := data.String()
		data.Reset()
		var event workerEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil || event.Type == "" {
			return true
		}
		select {
		case frames <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if !flush() {
				return
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimPrefix(chunk, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(chunk)
		default:
			// id: / event: / other fields are not needed; the type is in data.
		}
	}
	flush()
}

// concurrencyFromEvents reconstructs the effective concurrency evidence from
// the worker events. It prefers the structured task-pool and
// adaptive-performance context; the caller supplies the plain-text log as a
// fallback for older builds that did not attach event context.
func concurrencyFromEvents(events []workerEvent, logText string) concurrencyEvidence {
	evidence := concurrencyEvidence{Source: "unavailable"}

	for _, event := range events {
		if event.Type != "task-pool" {
			continue
		}
		context, ok := decodeConcurrencyContext(event.Context)
		if !ok {
			continue
		}
		evidence.Desired = context.DesiredConcurrency
		evidence.PlannedWorkers = context.TaskWorkers
		evidence.PerTaskConcurrency = context.PerTaskConcurrency
		evidence.PlannedEffective = context.EffectiveConcurrency
		evidence.FinalEffective = context.EffectiveConcurrency
		evidence.Source = "worker-events"
	}

	for _, event := range events {
		if event.Type != "adaptive-performance" {
			continue
		}
		context, ok := decodeConcurrencyContext(event.Context)
		if !ok || context.EffectiveConcurrency == 0 {
			continue
		}
		if context.EffectiveConcurrency < context.PreviousConcurrency {
			evidence.AdaptiveReductions++
		}
		evidence.FinalEffective = context.EffectiveConcurrency
		evidence.Source = "worker-events"
	}

	if evidence.Source == "unavailable" {
		return concurrencyFromLog(logText)
	}

	return evidence
}

// decodeConcurrencyContext decodes the context_json string of a worker event.
func decodeConcurrencyContext(raw string) (concurrencyContext, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return concurrencyContext{}, false
	}
	var context concurrencyContext
	if err := json.Unmarshal([]byte(trimmed), &context); err != nil {
		return concurrencyContext{}, false
	}

	return context, true
}

// concurrencyFromLog recovers effective concurrency from the plain-text log
// lines when structured event context is not available.
func concurrencyFromLog(logText string) concurrencyEvidence {
	evidence := concurrencyEvidence{Source: "unavailable"}

	if match := taskPoolLogPattern.FindStringSubmatch(logText); match != nil {
		workers, _ := strconv.Atoi(match[1])
		perTask, _ := strconv.Atoi(match[2])
		evidence.PlannedWorkers = workers
		evidence.PerTaskConcurrency = perTask
		evidence.PlannedEffective = workers * perTask
		evidence.FinalEffective = workers * perTask
		evidence.Source = "log-messages"
	}

	previous := 0
	for _, match := range adaptiveLogPattern.FindAllStringSubmatch(logText, -1) {
		from, _ := strconv.Atoi(match[1])
		to, _ := strconv.Atoi(match[2])
		if to < from {
			evidence.AdaptiveReductions++
		}
		evidence.FinalEffective = to
		evidence.Source = "log-messages"
		previous = to
	}
	_ = previous

	return evidence
}

// failureKindsFromEvents counts the worker events that name a task failure
// cause, keyed by the event type. It counts any known failure type and any
// other warning/error event whose type is not a recognised non-failure event,
// so a new failure kind the classification specialist emits is still recorded
// without a code change here.
func failureKindsFromEvents(events []workerEvent) map[string]int64 {
	kinds := map[string]int64{}
	for _, event := range events {
		if event.Type == "" {
			continue
		}
		if _, known := failureEventTypes[event.Type]; known {
			kinds[event.Type]++
			continue
		}
		if _, skip := nonFailureEventTypes[event.Type]; skip {
			continue
		}
		severity := strings.ToLower(event.Severity)
		if severity == "warning" || severity == "error" {
			kinds[event.Type]++
		}
	}

	return kinds
}

// eventsByType totals every worker event keyed by type, mirroring the durable
// pipeline evidence a consumer would otherwise read from the application.
func eventsByType(events []workerEvent) map[string]int64 {
	counts := map[string]int64{}
	for _, event := range events {
		if event.Type == "" {
			continue
		}
		counts[event.Type]++
	}

	return counts
}
