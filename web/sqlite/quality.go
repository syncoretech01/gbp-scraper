package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

type qualityInput struct {
	businessStatus  string
	website         string
	phone           string
	category        string
	address         string
	description     string
	latitude        sql.NullFloat64
	longitude       sql.NullFloat64
	rating          sql.NullFloat64
	reviewCount     sql.NullInt64
	placeID         string
	cid             string
	dataID          string
	lastSeenAt      int64
	websiteStatus   string
	websiteHTTPS    sql.NullInt64
	responseTimeMS  sql.NullInt64
	pageTitle       string
	metaDescription string
	websiteAudited  int64
	emailCount      int64
	validEmailCount int64
	socialCount     int64
	// websiteState is the canonical website state resolved from the same
	// durable evidence the rest of the application reads. Quality scoring
	// consumes the state rather than the raw transport status, so an
	// unaudited site can never be scored as if it had been checked.
	websiteState string
	// socialPlatform names the network when the listed URL is a social
	// profile rather than a site the business owns.
	socialPlatform string
	// sharedDomainEvidence reports that the website evidence came from
	// another business on the same domain.
	sharedDomainEvidence bool
}

// ActiveQualityRules returns the one active immutable scoring rule version.
func (repo *repo) ActiveQualityRules(ctx context.Context) (web.QualityRuleSet, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("start quality rule read: %w", err)
	}
	rules, err := ensureActiveQualityRules(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return web.QualityRuleSet{}, err
	}
	if err := tx.Commit(); err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("commit quality rule initialization: %w", err)
	}

	return rules, nil
}

