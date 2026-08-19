package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKeywordSetServiceValidatesAndNormalizes(t *testing.T) {
	t.Parallel()

	repository := newKeywordSetTestRepository()
	service := NewService(repository, t.TempDir())
	ctx := context.Background()

	if !service.SupportsKeywordSets() {
		t.Fatal("capable repository reported as unsupported")
	}

	saved, err := service.SaveKeywordSet(ctx, KeywordSet{
		Name:        "  Bay Area dental  ",
		Description: " Recurring dental sweep ",
		Keywords:    []string{" dentists in San Francisco ", "", "dentists in San Francisco", "dental clinics in Oakland"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if saved.ID == "" || saved.Name != "Bay Area dental" || saved.Description != "Recurring dental sweep" {
		t.Fatalf("normalized set = %+v", saved)
	}

	if len(saved.Keywords) != 2 || saved.Keywords[0] != "dentists in San Francisco" || saved.Keywords[1] != "dental clinics in Oakland" {
		t.Fatalf("keywords = %v", saved.Keywords)
	}

	rejected := []KeywordSet{
		{Name: "", Keywords: []string{"a"}},
		{Name: strings.Repeat("n", MaximumKeywordSetNameLength+1), Keywords: []string{"a"}},
		{Name: "line\nbreak", Keywords: []string{"a"}},
		{Name: "ok", Description: strings.Repeat("d", MaximumKeywordSetDescriptionLength+1), Keywords: []string{"a"}},
		{Name: "ok", Keywords: nil},
		{Name: "ok", Keywords: []string{" ", ""}},
		{Name: "ok", Keywords: []string{strings.Repeat("k", MaximumKeywordSetKeywordLength+1)}},
		{Name: "ok", Keywords: tooManyTestKeywords()},
	}
	for index, set := range rejected {
		if _, err := service.SaveKeywordSet(ctx, set); !errors.Is(err, ErrInvalidKeywordSet) {
			t.Fatalf("case %d accepted invalid set: %v", index, err)
		}
	}

	legacy := NewService(&lifecycleCaptureRepository{}, t.TempDir())
	if legacy.SupportsKeywordSets() {
		t.Fatal("legacy repository reported keyword set support")
	}

	if _, err := legacy.SaveKeywordSet(ctx, KeywordSet{Name: "ok", Keywords: []string{"a"}}); !errors.Is(err, ErrKeywordSetsUnsupported) {
		t.Fatalf("legacy save returned %v, want unsupported", err)
	}

	if _, err := legacy.ListKeywordSets(ctx); !errors.Is(err, ErrKeywordSetsUnsupported) {
		t.Fatalf("legacy list returned %v, want unsupported", err)
	}
}

func TestKeywordSetRoutesEnforceCSRFAndRoundTrip(t *testing.T) {
	t.Parallel()

	repository := newKeywordSetTestRepository()
	srv, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerKeywordSetRoutes(mux)

	// A mutation without the CSRF token is refused before any work happens.
	noToken := url.Values{"name": {"Blocked"}, "keywords": {"a"}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets", strings.NewReader(noToken.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", recorder.Code)
	}

	// A urlencoded form create succeeds; the textarea carries one query per
	// line and exact duplicates are removed.
	form := url.Values{
		"csrf_token":  {srv.csrfToken},
		"name":        {"Bay Area dental"},
		"description": {"Recurring sweep"},
		"keywords":    {"dentists in San Francisco\r\ndental clinics in Oakland\r\ndentists in San Francisco"},
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("form create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	stored := repository.snapshotKeywordSets()
	if len(stored) != 1 || len(stored[0].Keywords) != 2 || stored[0].Description != "Recurring sweep" {
		t.Fatalf("stored sets = %+v", stored)
	}

	// The listing exposes the saved set.
	request = httptest.NewRequest(http.MethodGet, "/api/v1/keyword-sets", http.NoBody)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Bay Area dental") {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Using the set returns the keywords and bumps the usage counter.
	request = httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets/"+stored[0].ID+"/use", http.NoBody)
	request.Header.Set("X-CSRF-Token", srv.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("use status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "dentists in San Francisco") || !strings.Contains(body, `"use_count":1`) {
		t.Fatalf("use response = %s", body)
	}

	// Deleting removes the set; a second delete reports not-found.
	request = httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets/"+stored[0].ID+"/delete", http.NoBody)
	request.Header.Set("X-CSRF-Token", srv.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if remaining := repository.snapshotKeywordSets(); len(remaining) != 0 {
		t.Fatalf("sets after delete = %+v", remaining)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets/"+stored[0].ID+"/delete", http.NoBody)
	request.Header.Set("X-CSRF-Token", srv.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d", recorder.Code)
	}
}

func TestKeywordSetJSONCreateRejectsUnknownFieldsAndExtraObjects(t *testing.T) {
	t.Parallel()

	repository := newKeywordSetTestRepository()
	srv, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerKeywordSetRoutes(mux)

	for _, body := range []string{
		`{"name":"ok","keywords":["a"],"surprise":true}`,
		`{"name":"ok","keywords":["a"]}{"name":"two","keywords":["b"]}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", srv.csrfToken)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("body %q status = %d", body, recorder.Code)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/keyword-sets",
		strings.NewReader(`{"name":"JSON set","keywords":["cafes in Lisbon"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", srv.csrfToken)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "JSON set") {
		t.Fatalf("JSON create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestNewScrapePageOffersKeywordSetsAndScrapeDefaults(t *testing.T) {
	t.Parallel()

	repository := newKeywordSetTestRepository()
	repository.settings = map[string]string{
		"scrape.location_label": "Austin, Texas, United States",
		"scrape.latitude":       "30.2672",
		"scrape.longitude":      "-97.7431",
	}
	if _, err := NewService(repository, t.TempDir()).SaveKeywordSet(context.Background(), KeywordSet{
		Name: "Texas coffee", Keywords: []string{"coffee shops in Austin"},
	}); err != nil {
		t.Fatalf("seed keyword set: %v", err)
	}

	srv, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recorder := httptest.NewRecorder()
	srv.newScrapePage(recorder, httptest.NewRequest(http.MethodGet, "/app/scrapes/new", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, expected := range []string{
		"Texas coffee",
		"data-keyword-set-picker",
		`data-action="save-keyword-set"`,
		"data-include-terms",
		"data-exclude-terms",
		"data-combo-categories",
		"data-combo-locations-file",
		`data-action="generate-combinations"`,
		"Austin, Texas, United States",
		"30.2672",
		"-97.7431",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("wizard missing %q", expected)
		}
	}
}

func TestNewScrapePageHidesKeywordSetControlsWithoutSupport(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, t.TempDir())
	recorder := httptest.NewRecorder()
	srv.newScrapePage(recorder, httptest.NewRequest(http.MethodGet, "/app/scrapes/new", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}

	body := recorder.Body.String()
	for _, hidden := range []string{"data-keyword-set-picker", `data-action="save-keyword-set"`} {
		if strings.Contains(body, hidden) {
			t.Fatalf("unsupported repository still renders %q", hidden)
		}
	}

	// The purely client-side helpers stay available on any repository.
	if !strings.Contains(body, "data-include-terms") || !strings.Contains(body, "data-combo-categories") {
		t.Fatal("client-side query tools are missing")
	}
}

func tooManyTestKeywords() []string {
	keywords := make([]string, 0, MaximumKeywordSetKeywords+1)
	for index := 0; index <= MaximumKeywordSetKeywords; index++ {
		keywords = append(keywords, fmt.Sprintf("keyword %d", index))
	}

	return keywords
}

// keywordSetTestRepository is an in-memory keyword-set store on top of a
// no-op job repository, with optional settings support for page prefills.
type keywordSetTestRepository struct {
	mu       sync.Mutex
	sets     []KeywordSet
	settings map[string]string
}

func newKeywordSetTestRepository() *keywordSetTestRepository {
	return &keywordSetTestRepository{}
}

func (r *keywordSetTestRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (r *keywordSetTestRepository) Create(context.Context, *Job) error   { return nil }
func (r *keywordSetTestRepository) Delete(context.Context, string) error { return nil }
func (r *keywordSetTestRepository) Update(context.Context, *Job) error   { return nil }

func (r *keywordSetTestRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (r *keywordSetTestRepository) LoadSettings(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	values := make(map[string]string, len(r.settings))
	for key, value := range r.settings {
		values[key] = value
	}

	return values, nil
}

func (r *keywordSetTestRepository) SaveSettings(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.settings == nil {
		r.settings = make(map[string]string, len(values))
	}
	for key, value := range values {
		r.settings[key] = value
	}

	return nil
}

func (r *keywordSetTestRepository) ListKeywordSets(_ context.Context, limit int) ([]KeywordSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sets := make([]KeywordSet, 0, len(r.sets))
	for index := len(r.sets) - 1; index >= 0 && len(sets) < limit; index-- {
		sets = append(sets, r.sets[index])
	}

	return sets, nil
}

func (r *keywordSetTestRepository) SaveKeywordSet(_ context.Context, set KeywordSet) (KeywordSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index, existing := range r.sets {
		if strings.EqualFold(existing.Name, set.Name) {
			set.ID = existing.ID
			set.UseCount = existing.UseCount
			set.CreatedAt = existing.CreatedAt
			r.sets[index] = set

			return set, nil
		}
	}

	r.sets = append(r.sets, set)

	return set, nil
}

func (r *keywordSetTestRepository) DeleteKeywordSet(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index, existing := range r.sets {
		if existing.ID == id {
			r.sets = append(r.sets[:index], r.sets[index+1:]...)

			return nil
		}
	}

	return ErrKeywordSetNotFound
}

func (r *keywordSetTestRepository) TouchKeywordSetUse(_ context.Context, id string, usedAt time.Time) (KeywordSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index, existing := range r.sets {
		if existing.ID == id {
			existing.UseCount++
			existing.LastUsedAt = &usedAt
			r.sets[index] = existing

			return existing, nil
		}
	}

	return KeywordSet{}, ErrKeywordSetNotFound
}

func (r *keywordSetTestRepository) snapshotKeywordSets() []KeywordSet {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]KeywordSet(nil), r.sets...)
}
