package web

import (
	"context"
	"time"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// ProspectClassification is one business's stored GBP prospecting state: the
// taxonomy status, the configurable worth-calling score with its tier, and
// the signals plus reasons that produced them.
type ProspectClassification struct {
	BusinessID string            `json:"business_id"`
	Status     string            `json:"status"`
	Score      float64           `json:"score"`
	Tier       string            `json:"tier"`
	Signals    prospect.Signals  `json:"signals"`
	Reasons    []prospect.Reason `json:"reasons"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// prospectRepository is the persistence surface the prospecting service layer
// type-asserts from the configured repository. RecomputeProspects refreshes
// the stored classification for the given businesses (all live businesses
// when the slice is empty) and returns how many rows it processed.
type prospectRepository interface {
	RecomputeProspects(ctx context.Context, weights prospect.ScoreWeights, businessIDs []string) (int64, error)
	ProspectSummary(ctx context.Context) (ProspectSummary, error)
}

// ProspectSummary aggregates the stored prospect columns for the dashboard.
type ProspectSummary struct {
	ByStatus []DashboardCountPoint `json:"by_status"`
	ByTier   []DashboardCountPoint `json:"by_tier"`
	Scored   int64                 `json:"scored"`
}
