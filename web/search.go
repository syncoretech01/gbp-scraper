package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultGlobalSearchLimit = 8
	maximumGlobalSearchLimit = 25
	maximumGlobalSearchQuery = 200
)

// ErrGlobalSearchUnsupported indicates that a repository cannot search the
// local workspace entities.
var ErrGlobalSearchUnsupported = errors.New("global workspace search is unavailable")

// GlobalSearchItem is one safe navigation result from the local workspace.
type GlobalSearchItem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	URL      string `json:"url"`
}

type globalSearchRepository interface {
	GlobalSearch(context.Context, string, int) ([]GlobalSearchItem, error)
}

// GlobalSearch searches businesses, jobs, tags, templates, saved views, and
// export history without accessing any remote service.
func (s *Service) GlobalSearch(ctx context.Context, query string, limit int) ([]GlobalSearchItem, error) {
	repository, ok := s.repo.(globalSearchRepository)
	if !ok {
		return nil, ErrGlobalSearchUnsupported
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 || utf8.RuneCountInString(query) > maximumGlobalSearchQuery {
		return []GlobalSearchItem{}, nil
	}
	if limit < 1 || limit > maximumGlobalSearchLimit {
		limit = defaultGlobalSearchLimit
	}

	return repository.GlobalSearch(ctx, query, limit)
}

func (s *Server) registerGlobalSearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/search", s.apiGlobalSearch)
}

func (s *Server) apiGlobalSearch(w http.ResponseWriter, r *http.Request) {
	limit := defaultGlobalSearchLimit
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maximumGlobalSearchLimit {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_search", "Search limit must be between 1 and 25")
			return
		}
		limit = parsed
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) < 2 || utf8.RuneCountInString(query) > maximumGlobalSearchQuery {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_search", "Search query must contain 2 to 200 characters")
		return
	}
	items, err := s.svc.GlobalSearch(r.Context(), query, limit)
	if err != nil {
		if errors.Is(err, ErrGlobalSearchUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "search_unavailable", "Global local search is unavailable")
			return
		}
		renderLocalAPIError(w, http.StatusInternalServerError, "search_failed", "Could not search the local workspace")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{"items": items}})
}
