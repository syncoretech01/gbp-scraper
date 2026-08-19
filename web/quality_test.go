package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultQualityRulesAreValidAndBounded(t *testing.T) {
	t.Parallel()

	rules := DefaultQualityRuleSet()
	if err := ValidateQualityRuleSet(rules); err != nil {
		t.Fatalf("ValidateQualityRuleSet(default) error = %v", err)
	}
	rules.RatingThreshold = 6
	if err := ValidateQualityRuleSet(rules); err == nil {
		t.Fatal("ValidateQualityRuleSet() accepted rating threshold above 5")
	}
	rules = DefaultQualityRuleSet()
	rules.OpenWeight = -1
	if err := ValidateQualityRuleSet(rules); err == nil {
		t.Fatal("ValidateQualityRuleSet() accepted a negative weight")
	}
}

func TestDecodeBoundedQualityJSONIsStrict(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("PUT", "/api/v1/quality/rules", strings.NewReader(`{"name":"x","unknown":true}`))
	var rules QualityRuleSet
	if err := decodeBoundedQualityJSON(httptest.NewRecorder(), request, &rules); err == nil {
		t.Fatal("decodeBoundedQualityJSON() accepted an unknown field")
	}

	request = httptest.NewRequest("PUT", "/api/v1/quality/rules", strings.NewReader(`{"name":"x"} {}`))
	if err := decodeBoundedQualityJSON(httptest.NewRecorder(), request, &rules); err == nil {
		t.Fatal("decodeBoundedQualityJSON() accepted trailing JSON")
	}
}
