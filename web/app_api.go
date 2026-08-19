package web

import (
	"net/http"
	"strings"
)

type apiWorkspacePageData struct {
	BaseURL               string
	AuthenticationSummary string
	ExposedBeyondLoopback bool
	Endpoints             []apiEndpointView
	APIKeys               []APIKeyRecord
	Integrations          []IntegrationRecord
	RequestLogs           []APIRequestLog
	RateLimit             int64
	Notice                string
}

type apiEndpointView struct {
	Method      string
	Path        string
	Description string
}

func (s *Server) apiWorkspacePage(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		host = s.srv.Addr
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	activity, _ := s.appActivity(r)
	page := apiWorkspacePageData{
		BaseURL:               scheme + "://" + host,
		AuthenticationSummary: "Loopback-compatible API keys with read-only and full-access permissions",
		ExposedBeyondLoopback: wildcardBind(s.srv.Addr),
		RateLimit:             s.apiRateLimit.Load(),
		Notice:                strings.TrimSpace(r.URL.Query().Get("notice")),
		Endpoints: []apiEndpointView{
			{Method: "GET", Path: "/api/v1/jobs", Description: "List jobs"},
			{Method: "POST", Path: "/api/v1/jobs", Description: "Create a validated job"},
			{Method: "GET", Path: "/api/v1/jobs/{id}", Description: "Read a job and its configuration"},
			{Method: "POST", Path: "/api/v1/jobs/{id}/pause", Description: "Pause at a safe worker boundary"},
			{Method: "POST", Path: "/api/v1/jobs/{id}/resume", Description: "Resume a paused job"},
			{Method: "POST", Path: "/api/v1/jobs/{id}/cancel", Description: "Cancel and retain committed results"},
			{Method: "GET", Path: "/api/v1/jobs/{id}/events", Description: "Stream lifecycle events with SSE"},
			{Method: "GET", Path: "/api/v1/jobs/{id}/download", Description: "Download current compatible CSV"},
			{Method: "GET", Path: "/api/v1/results", Description: "Search and filter normalized businesses"},
			{Method: "GET", Path: "/api/v1/results/{id}", Description: "Read details, provenance and versions"},
			{Method: "POST", Path: "/api/v1/exports", Description: "Create a filtered, selected, saved-view, or full export"},
			{Method: "GET", Path: "/api/v1/exports/{id}", Description: "Read export status and verified parts"},
			{Method: "POST", Path: "/api/v1/exports/{id}/repeat", Description: "Repeat the exact export configuration"},
			{Method: "GET", Path: "/api/v1/integrations", Description: "List safe local integration metadata"},
			{Method: "GET", Path: "/api/v1/system/health", Description: "Read database and workspace health"},
			{Method: "POST", Path: "/api/v1/system/backups", Description: "Create a verified local SQLite backup"},
			{Method: "GET", Path: "/api/v1/ai/status", Description: "Check the optional loopback-only Ollama connection"},
			{Method: "POST", Path: "/api/v1/ai/assist", Description: "Run a bounded structured task on an enabled local model"},
		},
	}
	page.APIKeys, _ = s.svc.ListAPIKeys(r.Context(), 100)
	page.Integrations, _ = s.svc.ListIntegrations(r.Context(), false, maximumIntegrations)
	page.RequestLogs, _ = s.svc.ListAPIRequestLogs(r.Context(), 50)

	s.renderAppPage(w, "api", appPageData{
		Title:     "Local API",
		Subtitle:  "Use the versioned interface from scripts running on this computer.",
		ActiveNav: "api",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) apiOpenAPI(w http.ResponseWriter, _ *http.Request) {
	paths := map[string]any{
		"/api/v1/jobs": map[string]any{
			"get":  map[string]any{"summary": "List jobs", "responses": map[string]any{"200": map[string]string{"description": "Jobs"}}},
			"post": map[string]any{"summary": "Create job", "responses": map[string]any{"201": map[string]string{"description": "Created"}}},
		},
		"/api/v1/jobs/{id}": map[string]any{
			"get": map[string]any{"summary": "Get job", "responses": map[string]any{"200": map[string]string{"description": "Job"}}},
		},
		"/api/v1/jobs/{id}/events": map[string]any{
			"get": map[string]any{"summary": "Stream job events", "responses": map[string]any{"200": map[string]string{"description": "text/event-stream"}}},
		},
		"/api/v1/results": map[string]any{
			"get": map[string]any{"summary": "Search normalized results", "responses": map[string]any{"200": map[string]string{"description": "Results"}}},
		},
		"/api/v1/exports": map[string]any{
			"get":  map[string]any{"summary": "List export history", "responses": map[string]any{"200": map[string]string{"description": "Exports"}}},
			"post": map[string]any{"summary": "Create a configurable local export", "responses": map[string]any{"201": map[string]string{"description": "Created"}}},
		},
		"/api/v1/exports/{id}": map[string]any{
			"get":    map[string]any{"summary": "Get export status and parts", "responses": map[string]any{"200": map[string]string{"description": "Export"}}},
			"delete": map[string]any{"summary": "Delete export files and history", "responses": map[string]any{"200": map[string]string{"description": "Deleted"}}},
		},
		"/api/v1/integrations": map[string]any{
			"get":  map[string]any{"summary": "List local integrations", "responses": map[string]any{"200": map[string]string{"description": "Integrations"}}},
			"post": map[string]any{"summary": "Create a webhook, watch-folder, or explicitly enabled command hook", "responses": map[string]any{"201": map[string]string{"description": "Created"}}},
		},
		"/api/v1/api-keys": map[string]any{
			"get":  map[string]any{"summary": "List API-key metadata", "responses": map[string]any{"200": map[string]string{"description": "API keys"}}},
			"post": map[string]any{"summary": "Create an API key (token returned once)", "responses": map[string]any{"201": map[string]string{"description": "Created"}}},
		},
		"/api/v1/system/health": map[string]any{
			"get": map[string]any{"summary": "Read local health", "responses": map[string]any{"200": map[string]string{"description": "Health"}}},
		},
		"/api/v1/ai/status": map[string]any{
			"get": map[string]any{"summary": "Check optional local Ollama status", "responses": map[string]any{"200": map[string]string{"description": "Local AI status"}}},
		},
		"/api/v1/ai/assist": map[string]any{
			"post": map[string]any{"summary": "Run a bounded structured local-AI task", "responses": map[string]any{"200": map[string]string{"description": "Structured result"}}},
		},
	}
	renderJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.1.0",
		"info": map[string]string{
			"title":       "Google Maps Scraper Local API",
			"version":     "1.0.0",
			"description": "Loopback-first API. Local keys support read-only or full access; mutating same-origin browser requests also require CSRF.",
		},
		"servers": []map[string]string{{"url": "/"}},
		"components": map[string]any{"securitySchemes": map[string]any{
			"bearerAuth": map[string]string{"type": "http", "scheme": "bearer"},
			"apiKey":     map[string]string{"type": "apiKey", "in": "header", "name": "X-API-Key"},
		}},
		"security": []map[string][]string{{"bearerAuth": {}}, {"apiKey": {}}},
		"paths":    paths,
	})
}
