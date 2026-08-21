package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	jobEventPollInterval = 2 * time.Second
	jobEventHeartbeat    = 15 * time.Second
	jobEventBatchSize    = 500
)

func (s *Server) apiJobEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		renderLocalAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	after, err := eventCursor(r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_event_cursor", err.Error())

		return
	}

	if _, ok := w.(http.Flusher); !ok {
		renderLocalAPIError(w, http.StatusInternalServerError, "streaming_unavailable", "Streaming is unavailable")

		return
	}

	runtime, err := s.svc.GetRuntime(r.Context(), id.String())
	if err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

		return
	}

	if _, ok := s.svc.repo.(LifecycleRepository); !ok {
		renderLocalAPIError(w, http.StatusNotImplemented, "lifecycle_unavailable", "Event replay requires the upgraded local database")

		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	if err := writeSSE(w, 0, "snapshot", runtime); err != nil {
		return
	}
	w.(http.Flusher).Flush()

	poll := time.NewTicker(jobEventPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(jobEventHeartbeat)
	defer heartbeat.Stop()

	for {
		if err := s.writeAvailableEvents(r, w, id.String(), &after); err != nil {
			if errors.Is(err, r.Context().Err()) {
				return
			}

			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}
}

// jobEventDTO is the streamed shape of one lifecycle event. It embeds JobEvent
// so every historical field keeps its name and position, and adds the two
// values the console cannot derive on its own: the operator-facing log level
// and the link to whatever the event happened to. Classification stays on the
// server so the stream, the rendered page, and the downloaded log file can
// never disagree about what a line is.
type jobEventDTO struct {
	JobEvent

	Level     string `json:"level"`
	TargetURL string `json:"target_url,omitempty"`
}

func newJobEventDTO(jobID string, event JobEvent) jobEventDTO {
	return jobEventDTO{
		JobEvent:  event,
		Level:     classifyJobLogLevel(event),
		TargetURL: jobLogTarget(jobID, event),
	}
}

func (s *Server) writeAvailableEvents(r *http.Request, w http.ResponseWriter, jobID string, after *int64) error {
	events, err := s.svc.EventsAfter(r.Context(), jobID, *after, jobEventBatchSize)
	if err != nil {
		return err
	}

	for _, event := range events {
		if event.ID <= *after {
			continue
		}

		if err := writeSSE(w, event.ID, event.Type, newJobEventDTO(jobID, event)); err != nil {
			return err
		}

		*after = event.ID
	}

	if len(events) > 0 {
		w.(http.Flusher).Flush()
	}

	return nil
}

func eventCursor(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.URL.Query().Get("after"))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}

	if value == "" {
		return 0, nil
	}

	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, fmt.Errorf("event cursor must be a non-negative integer")
	}

	return cursor, nil
}

func writeSSE(w http.ResponseWriter, id int64, eventType string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	if id > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}

	if eventType != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
			return err
		}
	}

	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)

	return err
}
