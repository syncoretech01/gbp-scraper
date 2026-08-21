package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ErrTemplateMetricsUnsupported indicates the repository cannot report a
// template's run history.
var ErrTemplateMetricsUnsupported = errors.New("template run metrics are unavailable")

// ScrapeTemplateMetrics is one saved template's run history, derived from the
// jobs that recorded the template in their configuration.
type ScrapeTemplateMetrics struct {
	TemplateID string `json:"template_id"`
	// RunCount is how many jobs were created from the template.
	RunCount int64 `json:"run_count"`
	// TimedRunCount is how many of those runs both started and finished, and
	// therefore contributed to AverageDuration.
	TimedRunCount int64 `json:"timed_run_count"`
	// AverageResults is the mean number of distinct businesses one run
	// linked to itself.
	AverageResults float64 `json:"average_results"`
	// AverageDuration is the mean wall time of the timed runs. It is
	// serialized as a nanosecond integer, matching every other duration in
	// this API.
	AverageDuration time.Duration `json:"average_duration"`
	// LastRunAt is when the most recent job was created from the template.
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
}

type templateMetricsRepository interface {
	ScrapeTemplateMetrics(context.Context, string) (ScrapeTemplateMetrics, error)
}

// SupportsTemplateMetrics reports whether run history can be derived.
func (s *Service) SupportsTemplateMetrics() bool {
	_, ok := s.repo.(templateMetricsRepository)

	return ok
}

// ScrapeTemplateMetricsFor returns one template's run history. A template
// that has never run reports zeroes, which is a valid answer rather than an
// error.
func (s *Service) ScrapeTemplateMetricsFor(ctx context.Context, id string) (ScrapeTemplateMetrics, error) {
	repository, ok := s.repo.(templateMetricsRepository)
	if !ok {
		return ScrapeTemplateMetrics{}, ErrTemplateMetricsUnsupported
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ScrapeTemplateMetrics{}, ErrReusableNotFound
	}
	// Confirming the template exists is what separates "never run" from
	// "no such template", which the API reports differently.
	if _, err := s.GetScrapeTemplate(ctx, id); err != nil {
		return ScrapeTemplateMetrics{}, err
	}

	return repository.ScrapeTemplateMetrics(ctx, id)
}

// registerTemplateMetricRoutes exposes one saved template's run history and
// the expansion preview of a parameterised template.
func (s *Server) registerTemplateMetricRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates/{id}/metrics", s.apiScrapeTemplateMetrics)
	mux.HandleFunc("POST /api/v1/templates/parameters/preview", s.apiPreviewTemplateParameters)
}

func (s *Server) apiScrapeTemplateMetrics(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	metrics, err := s.svc.ScrapeTemplateMetricsFor(r.Context(), id)
	switch {
	case errors.Is(err, ErrReusableNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "template_not_found", "No such scrape template")

		return
	case errors.Is(err, ErrTemplateMetricsUnsupported), errors.Is(err, ErrReusableStoreUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "template_metrics_unavailable",
			"Template run history needs the upgraded local database")

		return
	case err != nil:
		renderLocalAPIError(w, http.StatusInternalServerError, "template_metrics_failed",
			"Could not read the template's run history")

		return
	}

	template, err := s.svc.GetScrapeTemplate(r.Context(), id)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "template_metrics_failed",
			"Could not read the template's run history")

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: metrics,
		Meta: map[string]any{
			"name":      template.Name,
			"use_count": template.UseCount,
			"parameterised": template.Configuration.Parameters != nil &&
				!template.Configuration.Parameters.Empty(),
		},
	})
}

type templateParameterPreviewInput struct {
	Categories []string `json:"categories"`
	Locations  []string `json:"locations"`
	Pattern    string   `json:"query_pattern,omitempty"`
}

func (s *Server) apiPreviewTemplateParameters(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	var input templateParameterPreviewInput
	if err := decodeScrapePlanJSON(w, r, &input); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_template_parameters", err.Error())

		return
	}

	parameters := JobParameters{Categories: input.Categories, Locations: input.Locations, Pattern: input.Pattern}
	queries, err := parameters.ExpandQueries()
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_template_parameters", err.Error())

		return
	}

	// The preview is bounded so a 5,000-line expansion cannot be shipped to
	// the browser; the count is always the complete figure.
	const previewLimit = 100
	preview := queries
	if len(preview) > previewLimit {
		preview = preview[:previewLimit]
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: preview,
		Meta: map[string]any{"count": len(queries), "truncated": len(queries) > len(preview)},
	})
}
