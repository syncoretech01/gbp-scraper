package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGlobalSearchAPIValidatesAndReturnsLocalItems(t *testing.T) {
	t.Parallel()

	repository := &globalSearchTestRepository{
		fixedJobRepository: &fixedJobRepository{},
		items:              []GlobalSearchItem{{Type: "Business", Title: "Golden Gate Dental", URL: "/app/results/business-1"}},
	}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.registerGlobalSearchRoutes(mux)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=dental&limit=8", http.NoBody))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Golden Gate Dental") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if repository.query != "dental" || repository.limit != 8 {
		t.Fatalf("search input = %q limit=%d", repository.query, repository.limit)
	}

	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=x&limit=1000", http.NoBody))
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status = %d", invalid.Code)
	}
}

type globalSearchTestRepository struct {
	*fixedJobRepository
	items []GlobalSearchItem
	query string
	limit int
}

func (repository *globalSearchTestRepository) GlobalSearch(_ context.Context, query string, limit int) ([]GlobalSearchItem, error) {
	repository.query = query
	repository.limit = limit

	return repository.items, nil
}
