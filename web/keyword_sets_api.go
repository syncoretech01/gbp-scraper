package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maximumKeywordSetRequestBytes = 256 << 10

func (s *Server) registerKeywordSetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/keyword-sets", s.apiListKeywordSets)
	mux.HandleFunc("POST /api/v1/keyword-sets", s.apiSaveKeywordSet)
	mux.HandleFunc("POST /api/v1/keyword-sets/{id}/delete", s.apiDeleteKeywordSet)
	mux.HandleFunc("POST /api/v1/keyword-sets/{id}/use", s.apiUseKeywordSet)
}

func (s *Server) apiListKeywordSets(w http.ResponseWriter, r *http.Request) {
	sets, err := s.svc.ListKeywordSets(r.Context())
	if err != nil {
		renderKeywordSetAPIError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: sets})
}

func (s *Server) apiSaveKeywordSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	set, err := decodeKeywordSetSave(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_keyword_set", err.Error())

		return
	}

	saved, err := s.svc.SaveKeywordSet(r.Context(), set)
	if err != nil {
		renderKeywordSetAPIError(w, err)

		return
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") &&
		!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Redirect(w, r, "/app/scrapes/new?notice=Keyword+set+saved", http.StatusSeeOther)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: saved})
}

func (s *Server) apiDeleteKeywordSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	if err := s.svc.DeleteKeywordSet(r.Context(), strings.TrimSpace(r.PathValue("id"))); err != nil {
		renderKeywordSetAPIError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Keyword set deleted"}})
}

func (s *Server) apiUseKeywordSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	set, err := s.svc.UseKeywordSet(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		renderKeywordSetAPIError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: set})
}

// decodeKeywordSetSave accepts the wizard's JSON body or a plain HTML form
// whose keywords textarea carries one query per line.
func decodeKeywordSetSave(w http.ResponseWriter, r *http.Request) (KeywordSet, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumKeywordSetRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var request struct {
			Name        string   `json:"name"`
			Description string   `json:"description,omitempty"`
			Keywords    []string `json:"keywords"`
		}

		if err := decoder.Decode(&request); err != nil {
			return KeywordSet{}, fmt.Errorf("invalid JSON")
		}

		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return KeywordSet{}, fmt.Errorf("request must contain exactly one JSON object")
		}

		return KeywordSet{
			Name:        request.Name,
			Description: request.Description,
			Keywords:    request.Keywords,
		}, nil
	}

	if err := parseBoundedRequestForm(w, r, maximumKeywordSetRequestBytes); err != nil {
		return KeywordSet{}, fmt.Errorf("invalid form")
	}

	return KeywordSet{
		Name:        r.FormValue("name"),
		Description: r.FormValue("description"),
		Keywords:    strings.Split(strings.ReplaceAll(r.FormValue("keywords"), "\r\n", "\n"), "\n"),
	}, nil
}

func renderKeywordSetAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrKeywordSetsUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "keyword_sets_unavailable", "Keyword sets are unavailable")
	case errors.Is(err, ErrKeywordSetNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "keyword_set_not_found", "Keyword set was not found")
	case errors.Is(err, ErrInvalidKeywordSet):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_keyword_set", err.Error())
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "keyword_set_failed", "Could not process the keyword set")
	}
}