// SaveQualityRules stores and activates a content-addressed rule version.
func (repo *repo) SaveQualityRules(ctx context.Context, rules web.QualityRuleSet) (web.QualityRuleSet, error) {
	if err := web.ValidateQualityRuleSet(rules); err != nil {
		return web.QualityRuleSet{}, err
	}
	rules.Version = qualityRuleVersion(rules)
	encoded, err := json.Marshal(rules)
	if err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("encode quality rules: %w", err)
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("start quality rule update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE quality_rule_sets SET active = 0 WHERE active = 1"); err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("deactivate quality rules: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO quality_rule_sets(version, name, rules, active, created_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(version) DO UPDATE SET active = 1`,
		rules.Version,
		rules.Name,
		string(encoded),
		time.Now().UTC().Unix(),
	); err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("save quality rules: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, details, created_at)
		VALUES ('quality_rules_activated', 'quality_rule_set', ?, ?)`,
		mustJSON(map[string]string{"version": rules.Version, "name": rules.Name}, "{}"),
		time.Now().UTC().Unix(),
	); err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("audit quality rules: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("commit quality rules: %w", err)
	}

	return rules, nil
}

// BusinessQuality returns a stored score, calculating a missing breakdown on
// demand for databases upgraded from an earlier schema.
func (repo *repo) BusinessQuality(ctx context.Context, id string) (web.BusinessQualityReport, error) {
	var componentCount int64
	err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM business_score_components WHERE business_id = ? AND rule_version =
			(SELECT scoring_rule_version FROM businesses WHERE id = ?)`,
		id,
		id,
	).Scan(&componentCount)
	if err != nil {
		return web.BusinessQualityReport{}, fmt.Errorf("inspect business quality: %w", err)
	}
	if componentCount == 0 {
		if _, err := repo.RecalculateQuality(ctx, []string{id}); err != nil {
			return web.BusinessQualityReport{}, err
		}
	}

	var report web.BusinessQualityReport
	var evaluatedAt int64
	err = repo.db.QueryRowContext(
		ctx,
		`SELECT businesses.id, businesses.quality_score, businesses.quality_confidence,
			businesses.scoring_rule_version, quality_rule_sets.name,
			COALESCE(MAX(business_score_components.evaluated_at), 0)
		FROM businesses
		JOIN quality_rule_sets ON quality_rule_sets.version = businesses.scoring_rule_version
		LEFT JOIN business_score_components ON business_score_components.business_id = businesses.id
			AND business_score_components.rule_version = businesses.scoring_rule_version
		WHERE businesses.id = ? AND businesses.deleted_at IS NULL
		GROUP BY businesses.id`,
		id,
	).Scan(
		&report.BusinessID,
		&report.Score,
		&report.Confidence,
		&report.RuleVersion,
		&report.RuleName,
		&evaluatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return web.BusinessQualityReport{}, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, id)
	}
	if err != nil {
		return web.BusinessQualityReport{}, fmt.Errorf("read business quality: %w", err)
	}
	report.EvaluatedAt = time.Unix(evaluatedAt, 0).UTC()

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT component, contribution, maximum, passed, reason
		FROM business_score_components
		WHERE business_id = ? AND rule_version = ?
		ORDER BY contribution DESC, component`,
		id,
		report.RuleVersion,
	)
	if err != nil {
		return web.BusinessQualityReport{}, fmt.Errorf("read quality components: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var contribution web.QualityContribution
		var passed int
		if err := rows.Scan(
			&contribution.Component,
			&contribution.Contribution,
			&contribution.Maximum,
			&passed,
			&contribution.Reason,
		); err != nil {
			return web.BusinessQualityReport{}, fmt.Errorf("scan quality component: %w", err)
		}
		contribution.Passed = passed != 0
		report.Contributions = append(report.Contributions, contribution)
	}
	if err := rows.Err(); err != nil {
		return web.BusinessQualityReport{}, fmt.Errorf("read quality components: %w", err)
	}

	return report, nil
}

// RecalculateQuality evaluates selected active businesses, or all active
// businesses when ids is empty, in one atomic local transaction.
func (repo *repo) RecalculateQuality(ctx context.Context, ids []string) (int64, error) {
	if len(ids) > 100_000 {
		return 0, fmt.Errorf("%w: too many businesses selected", web.ErrInvalidResultMutation)
	}
	if len(ids) == 0 {
		rows, err := repo.db.QueryContext(
			ctx,
			"SELECT id FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL ORDER BY id",
		)
		if err != nil {
			return 0, fmt.Errorf("list businesses for quality scoring: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return 0, fmt.Errorf("scan business for quality scoring: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("list businesses for quality scoring: %w", err)
		}
		if err := rows.Close(); err != nil {
			return 0, fmt.Errorf("close quality business list: %w", err)
		}
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("start quality recalculation: %w", err)
	}
	defer tx.Rollback()
	rules, err := ensureActiveQualityRules(ctx, tx)
	if err != nil {
		return 0, err
	}
	evaluatedAt := time.Now().UTC()
	var scored int64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 {
			return 0, fmt.Errorf("%w: invalid business ID", web.ErrInvalidResultMutation)
		}
		if _, err := scoreBusiness(ctx, tx, id, rules, evaluatedAt); err != nil {
			return 0, err
		}
		scored++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit quality recalculation: %w", err)
	}

	return scored, nil
}

// maximumQualityDriftSamples bounds the sample list in a drift report.
const maximumQualityDriftSamples = 25

// qualityDriftTolerance is the rounding slack allowed between a stored score
// and the sum of its stored components. Both are rounded to two decimals, so
// anything past this is a real disagreement, not floating point noise.
const qualityDriftTolerance = 0.05

// QualityScoreDrift audits every stored record-quality score against its own
// stored breakdown and, when repair is set, rescores the rows that disagree.
//
// The score and its components are written together by scoreBusiness, so they
// can only come apart if some other writer touched the column afterwards. The
// audit therefore doubles as a detector for exactly that: a foreign number
// merged into the record-quality column.
func (repo *repo) QualityScoreDrift(ctx context.Context, repair bool) (web.QualityScoreDriftReport, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT businesses.id, businesses.name, businesses.quality_score,
			ROUND(SUM(components.contribution), 2), businesses.scoring_rule_version, COUNT(*)
		FROM businesses
		JOIN business_score_components AS components
			ON components.business_id = businesses.id
			AND components.rule_version = businesses.scoring_rule_version
		WHERE businesses.deleted_at IS NULL AND businesses.merged_into_id IS NULL
		GROUP BY businesses.id
		ORDER BY businesses.id`,
	)
	if err != nil {
		return web.QualityScoreDriftReport{}, fmt.Errorf("audit quality score drift: %w", err)
	}
	defer func() { _ = rows.Close() }()

	report := web.QualityScoreDriftReport{
		Applied: repair,
		Samples: make([]web.QualityScoreDriftSample, 0, maximumQualityDriftSamples),
	}
	drifted := make([]string, 0)
	for rows.Next() {
		var sample web.QualityScoreDriftSample
		if err := rows.Scan(
			&sample.BusinessID, &sample.Name, &sample.StoredScore,
			&sample.ExplainedSum, &sample.RuleVersion, &sample.ComponentRows,
		); err != nil {
			return web.QualityScoreDriftReport{}, fmt.Errorf("scan quality score drift: %w", err)
		}
		report.Checked++
		if math.Abs(sample.StoredScore-sample.ExplainedSum) <= qualityDriftTolerance {
			continue
		}
		report.Drifted++
		drifted = append(drifted, sample.BusinessID)
		if len(report.Samples) < maximumQualityDriftSamples {
			report.Samples = append(report.Samples, sample)
		}
	}
	if err := rows.Err(); err != nil {
		return web.QualityScoreDriftReport{}, fmt.Errorf("audit quality score drift: %w", err)
	}

	if !repair || len(drifted) == 0 {
		return report, nil
	}
	repaired, err := repo.RecalculateQuality(ctx, drifted)
	if err != nil {
		return web.QualityScoreDriftReport{}, err
	}
	report.Repaired = repaired
	if _, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES ('quality_score_drift_repaired', 'business', '', ?, ?)`,
		mustJSON(map[string]any{
			"checked":  report.Checked,
			"drifted":  report.Drifted,
			"repaired": report.Repaired,
		}, "{}"),
		time.Now().UTC().Unix(),
	); err != nil {
		return web.QualityScoreDriftReport{}, fmt.Errorf("record quality drift repair: %w", err)
	}

	return report, nil
}

func ensureActiveQualityRules(ctx context.Context, tx *sql.Tx) (web.QualityRuleSet, error) {
	var rulesJSON string
	err := tx.QueryRowContext(
		ctx,
		"SELECT rules FROM quality_rule_sets WHERE active = 1 LIMIT 1",
	).Scan(&rulesJSON)
	if err == nil {
		var rules web.QualityRuleSet
		if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
			return web.QualityRuleSet{}, fmt.Errorf("decode active quality rules: %w", err)
		}
		if err := web.ValidateQualityRuleSet(rules); err != nil {
			return web.QualityRuleSet{}, err
		}
		return rules, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return web.QualityRuleSet{}, fmt.Errorf("read active quality rules: %w", err)
	}

	rules := web.DefaultQualityRuleSet()
	encoded, err := json.Marshal(rules)
	if err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("encode default quality rules: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO quality_rule_sets(version, name, rules, active, created_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(version) DO UPDATE SET active = 1`,
		rules.Version,
		rules.Name,
		string(encoded),
		time.Now().UTC().Unix(),
	); err != nil {
		return web.QualityRuleSet{}, fmt.Errorf("initialize quality rules: %w", err)
	}

	return rules, nil
}

