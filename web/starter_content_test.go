package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSeedStarterContentPopulatesFreshWorkspace(t *testing.T) {
	t.Parallel()

	repository := newStarterSeedRepository()
	service := NewService(repository, t.TempDir())

	seeded, err := service.SeedStarterContent(context.Background())
	if err != nil {
		t.Fatalf("SeedStarterContent() error = %v", err)
	}

	if seeded <= 0 {
		t.Fatalf("SeedStarterContent() = %d, want > 0", seeded)
	}

	if repository.settings[starterContentSettingKey] != starterContentVersion {
		t.Fatalf("seed flag = %q, want %q", repository.settings[starterContentSettingKey], starterContentVersion)
	}

	templates, err := service.ListScrapeTemplates(context.Background(), "")
	if err != nil {
		t.Fatalf("ListScrapeTemplates() error = %v", err)
	}

	if len(templates) != 5 {
		t.Fatalf("seeded templates = %d, want 5", len(templates))
	}

	for _, template := range templates {
		if err := template.Configuration.Validate(); err != nil {
			t.Fatalf("starter template %q fails JobData validation: %v", template.Name, err)
		}

		if template.Name == "" || len(template.Name) > 120 {
			t.Fatalf("starter template name %q violates the save validation bounds", template.Name)
		}

		if len(template.Configuration.Proxies) > 0 {
			t.Fatalf("starter template %q carries inline proxies", template.Name)
		}

		if !containsStarterMarker(template.Description) {
			t.Fatalf("starter template %q does not describe itself as an editable starter: %q",
				template.Name, template.Description)
		}
	}

	views, err := service.ListSavedResultViews(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSavedResultViews() error = %v", err)
	}

	if len(views) != 6 {
		t.Fatalf("seeded views = %d, want 6", len(views))
	}

	if seeded != len(templates)+len(views) {
		t.Fatalf("SeedStarterContent() = %d, want %d", seeded, len(templates)+len(views))
	}

	// Every seeded view must load back through the persistence round trip and
	// its stored search must pass the repository's filter validation.
	for _, listed := range views {
		loaded, err := service.GetSavedResultView(context.Background(), listed.ID)
		if err != nil {
			t.Fatalf("GetSavedResultView(%s) error = %v", listed.ID, err)
		}

		if err := service.validateStarterViewSearch(context.Background(), loaded.Search); err != nil {
			t.Fatalf("seeded view %q does not validate after loading: %v", loaded.Name, err)
		}
	}
}

func TestSeedStarterContentRunsOnce(t *testing.T) {
	t.Parallel()

	repository := newStarterSeedRepository()
	service := NewService(repository, t.TempDir())

	first, err := service.SeedStarterContent(context.Background())
	if err != nil {
		t.Fatalf("first SeedStarterContent() error = %v", err)
	}

	if first <= 0 {
		t.Fatalf("first SeedStarterContent() = %d, want > 0", first)
	}

	templatesAfterFirst := len(repository.templates)
	viewsAfterFirst := len(repository.views)

	second, err := service.SeedStarterContent(context.Background())
	if err != nil {
		t.Fatalf("second SeedStarterContent() error = %v", err)
	}

	if second != 0 {
		t.Fatalf("second SeedStarterContent() = %d, want 0", second)
	}

	if len(repository.templates) != templatesAfterFirst || len(repository.views) != viewsAfterFirst {
		t.Fatalf("second run changed content: templates %d -> %d, views %d -> %d",
			templatesAfterFirst, len(repository.templates), viewsAfterFirst, len(repository.views))
	}
}

