package web

import (
	"errors"
	"net/http"

	"github.com/gosom/google-maps-scraper/grid"
)

type localAPIEnvelope struct {
	Data  any            `json:"data,omitempty"`
	Error *localAPIError `json:"error,omitempty"`
	Meta  any            `json:"meta,omitempty"`
}

type localAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type jobConfigSummary struct {
	Keywords            []string `json:"keywords"`
	Language            string   `json:"language"`
	Latitude            string   `json:"latitude,omitempty"`
	Longitude           string   `json:"longitude,omitempty"`
	Zoom                int      `json:"zoom"`
	RadiusMetres        int      `json:"radius_metres,omitempty"`
	Depth               int      `json:"depth"`
	FastMode            bool     `json:"fast_mode"`
	EmailCrawl          bool     `json:"email_crawl"`
	RuntimeLimitSeconds int64    `json:"runtime_limit_seconds"`
	GridBoundingBox     string   `json:"grid_bounding_box,omitempty"`
	GridCellKM          float64  `json:"grid_cell_km,omitempty"`
	EstimatedGridCells  int      `json:"estimated_grid_cells,omitempty"`
	EstimatedSeedTasks  int      `json:"estimated_seed_tasks"`
}

type jobProgressDTO struct {
	JobID        string                `json:"job_id"`
	Name         string                `json:"name"`
	State        string                `json:"state"`
	LegacyStatus string                `json:"legacy_status"`
	Stage        string                `json:"stage"`
	Percent      float64               `json:"percent"`
	Message      string                `json:"message,omitempty"`
	StopReason   string                `json:"stop_reason,omitempty"`
	Config       jobConfigSummary      `json:"config"`
	Results      ResultStats           `json:"results"`
	Execution    *JobExecutionSnapshot `json:"execution,omitempty"`
	Warnings     []string              `json:"warnings"`
}

type dashboardDTO struct {
	Jobs             int            `json:"jobs"`
	JobStates        map[string]int `json:"job_states"`
	RawRecords       int            `json:"raw_records"`
	UniqueBusinesses int            `json:"unique_businesses"`
	Duplicates       int            `json:"duplicates"`
	WithWebsite      int            `json:"with_website"`
	WithPhone        int            `json:"with_phone"`
	WithEmail        int            `json:"with_email"`
}

// sanitizedJobForAPI preserves the legacy job response shape while removing
// inline proxy URLs. Older jobs may contain credentialed proxy URLs in their
// compatible configuration snapshot; those values must never leave the
// process through a job API response.
func sanitizedJobForAPI(job Job) Job {
	job.Data.Proxies = []string{}

	return job
}

func sanitizedJobsForAPI(jobs []Job) []Job {
	result := make([]Job, len(jobs))
	for index := range jobs {
		result[index] = sanitizedJobForAPI(jobs[index])
	}

	return result
}

func (s *Server) apiJobProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		renderLocalAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	job, err := s.svc.Get(r.Context(), id.String())
	if err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

		return
	}

	stats, err := s.svc.GetResultStats(r.Context(), id.String())
	if err != nil && !errors.Is(err, ErrPlacesNotFound) {
		renderLocalAPIError(w, http.StatusInternalServerError, "result_summary_failed", "Could not summarize local results")

		return
	}

	runtime, err := s.svc.GetRuntime(r.Context(), id.String())
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "runtime_failed", "Could not load job runtime")

		return
	}

	dto := newJobProgressDTO(job, stats, runtime)
	if execution, executionErr := s.svc.GetJobExecution(r.Context(), id.String()); executionErr == nil {
		dto.Execution = &execution
	} else if !errors.Is(executionErr, ErrCheckpointUnsupported) {
		renderLocalAPIError(w, http.StatusInternalServerError, "checkpoint_failed", "Could not load job checkpoint evidence")

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: dto})
}

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		renderLocalAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")

		return
	}

	jobs, err := s.svc.All(r.Context())
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "jobs_failed", "Could not load local jobs")

		return
	}

	dashboard := dashboardDTO{
		Jobs:      len(jobs),
		JobStates: make(map[string]int),
	}

	for _, job := range jobs {
		runtime, err := s.svc.GetRuntime(r.Context(), job.ID)
		if err != nil {
			renderLocalAPIError(w, http.StatusInternalServerError, "runtime_failed", "Could not load job runtime")

			return
		}

		dashboard.JobStates[string(runtime.State)]++

		stats, err := s.svc.GetResultStats(r.Context(), job.ID)
		if errors.Is(err, ErrPlacesNotFound) {
			continue
		}

		if err != nil {
			renderLocalAPIError(w, http.StatusInternalServerError, "result_summary_failed", "Could not summarize local results")

			return
		}

		dashboard.RawRecords += stats.Rows
		dashboard.UniqueBusinesses += stats.UniqueBusinesses
		dashboard.Duplicates += stats.Duplicates
		dashboard.WithWebsite += stats.WithWebsite
		dashboard.WithPhone += stats.WithPhone
		dashboard.WithEmail += stats.WithEmail
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: dashboard})
}

func newJobProgressDTO(job Job, stats ResultStats, runtime JobRuntime) jobProgressDTO {
	gridCells := 0
	if job.Data.GridBBox != "" {
		if bbox, err := grid.ParseBoundingBox(job.Data.GridBBox); err == nil {
			gridCells = grid.EstimateCellCount(bbox, job.Data.GridCellKM)
		}
	}

	seedTasks := len(job.Data.Keywords)
	if gridCells > 0 {
		seedTasks *= gridCells
	}

	warnings := []string{}
	if job.Data.Email && job.Data.MaxTime.Minutes() <= 15 {
		warnings = append(warnings, "Website and email crawling can consume most of a short runtime; use a longer limit or collect Maps results first.")
	}

	if !job.Data.FastMode && job.Data.GridBBox == "" && job.Data.Radius > 0 {
		warnings = append(warnings, "Radius is only a strict distance filter in Fast Mode; use grid coverage for a thorough city search.")
	}

	if job.Data.FastMode {
		warnings = append(warnings, "Fast Mode returns a small distance-sorted sample per query and is not intended for exhaustive coverage.")
	}

	return jobProgressDTO{
		JobID:        job.ID,
		Name:         job.Name,
		State:        string(runtime.State),
		LegacyStatus: job.Status,
		Stage:        string(runtime.Stage),
		Percent:      runtime.Progress,
		Message:      runtime.Message,
		StopReason:   string(runtime.OutcomeReason),
		Config: jobConfigSummary{
			Keywords:            job.Data.Keywords,
			Language:            job.Data.Lang,
			Latitude:            job.Data.Lat,
			Longitude:           job.Data.Lon,
			Zoom:                job.Data.Zoom,
			RadiusMetres:        job.Data.Radius,
			Depth:               job.Data.Depth,
			FastMode:            job.Data.FastMode,
			EmailCrawl:          job.Data.Email,
			RuntimeLimitSeconds: int64(job.Data.MaxTime.Seconds()),
			GridBoundingBox:     job.Data.GridBBox,
			GridCellKM:          job.Data.GridCellKM,
			EstimatedGridCells:  gridCells,
			EstimatedSeedTasks:  seedTasks,
		},
		Results:  stats,
		Warnings: warnings,
	}
}

func legacyState(status string) string {
	switch status {
	case StatusPending:
		return "queued"
	case StatusWorking:
		return "running"
	case StatusOK:
		return "completed"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func renderLocalAPIError(w http.ResponseWriter, status int, code, message string) {
	renderJSON(w, status, localAPIEnvelope{Error: &localAPIError{Code: code, Message: message}})
}
