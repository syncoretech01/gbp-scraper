package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The specification's Exports endpoint group names six operations: create,
// status, list, download, repeat, and delete. Each has to be reachable through
// the real handler chain, not merely defined as a method, and each has to fail
// with its own local error rather than a routing miss.

func TestExportsEndpointGroupIsReachableThroughTheRealHandlerChain(t *testing.T) {
	t.Parallel()

	server := newMaintenanceActionServer(t, t.TempDir())

	operations := []struct {
		name   string
		method string
		path   string
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/exports"},
		{name: "create", method: http.MethodPost, path: "/api/v1/exports"},
		{name: "presets", method: http.MethodGet, path: "/api/v1/exports/presets"},
		{name: "status", method: http.MethodGet, path: "/api/v1/exports/export-one"},
		{name: "download", method: http.MethodGet, path: "/api/v1/exports/export-one/download"},
		{name: "repeat", method: http.MethodPost, path: "/api/v1/exports/export-one/repeat"},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/exports/export-one"},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(operation.method, operation.path, http.NoBody)
			request.Header.Set("Accept", "application/json")
			request.Header.Set("X-CSRF-Token", server.csrfToken)

			recorder := httptest.NewRecorder()
			server.srv.Handler.ServeHTTP(recorder, request)

			if recorder.Code == http.StatusNotFound && strings.Contains(recorder.Body.String(), "page not found") {
				t.Fatalf("%s %s is not registered", operation.method, operation.path)
			}
			if recorder.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s is registered for another method only", operation.method, operation.path)
			}
		})
	}

	// Every one of those operations is also documented, so a script author
	// discovers the whole group from the generated OpenAPI document.
	documented := make(map[string]struct{}, len(localAPICatalogue()))
	for _, entry := range localAPICatalogue() {
		documented[entry.Method+" "+entry.Path] = struct{}{}
	}
	for _, key := range []string{
		"GET /api/v1/exports",
		"POST /api/v1/exports",
		"GET /api/v1/exports/{id}",
		"GET /api/v1/exports/{id}/download",
		"POST /api/v1/exports/{id}/repeat",
		"DELETE /api/v1/exports/{id}",
	} {
		if _, ok := documented[key]; !ok {
			t.Fatalf("the exports endpoint group does not document %s", key)
		}
	}
}
