package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maximumDuplicateRequestBytes = 16 << 10

func (s *Server) registerDuplicateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/results/duplicates", s.apiListDuplicateCandidates)
	mux.HandleFunc("POST /api/v1/results/duplicates/merge", s.apiResolveDuplicate("merge"))
	mux.HandleFunc("POST /api/v1/results/duplicates/keep-both", s.apiResolveDuplicate("keep_both"))
	mux.HandleFunc("POST /api/v1/results/duplicates/ignore", s.apiResolveDuplicate("ignore"))
}

func (s *Server) apiListDuplicateCandidates(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit", "Limit must be between 1 and 200")

			return
		}

		limit = value
	}

	pairs, err := s.svc.ListDuplicateCandidates(r.Context(), limit)
	if err != nil {
		renderDuplicateAPIError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: pairs})
}

// apiResolveDuplicate builds the handler for one decision. The action comes from
// the route rather than the request body so a mistyped body cannot merge records.
func (s *Server) apiResolveDuplicate(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireCSRF(w, r) {
			return
		}

		decision, err := decodeDuplicateDecision(w, r)
		if err != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_duplicate_decision", err.Error())

			return
		}

		decision.Action = action

		resolution, err := s.svc.ResolveDuplicateCandidate(r.Context(), decision)
		if err != nil {
			renderDuplicateAPIError(w, err)

			return
		}

		if !strings.Contains(r.Header.Get("Accept"), "application/json") &&
			!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			http.Redirect(w, r, "/app/results?notice=Duplicate+decision+saved", http.StatusSeeOther)

			return
		}

		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: resolution})
	}
}

func decodeDuplicateDecision(w http.ResponseWriter, r *http.Request) (DuplicateDecision, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumDuplicateRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var decision DuplicateDecision
		if err := decoder.Decode(&decision); err != nil {
			return DuplicateDecision{}, fmt.Errorf("invalid JSON")
		}

		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return DuplicateDecision{}, fmt.Errorf("request must contain exactly one JSON object")
		}

		return decision, nil
	}

	if err := parseBoundedRequestForm(w, r, maximumDuplicateRequestBytes); err != nil {
		return DuplicateDecision{}, fmt.Errorf("invalid form")
	}

	candidateID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("candidate_id")), 10, 64)
	if err != nil || candidateID <= 0 {
		return DuplicateDecision{}, fmt.Errorf("a valid candidate is required")
	}

	return DuplicateDecision{
		CandidateID:    candidateID,
		KeepBusinessID: strings.TrimSpace(r.FormValue("keep_business_id")),
		FieldStrategy:  strings.TrimSpace(r.FormValue("field_strategy")),
		Note:           strings.TrimSpace(r.FormValue("note")),
	}, nil
}

func renderDuplicateAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDuplicateReviewUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "duplicates_unavailable", "Duplicate review is unavailable")
	case errors.Is(err, ErrDuplicateCandidateNotFound), errors.Is(err, ErrBusinessNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "duplicate_not_found", "Duplicate candidate was not found")
	case errors.Is(err, ErrDuplicateAlreadyResolved):
		renderLocalAPIError(w, http.StatusConflict, "duplicate_resolved", "This duplicate was already reviewed")
	case errors.Is(err, ErrInvalidDuplicateDecision):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_duplicate_decision", err.Error())
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "duplicate_failed", "Could not save the duplicate decision")
	}
}