func qualityRuleVersion(rules web.QualityRuleSet) string {
	rules.Version = ""
	encoded, _ := json.Marshal(rules)
	sum := sha256.Sum256(encoded)

	return "rules-" + hex.EncodeToString(sum[:8])
}

func scoreBusiness(
	ctx context.Context,
	tx *sql.Tx,
	id string,
	rules web.QualityRuleSet,
	evaluatedAt time.Time,
) (web.BusinessQualityReport, error) {
	input, err := readQualityInput(ctx, tx, id)
	if err != nil {
		return web.BusinessQualityReport{}, err
	}
	contributions, score, confidence := calculateQuality(input, rules, evaluatedAt)
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET quality_score = ?, quality_confidence = ?, scoring_rule_version = ?
		WHERE id = ? AND deleted_at IS NULL`,
		score,
		confidence,
		rules.Version,
		id,
	); err != nil {
		return web.BusinessQualityReport{}, fmt.Errorf("update business quality: %w", err)
	}
	for _, contribution := range contributions {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO business_score_components(
				business_id, rule_version, component, contribution, maximum, passed, reason, evaluated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(business_id, rule_version, component) DO UPDATE SET
				contribution = excluded.contribution,
				maximum = excluded.maximum,
				passed = excluded.passed,
				reason = excluded.reason,
				evaluated_at = excluded.evaluated_at`,
			id,
			rules.Version,
			contribution.Component,
			contribution.Contribution,
			contribution.Maximum,
			boolInt(contribution.Passed),
			contribution.Reason,
			evaluatedAt.Unix(),
		); err != nil {
			return web.BusinessQualityReport{}, fmt.Errorf("store quality component: %w", err)
		}
	}

	return web.BusinessQualityReport{
		BusinessID:    id,
		Score:         score,
		Confidence:    confidence,
		RuleVersion:   rules.Version,
		RuleName:      rules.Name,
		Contributions: contributions,
		EvaluatedAt:   evaluatedAt,
	}, nil
}

