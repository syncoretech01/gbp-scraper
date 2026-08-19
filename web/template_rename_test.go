package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// templateRenameRepository is a self-contained in-memory reusable store for
// the dedicated template rename action.
type templateRenameRepository struct {
	*fixedJobRepository
	templates map[string]ScrapeTemplate
}

func newTemplateRenameRepository() *templateRenameRepository {
	return &templateRenameRepository{
		fixedJobRepository: &fixedJobRepository{},
		templates:          make(map[string]ScrapeTemplate),
	}
}

func (repository *templateRenameRepository) ListScrapeTemplates(context.Context, string) ([]ScrapeTemplate, error) {
	templates := make([]ScrapeTemplate, 0, len(repository.templates))
	for _, template := range repository.templates {
		templates = append(templates, template)
	}
	return templates, nil
}

func (repository *templateRenameRepository) GetScrapeTemplate(_ context.Context, id string) (ScrapeTemplate, error) {
	template, ok := repository.templates[id]
	if !ok {
		return ScrapeTemplate{}, ErrReusableNotFound
	}
	return template, nil
}

func (repository *templateRenameRepository) SaveScrapeTemplate(_ context.Context, template ScrapeTemplate) error {
	repository.templates[template.ID] = template
	return nil
}

func (repository *templateRenameRepository) DeleteScrapeTemplate(_ context.Context, id string) error {
	if _, ok := repository.templates[id]; !ok {
		return ErrReusableNotFound
	}
	delete(repository.templates, id)
	return nil
}

func (repository *templateRenameRepository) SetScrapeTemplatePinned(_ context.Context, id string, pinned bool) error {
	template, ok := repository.templates[id]
	if !ok {
		return ErrReusableNotFound
	}
	template.Pinned = pinned
	repository.templates[id] = template
	return nil
}

func (repository *templateRenameRepository) RecordScrapeTemplateUse(_ context.Context, id string, usedAt time.Time) error {
	template, ok := repository.templates[id]
	if !ok {
		return ErrReusableNotFound
	}
	template.UseCount++
	template.LastRunAt = &usedAt
	repository.templates[id] = template
	return nil
}

func (repository *templateRenameRepository) ListSavedResultViews(context.Context, string) ([]SavedResultView, error) {
	return nil, nil
}

func (repository *templateRenameRepository) GetSavedResultView(context.Context, string) (SavedResultView, error) {
	return SavedResultView{}, ErrReusableNotFound
}

func (repository *templateRenameRepository) SaveResultView(context.Context, SavedResultView) error {
	return nil
}

func (repository *templateRenameRepository) DeleteSavedResultView(context.Context, string) error {
	return ErrReusableNotFound
}

var _ reusableRepository = (*templateRenameRepository)(nil)

func templateRenameForm(target, csrf, name string) *http.Request {
	values := url.Values{}
	if csrf != "" {
		values.Set("csrf_token", csrf)
	}
	values.Set("name", name)
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// TestTemplateRenameActionRoundTrip covers the dedicated rename action:
// CSRF gating, name validation, the browser redirect, the JSON envelope, the
// persisted new name, and the unknown-id case.
func TestTemplateRenameActionRoundTrip(t *testing.T) {
	t.Parallel()

	repository := newTemplateRenameRepository()
	lastRun := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	repository.templates["tpl-rename-1"] = ScrapeTemplate{
		ID: "tpl-rename-1", Name: "Dentists SF", Description: "Dental offices",
		Configuration: JobData{Keywords: []string{"dentist"}, MaxTime: 10 * time.Minute},
		Pinned:        true, UseCount: 7, LastRunAt: &lastRun,
		CreatedAt: lastRun, UpdatedAt: lastRun,
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.srv.Handler.ServeHTTP(recorder, request)
		return recorder
	}

	// The saved-searches page serves a rename form per template card.
	page := serve(httptest.NewRequest(http.MethodGet, "/app/saved-searches", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("saved-searches status = %d body = %s", page.Code, page.Body.String())
	}
	for _, expected := range []string{
		`action="/api/v1/templates/tpl-rename-1/rename"`,
		"New name for Dentists SF",
		">Rename<",
	} {
		if !strings.Contains(page.Body.String(), expected) {
			t.Fatalf("saved-searches page missing %q", expected)
		}
	}

	// Missing CSRF token is rejected and nothing is persisted.
	response := serve(templateRenameForm("/api/v1/templates/tpl-rename-1/rename", "", "Blocked rename"))
	if response.Code != http.StatusForbidden || repository.templates["tpl-rename-1"].Name != "Dentists SF" {
		t.Fatalf("rename without CSRF = %d, name = %q", response.Code, repository.templates["tpl-rename-1"].Name)
	}

	// Invalid names are rejected with 422.
	for _, invalid := range []string{"", "   ", strings.Repeat("x", 121), "line\nbreak", "nul\x00char"} {
		response = serve(templateRenameForm("/api/v1/templates/tpl-rename-1/rename", server.csrfToken, invalid))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("rename with name %q = %d, want 422", invalid, response.Code)
		}
	}
	if repository.templates["tpl-rename-1"].Name != "Dentists SF" {
		t.Fatalf("invalid renames must not persist, name = %q", repository.templates["tpl-rename-1"].Name)
	}

	// A browser form post redirects back to the saved-searches page.
	response = serve(templateRenameForm("/api/v1/templates/tpl-rename-1/rename", server.csrfToken, "  Dentists downtown  "))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("rename status = %d body = %s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); !strings.HasPrefix(location, "/app/saved-searches?") || !strings.Contains(location, "notice=") {
		t.Fatalf("rename redirect = %q", location)
	}
	renamed := repository.templates["tpl-rename-1"]
	if renamed.Name != "Dentists downtown" || !renamed.Pinned || renamed.UseCount != 7 ||
		renamed.LastRunAt == nil || renamed.Description != "Dental offices" {
		t.Fatalf("renamed template = %+v; rename must only change the name", renamed)
	}
	if !renamed.UpdatedAt.After(lastRun) {
		t.Fatalf("rename must refresh UpdatedAt, got %s", renamed.UpdatedAt)
	}

	// API clients receive a JSON envelope instead of the redirect.
	request := templateRenameForm("/api/v1/templates/tpl-rename-1/rename", server.csrfToken, "Dentists via API")
	request.Header.Set("Accept", "application/json")
	response = serve(request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"name":"Dentists via API"`) ||
		!strings.Contains(response.Body.String(), `"data"`) {
		t.Fatalf("JSON rename = %d body = %s", response.Code, response.Body.String())
	}
	if repository.templates["tpl-rename-1"].Name != "Dentists via API" {
		t.Fatalf("JSON rename not persisted, name = %q", repository.templates["tpl-rename-1"].Name)
	}

	// Unknown templates return 404.
	response = serve(templateRenameForm("/api/v1/templates/missing-template/rename", server.csrfToken, "Anything"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown template rename = %d body = %s", response.Code, response.Body.String())
	}
}
