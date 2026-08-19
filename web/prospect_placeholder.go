// PLACEHOLDER — replaced at integration by the prospect workstream.
//
// This file only carries the minimal service surface the prospecting UI
// needs to compile and render honestly (capability gate, dashboard summary,
// and call-opener templates). The persistence and API workstreams ship the
// real implementations in web/prospects.go and web/prospect_types.go.
package web

import (
	"context"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// ProspectSummary aggregates stored prospect signals for the dashboard.
type ProspectSummary struct {
	ByStatus []DashboardCountPoint `json:"by_status"`
	ByTier   []DashboardCountPoint `json:"by_tier"`
	Scored   int64                 `json:"scored"`
}

// SupportsProspects reports whether prospect signals can be stored and read.
func (s *Service) SupportsProspects() bool {
	if s == nil || s.repo == nil {
		return false
	}
	_, ok := s.repo.(ResultRepository)

	return ok
}

// ProspectSummaryData returns dashboard counts by prospect status and tier.
// The placeholder reports an honest empty summary.
func (s *Service) ProspectSummaryData(_ context.Context) (ProspectSummary, error) {
	if !s.SupportsProspects() {
		return ProspectSummary{}, ErrResultStoreUnsupported
	}

	return ProspectSummary{}, nil
}

// ProspectOpenerTemplates returns the call-opener templates keyed by prospect
// status, with a "default" fallback template.
func (s *Service) ProspectOpenerTemplates(_ context.Context) (map[string]string, error) {
	if !s.SupportsProspects() {
		return nil, ErrResultStoreUnsupported
	}

	return prospect.DefaultOpenerTemplates(), nil
}