func readQualityInput(ctx context.Context, tx *sql.Tx, id string) (qualityInput, error) {
	var (
		input                                 qualityInput
		mapsURL, legacyStatus, taskState      string
		sharedStatus, sharedTitle, sharedMeta string
		sharedHTTPS, sharedResponseMS         sql.NullInt64
		sharedAudited                         int
	)
	err := tx.QueryRowContext(
		ctx,
		`SELECT businesses.business_status, businesses.website, businesses.normalized_phone,
			businesses.primary_category, businesses.address, businesses.description,
			businesses.latitude, businesses.longitude, businesses.rating, businesses.review_count,
			businesses.place_id, businesses.cid, businesses.data_id, businesses.last_seen_at,
			COALESCE(website.status, ''), website.https, website.response_time_ms,
			COALESCE(website.page_title, ''), COALESCE(website.meta_description, ''),
			CASE WHEN website.id IS NULL THEN 0 ELSE 1 END,
			(SELECT COUNT(*) FROM emails WHERE emails.business_id = businesses.id),
			(SELECT COUNT(*) FROM emails WHERE emails.business_id = businesses.id
				AND disposable = 0 AND COALESCE(domain_has_mx, 1) = 1),
			(SELECT COUNT(*) FROM social_profiles WHERE social_profiles.business_id = businesses.id),
			businesses.maps_url, businesses.website_status,
			COALESCE(task.state, ''),
			COALESCE(shared.status, ''), shared.https, shared.response_time_ms,
			COALESCE(shared.page_title, ''), COALESCE(shared.meta_description, ''),
			CASE WHEN shared.id IS NULL THEN 0 ELSE 1 END
		FROM businesses
		LEFT JOIN websites AS website ON website.id = (
			SELECT id FROM websites WHERE websites.business_id = businesses.id
			ORDER BY COALESCE(last_checked_at, 0) DESC, id DESC LIMIT 1
		)
		LEFT JOIN enrichment_tasks AS task
			ON task.business_id = businesses.id AND task.state IN ('queued', 'running')
		LEFT JOIN websites AS shared ON website.id IS NULL AND shared.id = (
			SELECT peer.id FROM websites AS peer
			WHERE peer.domain = businesses.domain AND businesses.domain <> ''
			ORDER BY COALESCE(peer.last_checked_at, 0) DESC, peer.id DESC LIMIT 1
		)
		WHERE businesses.id = ? AND businesses.deleted_at IS NULL`,
		id,
	).Scan(
		&input.businessStatus,
		&input.website,
		&input.phone,
		&input.category,
		&input.address,
		&input.description,
		&input.latitude,
		&input.longitude,
		&input.rating,
		&input.reviewCount,
		&input.placeID,
		&input.cid,
		&input.dataID,
		&input.lastSeenAt,
		&input.websiteStatus,
		&input.websiteHTTPS,
		&input.responseTimeMS,
		&input.pageTitle,
		&input.metaDescription,
		&input.websiteAudited,
		&input.emailCount,
		&input.validEmailCount,
		&input.socialCount,
		&mapsURL,
		&legacyStatus,
		&taskState,
		&sharedStatus,
		&sharedHTTPS,
		&sharedResponseMS,
		&sharedTitle,
		&sharedMeta,
		&sharedAudited,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return qualityInput{}, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, id)
	}
	if err != nil {
		return qualityInput{}, fmt.Errorf("read business quality inputs: %w", err)
	}

	resolveQualityWebsiteState(&input, qualityWebsiteEvidence{
		mapsURL:          mapsURL,
		legacyStatus:     legacyStatus,
		taskState:        taskState,
		sharedStatus:     sharedStatus,
		sharedHTTPS:      sharedHTTPS,
		sharedResponseMS: sharedResponseMS,
		sharedTitle:      sharedTitle,
		sharedMeta:       sharedMeta,
		sharedAudited:    sharedAudited != 0,
	})

	return input, nil
}

