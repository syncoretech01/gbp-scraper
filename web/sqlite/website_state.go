package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

// websiteStateRow is one business's durable website evidence, before the
// domain-level reuse pass fills in what the business itself never observed.
type websiteStateRow struct {
	businessID string
	name       string
	website    string
	mapsURL    string
	domain     string
	evidence   web.WebsiteStateEvidence
	hasOwnTask bool
}

// websiteDomainEvidence is the newest completed audit recorded for one
// domain, whichever business paid for it.
type websiteDomainEvidence struct {
	businessID  string
	reachable   bool
	statusCode  int
	auditError  string
	completedAt time.Time
}

// maximumWebsiteStateScan bounds one resolution pass. A local workspace far
// past this size is a signal to narrow the scope to one job rather than to
// silently truncate, so the readers below report the truncation.
const maximumWebsiteStateScan = 200_000

// readWebsiteStateRows loads the evidence for one scope: a single job, an
// explicit ID list, or the whole live workspace.
//
// The enrichment queue carries a unique partial index over business_id for
// the open states, so the task join can never fan a business out into two
// rows.
func readWebsiteStateRows(
	ctx context.Context,
	db *sql.DB,
	jobID string,
	ids []string,
) ([]websiteStateRow, error) {
	query := `SELECT businesses.id, businesses.name, businesses.website, businesses.maps_url,
			businesses.domain, businesses.website_status,
			COALESCE(tasks.state, ''),
			COALESCE(audits.reachable, 0), COALESCE(audits.status_code, 0),
			COALESCE(audits.error, ''), COALESCE(audits.completed_at, 0),
			CASE WHEN audits.id IS NULL THEN 0 ELSE 1 END
		FROM businesses
		LEFT JOIN enrichment_tasks AS tasks
			ON tasks.business_id = businesses.id AND tasks.state IN ('queued', 'running')
		LEFT JOIN website_audits AS audits ON audits.id = (
			SELECT candidate.id FROM website_audits candidate
			WHERE candidate.business_id = businesses.id
			ORDER BY candidate.completed_at DESC, candidate.id DESC
			LIMIT 1
		)
		WHERE businesses.deleted_at IS NULL AND businesses.merged_into_id IS NULL`

	args := make([]any, 0, len(ids)+1)
	if jobID != "" {
		query += ` AND businesses.id IN (SELECT business_id FROM job_businesses WHERE job_id = ?)`
		args = append(args, jobID)
	}
	if len(ids) > 0 {
		query += ` AND businesses.id IN (` + sqlPlaceholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query += fmt.Sprintf(` ORDER BY businesses.id LIMIT %d`, maximumWebsiteStateScan)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read website state rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	results := make([]websiteStateRow, 0, 128)
	for rows.Next() {
		var (
			row         websiteStateRow
			reachable   int
			completedAt int64
			audited     int
		)
		if err := rows.Scan(
			&row.businessID,
			&row.name,
			&row.website,
			&row.mapsURL,
			&row.domain,
			&row.evidence.LegacyStatus,
			&row.evidence.TaskState,
			&reachable,
			&row.evidence.AuditStatusCode,
			&row.evidence.AuditError,
			&completedAt,
			&audited,
		); err != nil {
			return nil, fmt.Errorf("scan website state row: %w", err)
		}
		row.evidence.Website = row.website
		row.evidence.MapsURL = row.mapsURL
		row.evidence.AuditReachable = reachable != 0
		row.evidence.AuditCompleted = audited != 0
		if completedAt > 0 {
			row.evidence.AuditCompletedAt = time.Unix(completedAt, 0).UTC()
		}
		row.hasOwnTask = row.evidence.TaskState != ""
		// The stored domain column is authoritative for indexing, but the
		// canonical rule lives in the pure prospect package. Recomputing keeps
		// rows imported before a domain rule change consistent with the audits
		// they are matched against.
		row.domain = prospect.DomainFromWebsite(row.website)
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read website state rows: %w", err)
	}

	return results, nil
}

// readWebsiteDomainEvidence collects the newest completed audit per domain
// across every live business, so a second business on an already-audited
// domain reuses that observation instead of paying for an identical probe.
func readWebsiteDomainEvidence(ctx context.Context, db *sql.DB) (map[string]websiteDomainEvidence, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT businesses.id, businesses.website, audits.reachable, audits.status_code,
			audits.error, audits.completed_at
		FROM website_audits AS audits
		JOIN businesses ON businesses.id = audits.business_id
		WHERE businesses.deleted_at IS NULL AND businesses.merged_into_id IS NULL
			AND businesses.website <> ''
		ORDER BY audits.completed_at ASC, audits.id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("read website domain evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()

	evidence := make(map[string]websiteDomainEvidence, 64)
	for rows.Next() {
		var (
			businessID, website, auditError string
			reachable, statusCode           int
			completedAt                     int64
		)
		if err := rows.Scan(&businessID, &website, &reachable, &statusCode, &auditError, &completedAt); err != nil {
			return nil, fmt.Errorf("scan website domain evidence: %w", err)
		}
		domain := prospect.DomainFromWebsite(website)
		if domain == "" {
			continue
		}
		// Ascending order means the last row written per domain is the newest.
		evidence[domain] = websiteDomainEvidence{
			businessID:  businessID,
			reachable:   reachable != 0,
			statusCode:  statusCode,
			auditError:  auditError,
			completedAt: time.Unix(completedAt, 0).UTC(),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read website domain evidence: %w", err)
	}

	return evidence, nil
}

// readWebsiteDomainTasks collects the open enrichment task per domain, so a
// probe already queued for one listing answers for every other listing on the
// same site instead of queueing a second identical probe.
func readWebsiteDomainTasks(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT businesses.website, tasks.state
		FROM enrichment_tasks AS tasks
		JOIN businesses ON businesses.id = tasks.business_id
		WHERE tasks.state IN ('queued', 'running')
			AND businesses.deleted_at IS NULL AND businesses.merged_into_id IS NULL
			AND businesses.website <> ''
		ORDER BY tasks.created_at ASC, tasks.id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("read website domain tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	states := make(map[string]string, 16)
	for rows.Next() {
		var website, state string
		if err := rows.Scan(&website, &state); err != nil {
			return nil, fmt.Errorf("scan website domain task: %w", err)
		}
		domain := prospect.DomainFromWebsite(website)
		if domain == "" {
			continue
		}
		// A running probe outranks a queued one for the same domain.
		if states[domain] == "running" {
			continue
		}
		states[domain] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read website domain tasks: %w", err)
	}

	return states, nil
}

// websiteDomainView bundles the two domain-level reuse maps so every caller
// resolves states from the same evidence.
type websiteDomainView struct {
	audits map[string]websiteDomainEvidence
	tasks  map[string]string
}

// readWebsiteDomainView loads both domain-level reuse maps.
func readWebsiteDomainView(ctx context.Context, db *sql.DB) (websiteDomainView, error) {
	audits, err := readWebsiteDomainEvidence(ctx, db)
	if err != nil {
		return websiteDomainView{}, err
	}
	tasks, err := readWebsiteDomainTasks(ctx, db)
	if err != nil {
		return websiteDomainView{}, err
	}

	return websiteDomainView{audits: audits, tasks: tasks}, nil
}

// resolveWebsiteStateRow applies domain-level evidence reuse and then the
// canonical resolver. A business that never ran its own audit adopts the
// newest audit - or the in-flight probe - of another business on the same
// domain, flagged so the reason text says where the observation came from.
func resolveWebsiteStateRow(
	row websiteStateRow,
	domain websiteDomainView,
) web.WebsiteStateResolution {
	domainEvidence := domain.audits
	evidence := row.evidence
	if !row.hasOwnTask && row.domain != "" {
		if state, ok := domain.tasks[row.domain]; ok {
			evidence.TaskState = state
			evidence.EvidenceDomain = row.domain
		}
	}
	if !evidence.AuditCompleted && row.domain != "" {
		if shared, ok := domainEvidence[row.domain]; ok && shared.businessID != row.businessID {
			evidence.AuditCompleted = true
			evidence.AuditReachable = shared.reachable
			evidence.AuditStatusCode = shared.statusCode
			evidence.AuditError = shared.auditError
			evidence.AuditCompletedAt = shared.completedAt
			evidence.EvidenceDomain = row.domain
		}
	}

	return web.ResolveWebsiteState(evidence)
}

// WebsiteStateSummary counts every live business by canonical website state.
func (repo *repo) WebsiteStateSummary(ctx context.Context, jobID string) (web.WebsiteStateSummary, error) {
	rows, err := readWebsiteStateRows(ctx, repo.db, jobID, nil)
	if err != nil {
		return web.WebsiteStateSummary{}, err
	}
	domainView, err := readWebsiteDomainView(ctx, repo.db)
	if err != nil {
		return web.WebsiteStateSummary{}, err
	}

	counts := make(map[string]int64, len(web.WebsiteStates()))
	summary := web.WebsiteStateSummary{JobID: jobID}
	for _, row := range rows {
		resolution := resolveWebsiteStateRow(row, domainView)
		counts[resolution.State]++
		summary.Total++
		if resolution.Auditable && resolution.State != web.WebsiteStateNoWebsite {
			summary.WithWebsite++
		}
		switch resolution.State {
		case web.WebsiteStateNeverChecked:
			summary.NeverChecked++
		case web.WebsiteStateQueued, web.WebsiteStateChecking:
			summary.Pending++
		}
		if resolution.ReusedFromDomain != "" {
			summary.ReusedDomainEvidence++
		}
	}

	summary.Counts = make([]web.WebsiteStateCount, 0, len(web.WebsiteStates()))
	for _, state := range web.WebsiteStates() {
		summary.Counts = append(summary.Counts, web.WebsiteStateCount{
			State: state,
			Label: web.WebsiteStateLabel(state),
			Count: counts[state],
		})
	}

	return summary, nil
}

// BusinessWebsiteState resolves one business's canonical state.
func (repo *repo) BusinessWebsiteState(ctx context.Context, businessID string) (web.WebsiteStateResolution, error) {
	rows, err := readWebsiteStateRows(ctx, repo.db, "", []string{businessID})
	if err != nil {
		return web.WebsiteStateResolution{}, err
	}
	if len(rows) == 0 {
		return web.WebsiteStateResolution{}, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, businessID)
	}
	domainView, err := readWebsiteDomainView(ctx, repo.db)
	if err != nil {
		return web.WebsiteStateResolution{}, err
	}

	return resolveWebsiteStateRow(rows[0], domainView), nil
}

// websiteStateIDBatch bounds one IN clause. SQLite caps the number of bound
// variables per statement, and callers may legitimately name tens of
// thousands of businesses, so an explicit ID list is read in batches.
const websiteStateIDBatch = 500

// UnauditedBusinessIDs returns the businesses among ids (or every live
// business when ids is empty) whose website has never been checked.
func (repo *repo) UnauditedBusinessIDs(ctx context.Context, ids []string) ([]string, error) {
	domainView, err := readWebsiteDomainView(ctx, repo.db)
	if err != nil {
		return nil, err
	}
	pending := make([]string, 0)
	collect := func(batch []string) error {
		rows, err := readWebsiteStateRows(ctx, repo.db, "", batch)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if resolveWebsiteStateRow(row, domainView).State == web.WebsiteStateNeverChecked {
				pending = append(pending, row.businessID)
			}
		}

		return nil
	}
	if len(ids) == 0 {
		if err := collect(nil); err != nil {
			return nil, err
		}

		return pending, nil
	}
	for start := 0; start < len(ids); start += websiteStateIDBatch {
		end := min(start+websiteStateIDBatch, len(ids))
		if err := collect(ids[start:end]); err != nil {
			return nil, err
		}
	}

	return pending, nil
}

// DomainSiblingBusinessIDs lists the other live businesses that share one
// business's website domain. They are the rows whose stored scores go stale
// the moment that domain is audited, because one domain is one site.
func (repo *repo) DomainSiblingBusinessIDs(ctx context.Context, businessID string) ([]string, error) {
	var website string
	err := repo.db.QueryRowContext(
		ctx, `SELECT website FROM businesses WHERE id = ?`, businessID,
	).Scan(&website)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read business website: %w", err)
	}
	domain := prospect.DomainFromWebsite(website)
	if domain == "" || prospect.SocialPlatform(website) != "" {
		// A shared social host is not a shared site, so nothing is inherited.
		return nil, nil
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, website FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL AND domain = ? AND id <> ?
		ORDER BY id`,
		domain,
		businessID,
	)
	if err != nil {
		return nil, fmt.Errorf("read domain siblings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	siblings := make([]string, 0)
	for rows.Next() {
		var id, siblingWebsite string
		if err := rows.Scan(&id, &siblingWebsite); err != nil {
			return nil, fmt.Errorf("scan domain sibling: %w", err)
		}
		if prospect.DomainFromWebsite(siblingWebsite) != domain {
			continue
		}
		siblings = append(siblings, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read domain siblings: %w", err)
	}

	return siblings, nil
}

// BusinessWebsiteHealth grades the site one business owns, or explains why no
// grade exists.
func (repo *repo) BusinessWebsiteHealth(ctx context.Context, businessID string) (web.WebsiteHealthReport, error) {
	resolution, err := repo.BusinessWebsiteState(ctx, businessID)
	if err != nil {
		return web.WebsiteHealthReport{}, err
	}

	evidence := web.WebsiteHealthEvidence{
		State:       resolution.State,
		CurrentYear: time.Now().UTC().Year(),
	}
	if resolution.CheckedAt != nil {
		evidence.CompletedAt = *resolution.CheckedAt
	}

	// Only a state backed by a reachable-or-serving observation needs the full
	// audit read; every other state returns an explicitly unavailable report.
	if resolution.State == web.WebsiteStateLive || resolution.State == web.WebsiteStateDead {
		auditBusinessID := businessID
		if resolution.ReusedFromDomain != "" {
			auditBusinessID = ""
		}
		if err := readWebsiteHealthEvidence(
			ctx, repo.db, auditBusinessID, resolution.Domain, &evidence,
		); err != nil {
			return web.WebsiteHealthReport{}, err
		}
	}

	report := web.ScoreWebsiteHealth(businessID, evidence)
	report.Domain = resolution.Domain

	return report, nil
}

// readWebsiteHealthEvidence loads the newest completed audit for one business
// or, when the evidence is being reused, for the domain.
func readWebsiteHealthEvidence(
	ctx context.Context,
	db *sql.DB,
	businessID string,
	domain string,
	evidence *web.WebsiteHealthEvidence,
) error {
	query := `SELECT audits.id, audits.reachable, audits.status_code, audits.https, audits.tls_valid,
			audits.certificate_error, audits.response_time_ms, audits.parked, audits.coming_soon,
			audits.placeholder, audits.mixed_content, audits.broken_internal_link_count,
			audits.internal_links_checked, audits.completed_at
		FROM website_audits AS audits
		JOIN businesses ON businesses.id = audits.business_id
		WHERE businesses.deleted_at IS NULL`
	args := make([]any, 0, 2)
	if businessID != "" {
		query += ` AND audits.business_id = ?`
		args = append(args, businessID)
	} else {
		query += ` AND businesses.domain = ?`
		args = append(args, domain)
	}
	query += ` ORDER BY audits.completed_at DESC, audits.id DESC LIMIT 1`

	var (
		auditID                                       int64
		reachable, https, tlsValid                    int
		parked, comingSoon, placeholder, mixedContent int
		responseMS, completedAt                       int64
		statusCode, brokenLinks, linksChecked         int
		certificateError                              string
	)
	err := db.QueryRowContext(ctx, query, args...).Scan(
		&auditID, &reachable, &statusCode, &https, &tlsValid, &certificateError,
		&responseMS, &parked, &comingSoon, &placeholder, &mixedContent,
		&brokenLinks, &linksChecked, &completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read website health evidence: %w", err)
	}

	evidence.Reachable = reachable != 0
	evidence.StatusCode = statusCode
	evidence.HTTPS = https != 0
	evidence.TLSValid = tlsValid != 0
	evidence.CertificateError = certificateError
	evidence.ResponseMS = responseMS
	evidence.Parked = parked != 0
	evidence.ComingSoon = comingSoon != 0
	evidence.Placeholder = placeholder != 0
	evidence.MixedContent = mixedContent != 0
	evidence.BrokenLinks = brokenLinks
	evidence.LinksChecked = linksChecked
	if completedAt > 0 {
		evidence.CompletedAt = time.Unix(completedAt, 0).UTC()
	}

	var (
		pageTitle, metaDescription string
		mobileViewport             int
		copyrightYear              sql.NullInt64
	)
	pageErr := db.QueryRowContext(
		ctx,
		`SELECT page_title, meta_description, mobile_viewport, copyright_year
		FROM website_audit_pages
		WHERE audit_id = ?
		ORDER BY CASE WHEN page_kind = 'homepage' THEN 0 ELSE 1 END, id
		LIMIT 1`,
		auditID,
	).Scan(&pageTitle, &metaDescription, &mobileViewport, &copyrightYear)
	if pageErr != nil && !errors.Is(pageErr, sql.ErrNoRows) {
		return fmt.Errorf("read audited homepage: %w", pageErr)
	}
	if pageErr == nil {
		evidence.PageTitle = pageTitle
		evidence.MetaDescription = metaDescription
		evidence.MobileViewport = mobileViewport != 0
		if copyrightYear.Valid {
			evidence.CopyrightYear = int(copyrightYear.Int64)
		}
	}

	return nil
}

// sweepCandidate is one domain's chosen representative business.
type sweepCandidate struct {
	businessID string
	website    string
	domain     string
	duplicates int
}

// StartWebsiteAuditSweep queues one durable bulk audit.
//
// The whole sweep is one transaction over the existing enrichment queue: it
// creates nothing but enrichment_tasks rows plus a single audit_logs record
// describing the run. There is no second queue to recover, and the local
// worker drains these tasks exactly as it drains every other audit, so a
// restart mid-sweep resumes with no special case.
func (repo *repo) StartWebsiteAuditSweep(
	ctx context.Context,
	request web.WebsiteAuditSweepRequest,
) (web.WebsiteAuditSweep, error) {
	rows, err := readWebsiteStateRows(ctx, repo.db, request.JobID, nil)
	if err != nil {
		return web.WebsiteAuditSweep{}, err
	}
	domainView, err := readWebsiteDomainView(ctx, repo.db)
	if err != nil {
		return web.WebsiteAuditSweep{}, err
	}
	domainEvidence := domainView.audits

	wanted := make(map[string]struct{}, len(request.States))
	for _, state := range request.States {
		wanted[state] = struct{}{}
	}

	sweep := web.WebsiteAuditSweep{
		ID:          uuid.NewString(),
		JobID:       request.JobID,
		States:      request.States,
		RequestedBy: request.RequestedBy,
		CreatedAt:   time.Now().UTC(),
	}

	byDomain := make(map[string]*sweepCandidate, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		resolution := resolveWebsiteStateRow(row, domainView)
		if !resolution.Auditable {
			sweep.SkippedIneligible++

			continue
		}
		if _, ok := wanted[resolution.State]; !ok {
			continue
		}
		if row.hasOwnTask {
			sweep.SkippedAlreadyQueued++

			continue
		}
		domain := row.domain
		if domain == "" {
			domain = row.businessID
		}
		existing, seen := byDomain[domain]
		if !seen {
			byDomain[domain] = &sweepCandidate{
				businessID: row.businessID,
				website:    row.website,
				domain:     row.domain,
			}
			order = append(order, domain)

			continue
		}
		existing.duplicates++
		sweep.SkippedDuplicateDomain++
		if preferSweepURL(row.website, existing.website) {
			existing.businessID = row.businessID
			existing.website = row.website
		}
	}
	sort.Strings(order)
	sweep.UniqueDomains = len(order)

	staleCutoff := sweep.CreatedAt.Add(-time.Duration(request.Options.StaleAfterHours) * time.Hour)
	encodedOptions, err := json.Marshal(request.Options)
	if err != nil {
		return web.WebsiteAuditSweep{}, fmt.Errorf("encode sweep audit options: %w", err)
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.WebsiteAuditSweep{}, err
	}
	defer func() { _ = tx.Rollback() }()

	requestedBy := web.WebsiteAuditSweepRequestedBy(sweep.ID)
	var nullableJobID any
	if request.JobID != "" {
		nullableJobID = request.JobID
	}

	for _, domain := range order {
		candidate := byDomain[domain]
		if sweep.Queued >= request.Limit {
			sweep.Truncated = true

			break
		}
		if !request.Options.Force && candidate.domain != "" {
			if shared, ok := domainEvidence[candidate.domain]; ok && shared.completedAt.After(staleCutoff) {
				sweep.SkippedFresh++

				continue
			}
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO enrichment_tasks(
				id, business_id, job_id, website_url, state, requested_by,
				options, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
			uuid.NewString(),
			candidate.businessID,
			nullableJobID,
			candidate.website,
			requestedBy,
			string(encodedOptions),
			sweep.CreatedAt.Unix(),
			sweep.CreatedAt.Unix(),
		); err != nil {
			return web.WebsiteAuditSweep{}, fmt.Errorf("queue sweep audit task: %w", err)
		}
		sweep.Queued++
	}

	details, err := json.Marshal(sweep)
	if err != nil {
		return web.WebsiteAuditSweep{}, fmt.Errorf("encode sweep record: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, 'website_audit_sweep', ?, ?, ?)`,
		websiteAuditSweepAction,
		sweep.ID,
		string(details),
		sweep.CreatedAt.Unix(),
	); err != nil {
		return web.WebsiteAuditSweep{}, fmt.Errorf("record website audit sweep: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return web.WebsiteAuditSweep{}, fmt.Errorf("commit website audit sweep: %w", err)
	}

	progress, err := repo.websiteAuditSweepProgress(ctx, requestedBy)
	if err != nil {
		return web.WebsiteAuditSweep{}, err
	}
	sweep.Progress = progress

	return sweep, nil
}

// websiteAuditSweepAction mirrors the service-side constant. It is duplicated
// deliberately: the storage layer owns the audit_logs vocabulary and must not
// depend on an unexported service constant.
const websiteAuditSweepAction = "website_audit_sweep_started"

// preferSweepURL chooses which of two URLs on one domain to probe. HTTPS wins
// over plain HTTP, then the shorter URL (closer to the site root), then the
// lexicographically smaller one so the choice is reproducible.
func preferSweepURL(candidate, current string) bool {
	candidateSecure := strings.HasPrefix(strings.ToLower(candidate), "https://")
	currentSecure := strings.HasPrefix(strings.ToLower(current), "https://")
	if candidateSecure != currentSecure {
		return candidateSecure
	}
	if len(candidate) != len(current) {
		return len(candidate) < len(current)
	}

	return candidate < current
}

// websiteAuditSweepProgress counts one sweep's tasks in the durable queue.
func (repo *repo) websiteAuditSweepProgress(
	ctx context.Context,
	requestedBy string,
) (web.WebsiteAuditSweepProgress, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT state, COUNT(*) FROM enrichment_tasks WHERE requested_by = ? GROUP BY state`,
		requestedBy,
	)
	if err != nil {
		return web.WebsiteAuditSweepProgress{}, fmt.Errorf("read sweep progress: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var progress web.WebsiteAuditSweepProgress
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return web.WebsiteAuditSweepProgress{}, fmt.Errorf("scan sweep progress: %w", err)
		}
		progress.Total += count
		switch state {
		case "queued":
			progress.Queued = count
		case "running":
			progress.Running = count
		case "completed":
			progress.Completed = count
		case "failed":
			progress.Failed = count
		}
	}
	if err := rows.Err(); err != nil {
		return web.WebsiteAuditSweepProgress{}, fmt.Errorf("read sweep progress: %w", err)
	}
	progress.Done = progress.Queued == 0 && progress.Running == 0
	if progress.Total > 0 {
		finished := float64(progress.Completed + progress.Failed)
		progress.Percent = roundedQuality(finished / float64(progress.Total) * 100)
	}

	return progress, nil
}

// WebsiteAuditSweepByID reads one recorded sweep and its live progress.
func (repo *repo) WebsiteAuditSweepByID(ctx context.Context, sweepID string) (web.WebsiteAuditSweep, error) {
	var details string
	err := repo.db.QueryRowContext(
		ctx,
		`SELECT details FROM audit_logs WHERE action = ? AND entity_id = ? ORDER BY id DESC LIMIT 1`,
		websiteAuditSweepAction,
		sweepID,
	).Scan(&details)
	if errors.Is(err, sql.ErrNoRows) {
		return web.WebsiteAuditSweep{}, fmt.Errorf("%w: %s", web.ErrWebsiteAuditSweepNotFound, sweepID)
	}
	if err != nil {
		return web.WebsiteAuditSweep{}, fmt.Errorf("read website audit sweep: %w", err)
	}

	var sweep web.WebsiteAuditSweep
	if err := json.Unmarshal([]byte(details), &sweep); err != nil {
		return web.WebsiteAuditSweep{}, fmt.Errorf("decode website audit sweep: %w", err)
	}
	progress, err := repo.websiteAuditSweepProgress(ctx, web.WebsiteAuditSweepRequestedBy(sweep.ID))
	if err != nil {
		return web.WebsiteAuditSweep{}, err
	}
	sweep.Progress = progress

	return sweep, nil
}

// RecentWebsiteAuditSweeps lists recorded sweeps newest first, each with its
// live progress read back from the queue.
func (repo *repo) RecentWebsiteAuditSweeps(ctx context.Context, limit int) ([]web.WebsiteAuditSweep, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT details FROM audit_logs WHERE action = ? ORDER BY id DESC LIMIT ?`,
		websiteAuditSweepAction,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list website audit sweeps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sweeps := make([]web.WebsiteAuditSweep, 0, limit)
	for rows.Next() {
		var details string
		if err := rows.Scan(&details); err != nil {
			return nil, fmt.Errorf("scan website audit sweep: %w", err)
		}
		var sweep web.WebsiteAuditSweep
		if err := json.Unmarshal([]byte(details), &sweep); err != nil {
			continue
		}
		sweeps = append(sweeps, sweep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list website audit sweeps: %w", err)
	}
	for index := range sweeps {
		progress, err := repo.websiteAuditSweepProgress(
			ctx, web.WebsiteAuditSweepRequestedBy(sweeps[index].ID),
		)
		if err != nil {
			return nil, err
		}
		sweeps[index].Progress = progress
	}

	return sweeps, nil
}

// maximumSocialBackfillSamples bounds the sample list in the report.
const maximumSocialBackfillSamples = 25

// maximumSocialBackfillRows bounds one backfill pass when the caller names no
// limit, so a direct storage-layer call can never mean "correct nothing".
const maximumSocialBackfillRows = 100_000

// socialListingSourceConfidence is the confidence a social profile taken from
// the listing itself carries. The business published the link on its own
// Google Business Profile, so it is a first-party statement.
const socialListingSourceConfidence = 1.0

// BackfillSocialListings finds already-stored listing URLs whose primary
// destination is a social, messaging, link-in-bio, or review-profile network,
// records them in social_profiles where social links belong, and corrects the
// stored prospect classification and quality score.
//
// It never rewrites businesses.website: the listing said what it said, and
// the correction is in how the value is classified, not in destroying the
// observation.
func (repo *repo) BackfillSocialListings(
	ctx context.Context,
	apply bool,
	limit int,
) (web.SocialListingBackfill, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, name, website, maps_url, prospect_status
		FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL AND website <> ''
		ORDER BY id`,
	)
	if err != nil {
		return web.SocialListingBackfill{}, fmt.Errorf("read social backfill candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if limit <= 0 {
		limit = maximumSocialBackfillRows
	}
	report := web.SocialListingBackfill{
		Applied:    apply,
		ByPlatform: make(map[string]int64),
		Samples:    make([]web.SocialListingCorrection, 0, maximumSocialBackfillSamples),
	}
	corrections := make([]web.SocialListingCorrection, 0, 64)
	for rows.Next() {
		var id, name, website, mapsURL, prospectStatus string
		if err := rows.Scan(&id, &name, &website, &mapsURL, &prospectStatus); err != nil {
			return web.SocialListingBackfill{}, fmt.Errorf("scan social backfill candidate: %w", err)
		}
		report.Examined++
		platform := prospect.SocialPlatform(website)
		if platform == "" {
			continue
		}
		if len(corrections) >= limit {
			continue
		}
		report.Social++
		report.ByPlatform[platform]++
		corrections = append(corrections, web.SocialListingCorrection{
			BusinessID:             id,
			Name:                   name,
			URL:                    website,
			Platform:               platform,
			PreviousProspectStatus: prospectStatus,
		})
	}
	if err := rows.Err(); err != nil {
		return web.SocialListingBackfill{}, fmt.Errorf("read social backfill candidates: %w", err)
	}

	if !apply {
		report.Samples = sampleSocialCorrections(corrections)

		return report, nil
	}

	inserted, err := repo.storeSocialListings(ctx, corrections)
	if err != nil {
		return web.SocialListingBackfill{}, err
	}
	report.ProfilesInserted = inserted

	ids := make([]string, 0, len(corrections))
	for index := range corrections {
		if corrections[index].PreviousProspectStatus != prospect.StatusSocialOnly {
			report.StatusCorrected++
		}
		ids = append(ids, corrections[index].BusinessID)
	}
	if len(ids) > 0 {
		if _, _, err := repo.recomputeProspects(ctx, prospect.DefaultScoreWeights(), ids); err != nil {
			return web.SocialListingBackfill{}, fmt.Errorf("reclassify social listings: %w", err)
		}
		rescored, err := repo.RecalculateQuality(ctx, ids)
		if err != nil {
			return web.SocialListingBackfill{}, fmt.Errorf("rescore social listings: %w", err)
		}
		report.QualityRescored = rescored
	}
	if err := repo.writeProspectAuditLog(ctx, "social_listings_backfilled", map[string]any{
		"examined":          report.Examined,
		"social":            report.Social,
		"profiles_inserted": report.ProfilesInserted,
		"status_corrected":  report.StatusCorrected,
		"quality_rescored":  report.QualityRescored,
	}); err != nil {
		return web.SocialListingBackfill{}, err
	}
	report.Samples = sampleSocialCorrections(corrections)

	return report, nil
}

// storeSocialListings records each corrected listing URL in social_profiles.
// The table's unique key makes the write idempotent, so re-running the
// backfill inserts nothing the second time.
func (repo *repo) storeSocialListings(
	ctx context.Context,
	corrections []web.SocialListingCorrection,
) (int64, error) {
	if len(corrections) == 0 {
		return 0, nil
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var inserted int64
	for index := range corrections {
		result, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO social_profiles(business_id, platform, url, source_url, confidence)
			VALUES (?, ?, ?, ?, ?)`,
			corrections[index].BusinessID,
			corrections[index].Platform,
			corrections[index].URL,
			"google_business_profile_listing",
			socialListingSourceConfidence,
		)
		if err != nil {
			return 0, fmt.Errorf("store social listing: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store social listing: %w", err)
		}
		if affected > 0 {
			inserted++
			corrections[index].SocialProfileStored = true
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit social listing backfill: %w", err)
	}

	return inserted, nil
}

func sampleSocialCorrections(corrections []web.SocialListingCorrection) []web.SocialListingCorrection {
	samples := make([]web.SocialListingCorrection, 0, maximumSocialBackfillSamples)
	for index := range corrections {
		if len(samples) >= maximumSocialBackfillSamples {
			break
		}
		samples = append(samples, corrections[index])
	}

	return samples
}

// sqlPlaceholders renders "?, ?, ?" for an IN clause of the given size.
func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}

	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
