package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// exportProfileTestRepository is a JobRepository that also stores export
// records and presets, which is everything the profile API needs.
type exportProfileTestRepository struct {
	*campaignTestRepository

	exports map[string]ExportRecord
	presets map[string]ExportPreset
	parts   map[string][]ExportPart
}

func newExportProfileTestRepository() *exportProfileTestRepository {
	return &exportProfileTestRepository{
		campaignTestRepository: newCampaignTestRepository(),
		exports:                make(map[string]ExportRecord),
		presets:                make(map[string]ExportPreset),
		parts:                  make(map[string][]ExportPart),
	}
}

func (r *exportProfileTestRepository) CreateExport(_ context.Context, record ExportRecord) error {
	r.exports[record.ID] = record

	return nil
}

func (r *exportProfileTestRepository) UpdateExport(_ context.Context, record ExportRecord) error {
	r.exports[record.ID] = record

	return nil
}

func (r *exportProfileTestRepository) ListExports(context.Context, int) ([]ExportRecord, error) {
	records := make([]ExportRecord, 0, len(r.exports))
	for _, record := range r.exports {
		records = append(records, record)
	}

	return records, nil
}

func (r *exportProfileTestRepository) GetExport(_ context.Context, id string) (ExportRecord, error) {
	record, ok := r.exports[id]
	if !ok {
		return ExportRecord{}, ErrExportNotFound
	}

	return record, nil
}

func (r *exportProfileTestRepository) DeleteExport(_ context.Context, id string) error {
	delete(r.exports, id)

	return nil
}

func (r *exportProfileTestRepository) SaveExportPreset(
	_ context.Context,
	preset ExportPreset,
) (ExportPreset, error) {
	if preset.CreatedAt.IsZero() {
		preset.CreatedAt = time.Now().UTC()
	}

	preset.UpdatedAt = time.Now().UTC()
	r.presets[preset.ID] = preset

	return preset, nil
}

func (r *exportProfileTestRepository) ListExportPresets(
	context.Context,
	int,
) ([]ExportPreset, error) {
	presets := make([]ExportPreset, 0, len(r.presets))
	for _, preset := range r.presets {
		presets = append(presets, preset)
	}

	return presets, nil
}

func (r *exportProfileTestRepository) GetExportPreset(
	_ context.Context,
	id string,
) (ExportPreset, error) {
	preset, ok := r.presets[id]
	if !ok {
		return ExportPreset{}, ErrExportNotFound
	}

	return preset, nil
}

func (r *exportProfileTestRepository) DeleteExportPreset(_ context.Context, id string) error {
	if _, ok := r.presets[id]; !ok {
		return ErrExportNotFound
	}

	delete(r.presets, id)

	return nil
}

func (r *exportProfileTestRepository) ReplaceExportParts(
	_ context.Context,
	id string,
	parts []ExportPart,
) error {
	r.parts[id] = parts

	return nil
}

func (r *exportProfileTestRepository) ListExportParts(
	_ context.Context,
	id string,
) ([]ExportPart, error) {
	return r.parts[id], nil
}

func TestExportProfileFromInputValidatesTheWholeShape(t *testing.T) {
	t.Parallel()

	preset, err := exportProfileFromInput(exportProfileAPIInput{
		Name:   "New this week",
		Format: "csv",
		Columns: []ExportColumnSelection{
			{Key: "name", Label: "Business"},
			{Key: "phone", Label: "Phone"},
		},
		Search: &ResultSearch{
			Sort:    "updated_desc",
			Filters: []ResultFilter{{Field: "first_seen_job", Operator: "eq", Value: "run-1"}},
		},
	})
	if err != nil {
		t.Fatalf("exportProfileFromInput: %v", err)
	}

	if preset.ID == "" || preset.Name != "New this week" || preset.Format != "csv" {
		t.Fatalf("preset = %#v", preset)
	}

	columns, err := decodeExportColumns(preset.Columns)
	if err != nil || len(columns) != 2 || columns[0].Label != "Business" {
		t.Fatalf("stored columns = %#v (%v)", columns, err)
	}

	var search ResultSearch
	if err := json.Unmarshal([]byte(preset.Filters), &search); err != nil {
		t.Fatalf("decode stored filter: %v", err)
	}

	if len(search.Filters) != 1 || search.Filters[0].Field != "first_seen_job" {
		t.Fatalf("stored filter = %#v", search)
	}

	// A profile without a column list keeps the historical export shape.
	fallback, err := exportProfileFromInput(exportProfileAPIInput{Name: "All", Format: "csv"})
	if err != nil {
		t.Fatalf("default-column profile: %v", err)
	}

	options, err := decodeExportOptions(fallback.Options)
	if err != nil || !options.LegacyShape {
		t.Fatalf("default-column options = %#v (%v)", options, err)
	}

	for _, invalid := range []exportProfileAPIInput{
		{Name: "", Format: "csv"},
		{Name: "bad format", Format: "docx"},
		{Name: "bad column", Format: "csv", Columns: []ExportColumnSelection{{Key: "no_such_column"}}},
		{Name: "bad id", Format: "csv", ID: "no"},
	} {
		if _, err := exportProfileFromInput(invalid); err == nil {
			t.Fatalf("invalid profile %#v was accepted", invalid)
		}
	}
}

func TestExportProfileAPIRoundTrip(t *testing.T) {
	t.Parallel()

	repository := newExportProfileTestRepository()

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"name":"New this week","format":"csv",` +
		`"columns":[{"key":"name","label":"Business"},{"key":"phone","label":"Phone"}],` +
		`"search":{"sort":"updated_desc","filters":[` +
		`{"field":"first_seen_job","operator":"eq","value":"run-1"}]}}`

	request := httptest.NewRequest(http.MethodPost, "/api/v1/export-profiles", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("save without a CSRF token = %d, want 403", recorder.Code)
	}

	if len(repository.presets) != 0 {
		t.Fatalf("a rejected request stored %d profile(s)", len(repository.presets))
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/export-profiles", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("save = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var saved struct {
		Data ExportPreset `json:"data"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save response: %v", err)
	}

	if saved.Data.ID == "" || saved.Data.Name != "New this week" {
		t.Fatalf("save response = %#v", saved.Data)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/export-profiles", nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "New this week") {
		t.Fatalf("list = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/export-profiles/"+saved.Data.ID, nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "first_seen_job") {
		t.Fatalf("read = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Re-saving under the same ID replaces the profile rather than adding
	// a second one.
	replace := `{"id":"` + saved.Data.ID + `","name":"New this month","format":"xlsx"}`
	request = httptest.NewRequest(http.MethodPost, "/api/v1/export-profiles", strings.NewReader(replace))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated || len(repository.presets) != 1 {
		t.Fatalf("replace = %d with %d stored profile(s)", recorder.Code, len(repository.presets))
	}

	if repository.presets[saved.Data.ID].Format != "xlsx" {
		t.Fatalf("replaced profile = %#v", repository.presets[saved.Data.ID])
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/export-profiles/"+saved.Data.ID, nil)
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || len(repository.presets) != 0 {
		t.Fatalf("delete = %d with %d stored profile(s), body = %s",
			recorder.Code, len(repository.presets), recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/export-profiles/"+saved.Data.ID, nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("read after delete = %d, want 404", recorder.Code)
	}
}

func TestExportProfileAPIReportsAnUnsupportedStore(t *testing.T) {
	t.Parallel()

	server, err := New(NewService(newCampaignTestRepository(), t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/export-profiles", nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("list without export storage = %d, want 501", recorder.Code)
	}
}
