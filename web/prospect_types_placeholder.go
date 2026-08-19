// PLACEHOLDER — replaced at integration by the prospect workstream.
//
// These declarations mirror the persistence workstream's prospect_types.go so
// the service and API layers compile in isolation. The integrator deletes
// this file by its marker comment.

package web

import (
	"context"
	"time"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// ProspectClassification is one stored per-business prospect evaluation.
type ProspectClassification struct {
	BusinessID string            `json:"business_id"`
	Status     string            `json:"status"`
	Score      float64           `json:"score"`
	Tier       string            `json:"tier"`
	Reasons    []prospect.Reason `json:"reasons,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ProspectSummary aggregates stored prospect classifications for dashboards.
type ProspectSummary struct {
	ByStatus []DashboardCountPoint `json:"by_status"`
	ByTier   []DashboardCountPoint `json:"by_tier"`
	Scored   int64                 `json:"scored"`
}

// prospectRepository is the additive persistence capability for GBP
// prospecting signals.
type prospectRepository interface {
	RecomputeProspects(context.Context, prospect.ScoreWeights, []string) (int64, error)
	ProspectSummary(context.Context) (ProspectSummary, error)
}