// qualityWebsiteEvidence carries the extra columns readQualityInput needs to
// resolve a canonical website state: the listing's Maps URL, the stored
// transport status, the open enrichment task, and the audit another business
// on the same domain already paid for.
type qualityWebsiteEvidence struct {
	mapsURL          string
	legacyStatus     string
	taskState        string
	sharedStatus     string
	sharedHTTPS      sql.NullInt64
	sharedResponseMS sql.NullInt64
	sharedTitle      string
	sharedMeta       string
	sharedAudited    bool
}

// resolveQualityWebsiteState fills in the canonical website state that drives
// every website-dependent quality component.
//
// Two corrections matter here. A listed social profile is never treated as an
// owned website, so no audit evidence - not the business's own and certainly
// not another business that happens to share instagram.com - is allowed to
// stand in for one. And a business with no audit of its own inherits the
// audit of another business on the same domain, because one domain is one
// site and probing it twice would answer the same question twice.
func resolveQualityWebsiteState(input *qualityInput, evidence qualityWebsiteEvidence) {
	input.socialPlatform = prospect.SocialPlatform(input.website)
	if input.socialPlatform != "" {
		// Nothing measured about a rented profile page describes a site the
		// business owns, so the website evidence is cleared rather than
		// scored.
		input.websiteStatus = ""
		input.websiteHTTPS = sql.NullInt64{}
		input.responseTimeMS = sql.NullInt64{}
		input.pageTitle = ""
		input.metaDescription = ""
		input.websiteAudited = 0
		input.websiteState = web.WebsiteStateSocialOnly

		return
	}

	if input.websiteAudited == 0 && evidence.sharedAudited {
		input.websiteStatus = evidence.sharedStatus
		input.websiteHTTPS = evidence.sharedHTTPS
		input.responseTimeMS = evidence.sharedResponseMS
		input.pageTitle = evidence.sharedTitle
		input.metaDescription = evidence.sharedMeta
		input.websiteAudited = 1
		input.sharedDomainEvidence = true
	}

	stateEvidence := web.WebsiteStateEvidence{
		Website:      input.website,
		MapsURL:      evidence.mapsURL,
		LegacyStatus: evidence.legacyStatus,
		TaskState:    evidence.taskState,
	}
	if input.websiteAudited != 0 && strings.TrimSpace(input.websiteStatus) != "" {
		// The websites row is the stored outcome of a completed audit, so it
		// answers rules 4 and 5 of the resolver identically.
		stateEvidence.LegacyStatus = input.websiteStatus
	}
	input.websiteState = web.ResolveWebsiteState(stateEvidence).State
}