func TestSeedStarterContentKeepsUserTemplates(t *testing.T) {
	t.Parallel()

	repository := newStarterSeedRepository()
	service := NewService(repository, t.TempDir())

	userTemplate := ScrapeTemplate{
		ID:            "user-template",
		Name:          "My own template",
		Configuration: starterJobConfiguration("bakeries in Oakland"),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := service.SaveScrapeTemplate(context.Background(), userTemplate); err != nil {
		t.Fatalf("SaveScrapeTemplate() error = %v", err)
	}

	seeded, err := service.SeedStarterContent(context.Background())
	if err != nil {
		t.Fatalf("SeedStarterContent() error = %v", err)
	}

	templates, err := service.ListScrapeTemplates(context.Background(), "")
	if err != nil {
		t.Fatalf("ListScrapeTemplates() error = %v", err)
	}

	if len(templates) != 1 || templates[0].ID != "user-template" {
		t.Fatalf("templates after seeding a used workspace = %d, want only the user template", len(templates))
	}

	views, err := service.ListSavedResultViews(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSavedResultViews() error = %v", err)
	}

	if len(views) != 6 || seeded != 6 {
		t.Fatalf("seeded = %d with %d views, want 6 views and nothing else", seeded, len(views))
	}

	if repository.settings[starterContentSettingKey] != starterContentVersion {
		t.Fatalf("seed flag was not recorded")
	}
}

func TestSeedStarterContentSkipsViewsTheValidatorRejects(t *testing.T) {
	t.Parallel()

	repository := newStarterSeedRepository()
	repository.rejectedField = "website_status"
	service := NewService(repository, t.TempDir())

	seeded, err := service.SeedStarterContent(context.Background())
	if err != nil {
		t.Fatalf("SeedStarterContent() error = %v", err)
	}

	views, err := service.ListSavedResultViews(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSavedResultViews() error = %v", err)
	}

	if len(views) != 5 {
		t.Fatalf("views after one rejection = %d, want 5", len(views))
	}

	for _, view := range views {
		if view.Name == "Active website, no email" {
			t.Fatalf("a view the validator rejected was stored anyway")
		}
	}

	if seeded != 10 {
		t.Fatalf("SeedStarterContent() = %d, want 10 (5 templates + 5 accepted views)", seeded)
	}

	if repository.settings[starterContentSettingKey] != starterContentVersion {
		t.Fatalf("seed flag was not recorded after a partial seed")
	}
}

func containsStarterMarker(description string) bool {
	return strings.Contains(strings.ToLower(description), "starter template")
}

// starterSeedRepository is an in-memory JobRepository that also implements
// settings, reusable-content, and normalized-result storage. Its
// SearchBusinesses mirrors the bounded filter language of the SQLite
// repository closely enough to reject unknown fields and operators, so the
// seeding logic's validate-before-save behaviour is exercised for real.
type starterSeedRepository struct {
	settings      map[string]string
	templates     map[string]string
	views         map[string]string
	rejectedField string
}

func newStarterSeedRepository() *starterSeedRepository {
	return &starterSeedRepository{
		settings:  map[string]string{},
		templates: map[string]string{},
		views:     map[string]string{},
	}
}

func (repository *starterSeedRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (repository *starterSeedRepository) Create(context.Context, *Job) error   { return nil }
func (repository *starterSeedRepository) Delete(context.Context, string) error { return nil }
func (repository *starterSeedRepository) Update(context.Context, *Job) error   { return nil }

func (repository *starterSeedRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (repository *starterSeedRepository) LoadSettings(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(repository.settings))
	for key, value := range repository.settings {
		values[key] = value
	}

	return values, nil
}

func (repository *starterSeedRepository) SaveSettings(_ context.Context, values map[string]string) error {
	for key, value := range values {
		repository.settings[key] = value
	}

	return nil
}

func (repository *starterSeedRepository) ListScrapeTemplates(context.Context, string) ([]ScrapeTemplate, error) {
	templates := make([]ScrapeTemplate, 0, len(repository.templates))

	for _, encoded := range repository.templates {
		var template ScrapeTemplate
		if err := json.Unmarshal([]byte(encoded), &template); err != nil {
			return nil, err
		}

		templates = append(templates, template)
	}

	return templates, nil
}

func (repository *starterSeedRepository) GetScrapeTemplate(_ context.Context, id string) (ScrapeTemplate, error) {
	encoded, ok := repository.templates[id]
	if !ok {
		return ScrapeTemplate{}, ErrReusableNotFound
	}

	var template ScrapeTemplate
	if err := json.Unmarshal([]byte(encoded), &template); err != nil {
		return ScrapeTemplate{}, err
	}

	return template, nil
}

func (repository *starterSeedRepository) SaveScrapeTemplate(_ context.Context, template ScrapeTemplate) error {
	encoded, err := json.Marshal(template)
	if err != nil {
		return err
	}

	repository.templates[template.ID] = string(encoded)

	return nil
}

func (repository *starterSeedRepository) DeleteScrapeTemplate(_ context.Context, id string) error {
	delete(repository.templates, id)

	return nil
}

func (repository *starterSeedRepository) SetScrapeTemplatePinned(context.Context, string, bool) error {
	return nil
}

func (repository *starterSeedRepository) RecordScrapeTemplateUse(context.Context, string, time.Time) error {
	return nil
}

func (repository *starterSeedRepository) ListSavedResultViews(context.Context, string) ([]SavedResultView, error) {
	views := make([]SavedResultView, 0, len(repository.views))

	for _, encoded := range repository.views {
		var view SavedResultView
		if err := json.Unmarshal([]byte(encoded), &view); err != nil {
			return nil, err
		}

		views = append(views, view)
	}

	return views, nil
}

func (repository *starterSeedRepository) GetSavedResultView(_ context.Context, id string) (SavedResultView, error) {
	encoded, ok := repository.views[id]
	if !ok {
		return SavedResultView{}, ErrReusableNotFound
	}

	var view SavedResultView
	if err := json.Unmarshal([]byte(encoded), &view); err != nil {
		return SavedResultView{}, err
	}

	return view, nil
}

func (repository *starterSeedRepository) SaveResultView(_ context.Context, view SavedResultView) error {
	encoded, err := json.Marshal(view)
	if err != nil {
		return err
	}

	repository.views[view.ID] = string(encoded)

	return nil
}

func (repository *starterSeedRepository) DeleteSavedResultView(_ context.Context, id string) error {
	delete(repository.views, id)

	return nil
}

func (repository *starterSeedRepository) ImportLegacyCSV(context.Context, Job, string) (ResultFileImport, error) {
	return ResultFileImport{}, nil
}

func (repository *starterSeedRepository) GetBusiness(context.Context, string) (BusinessDetail, error) {
	return BusinessDetail{}, ErrBusinessNotFound
}

func (repository *starterSeedRepository) ResultOverview(context.Context) (ResultOverview, error) {
	return ResultOverview{}, nil
}

// SearchBusinesses validates the search against a mirror of the SQLite
// repository's filter language and returns an empty page when it passes.
func (repository *starterSeedRepository) SearchBusinesses(
	_ context.Context, search ResultSearch,
) (ResultPage, error) {
	for _, filter := range search.Filters {
		if err := repository.validateSeedFilter(filter); err != nil {
			return ResultPage{}, err
		}
	}

	if search.FilterGroup != nil {
		if err := repository.validateSeedGroup(*search.FilterGroup); err != nil {
			return ResultPage{}, err
		}
	}

	return ResultPage{Limit: search.Limit, Offset: search.Offset}, nil
}

func (repository *starterSeedRepository) validateSeedGroup(group ResultFilterGroup) error {
	if group.Logic != "" && group.Logic != "and" && group.Logic != "or" {
		return fmt.Errorf("%w: group logic %q", ErrInvalidResultQuery, group.Logic)
	}

	for _, filter := range group.Filters {
		if err := repository.validateSeedFilter(filter); err != nil {
			return err
		}
	}

	for _, child := range group.Groups {
		if err := repository.validateSeedGroup(child); err != nil {
			return err
		}
	}

	return nil
}

// validateSeedFilter mirrors the field/operator table in
// web/sqlite/results.go resultFilterSQL for the field kinds the starter
// views use.
func (repository *starterSeedRepository) validateSeedFilter(filter ResultFilter) error {
	if repository.rejectedField != "" && filter.Field == repository.rejectedField {
		return fmt.Errorf("%w: field %q rejected by test repository", ErrInvalidResultQuery, filter.Field)
	}

	textOperators := map[string]bool{
		"eq": true, "neq": true, "contains": true, "not_contains": true,
		"starts_with": true, "ends_with": true, "empty": true, "not_empty": true,
	}
	numericOperators := map[string]bool{
		"eq": true, "neq": true, "gte": true, "lte": true, "gt": true, "lt": true,
		"between": true, "empty": true, "not_empty": true,
	}

	textFields := map[string]bool{
		"id": true, "name": true, "address": true, "city": true, "state": true,
		"postal_code": true, "country": true, "category": true, "status": true,
		"business_status": true, "website_status": true, "domain": true,
		"change_status": true, "place_id": true, "cid": true, "data_id": true,
		"maps_url": true, "website": true, "email": true, "phone": true,
	}
	numericFields := map[string]bool{
		"rating": true, "reviews": true, "review_count": true,
		"quality_score": true, "confidence": true, "website_response_ms": true,
	}

	switch {
	case textFields[filter.Field]:
		if !textOperators[filter.Operator] {
			return fmt.Errorf("%w: %s/%s", ErrInvalidResultQuery, filter.Field, filter.Operator)
		}
	case numericFields[filter.Field]:
		if !numericOperators[filter.Operator] {
			return fmt.Errorf("%w: %s/%s", ErrInvalidResultQuery, filter.Field, filter.Operator)
		}

		if filter.Operator != "empty" && filter.Operator != "not_empty" && filter.Operator != "between" {
			if _, err := strconv.ParseFloat(filter.Value, 64); err != nil {
				return fmt.Errorf("%w: %s value %q", ErrInvalidResultQuery, filter.Field, filter.Value)
			}
		}
	default:
		return fmt.Errorf("%w: unsupported field %q", ErrInvalidResultQuery, filter.Field)
	}

	return nil
}

var (
	_ settingsRepository = (*starterSeedRepository)(nil)
	_ reusableRepository = (*starterSeedRepository)(nil)
	_ ResultRepository   = (*starterSeedRepository)(nil)
)
