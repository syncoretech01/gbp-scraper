package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// A saved list is the static counterpart to a saved view: the operator picks
// rows by hand instead of describing them with filters. It is stored as a tag
// on each selected business plus one saved view pinned to that tag, so a list
// needs no new table, survives restarts, and is filterable, exportable, and
// mappable through the machinery that already exists.

const (
	// MaximumResultListNameLength bounds a saved list name so the chip, the
	// tag, and the saved view all stay readable.
	MaximumResultListNameLength = 64
	// resultListViewPrefix distinguishes the saved view that backs a list from
	// a view the operator wrote by hand.
	resultListViewPrefix = "List: "
)

// ErrInvalidResultList identifies a rejected saved-list request.
var ErrInvalidResultList = errors.New("invalid saved list")

// ResultListResult reports what one "add to list" action changed.
type ResultListResult struct {
	List      string `json:"list"`
	Added     int64  `json:"added"`
	ViewID    string `json:"view_id,omitempty"`
	ViewSaved bool   `json:"view_saved"`
}

// AddBusinessesToList tags every selected business with the list name and makes
// sure a saved view pinned to that tag exists, so the list is immediately
// reachable from the Results toolbar.
func (s *Service) AddBusinessesToList(
	ctx context.Context,
	ids []string,
	name string,
) (ResultListResult, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaximumResultListNameLength {
		return ResultListResult{}, fmt.Errorf(
			"%w: a list name of 1 to %d characters is required", ErrInvalidResultList, MaximumResultListNameLength,
		)
	}

	if strings.ContainsFunc(name, unicode.IsControl) {
		return ResultListResult{}, fmt.Errorf("%w: the list name contains control characters", ErrInvalidResultList)
	}

	added, err := s.MutateBusinesses(ctx, ResultMutation{IDs: ids, Action: "tag", Value: name})
	if err != nil {
		return ResultListResult{}, err
	}

	result := ResultListResult{List: name, Added: added}

	viewID, saved, err := s.ensureResultListView(ctx, name)
	if err != nil {
		return result, err
	}

	result.ViewID = viewID
	result.ViewSaved = saved

	return result, nil
}

// ensureResultListView creates the saved view that opens a list, unless one
// already exists. A workspace without saved-view storage still gets the tag,
// which is what actually holds the list together.
func (s *Service) ensureResultListView(ctx context.Context, name string) (string, bool, error) {
	views, err := s.ListSavedResultViews(ctx, "")
	if err != nil {
		if errors.Is(err, ErrReusableStoreUnsupported) {
			return "", false, nil
		}

		return "", false, err
	}

	viewName := resultListViewPrefix + name
	for _, view := range views {
		if strings.EqualFold(view.Name, viewName) {
			return view.ID, false, nil
		}
	}

	now := time.Now().UTC()
	view := SavedResultView{
		ID:   uuid.NewString(),
		Name: viewName,
		Search: ResultSearch{
			Sort:    "updated_desc",
			Limit:   defaultResultListPageSize,
			Filters: []ResultFilter{{Field: "tags", Operator: "eq", Value: name}},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.SaveResultView(ctx, view); err != nil {
		return "", false, err
	}

	return view.ID, true, nil
}

// defaultResultListPageSize matches the Results table's own default page size.
const defaultResultListPageSize = 25

// registerResultListRoutes exposes the saved-list bulk action.
func (s *Server) registerResultListRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/results/lists", s.apiAddBusinessesToList)
}

// resultListAvailable reports whether both halves of a saved list can be
// stored: the tag and the view that opens it.
func (s *Server) resultListAvailable() bool {
	return s != nil && s.resultMutationAvailable() && s.reusableAvailable()
}

func (s *Server) apiAddBusinessesToList(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maximumResultListRequestBytes)
	if !s.requireCSRF(w, r) {
		return
	}

	if !s.resultListAvailable() {
		renderLocalAPIError(w, http.StatusNotImplemented, "lists_unavailable", "Saved lists are unavailable")

		return
	}

	result, err := s.svc.AddBusinessesToList(r.Context(), r.Form["result_ids"], r.FormValue("list"))
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidResultList), errors.Is(err, ErrInvalidResultMutation):
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_list", err.Error())
		case errors.Is(err, ErrResultStoreUnsupported), errors.Is(err, ErrReusableStoreUnsupported):
			renderLocalAPIError(w, http.StatusNotImplemented, "lists_unavailable", "Saved lists are unavailable")
		default:
			renderLocalAPIError(w, http.StatusInternalServerError, "list_failed", "Could not add the selection to a list")
		}

		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: result})

		return
	}

	returnTo := safeResultsReturnPath(r.FormValue("return_to"))
	separator := "?"

	if strings.Contains(returnTo, "?") {
		separator = "&"
	}

	http.Redirect(w, r, returnTo+separator+"notice=Added+to+list", http.StatusSeeOther)
}

// maximumResultListRequestBytes bounds the bulk selection a list request may
// carry, matching the results bulk endpoint.
const maximumResultListRequestBytes = 64 << 10
