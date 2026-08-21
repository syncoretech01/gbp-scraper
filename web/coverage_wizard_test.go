package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// coverageWizardForm builds a parsed POST form from the given wizard fields.
func coverageWizardForm(t *testing.T, values map[string]string) *http.Request {
	t.Helper()

	encoded := url.Values{}
	for key, value := range values {
		encoded.Set(key, value)
	}

	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(encoded.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if err := request.ParseForm(); err != nil {
		t.Fatalf("parse wizard form: %v", err)
	}

	return request
}

func TestWizardCoverageOptionsCarryTheZeroYieldChoice(t *testing.T) {
	t.Parallel()

	// No coverage fields at all keeps exactly the historical behaviour.
	options, err := wizardCoverageOptions(coverageWizardForm(t, map[string]string{}))
	if err != nil || options != nil {
		t.Fatalf("empty form options = %#v (%v), want nil", options, err)
	}

	// The knob absent from a form that does carry coverage fields leaves the
	// choice unset, so it follows AutoStop.
	options, err = wizardCoverageOptions(coverageWizardForm(t, map[string]string{
		"coverage_auto_stop": "on",
	}))
	if err != nil || options == nil || options.StopOnEmptyWindow != nil {
		t.Fatalf("implicit options = %#v (%v)", options, err)
	}

	if !options.StopOnEmptyWindowOrDefault() {
		t.Fatal("an implicit zero-yield choice must follow AutoStop")
	}

	// Present and unticked is an explicit "no", which must survive.
	options, err = wizardCoverageOptions(coverageWizardForm(t, map[string]string{
		"coverage_auto_stop":                "on",
		"coverage_stop_on_empty_window_set": "1",
	}))
	if err != nil || options == nil || options.StopOnEmptyWindow == nil || *options.StopOnEmptyWindow {
		t.Fatalf("explicit off options = %#v (%v)", options, err)
	}

	if options.StopOnEmptyWindowOrDefault() {
		t.Fatal("an explicit off choice must beat AutoStop")
	}

	// Present and ticked without AutoStop is an explicit "yes".
	options, err = wizardCoverageOptions(coverageWizardForm(t, map[string]string{
		"coverage_stop_on_empty_window":     "on",
		"coverage_stop_on_empty_window_set": "1",
	}))
	if err != nil || options == nil || !options.StopOnEmptyWindowOrDefault() || options.AutoStop {
		t.Fatalf("explicit on options = %#v (%v)", options, err)
	}
}
