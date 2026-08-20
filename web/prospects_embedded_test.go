package web

import (
	"strings"
	"testing"
)

// TestGenerateProspectQueriesUsesEmbeddedZIPDataset proves that query
// generation without an uploaded CSV draws from the full embedded US
// ZIP dataset rather than the 60-ZIP sample: Wyoming is absent from
// the sample, and the pool of considered ZIPs exceeds 40000.
func TestGenerateProspectQueriesUsesEmbeddedZIPDataset(t *testing.T) {
	t.Parallel()

	service := NewService(newProspectStubRepository(), t.TempDir())

	const topN = 5

	plan, err := service.GenerateProspectQueries("WY", "", topN, []string{"dentist"}, nil)
	if err != nil {
		t.Fatalf("GenerateProspectQueries(WY) error = %v", err)
	}

	if plan.ZIPCount != topN || len(plan.Queries) != topN {
		t.Fatalf("plan = %+v, want %d WY queries", plan, topN)
	}

	for _, query := range plan.Queries {
		if !strings.Contains(query, " WY ") {
			t.Fatalf("query %q does not target Wyoming", query)
		}
	}

	const minimumEmbeddedPool = 40000
	if pool := plan.ZIPCount + plan.Skipped; pool < minimumEmbeddedPool {
		t.Fatalf("query generation considered %d ZIP areas, want at least %d (embedded dataset)", pool, minimumEmbeddedPool)
	}
}
