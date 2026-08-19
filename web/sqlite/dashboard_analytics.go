package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

const dashboardBreakdownLimit = 10

// DashboardAnalytics returns bounded aggregates directly from normalized
// tables so the dashboard never has to load every business into Go memory.
func (repo *repo) DashboardAnalytics(ctx context.Context, since time.Time) (web.DashboardAnalytics, error) {
	analytics := web.DashboardAnalytics{}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	week := today.AddDate(0, 0, -6)
	month := today.AddDate(0, 0, -29)

	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN first_seen_at >= ? THEN 1 END),
			COUNT(CASE WHEN first_seen_at >= ? THEN 1 END),
			COUNT(CASE WHEN first_seen_at >= ? THEN 1 END),
			COUNT(CASE WHEN website <> '' THEN 1 END),
			COUNT(CASE WHEN EXISTS (SELECT 1 FROM emails WHERE emails.business_id = businesses.id) THEN 1 END),
			COUNT(CASE WHEN normalized_phone <> '' OR EXISTS (SELECT 1 FROM phones WHERE phones.business_id = businesses.id) THEN 1 END),
			COUNT(CASE WHEN EXISTS (SELECT 1 FROM social_profiles WHERE social_profiles.business_id = businesses.id)
				OR (social_profiles NOT IN ('', '{}', '[]', 'null')) THEN 1 END),
			COUNT(CASE WHEN website <> '' AND website_status = 'active' THEN 1 END),
			COUNT(CASE WHEN website <> '' AND website_status IN ('inactive', 'error') THEN 1 END)
		FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL`,
		today.Unix(), week.Unix(), month.Unix(),
	).Scan(
		&analytics.CollectedToday,
		&analytics.CollectedWeek,
		&analytics.CollectedMonth,
		&analytics.Availability.Websites,
		&analytics.Availability.Emails,
		&analytics.Availability.Phones,
		&analytics.Availability.SocialProfiles,
		&analytics.Availability.WebsiteActive,
		&analytics.Availability.WebsiteInactive,
	); err != nil {
		return web.DashboardAnalytics{}, fmt.Errorf("read dashboard summary: %w", err)
	}

	var err error
	analytics.CollectionByDate, err = repo.dashboardCountPoints(ctx, `
		SELECT date(first_seen_at, 'unixepoch') AS label, COUNT(*) AS value
		FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL AND first_seen_at >= ?
		GROUP BY label ORDER BY label`, since.Unix())
	if err != nil {
		return web.DashboardAnalytics{}, fmt.Errorf("read collection trend: %w", err)
	}
	analytics.Cities, err = repo.dashboardCountPoints(ctx, `
		SELECT COALESCE(NULLIF(TRIM(city), ''), 'Unknown') AS label, COUNT(*) AS value
		FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL
		GROUP BY label ORDER BY value DESC, label LIMIT ?`, dashboardBreakdownLimit)
	if err != nil {
		return web.DashboardAnalytics{}, fmt.Errorf("read city breakdown: %w", err)
	}
	analytics.Categories, err = repo.dashboardCountPoints(ctx, `
		SELECT COALESCE(NULLIF(TRIM(primary_category), ''), 'Unknown') AS label, COUNT(*) AS value
		FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL
		GROUP BY label ORDER BY value DESC, label LIMIT ?`, dashboardBreakdownLimit)
	if err != nil {
		return web.DashboardAnalytics{}, fmt.Errorf("read category breakdown: %w", err)
	}
	analytics.Statuses, err = repo.dashboardCountPoints(ctx, `
		SELECT COALESCE(NULLIF(TRIM(business_status), ''), 'Unknown') AS label, COUNT(*) AS value
		FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL
		GROUP BY label ORDER BY value DESC, label LIMIT ?`, dashboardBreakdownLimit)
	if err != nil {
		return web.DashboardAnalytics{}, fmt.Errorf("read status breakdown: %w", err)
	}
	analytics.RatingBands, err = repo.dashboardCountPoints(ctx, `
		SELECT CASE
			WHEN rating IS NULL THEN 'Not rated'
			WHEN rating < 2 THEN 'Under 2.0'
			WHEN rating < 3 THEN '2.0–2.9'
			WHEN rating < 4 THEN '3.0–3.9'
			WHEN rating < 4.5 THEN '4.0–4.4'
			ELSE '4.5–5.0' END AS label,
			COUNT(*) AS value
		FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL
		GROUP BY label
		ORDER BY CASE label WHEN 'Under 2.0' THEN 1 WHEN '2.0–2.9' THEN 2
			WHEN '3.0–3.9' THEN 3 WHEN '4.0–4.4' THEN 4 WHEN '4.5–5.0' THEN 5 ELSE 6 END`, nil)
	if err != nil {
		return web.DashboardAnalytics{}, fmt.Errorf("read rating breakdown: %w", err)
	}

	if analytics.JobTrends, err = repo.dashboardJobTrends(ctx, since); err != nil {
		return web.DashboardAnalytics{}, err
	}
	if analytics.SpeedTrends, err = repo.dashboardSpeedTrends(ctx, since); err != nil {
		return web.DashboardAnalytics{}, err
	}
	if analytics.Proxy, err = repo.dashboardProxySummary(ctx); err != nil {
		return web.DashboardAnalytics{}, err
	}
	if analytics.ProxyLatencyBuckets, err = repo.dashboardProxyLatencyBuckets(ctx); err != nil {
		return web.DashboardAnalytics{}, err
	}
	if analytics.ProxyReliability, err = repo.dashboardProxyPoolReliability(ctx); err != nil {
		return web.DashboardAnalytics{}, err
	}

	return analytics, nil
}

// dashboardProxyLatencyBuckets buckets enabled proxies by their most recent
// latency sample. The newest proxy_health row wins so a stale counter cannot
// misrepresent a proxy that has degraded; proxies.latency_ms is only a
// fallback for rows recorded before health sampling existed.
func (repo *repo) dashboardProxyLatencyBuckets(ctx context.Context) ([]web.DashboardCountPoint, error) {
	points, err := repo.dashboardCountPoints(ctx, `
		SELECT CASE
			WHEN last_latency IS NULL THEN 'Unknown'
			WHEN last_latency < 200 THEN '<200 ms'
			WHEN last_latency < 500 THEN '200–499 ms'
			WHEN last_latency < 1000 THEN '500–999 ms'
			ELSE '1000+ ms' END AS label,
			COUNT(*) AS value
		FROM (
			SELECT COALESCE(
				(SELECT proxy_health.latency_ms FROM proxy_health
					WHERE proxy_health.proxy_id = proxies.id
					ORDER BY proxy_health.checked_at DESC, proxy_health.id DESC LIMIT 1),
				proxies.latency_ms) AS last_latency
			FROM proxies WHERE proxies.enabled = 1
		)
		GROUP BY label
		ORDER BY CASE label WHEN '<200 ms' THEN 1 WHEN '200–499 ms' THEN 2
			WHEN '500–999 ms' THEN 3 WHEN '1000+ ms' THEN 4 ELSE 5 END`, nil)
	if err != nil {
		return nil, fmt.Errorf("read proxy latency buckets: %w", err)
	}

	return points, nil
}

// dashboardProxyPoolReliability returns success/(success+failure) per pool as
// a whole percent. The HAVING clause both bounds the output to pools with real
// evidence and guards the division against a zero denominator.
func (repo *repo) dashboardProxyPoolReliability(ctx context.Context) ([]web.DashboardCountPoint, error) {
	points, err := repo.dashboardCountPoints(ctx, `
		SELECT proxy_pools.name AS label,
			CAST(ROUND(100.0 * SUM(proxies.success_count)
				/ (SUM(proxies.success_count) + SUM(proxies.failure_count))) AS INTEGER) AS value
		FROM proxy_pools
		JOIN proxy_pool_members ON proxy_pool_members.pool_id = proxy_pools.id
		JOIN proxies ON proxies.id = proxy_pool_members.proxy_id
		GROUP BY proxy_pools.id
		HAVING SUM(proxies.success_count) + SUM(proxies.failure_count) > 0
		ORDER BY label LIMIT ?`, dashboardBreakdownLimit)
	if err != nil {
		return nil, fmt.Errorf("read proxy pool reliability: %w", err)
	}

	return points, nil
}

func (repo *repo) dashboardCountPoints(ctx context.Context, query string, argument any) ([]web.DashboardCountPoint, error) {
	var (
		rows interface {
			Next() bool
			Scan(...any) error
			Err() error
			Close() error
		}
		err error
	)
	if argument == nil {
		rows, err = repo.db.QueryContext(ctx, query)
	} else {
		rows, err = repo.db.QueryContext(ctx, query, argument)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]web.DashboardCountPoint, 0)
	for rows.Next() {
		var point web.DashboardCountPoint
		if err := rows.Scan(&point.Label, &point.Value); err != nil {
			return nil, err
		}
		points = append(points, point)
	}

	return points, rows.Err()
}

func (repo *repo) dashboardJobTrends(ctx context.Context, since time.Time) ([]web.DashboardJobTrend, error) {
	rows, err := repo.db.QueryContext(ctx, `
		SELECT date(COALESCE(job_runtime.finished_at, job_runtime.updated_at), 'unixepoch') AS label,
			SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END),
			SUM(CASE WHEN state = 'partial' THEN 1 ELSE 0 END),
			SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END),
			SUM(CASE WHEN state = 'cancelled' THEN 1 ELSE 0 END)
		FROM job_runtime
		WHERE COALESCE(finished_at, updated_at) >= ?
		GROUP BY label ORDER BY label`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("query job outcome trends: %w", err)
	}
	defer rows.Close()

	trends := make([]web.DashboardJobTrend, 0)
	for rows.Next() {
		var trend web.DashboardJobTrend
		if err := rows.Scan(&trend.Label, &trend.Completed, &trend.Partial, &trend.Failed, &trend.Cancelled); err != nil {
			return nil, fmt.Errorf("scan job outcome trend: %w", err)
		}
		trends = append(trends, trend)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read job outcome trends: %w", err)
	}

	return trends, nil
}

func (repo *repo) dashboardSpeedTrends(ctx context.Context, since time.Time) ([]web.DashboardSpeedTrend, error) {
	rows, err := repo.db.QueryContext(ctx, `
		WITH dates AS (
			SELECT date(updated_at, 'unixepoch') AS label,
				AVG(CASE WHEN places_per_minute > 0 THEN places_per_minute END) AS speed
			FROM job_progress WHERE updated_at >= ? GROUP BY label
		), warnings AS (
			-- The scraper engine emits no block-rate callback, so this counts the
			-- degradation events the worker really does record (low disk, adaptive
			-- concurrency changes, task errors) rather than a rate that cannot be
			-- measured. See docs/technical-limitations.md.
			SELECT date(created_at, 'unixepoch') AS label, COUNT(*) AS count
			FROM job_events
			WHERE created_at >= ? AND severity IN ('warning', 'error')
			GROUP BY label
		), labels AS (
			SELECT label FROM dates UNION SELECT label FROM warnings
		)
		SELECT labels.label, COALESCE(dates.speed, 0), COALESCE(warnings.count, 0)
		FROM labels LEFT JOIN dates USING(label) LEFT JOIN warnings USING(label)
		ORDER BY labels.label`, since.Unix(), since.Unix())
	if err != nil {
		return nil, fmt.Errorf("query speed and warning trends: %w", err)
	}
	defer rows.Close()

	trends := make([]web.DashboardSpeedTrend, 0)
	for rows.Next() {
		var trend web.DashboardSpeedTrend
		if err := rows.Scan(&trend.Label, &trend.PlacesPerMinute, &trend.WarningEvents); err != nil {
			return nil, fmt.Errorf("scan speed and warning trend: %w", err)
		}
		trends = append(trends, trend)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read speed and warning trends: %w", err)
	}

	return trends, nil
}

func (repo *repo) dashboardProxySummary(ctx context.Context) (web.DashboardProxySummary, error) {
	summary := web.DashboardProxySummary{}
	if err := repo.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN enabled = 1 AND status IN ('healthy', 'slow') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(success_count), 0), COALESCE(SUM(failure_count), 0),
			COALESCE(SUM(block_count), 0), COALESCE(AVG(latency_ms), 0)
		FROM proxies`).Scan(
		&summary.Total,
		&summary.Enabled,
		&summary.Healthy,
		&summary.Successes,
		&summary.Failures,
		&summary.Blocks,
		&summary.AverageLatencyMS,
	); err != nil {
		return web.DashboardProxySummary{}, fmt.Errorf("read proxy summary: %w", err)
	}

	var err error
	summary.LatencyDistribution, err = repo.dashboardCountPoints(ctx, `
		SELECT CASE WHEN latency_ms IS NULL THEN 'Not tested' WHEN latency_ms < 250 THEN '<250 ms'
			WHEN latency_ms < 750 THEN '250–749 ms' WHEN latency_ms < 1500 THEN '750–1499 ms'
			ELSE '1500+ ms' END AS label, COUNT(*) AS value
		FROM proxies GROUP BY label
		ORDER BY CASE label WHEN '<250 ms' THEN 1 WHEN '250–749 ms' THEN 2
			WHEN '750–1499 ms' THEN 3 WHEN '1500+ ms' THEN 4 ELSE 5 END`, nil)
	if err != nil {
		return web.DashboardProxySummary{}, fmt.Errorf("read proxy latency distribution: %w", err)
	}
	summary.ReliabilityDistribution, err = repo.dashboardCountPoints(ctx, `
		SELECT CASE WHEN success_count + failure_count = 0 THEN 'Not tested'
			WHEN (100.0 * success_count / (success_count + failure_count)) >= 95 THEN '95–100%'
			WHEN (100.0 * success_count / (success_count + failure_count)) >= 80 THEN '80–94%'
			WHEN (100.0 * success_count / (success_count + failure_count)) >= 50 THEN '50–79%'
			ELSE 'Under 50%' END AS label, COUNT(*) AS value
		FROM proxies GROUP BY label
		ORDER BY CASE label WHEN '95–100%' THEN 1 WHEN '80–94%' THEN 2
			WHEN '50–79%' THEN 3 WHEN 'Under 50%' THEN 4 ELSE 5 END`, nil)
	if err != nil {
		return web.DashboardProxySummary{}, fmt.Errorf("read proxy reliability distribution: %w", err)
	}

	return summary, nil
}