func calculateQuality(
	input qualityInput,
	rules web.QualityRuleSet,
	evaluatedAt time.Time,
) ([]web.QualityContribution, float64, float64) {
	type weightedContribution struct {
		web.QualityContribution
		known bool
	}
	items := make([]weightedContribution, 0, 12)
	add := func(component string, contribution, maximum float64, passed, known bool, reason string) {
		items = append(items, weightedContribution{QualityContribution: web.QualityContribution{
			Component: component, Contribution: contribution, Maximum: maximum, Passed: passed, Reason: reason,
		}, known: known})
	}

	// readQualityInput resolves the canonical state from the full evidence.
	// Callers that build a qualityInput straight from columns get the same
	// answer derived from those columns, so the website components are never
	// silently scored as "unknown" just because a field was left unset.
	if input.socialPlatform == "" {
		input.socialPlatform = prospect.SocialPlatform(input.website)
	}
	if input.websiteState == "" {
		input.websiteState = web.ResolveWebsiteState(web.WebsiteStateEvidence{
			Website:      input.website,
			LegacyStatus: input.websiteStatus,
		}).State
	}

	status := strings.ToLower(input.businessStatus)
	switch {
	case strings.Contains(status, "permanent") && strings.Contains(status, "closed"):
		add("business_open", -rules.OpenWeight, rules.OpenWeight, false, true, "Listing is permanently closed")
	case strings.Contains(status, "temporar") && strings.Contains(status, "closed"):
		add("business_open", -rules.OpenWeight/2, rules.OpenWeight, false, true, "Listing is temporarily closed")
	case strings.Contains(status, "open"), strings.Contains(status, "operational"):
		add("business_open", rules.OpenWeight, rules.OpenWeight, true, true, "Listing is open")
	default:
		add("business_open", 0, rules.OpenWeight, false, false, "Business status has not been confirmed")
	}

	// The website components are driven by the canonical website state, never
	// by the raw URL string. An unaudited website is worth exactly nothing in
	// either direction and is marked unknown, so it lowers record confidence
	// instead of inventing points: before this, a listing nobody had ever
	// checked collected half the active-website weight and full HTTPS credit
	// purely because its URL started with "https://".
	reused := ""
	if input.sharedDomainEvidence {
		reused = " (evidence reused from another business on the same domain)"
	}
	switch input.websiteState {
	case web.WebsiteStateNoWebsite:
		add("active_website", 0, rules.ActiveWebsiteWeight, false, true, "No website is listed")
	case web.WebsiteStateSocialOnly:
		add("active_website", 0, rules.ActiveWebsiteWeight, false, true,
			fmt.Sprintf("The listed URL is a social profile (%s), not a website the business owns", input.socialPlatform))
	case web.WebsiteStateLive:
		add("active_website", rules.ActiveWebsiteWeight, rules.ActiveWebsiteWeight, true, true,
			"An audit reached the website and it answered"+reused)
	case web.WebsiteStateDead:
		add("active_website", -rules.ActiveWebsiteWeight/2, rules.ActiveWebsiteWeight, false, true,
			"An audit reached the server and the website returned an error"+reused)
	case web.WebsiteStateError:
		add("active_website", 0, rules.ActiveWebsiteWeight, false, false,
			"The audit could not get an answer from the website, so its condition is unknown"+reused)
	default:
		add("active_website", 0, rules.ActiveWebsiteWeight, false, false,
			"The website has not been audited yet, so it neither earns nor loses points")
	}
	switch {
	case input.websiteState == web.WebsiteStateNoWebsite:
		add("https", 0, rules.HTTPSWeight, false, true, "There is no website to secure")
	case input.websiteState == web.WebsiteStateSocialOnly:
		add("https", 0, rules.HTTPSWeight, false, true,
			"Transport security of a social network is not the business's to fix")
	case input.websiteHTTPS.Valid:
		if input.websiteHTTPS.Int64 != 0 {
			add("https", rules.HTTPSWeight, rules.HTTPSWeight, true, true, "Website uses HTTPS"+reused)
		} else {
			add("https", 0, rules.HTTPSWeight, false, true, "Website does not use HTTPS"+reused)
		}
	default:
		add("https", 0, rules.HTTPSWeight, false, false,
			"HTTPS has not been verified: no audit has reached this website")
	}
	add("phone", boolWeight(input.phone != "", rules.PhoneWeight), rules.PhoneWeight, input.phone != "", true,
		chooseReason(input.phone != "", "Phone number is available", "No phone number is available"))
	if input.validEmailCount > 0 {
		add("email", rules.EmailWeight, rules.EmailWeight, true, true, "At least one non-disposable email passes local domain checks")
	} else if input.emailCount > 0 {
		add("email", -rules.EmailWeight/2, rules.EmailWeight, false, true, "Email exists but fails or has not passed local domain checks")
	} else {
		add("email", 0, rules.EmailWeight, false, true, "No email address is available")
	}
	// A social profile listed as the business's website is still a social
	// profile, and counting it here is what keeps the same URL from being
	// double-counted as a website above.
	socialPresent := input.socialCount > 0 || input.socialPlatform != ""
	socialReason := "No social profile is available"
	switch {
	case input.socialPlatform != "":
		socialReason = fmt.Sprintf("The listing points at a social profile (%s)", input.socialPlatform)
	case input.socialCount > 0:
		socialReason = "Social profile is available"
	}
	add("social_profiles", boolWeight(socialPresent, rules.SocialWeight), rules.SocialWeight, socialPresent, true,
		socialReason)
	if input.rating.Valid {
		ratio := min(1.0, max(0.0, input.rating.Float64/rules.RatingThreshold))
		add("rating", rules.RatingWeight*ratio, rules.RatingWeight, input.rating.Float64 >= rules.RatingThreshold, true,
			fmt.Sprintf("Rating %.1f; target %.1f", input.rating.Float64, rules.RatingThreshold))
	} else {
		add("rating", 0, rules.RatingWeight, false, false, "Rating is not available")
	}
	if input.reviewCount.Valid {
		ratio := min(1.0, float64(max(int64(0), input.reviewCount.Int64))/float64(rules.ReviewCountThreshold))
		add("review_count", rules.ReviewCountWeight*ratio, rules.ReviewCountWeight,
			input.reviewCount.Int64 >= rules.ReviewCountThreshold, true,
			fmt.Sprintf("%d reviews; target %d", input.reviewCount.Int64, rules.ReviewCountThreshold))
	} else {
		add("review_count", 0, rules.ReviewCountWeight, false, false, "Review count is not available")
	}
	complete := 0
	for _, present := range []bool{
		input.category != "", input.address != "", input.latitude.Valid && input.longitude.Valid,
		input.placeID != "" || input.cid != "" || input.dataID != "", input.website != "",
		input.phone != "", input.emailCount > 0, input.description != "",
	} {
		if present {
			complete++
		}
	}
	completenessRatio := float64(complete) / 8
	add("listing_completeness", rules.CompletenessWeight*completenessRatio, rules.CompletenessWeight,
		complete == 8, true, fmt.Sprintf("%d of 8 core field groups are complete", complete))
	age := evaluatedAt.Sub(time.Unix(input.lastSeenAt, 0).UTC())
	fresh := age <= time.Duration(rules.FreshnessDays)*24*time.Hour
	add("data_freshness", boolWeight(fresh, rules.FreshnessWeight), rules.FreshnessWeight, fresh, true,
		fmt.Sprintf("Last observed %d days ago; target %d days", max(0, int(age.Hours()/24)), rules.FreshnessDays))
	switch {
	case input.websiteState == web.WebsiteStateNoWebsite:
		add("website_quality", 0, rules.WebsiteQualityWeight, false, true, "There is no website to grade")
	case input.websiteState == web.WebsiteStateSocialOnly:
		add("website_quality", 0, rules.WebsiteQualityWeight, false, true,
			"A social profile is not a website whose quality the business controls")
	case input.websiteAudited != 0:
		websiteFields := 0
		if input.pageTitle != "" {
			websiteFields++
		}
		if input.metaDescription != "" {
			websiteFields++
		}
		add("website_quality", rules.WebsiteQualityWeight*float64(websiteFields)/2, rules.WebsiteQualityWeight,
			websiteFields == 2, true, fmt.Sprintf("%d of 2 basic metadata checks pass%s", websiteFields, reused))
	default:
		add("website_quality", 0, rules.WebsiteQualityWeight, false, false, "Website quality has not been audited")
	}
	if input.responseTimeMS.Valid {
		fast := input.responseTimeMS.Int64 <= rules.ResponseTimeMS
		contribution := 0.0
		if fast {
			contribution = rules.ResponseTimeWeight
		} else if input.responseTimeMS.Int64 > rules.ResponseTimeMS*3 {
			contribution = -rules.ResponseTimeWeight / 2
		}
		add("website_response", contribution, rules.ResponseTimeWeight, fast, true,
			fmt.Sprintf("Response %d ms; target %d ms", input.responseTimeMS.Int64, rules.ResponseTimeMS))
	} else if input.websiteState == web.WebsiteStateNoWebsite || input.websiteState == web.WebsiteStateSocialOnly {
		add("website_response", 0, rules.ResponseTimeWeight, false, true,
			"There is no owned website whose response time could be measured")
	} else {
		add("website_response", 0, rules.ResponseTimeWeight, false, false, "Website response time has not been measured")
	}

	totalWeight := 0.0
	knownWeight := 0.0
	for _, item := range items {
		totalWeight += item.Maximum
		if item.known {
			knownWeight += item.Maximum
		}
	}
	scale := 1.0
	if totalWeight > 0 {
		scale = 100 / totalWeight
	}
	result := make([]web.QualityContribution, 0, len(items))
	score := 0.0
	for _, item := range items {
		item.Contribution = roundedQuality(item.Contribution * scale)
		item.Maximum = roundedQuality(item.Maximum * scale)
		score += item.Contribution
		result = append(result, item.QualityContribution)
	}
	if rules.ExcludeClosed && strings.Contains(status, "closed") {
		score = 0
	}
	score = roundedQuality(min(100, max(0, score)))
	confidence := 0.0
	if totalWeight > 0 {
		confidence = roundedQuality(min(1, knownWeight/totalWeight))
	}

	return result, score, confidence
}

func boolWeight(condition bool, weight float64) float64 {
	if condition {
		return weight
	}
	return 0
}

func chooseReason(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func roundedQuality(value float64) float64 {
	return math.Round(value*100) / 100
}
