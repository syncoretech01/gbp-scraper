package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// registerBenchmarkRoutes exposes the read-only production benchmark report
// and the two-run comparison. Both endpoints share the standard local-API
// authentication; neither mutates stored data.
func (s *Server) registerBenchmarkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs/{id}/benchmark", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)
		id, ok := getIDFromRequest(r)
		if !ok {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

			return
		}
		report, ok := s.loadBenchmarkReport(w, r, id.String())
		if !ok {
			return
		}

		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
	})

	mux.HandleFunc("POST /api/v1/jobs/{id}/benchmark/snapshot", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)
		id, ok := getIDFromRequest(r)
		if !ok {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

			return
		}
		if !s.requireCSRF(w, r) {
			return
		}
		if job, err := s.svc.Get(r.Context(), id.String()); err != nil || job.ID == "" {
			renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

			return
		}
		snapshot, err := s.svc.CaptureJobBenchmark(r.Context(), id.String())
		if err != nil {
			s.renderBenchmarkError(w, err)

			return
		}

		renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: snapshot})
	})

	mux.HandleFunc("GET /api/v1/benchmark/history", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > MaximumBenchmarkHistoryLimit {
				renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit",
					"Limit must be a whole number between 1 and 200")

				return
			}
			limit = parsed
		}
		snapshots, err := s.svc.BenchmarkHistory(r.Context(), limit)
		if err != nil {
			s.renderBenchmarkError(w, err)

			return
		}

		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: snapshots})
	})

	mux.HandleFunc("GET /api/v1/benchmark/compare", func(w http.ResponseWriter, r *http.Request) {
		// A campaign or an explicit run list returns a chartable series;
		// the historical base/candidate pair keeps its exact response shape.
		if s.renderBenchmarkSeries(w, r) {
			return
		}
		baseID, err := uuid.Parse(r.URL.Query().Get("base"))
		if err != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_base_id", "Invalid base job ID")

			return
		}
		candidateID, err := uuid.Parse(r.URL.Query().Get("candidate"))
		if err != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_candidate_id", "Invalid candidate job ID")

			return
		}

		for _, jobID := range []string{baseID.String(), candidateID.String()} {
			if job, err := s.svc.Get(r.Context(), jobID); err != nil || job.ID == "" {
				renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

				return
			}
		}
		comparison, err := s.svc.CompareJobBenchmarks(r.Context(), baseID.String(), candidateID.String())
		if err != nil {
			s.renderBenchmarkError(w, err)

			return
		}

		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: comparison})
	})
}

// loadBenchmarkReport resolves one job's report, writing the API error and
// returning ok=false when the job is missing or evidence is unavailable.
func (s *Server) loadBenchmarkReport(w http.ResponseWriter, r *http.Request, jobID string) (BenchmarkReport, bool) {
	if job, err := s.svc.Get(r.Context(), jobID); err != nil || job.ID == "" {
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

		return BenchmarkReport{}, false
	}
	report, err := s.svc.JobBenchmark(r.Context(), jobID)
	if err != nil {
		s.renderBenchmarkError(w, err)

		return BenchmarkReport{}, false
	}

	return report, true
}

// renderBenchmarkSeries answers GET /api/v1/benchmark/compare when it asks
// for a campaign or a run list rather than the historical base/candidate
// pair. It reports whether it handled the request, so the original two-run
// comparison keeps its exact response shape.
func (s *Server) renderBenchmarkSeries(w http.ResponseWriter, r *http.Request) bool {
	query := r.URL.Query()

	campaign := strings.TrimSpace(query.Get("campaign"))
	jobs := strings.TrimSpace(query.Get("jobs"))

	if campaign == "" && jobs == "" {
		return false
	}

	request := BenchmarkSeriesRequest{CampaignID: campaign}

	for _, value := range strings.Split(jobs, ",") {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			request.JobIDs = append(request.JobIDs, trimmed)
		}
	}

	series, err := s.svc.CompareJobBenchmarkSeries(r.Context(), request)
	if err != nil {
		if errors.Is(err, ErrInvalidBenchmarkSeries) {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_benchmark_series", err.Error())

			return true
		}

		s.renderBenchmarkError(w, err)

		return true
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: series})

	return true
}

func (s *Server) renderBenchmarkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBenchmarkHistoryUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "benchmark_history_unavailable",
			"Benchmark history requires the upgraded local database")
	case errors.Is(err, ErrBenchmarkUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "benchmark_unavailable", "Benchmark evidence is unavailable")
	case errors.Is(err, ErrLifecycleNotFound), errors.Is(err, ErrNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "benchmark_failed", "Could not assemble the benchmark report")
	}
}
