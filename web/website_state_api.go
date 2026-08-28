package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maximumWebsiteStateJSONBytes = 64 << 10

// websiteAuditSweepInput is the JSON body of a sweep request. It mirrors
// WebsiteAuditSweepRequest minus the server-set RequestedBy.
type websiteAuditSweepInput struct {
	JobID   string            `json:"job_id"`
	States  []string          `json:"states"`
	Limit   int               `json:"limit"`
	Options EnrichmentOptions `json:"options"`
}

// registerWebsiteStateRoutes wires the canonical website-state surface: the
// state breakdown, the durable bulk audit of unchecked websites, the social
// reclassification backfill, and the per-business state and health reports.
//
// Reads use the same API-key auth as the other read-only endpoints; every
// mutation is CSRF-gated.
func (s *Server) registerWebsiteStateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/websites/states", s.apiWebsiteStateSummary)
	mux.HandleFunc("GET /api/v1/websites/audit-sweeps", s.apiWebsiteAuditSweeps)
	mux.HandleFunc("POST /api/v1/websites/audit-sweeps", s.apiStartWebsiteAuditSweep)
	mux.HandleFunc("GET /api/v1/websites/audit-sweeps/{id}", s.apiWebsiteAuditSweep)
	mux.HandleFunc("POST /api/v1/websites/social-backfill", s.apiBackfillSocialListings)
	mux.HandleFunc("GET /api/v1/results/{id}/website-state", s.apiBusinessWebsiteState)
	mux.HandleFunc("GET /api/v1/results/{id}/website-health", s.apiBusinessWebsiteHealth)
}

func (s *Server) apiWebsiteStateSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.svc.WebsiteStateSummary(r.Context(), r.URL.Query().Get("job_id"))
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: summary})
}

func (s *Server) apiWebsiteAuditSweeps(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_website_state_request",
				"Limit must be between 1 and 100")

			return
		}
		limit = value
	}
	sweeps, err := s.svc.RecentWebsiteAuditSweeps(r.Context(), limit)
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	if sweeps == nil {
		sweeps = []WebsiteAuditSweep{}
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: sweeps})
}

func (s *Server) apiWebsiteAuditSweep(w http.ResponseWriter, r *http.Request) {
	sweep, err := s.svc.WebsiteAuditSweepStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: sweep})
}

// apiStartWebsiteAuditSweep queues the durable "audit every never-checked
// website" run. It accepts the settings form encoding as well as JSON so the
// control can be a plain form post under the strict CSP.
func (s *Server) apiStartWebsiteAuditSweep(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	var input websiteAuditSweepInput
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if err := parseBoundedRequestForm(w, r, maximumWebsiteStateJSONBytes); err != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_website_state_request",
				"Request form could not be parsed")

			return
		}
		input.JobID = r.FormValue("job_id")
		for _, state := range strings.Split(r.FormValue("states"), ",") {
			if state = strings.TrimSpace(state); state != "" {
				input.States = append(input.States, state)
			}
		}
		if raw := strings.TrimSpace(r.FormValue("limit")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_website_state_request",
					"Limit must be a whole number")

				return
			}
			input.Limit = value
		}
	} else if err := decodeBoundedWebsiteStateJSON(w, r, &input); err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}

	sweep, err := s.svc.StartWebsiteAuditSweep(r.Context(), WebsiteAuditSweepRequest{
		JobID:       input.JobID,
		States:      input.States,
		Limit:       input.Limit,
		Options:     input.Options,
		RequestedBy: "local_api",
	})
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: sweep})
}

// apiBackfillSocialListings reclassifies stored listing URLs that are really
// social profiles. It defaults to a dry run so an operator can see the size
// of the correction before committing it.
func (s *Server) apiBackfillSocialListings(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	apply := false
	if raw := strings.TrimSpace(r.URL.Query().Get("apply")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_website_state_request",
				"apply must be true or false")

			return
		}
		apply = value
	}
	report, err := s.svc.BackfillSocialListings(r.Context(), apply, 0)
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
}

func (s *Server) apiBusinessWebsiteState(w http.ResponseWriter, r *http.Request) {
	resolution, err := s.svc.BusinessWebsiteState(r.Context(), r.PathValue("id"))
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: resolution})
}

func (s *Server) apiBusinessWebsiteHealth(w http.ResponseWriter, r *http.Request) {
	report, err := s.svc.BusinessWebsiteHealth(r.Context(), r.PathValue("id"))
	if err != nil {
		renderWebsiteStateAPIError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
}

func decodeBoundedWebsiteStateJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximumWebsiteStateJSONBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return errWebsiteStateRequest("request body is too large")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		// An empty body is the "sweep every never-checked website with the
		// default profile" request, which is exactly what the button sends.
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errWebsiteStateRequest("invalid JSON: " + err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errWebsiteStateRequest("request must contain one JSON document")
	}

	return nil
}

func errWebsiteStateRequest(detail string) error {
	return &websiteStateRequestError{detail: detail}
}

type websiteStateRequestError struct {
	detail string
}

func (e *websiteStateRequestError) Error() string {
	return ErrInvalidWebsiteStateRequest.Error() + ": " + e.detail
}

func (e *websiteStateRequestError) Unwrap() error {
	return ErrInvalidWebsiteStateRequest
}

func renderWebsiteStateAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidWebsiteStateRequest), errors.Is(err, ErrInvalidEnrichment):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_website_state_request", err.Error())
	case errors.Is(err, ErrWebsiteAuditSweepNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "website_audit_sweep_not_found",
			"That website audit sweep does not exist")
	case errors.Is(err, ErrBusinessNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "business_not_found", "That business does not exist")
	case errors.Is(err, ErrWebsiteStateUnsupported), errors.Is(err, ErrEnrichmentUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "website_state_unavailable",
			"Website state tracking is unavailable")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "website_state_failed",
			"Could not process the website state request")
	}
}
