package web

import (
	"context"
	"net/http"
)

// The application is fully standalone: the Lead-Engine/CRM surfaces below are
// dormant design boundaries that stay off until an operator explicitly turns
// them on. The toggle is stored under settingProspectFutureIntegrations and
// only futureIntegrationsEnabledValue counts as on, so a missing key, an
// empty value, or unavailable settings storage all keep the surfaces off.
const (
	settingProspectFutureIntegrations = "prospect.future_integrations"
	futureIntegrationsEnabledValue    = "enabled"
	futureIntegrationsDisabledCode    = "future_integrations_disabled"
)

// exportFormatDiscoveredCompanies is the only export format that exists for
// the future Lead-Engine ingestion contract rather than for local use.
const exportFormatDiscoveredCompanies = "discovered_companies"

// futureIntegrationsDisabledMessage explains the gate and how to lift it.
const futureIntegrationsDisabledMessage = "This surface serves the future Lead-Engine integration and is dormant: " +
	"the application is fully standalone and never calls an external service. " +
	"Enable it deliberately under Settings > Future integrations (dormant) if you need the Lead-Engine contract surfaces."

// FutureIntegrationsEnabled reports whether the operator explicitly enabled
// the dormant Lead-Engine integration surfaces. It defaults to off and stays
// off whenever settings storage is unavailable.
func (s *Service) FutureIntegrationsEnabled(ctx context.Context) bool {
	values, err := s.LoadSettings(ctx)
	if err != nil {
		return false
	}
	return values[settingProspectFutureIntegrations] == futureIntegrationsEnabledValue
}

// ProspectIntegrationConfig extends the stored boundary URLs with the
// future-integrations toggle. The URL fields stay dormant configuration:
// they are validated and stored but never called, whether or not the toggle
// is on.
type ProspectIntegrationConfig struct {
	ProspectIntegrationSettings
	// Enabled reports (GET) and stores (PUT) the future-integrations toggle.
	// A PUT that omits the field keeps the stored value, so pre-toggle
	// clients that only manage the boundary URLs cannot flip the dormant
	// surfaces by accident.
	Enabled *bool `json:"enabled"`
}

// ProspectIntegrationConfiguration returns the stored boundary URLs together
// with the future-integrations toggle state.
func (s *Service) ProspectIntegrationConfiguration(ctx context.Context) (ProspectIntegrationConfig, error) {
	settings, err := s.ProspectIntegrations(ctx)
	if err != nil {
		return ProspectIntegrationConfig{}, err
	}
	enabled := s.FutureIntegrationsEnabled(ctx)
	return ProspectIntegrationConfig{
		ProspectIntegrationSettings: settings,
		Enabled:                     &enabled,
	}, nil
}

// SaveProspectIntegrationConfiguration persists the boundary URLs and, when
// the request carries the field, the future-integrations toggle. Storing
// boundary URLs stays allowed while the toggle is off: they are dormant
// configuration for a future workstream.
func (s *Service) SaveProspectIntegrationConfiguration(ctx context.Context, config ProspectIntegrationConfig) error {
	if err := s.SaveProspectIntegrations(ctx, config.ProspectIntegrationSettings); err != nil {
		return err
	}
	if config.Enabled == nil {
		return nil
	}
	stored := ""
	if *config.Enabled {
		stored = futureIntegrationsEnabledValue
	}
	return s.SaveSettings(ctx, map[string]string{settingProspectFutureIntegrations: stored})
}

// requireFutureIntegrations gates one dormant Lead-Engine surface. It renders
// the explanatory 403 and returns false while the toggle is off.
func (s *Server) requireFutureIntegrations(w http.ResponseWriter, r *http.Request) bool {
	if s.svc.FutureIntegrationsEnabled(r.Context()) {
		return true
	}
	renderLocalAPIError(w, http.StatusForbidden, futureIntegrationsDisabledCode, futureIntegrationsDisabledMessage)
	return false
}

// allowExportFormat rejects export formats that only exist for the dormant
// Lead-Engine boundary while the future-integrations toggle is off. Every
// local format passes untouched.
func (s *Server) allowExportFormat(w http.ResponseWriter, r *http.Request, format string) bool {
	if format != exportFormatDiscoveredCompanies {
		return true
	}
	return s.requireFutureIntegrations(w, r)
}
