package web

import (
	"errors"
	"net/http"

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

	mux.HandleFunc("GET /api/v1/benchmark/compare", func(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) renderBenchmarkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBenchmarkUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "benchmark_unavailable", "Benchmark evidence is unavailable")
	case errors.Is(err, ErrLifecycleNotFound), errors.Is(err, ErrNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "benchmark_failed", "Could not assemble the benchmark report")
	}
}
