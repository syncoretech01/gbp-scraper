package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

const maximumResultQueryLength = 500

func (s *Server) registerResultRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/results", s.apiResults)
	mux.HandleFunc("GET /api/v1/results/{id}", s.apiBusiness)
	mux.HandleFunc("POST /api/v1/results/bulk", s.apiMutateBusinesses)
	mux.HandleFunc("POST /api/v1/jobs/{id}/results/import", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)
		s.apiImportJobResults(w, r)
	})
}

func (s *Server) apiMutateBusinesses(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if !s.requireCSRF(w, r) {
		return
	}
	mutation := ResultMutation{
		IDs:    r.Form["result_ids"],
		Action: r.FormValue("action"),
		Value:  r.FormValue("value"),
	}
	if mutation.Value == "" {
		mutation.Value = r.FormValue("tag")
	}
	changed, err := s.svc.MutateBusinesses(r.Context(), mutation)
	if err != nil {
		if errors.Is(err, ErrInvalidResultMutation) {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_result_mutation", err.Error())
		} else if errors.Is(err, ErrResultStoreUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "result_mutation_unavailable", "Result workflow updates are unavailable")
		} else {
			renderLocalAPIError(w, http.StatusInternalServerError, "result_mutation_failed", "Could not update the selected businesses")
		}
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{"changed": changed}})
		return
	}
	returnTo := safeResultsReturnPath(r.FormValue("return_to"))
	separator := "?"
	if strings.Contains(returnTo, "?") {
		separator = "&"
	}
	http.Redirect(w, r, returnTo+separator+"notice=Workflow+updated", http.StatusSeeOther)
}

func safeResultsReturnPath(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/app/results") || strings.HasPrefix(value, "//") ||
		strings.ContainsAny(value, "\r\n") {
		return "/app/results"
	}
	return value
}

func (s *Server) apiResults(w http.ResponseWriter, r *http.Request) {
	search, err := parseResultSearch(r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_result_query", err.Error())

		return
	}

	page, err := s.svc.SearchBusinesses(r.Context(), search)
	if err != nil {
		switch {
		case errors.Is(err, ErrResultStoreUnsupported):
			renderLocalAPIError(w, http.StatusNotImplemented, "result_store_unavailable", "Normalized result storage is unavailable")
		case errors.Is(err, ErrInvalidResultQuery):
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "result_query_failed", err.Error())
		default:
			renderLocalAPIError(w, http.StatusInternalServerError, "result_query_failed", "Could not query normalized local results")
		}

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: page.Results,
		Meta: map[string]any{
			"total":  page.Total,
			"limit":  page.Limit,
			"offset": page.Offset,
		},
	})
}

func (s *Server) apiBusiness(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_business_id", "Invalid business ID")

		return
	}

	detail, err := s.svc.GetBusiness(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrBusinessNotFound) {
			renderLocalAPIError(w, http.StatusNotFound, "business_not_found", "Business not found")
		} else if errors.Is(err, ErrResultStoreUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "result_store_unavailable", "Normalized result storage is unavailable")
		} else {
			renderLocalAPIError(w, http.StatusInternalServerError, "business_read_failed", "Could not load the local business record")
		}

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: detail})
}

func (s *Server) apiImportJobResults(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" && !s.requireCSRF(w, r) {
		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	result, err := s.svc.ImportJobResults(r.Context(), id.String())
	if err != nil {
		switch {
		case errors.Is(err, ErrPlacesNotFound):
			renderLocalAPIError(w, http.StatusNotFound, "results_not_found", "This job does not have a CSV result file")
		case errors.Is(err, ErrResultStoreUnsupported):
			renderLocalAPIError(w, http.StatusNotImplemented, "result_store_unavailable", "Normalized result storage is unavailable")
		default:
			renderLocalAPIError(w, http.StatusInternalServerError, "result_import_failed", "Could not import this job's local CSV")
		}

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: result})
}

func parseResultSearch(r *http.Request) (ResultSearch, error) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > maximumResultQueryLength {
		return ResultSearch{}, errors.New("search text is too long")
	}

	limit := positiveQueryInt(r.URL.Query().Get("page_size"), 25)
	limit = min(limit, 250)
	page := positiveQueryInt(r.URL.Query().Get("page"), 1)
	search := ResultSearch{
		Query:             query,
		JobID:             strings.TrimSpace(r.URL.Query().Get("job_id")),
		Sort:              strings.TrimSpace(r.URL.Query().Get("sort")),
		IncludeDuplicates: r.URL.Query().Get("include_duplicates") == "true",
		Limit:             limit,
		Offset:            (page - 1) * limit,
	}
	if len(search.JobID) > 128 {
		return ResultSearch{}, errors.New("source job ID is too long")
	}
	if len(search.Sort) > 64 {
		return ResultSearch{}, errors.New("sort value is too long")
	}

	fields := r.URL.Query()["filter_field"]
	operators := r.URL.Query()["filter_operator"]
	values := r.URL.Query()["filter_value"]
	if len(fields) != len(operators) || len(fields) != len(values) {
		return ResultSearch{}, errors.New("result filter rows are incomplete")
	}
	if len(fields) > 25 {
		return ResultSearch{}, errors.New("too many result filters")
	}
	for index := range fields {
		field := strings.TrimSpace(fields[index])
		operator := strings.TrimSpace(operators[index])
		value := strings.TrimSpace(values[index])
		if len(field) > 64 || len(operator) > 64 || len(value) > 1000 {
			return ResultSearch{}, errors.New("result filter value is too long")
		}
		if field == "" || operator == "" {
			continue
		}
		search.Filters = append(search.Filters, ResultFilter{
			Field:    field,
			Operator: operator,
			Value:    value,
		})
	}

	return search, nil
}

func validBusinessID(value string) bool {
	if len(value) < 5 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			continue
		}

		return false
	}

	return true
}

func resultPageNumber(search ResultSearch) int {
	if search.Limit <= 0 {
		return 1
	}

	return search.Offset/search.Limit + 1
}

func resultPageSizeLabel(search ResultSearch) string {
	return strconv.Itoa(search.Limit)
}
