package web

import (
	"context"
	"errors"
	"time"
)

// ErrDashboardAnalyticsUnsupported indicates that the active repository does
// not expose the inexpensive, database-backed dashboard projection.
var ErrDashboardAnalyticsUnsupported = errors.New("dashboard analytics are unavailable")

// DashboardCountPoint is one labelled aggregate used by the accessible chart
// tables on the local dashboard.
type DashboardCountPoint struct {
	Label string `json:"label"`
	Value int64  `json:"value"`
}

// DashboardJobTrend is one UTC day of terminal job outcomes.
type DashboardJobTrend struct {
	Label     string `json:"label"`
	Completed int64  `json:"completed"`
	Partial   int64  `json:"partial"`
	Failed    int64  `json:"failed"`
	Cancelled int64  `json:"cancelled"`
}

// DashboardSpeedTrend joins recorded worker throughput with durable block or
// rate-limit events for one UTC day.
type DashboardSpeedTrend struct {
	Label           string  `json:"label"`
	PlacesPerMinute float64 `json:"places_per_minute"`
	WarningEvents   int64   `json:"warning_events"`
	// BlockEvents counts the durable worker events that mean the platform or a
	// proxy refused the request — proxy failures, rate limits, and captcha
	// interruptions — rather than every warning.
	BlockEvents int64 `json:"block_events"`
	// BlockRatePercent expresses BlockEvents as a share of the day's task
	// outcomes: blocks / (blocks + tasks that finished that day) × 100. The
	// scraper engine publishes no per-request counter, so the denominator is
	// the durable task plan, which is the finest granularity the workspace
	// actually records. It stays zero on a day with no evidence at all.
	BlockRatePercent float64 `json:"block_rate_percent"`
}

// DashboardAvailabilitySummary reports unique-business availability. Counts
// deliberately use businesses rather than contact rows so percentages cannot
// exceed 100 when enrichment discovers several values for one business.
type DashboardAvailabilitySummary struct {
	Websites        int64 `json:"websites"`
	Emails          int64 `json:"emails"`
	Phones          int64 `json:"phones"`
	SocialProfiles  int64 `json:"social_profiles"`
	WebsiteActive   int64 `json:"website_active"`
	WebsiteInactive int64 `json:"website_inactive"`
}

// DashboardProxySummary is a credential-free operational projection over the
// locally stored proxy counters and health samples.
type DashboardProxySummary struct {
	Total                   int64                 `json:"total"`
	Enabled                 int64                 `json:"enabled"`
	Healthy                 int64                 `json:"healthy"`
	Successes               int64                 `json:"successes"`
	Failures                int64                 `json:"failures"`
	Blocks                  int64                 `json:"blocks"`
	AverageLatencyMS        float64               `json:"average_latency_ms"`
	LatencyDistribution     []DashboardCountPoint `json:"latency_distribution"`
	ReliabilityDistribution []DashboardCountPoint `json:"reliability_distribution"`
}

// DashboardAnalytics is a bounded, database-backed workspace projection. The
// repository caps high-cardinality breakdowns before they reach templates.
type DashboardAnalytics struct {
	CollectedToday   int64                        `json:"collected_today"`
	CollectedWeek    int64                        `json:"collected_week"`
	CollectedMonth   int64                        `json:"collected_month"`
	Availability     DashboardAvailabilitySummary `json:"availability"`
	CollectionByDate []DashboardCountPoint        `json:"collection_by_date"`
	Cities           []DashboardCountPoint        `json:"cities"`
	Categories       []DashboardCountPoint        `json:"categories"`
	Statuses         []DashboardCountPoint        `json:"statuses"`
	RatingBands      []DashboardCountPoint        `json:"rating_bands"`
	JobTrends        []DashboardJobTrend          `json:"job_trends"`
	SpeedTrends      []DashboardSpeedTrend        `json:"speed_trends"`
	Proxy            DashboardProxySummary        `json:"proxy"`

	// ProxyLatencyBuckets buckets only enabled proxies by their most recent
	// recorded latency sample (<200 ms, 200–499 ms, 500–999 ms, 1000+ ms,
	// Unknown). Disabled proxies are excluded because they can no longer be
	// selected for a scrape.
	ProxyLatencyBuckets []DashboardCountPoint `json:"proxy_latency_buckets"`
	// ProxyReliability reports one point per proxy pool: the label is the pool
	// name and the value is the success share as a whole percent (0–100),
	// computed as success/(success+failure) over the pool's stored counters.
	// Pools without a single recorded success or failure are omitted rather
	// than shown as a misleading 0%.
	ProxyReliability []DashboardCountPoint `json:"proxy_reliability"`
}

type dashboardAnalyticsRepository interface {
	DashboardAnalytics(context.Context, time.Time) (DashboardAnalytics, error)
}

// DashboardAnalytics returns dashboard aggregates since the supplied UTC
// instant. Existing embedders remain compatible because this is additive.
func (s *Service) DashboardAnalytics(ctx context.Context, since time.Time) (DashboardAnalytics, error) {
	repository, ok := s.repo.(dashboardAnalyticsRepository)
	if !ok {
		return DashboardAnalytics{}, ErrDashboardAnalyticsUnsupported
	}

	return repository.DashboardAnalytics(ctx, since.UTC())
}
