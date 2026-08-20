package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// exportCapableProspectRepository extends the prospect stub with an
// in-memory export registry so the full export-creation path can run.
type exportCapableProspectRepository struct {
	*prospectStubRepository
	exports map[string]ExportRecord
}

func newExportCapableProspectRepository() *exportCapableProspectRepository {
	return &exportCapableProspectRepository{
		prospectStubRepository: newProspectStubRepository(),
		exports:                map[string]ExportRecord{},
	}
}

func (r *exportCapableProspectRepository) CreateExport(_ context.Context, record ExportRecord) error {
	r.exports[record.ID] = record
	return nil
}

func (r *exportCapableProspectRepository) UpdateExport(_ context.Context, record ExportRecord) error {
	r.exports[record.ID] = record
	return nil
}

func (r *exportCapableProspectRepository) ListExports(context.Context, int) ([]ExportRecord, error) {
	records := make([]ExportRecord, 0, len(r.exports))
	for _, record := range r.exports {
		records = append(records, record)
	}
	return records, nil
}

func (r *exportCapableProspectRepository) GetExport(_ context.Context, id string) (ExportRecord, error) {
	record, ok := r.exports[id]
	if !ok {
		return ExportRecord{}, ErrExportNotFound
	}
	return record, nil
}

func (r *exportCapableProspectRepository) DeleteExport(_ context.Context, id string) error {
	delete(r.exports, id)
	return nil
}

func TestFutureIntegrationsDefaultOff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// A fresh workspace keeps the dormant surfaces off.
	repository := newProspectStubRepository()
	service := NewService(repository, t.TempDir())
	if service.FutureIntegrationsEnabled(ctx) {
		t.Fatal("future integrations were enabled without any stored opt-in")
	}

	// Only the exact stored opt-in value counts as on.
	for _, value := range []string{"", "true", "on", "ENABLED"} {
		repository.settings[settingProspectFutureIntegrations] = value
		if service.FutureIntegrationsEnabled(ctx) {
			t.Fatalf("stored value %q enabled future integrations", value)
		}
	}
	repository.settings[settingProspectFutureIntegrations] = futureIntegrationsEnabledValue
	if !service.FutureIntegrationsEnabled(ctx) {
		t.Fatal("the stored opt-in did not enable future integrations")
	}

	// Without settings storage the toggle stays off instead of erroring.
	bare := NewService(&fixedJobRepository{}, t.TempDir())
	if bare.FutureIntegrationsEnabled(ctx) {
		t.Fatal("future integrations were enabled without settings storage")
	}
}

func TestDiscoveredCompaniesEndpointIsDormantUntilEnabled(t *testing.T) {
	t.Parallel()

	repository := newProspectStubRepository()
	repository.businesses = prospectTestBusinesses()
	_, mux := newProspectTestServer(t, repository)

	// While the toggle is off the Engine boundary answers the explanatory 403.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered?job_id=job-1", http.NoBody))
	body := recorder.Body.String()
	if recorder.Code != http.StatusForbidden ||
		!strings.Contains(body, futureIntegrationsDisabledCode) || !strings.Contains(body, "Settings") {
		t.Fatalf("dormant discovered = %d, body = %s", recorder.Code, body)
	}

	// The explicit opt-in restores the documented contract behaviour.
	repository.settings[settingProspectFutureIntegrations] = futureIntegrationsEnabledValue
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered?job_id=job-1", http.NoBody))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"providerCompanyId"`) {
		t.Fatalf("enabled discovered = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Request validation is unchanged once the surface is awake.
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered", http.NoBody))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enabled discovered without job_id = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProspectIntegrationsRoundTripEnabledToggle(t *testing.T) {
	t.Parallel()

	server, mux := newProspectTestServer(t, newProspectStubRepository())

	get := func() string {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/integrations", http.NoBody))
		if recorder.Code != http.StatusOK {
			t.Fatalf("integrations GET = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	put := func(payload string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/prospects/integrations", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", server.csrfToken)
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	// GET reports the additive field, defaulting to off.
	if body := get(); !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("default integrations GET lacks enabled=false: %s", body)
	}

	// PUT stores the toggle and echoes it back.
	response := put(`{"email_verifier_url":"","audit_service_url":"","enabled":true}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":true`) {
		t.Fatalf("enable PUT = %d, body = %s", response.Code, response.Body.String())
	}
	if !server.svc.FutureIntegrationsEnabled(context.Background()) {
		t.Fatal("PUT enabled=true did not persist the toggle")
	}

	// A URL-only PUT keeps the stored toggle: boundary URLs stay storable
	// while the surfaces are dormant and never flip the switch by omission.
	response = put(`{"email_verifier_url":"http://127.0.0.1:8085/verify","audit_service_url":""}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":true`) {
		t.Fatalf("URL-only PUT = %d, body = %s", response.Code, response.Body.String())
	}
	if body := get(); !strings.Contains(body, "http://127.0.0.1:8085/verify") || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("URL-only PUT changed the toggle or lost the URL: %s", body)
	}

	// PUT can switch the surfaces back off; the stored URL survives.
	response = put(`{"email_verifier_url":"http://127.0.0.1:8085/verify","audit_service_url":"","enabled":false}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("disable PUT = %d, body = %s", response.Code, response.Body.String())
	}
	if server.svc.FutureIntegrationsEnabled(context.Background()) {
		t.Fatal("PUT enabled=false did not persist the toggle")
	}
	if body := get(); !strings.Contains(body, "http://127.0.0.1:8085/verify") {
		t.Fatalf("disabling the toggle dropped the dormant URL: %s", body)
	}
}

func TestDiscoveredCompaniesExportFormatIsGatedByToggle(t *testing.T) {
	t.Parallel()

	repository := newExportCapableProspectRepository()
	repository.businesses = prospectTestBusinesses()
	server, _ := newProspectTestServer(t, repository)

	post := func(form string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(form))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		server.createResultsExport(recorder, request)
		return recorder
	}

	// The dormant Lead-Engine format is rejected before anything is created.
	response := post("name=lead-drop&format=discovered_companies&source_scope=all")
	if response.Code != http.StatusForbidden ||
		!strings.Contains(response.Body.String(), futureIntegrationsDisabledCode) {
		t.Fatalf("dormant export = %d, body = %s", response.Code, response.Body.String())
	}
	if len(repository.exports) != 0 {
		t.Fatalf("rejected export still registered %d record(s)", len(repository.exports))
	}

	// Local formats are untouched by the gate.
	response = post("name=local-drop&format=csv&source_scope=all")
	if response.Code != http.StatusCreated {
		t.Fatalf("csv export while dormant = %d, body = %s", response.Code, response.Body.String())
	}

	// With the opt-in stored, the format works exactly as before the gate.
	repository.settings[settingProspectFutureIntegrations] = futureIntegrationsEnabledValue
	response = post("name=lead-drop&format=discovered_companies&source_scope=all")
	if response.Code != http.StatusCreated {
		t.Fatalf("enabled export = %d, body = %s", response.Code, response.Body.String())
	}
	completed := 0
	for _, record := range repository.exports {
		if record.Format == exportFormatDiscoveredCompanies {
			if record.State != "completed" || record.RecordCount != 2 {
				t.Fatalf("enabled export record = %+v", record)
			}
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("discovered_companies exports registered = %d, want 1", completed)
	}
}
