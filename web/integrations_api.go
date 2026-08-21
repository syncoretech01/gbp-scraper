package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

type integrationAPIInput struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Enabled *bool    `json:"enabled,omitempty"`
	URL     string   `json:"url,omitempty"`
	Secret  string   `json:"secret,omitempty"`
	Events  []string `json:"events,omitempty"`
	Folder  string   `json:"folder,omitempty"`
	Driver  string   `json:"driver,omitempty"`
	Target  string   `json:"target,omitempty"`
	Table   string   `json:"table,omitempty"`
	DSN     string   `json:"dsn,omitempty"`
}

func (s *Server) registerIntegrationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/integrations", s.listIntegrationsAPI)
	mux.HandleFunc("POST /api/v1/integrations", s.saveIntegrationAPI)
	mux.HandleFunc("PUT /api/v1/integrations/{id}", s.saveIntegrationAPI)
	mux.HandleFunc("DELETE /api/v1/integrations/{id}", s.deleteIntegrationAPI)
	mux.HandleFunc("POST /api/v1/integrations/{id}/delete", s.deleteIntegrationAPI)
	mux.HandleFunc("POST /api/v1/integrations/{id}/test", s.testIntegrationAPI)
	mux.HandleFunc("GET /api/v1/integrations/deliveries", s.listIntegrationDeliveriesAPI)
	mux.HandleFunc("GET /api/v1/integrations/{id}/deliveries", s.listIntegrationDeliveriesAPI)
}

func (s *Server) listIntegrationsAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.svc.ListIntegrations(r.Context(), false, maximumIntegrations)
	if err != nil {
		if errors.Is(err, ErrIntegrationStoreUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "integrations_unavailable", "Local integrations are unavailable")
			return
		}
		renderLocalAPIError(w, http.StatusInternalServerError, "integration_list_failed", "Could not list local integrations")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: records})
}

// listIntegrationDeliveriesAPI returns recent delivery history so an operator
// can prove what a local automation tool was actually sent.
func (s *Server) listIntegrationDeliveriesAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id != "" && !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration_id", "Invalid integration ID")
		return
	}
	limit := maximumDeliveryHistory
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumDeliveryHistory {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit",
				fmt.Sprintf("limit must be between 1 and %d", maximumDeliveryHistory))
			return
		}
		limit = value
	}
	deliveries, err := s.svc.ListIntegrationDeliveries(r.Context(), id, limit)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "delivery_history_failed", "Could not read delivery history")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: deliveries})
}

func (s *Server) saveIntegrationAPI(w http.ResponseWriter, r *http.Request) {
	input, err := decodeIntegrationAPIInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration", err.Error())
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	if err := validateIntegrationName(input.Name); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration_name", err.Error())
		return
	}
	publicConfig, secretConfig, err := validateIntegrationConfiguration(input.Kind, integrationConfiguration{
		URL: strings.TrimSpace(input.URL), Secret: strings.TrimSpace(input.Secret), Events: input.Events,
		Folder: strings.TrimSpace(input.Folder), Driver: strings.TrimSpace(input.Driver),
		Target: strings.TrimSpace(input.Target), Table: strings.TrimSpace(input.Table),
		DSN: strings.TrimSpace(input.DSN),
	})
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration_configuration", err.Error())
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		id = uuid.NewString()
	} else if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration_id", "Invalid integration ID")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	record := IntegrationRecord{
		ID: id, Name: input.Name, Kind: input.Kind, Enabled: enabled,
		Configuration: publicConfig, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.svc.SaveIntegration(r.Context(), record, secretConfig); err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "integration_save_failed", "Could not save local integration")
		return
	}
	record.SecretSafe()
	if acceptsJSON(r) {
		renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: record})
		return
	}
	http.Redirect(w, r, "/app/api?notice=Integration+saved", http.StatusSeeOther)
}

func decodeIntegrationAPIInput(w http.ResponseWriter, r *http.Request) (integrationAPIInput, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumIntegrationPayload)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input integrationAPIInput
		if err := decoder.Decode(&input); err != nil {
			return integrationAPIInput{}, fmt.Errorf("invalid JSON: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return integrationAPIInput{}, fmt.Errorf("request must contain exactly one JSON object")
		}
		return input, nil
	}
	if err := parseBoundedRequestForm(w, r, maximumIntegrationPayload); err != nil {
		return integrationAPIInput{}, fmt.Errorf("invalid form")
	}
	enabled := formBoolean(r, "enabled", true)
	return integrationAPIInput{
		Name: r.FormValue("name"), Kind: r.FormValue("kind"), Enabled: &enabled,
		URL: r.FormValue("url"), Secret: r.FormValue("secret"), Events: r.Form["events"],
		Folder: r.FormValue("folder"), Driver: r.FormValue("driver"),
		Target: r.FormValue("target"), Table: r.FormValue("table"), DSN: r.FormValue("dsn"),
	}, nil
}

func (s *Server) deleteIntegrationAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration_id", "Invalid integration ID")
		return
	}
	if err := s.svc.DeleteIntegration(r.Context(), id); err != nil {
		if errors.Is(err, ErrIntegrationNotFound) {
			renderLocalAPIError(w, http.StatusNotFound, "integration_not_found", "Integration not found")
		} else {
			renderLocalAPIError(w, http.StatusInternalServerError, "integration_delete_failed", "Could not delete integration")
		}
		return
	}
	if acceptsJSON(r) || r.Method == http.MethodDelete {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Integration deleted"}})
		return
	}
	http.Redirect(w, r, "/app/api?notice=Integration+deleted", http.StatusSeeOther)
}

// testIntegrationAPI performs one on-demand delivery. Without an export ID it
// sends the signed integration.test event, which is what a local automation
// tool needs while wiring up a trigger; with one it replays a real export
// delivery end to end.
func (s *Server) testIntegrationAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_integration_id", "Invalid integration ID")
		return
	}
	integration, err := s.svc.GetIntegrationSecret(r.Context(), id)
	if err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "integration_not_found", "Integration not found")
		return
	}
	event := integrationEvent{
		Name: IntegrationEventTest, OccurredAt: time.Now().UTC(),
		Data: map[string]any{"message": "Local integration test delivery", "integration_id": id},
	}
	if exportID := strings.TrimSpace(r.FormValue("export_id")); exportID != "" {
		if !validBusinessID(exportID) {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_export_id", "Invalid export ID")
			return
		}
		record, path, exportErr := s.svc.GetExport(r.Context(), exportID)
		if exportErr != nil {
			renderLocalAPIError(w, http.StatusNotFound, "export_not_found", "Completed export not found")
			return
		}
		event.Name = IntegrationEventExportCompleted
		event.Data = exportEventData(record)
		event.Export = &record
		event.ExportPath = path
	}
	deliveryErr := s.deliverIntegrationEvent(r.Context(), integration, event)
	message := ""
	if deliveryErr != nil {
		message = jobruntime.RedactString(deliveryErr.Error())
	}
	if repository, repositoryErr := s.svc.integrationRepository(); repositoryErr == nil {
		_ = repository.RecordIntegrationRun(r.Context(), id, time.Now().UTC(), message)
	}
	if deliveryErr != nil {
		renderLocalAPIError(w, http.StatusBadGateway, "integration_delivery_failed", message)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Integration delivery succeeded"}})
}

func (record *IntegrationRecord) SecretSafe() {
	if record == nil {
		return
	}
	record.LastError = jobruntime.RedactString(record.LastError)
}

func acceptsJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") ||
		strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
}
