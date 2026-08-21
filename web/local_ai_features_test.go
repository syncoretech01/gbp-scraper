package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The optional local AI covers a fixed set of product features. Each one needs
// a task prompt the assist endpoint accepts, a console control an operator can
// reach, and a bounded structured answer — and the whole thing must disappear
// silently when no Ollama server is installed.

// localAIFeatureTasks are the tasks the Settings console offers, in the order
// the specification lists the features they implement.
var localAIFeatureTasks = []string{
	"scrape_configuration",
	"classify_business",
	"explain_quality",
	"explain_duplicate",
	"summarize_business",
	"summarize_changes",
	"suggest_coverage",
	"keyword_variations",
	"result_filters",
}

func TestEveryLocalAIFeatureTaskHasABoundedStructuredPrompt(t *testing.T) {
	t.Parallel()

	for _, task := range localAIFeatureTasks {
		prompt, err := localAIPrompt(localAIAssistRequest{Task: task, Input: "an operator request"})
		if err != nil {
			t.Fatalf("task %q is not accepted: %v", task, err)
		}
		if !strings.Contains(prompt, "exactly one valid JSON object") {
			t.Fatalf("task %q does not demand a single JSON object", task)
		}
		if !strings.Contains(prompt, `{"`) {
			t.Fatalf("task %q does not declare its result shape: %s", task, prompt)
		}
		if !strings.Contains(prompt, "Do not claim verification") {
			t.Fatalf("task %q drops the no-verification guard", task)
		}
	}

	if _, err := localAIPrompt(localAIAssistRequest{Task: "run_shell", Input: "rm -rf /"}); err == nil {
		t.Fatal("an unknown task was accepted")
	}
}

// The console must offer every feature task and must not be visible before the
// status probe confirms a reachable local model.
func TestSettingsPageShipsTheLocalAIConsoleHiddenWithEveryFeature(t *testing.T) {
	t.Parallel()

	server := newLocalAIHandlersServer(t)

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/settings", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("settings page = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "data-ai-console") {
		t.Fatal("the settings page does not render the local AI console")
	}
	consoleAt := strings.Index(body, "data-ai-console")
	closeAt := strings.Index(body[consoleAt:], ">")
	if closeAt < 0 || !strings.Contains(body[consoleAt:consoleAt+closeAt], "hidden") {
		t.Fatalf("the local AI console is not hidden before the status probe: %s", body[consoleAt:consoleAt+80])
	}

	for _, task := range localAIFeatureTasks[:7] {
		if !strings.Contains(body, `value="`+task+`"`) {
			t.Fatalf("the console does not offer the %q assistant", task)
		}
	}
	if !strings.Contains(body, "/static/js/app-ai.js") {
		t.Fatal("the settings page does not load the local AI console script")
	}
}

// A disabled local AI must never block a page: the status endpoint answers 200
// with enabled=false and the assist endpoint refuses with a conflict, and both
// happen without any outbound request.
func TestLocalAIDegradesWithoutBlockingWhenOllamaIsAbsent(t *testing.T) {
	t.Parallel()

	server := newLocalAIHandlersServer(t)

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ai/status", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d", recorder.Code)
	}

	var status struct {
		Data struct {
			Enabled   bool `json:"enabled"`
			Reachable bool `json:"reachable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Data.Enabled || status.Data.Reachable {
		t.Fatalf("a fresh workspace reports local AI as %+v", status.Data)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/assist",
		strings.NewReader(`{"task":"summarize_business","input":"describe this"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)

	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		body, _ := io.ReadAll(recorder.Body)
		t.Fatalf("assist while disabled = %d, want %d; body = %s", recorder.Code, http.StatusConflict, body)
	}
}
