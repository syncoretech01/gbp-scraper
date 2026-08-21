package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A saved list is a hand-picked selection rather than a query. It is stored as
// a tag plus one saved view pinned to that tag, so these tests check both
// halves land and that the bulk action is hidden when either half cannot be
// stored.

type listCapableRepository struct {
	*fixedResultRepository
	mutations []ResultMutation
	views     map[string]SavedResultView
}

func newListCapableRepository() *listCapableRepository {
	return &listCapableRepository{
		fixedResultRepository: &fixedResultRepository{fixedJobRepository: &fixedJobRepository{}},
		views:                 map[string]SavedResultView{},
	}
}

func (r *listCapableRepository) MutateBusinesses(
	_ context.Context,
	mutation ResultMutation,
) (int64, error) {
	r.mutations = append(r.mutations, mutation)

	return int64(len(mutation.IDs)), nil
}

func (r *listCapableRepository) ListScrapeTemplates(context.Context, string) ([]ScrapeTemplate, error) {
	return nil, nil
}

func (r *listCapableRepository) GetScrapeTemplate(context.Context, string) (ScrapeTemplate, error) {
	return ScrapeTemplate{}, ErrReusableNotFound
}

func (r *listCapableRepository) SaveScrapeTemplate(context.Context, ScrapeTemplate) error { return nil }
func (r *listCapableRepository) DeleteScrapeTemplate(context.Context, string) error       { return nil }
func (r *listCapableRepository) SetScrapeTemplatePinned(context.Context, string, bool) error {
	return nil
}

func (r *listCapableRepository) RecordScrapeTemplateUse(context.Context, string, time.Time) error {
	return nil
}

func (r *listCapableRepository) ListSavedResultViews(context.Context, string) ([]SavedResultView, error) {
	views := make([]SavedResultView, 0, len(r.views))
	for _, view := range r.views {
		views = append(views, view)
	}

	return views, nil
}

func (r *listCapableRepository) GetSavedResultView(_ context.Context, id string) (SavedResultView, error) {
	view, ok := r.views[id]
	if !ok {
		return SavedResultView{}, ErrReusableNotFound
	}

	return view, nil
}

func (r *listCapableRepository) SaveResultView(_ context.Context, view SavedResultView) error {
	r.views[view.ID] = view

	return nil
}

func (r *listCapableRepository) DeleteSavedResultView(_ context.Context, id string) error {
	delete(r.views, id)

	return nil
}

func TestAddBusinessesToListTagsTheSelectionAndPinsAView(t *testing.T) {
	t.Parallel()

	repository := newListCapableRepository()
	service := NewService(repository, t.TempDir())

	result, err := service.AddBusinessesToList(
		context.Background(), []string{"biz_abcde", "biz_fghij"}, "Tuesday calls",
	)
	if err != nil {
		t.Fatalf("AddBusinessesToList() error = %v", err)
	}

	if result.Added != 2 || !result.ViewSaved || result.ViewID == "" {
		t.Fatalf("result = %+v, want two tagged businesses and a new view", result)
	}

	if len(repository.mutations) != 1 || repository.mutations[0].Action != "tag" ||
		repository.mutations[0].Value != "Tuesday calls" {
		t.Fatalf("mutations = %+v, want one tag mutation", repository.mutations)
	}

	view := repository.views[result.ViewID]
	if view.Name != "List: Tuesday calls" {
		t.Fatalf("view name = %q, want the list view name", view.Name)
	}

	if len(view.Search.Filters) != 1 || view.Search.Filters[0].Field != "tags" ||
		view.Search.Filters[0].Value != "Tuesday calls" {
		t.Fatalf("view filters = %+v, want a single tag filter", view.Search.Filters)
	}

	// Adding to the same list again reuses the view rather than piling up
	// duplicates of it.
	repeat, err := service.AddBusinessesToList(context.Background(), []string{"biz_klmno"}, "Tuesday calls")
	if err != nil {
		t.Fatalf("AddBusinessesToList() repeat error = %v", err)
	}

	if repeat.ViewSaved {
		t.Fatal("the second add created a second view for the same list")
	}

	if len(repository.views) != 1 {
		t.Fatalf("stored views = %d, want 1", len(repository.views))
	}
}

func TestAddBusinessesToListRejectsUnusableNames(t *testing.T) {
	t.Parallel()

	service := NewService(newListCapableRepository(), t.TempDir())

	for _, name := range []string{"", "   ", strings.Repeat("x", MaximumResultListNameLength+1), "bad\nname"} {
		if _, err := service.AddBusinessesToList(context.Background(), []string{"biz_abcde"}, name); !errors.Is(err, ErrInvalidResultList) {
			t.Errorf("AddBusinessesToList(%q) error = %v, want ErrInvalidResultList", name, err)
		}
	}
}

func TestListRouteRequiresCSRFAndCapability(t *testing.T) {
	t.Parallel()

	plain, err := New(NewService(&fixedJobRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := url.Values{}
	form.Set("result_ids", "biz_abcde")
	form.Set("list", "Tuesday calls")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/lists", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	plain.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("list without CSRF = %d, want 403", recorder.Code)
	}

	form.Set("csrf_token", plain.csrfToken)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/results/lists", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder = httptest.NewRecorder()
	plain.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("list without capability = %d, want 501", recorder.Code)
	}
}

func TestResultsPageHidesTheListActionWithoutStorage(t *testing.T) {
	t.Parallel()

	plain := httptest.NewRecorder()
	newResultsExplorerServer(t, false).srv.Handler.ServeHTTP(
		plain, httptest.NewRequest(http.MethodGet, "/app/results", nil),
	)

	if strings.Contains(plain.Body.String(), "/api/v1/results/lists") {
		t.Fatal("results page offered a list action a repository without saved views cannot store")
	}

	row := coreColumnResultRow()
	repository := newListCapableRepository()
	repository.fixedJobRepository = &fixedJobRepository{job: Job{
		ID: "ba78441f-a048-4c9d-a8de-d0589e66f132", Name: "San Francisco dentists",
		Status: StatusOK, Date: row.UpdatedAt,
	}}
	repository.page = ResultPage{Total: 1, Limit: 25, Results: []BusinessResult{row}}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	capable := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(capable, httptest.NewRequest(http.MethodGet, "/app/results", nil))

	if capable.Code != http.StatusOK {
		t.Fatalf("results status = %d, body = %s", capable.Code, capable.Body.String())
	}

	body := capable.Body.String()
	for _, expected := range []string{`formaction="/api/v1/results/lists"`, `name="list"`, "Add to list"} {
		if !strings.Contains(body, expected) {
			t.Errorf("capable results page is missing %q", expected)
		}
	}
}
