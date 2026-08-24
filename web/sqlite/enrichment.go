package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/enrichment"
	"github.com/gosom/google-maps-scraper/web/resultimport"
)

func (repo *repo) QueueBusinessEnrichment(
	ctx context.Context,
	ids []string,
	options web.EnrichmentOptions,
	requestedBy string,
	jobID string,
) (web.EnrichmentBatch, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.EnrichmentBatch{}, err
	}
	defer func() { _ = tx.Rollback() }()

	candidates := make([]enrichmentCandidate, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}

		var website string
		err := tx.QueryRowContext(ctx, `SELECT website FROM businesses WHERE id = ? AND deleted_at IS NULL`, id).Scan(&website)
		if errors.Is(err, sql.ErrNoRows) {
			return web.EnrichmentBatch{}, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, id)
		}
		if err != nil {
			return web.EnrichmentBatch{}, fmt.Errorf("read enrichment target: %w", err)
		}
		candidates = append(candidates, enrichmentCandidate{businessID: id, websiteURL: website})
	}

	batch, err := queueEnrichmentCandidates(ctx, tx, candidates, options, requestedBy, jobID)
	if err != nil {
		return web.EnrichmentBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return web.EnrichmentBatch{}, err
	}

	return batch, nil
}

func (repo *repo) QueueJobEnrichment(
	ctx context.Context,
	jobID string,
	options web.EnrichmentOptions,
) (web.EnrichmentBatch, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.EnrichmentBatch{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.EnrichmentBatch{}, fmt.Errorf("job not found: %s", jobID)
		}
		return web.EnrichmentBatch{}, err
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT DISTINCT businesses.id, businesses.website
		FROM job_businesses
		JOIN businesses ON businesses.id = job_businesses.business_id
		WHERE job_businesses.job_id = ? AND businesses.deleted_at IS NULL
		ORDER BY businesses.id`,
		jobID,
	)
	if err != nil {
		return web.EnrichmentBatch{}, fmt.Errorf("read job enrichment targets: %w", err)
	}
	candidates := make([]enrichmentCandidate, 0)
	for rows.Next() {
		var candidate enrichmentCandidate
		if err := rows.Scan(&candidate.businessID, &candidate.websiteURL); err != nil {
			_ = rows.Close()
			return web.EnrichmentBatch{}, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return web.EnrichmentBatch{}, err
	}
	if err := rows.Close(); err != nil {
		return web.EnrichmentBatch{}, err
	}

	batch, err := queueEnrichmentCandidates(ctx, tx, candidates, options, "job_completion", jobID)
	if err != nil {
		return web.EnrichmentBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return web.EnrichmentBatch{}, err
	}

	return batch, nil
}

type enrichmentCandidate struct {
	businessID string
	websiteURL string
}

func queueEnrichmentCandidates(
	ctx context.Context,
	tx *sql.Tx,
	candidates []enrichmentCandidate,
	options web.EnrichmentOptions,
	requestedBy string,
	jobID string,
) (web.EnrichmentBatch, error) {
	encodedOptions, err := json.Marshal(options)
	if err != nil {
		return web.EnrichmentBatch{}, fmt.Errorf("encode enrichment options: %w", err)
	}
	now := time.Now().UTC()
	batch := web.EnrichmentBatch{Tasks: make([]web.EnrichmentTask, 0, len(candidates))}

	for _, candidate := range candidates {
		candidate.websiteURL = strings.TrimSpace(candidate.websiteURL)
		if candidate.websiteURL == "" {
			batch.Skipped++
			continue
		}

		existing, found, err := activeEnrichmentTask(ctx, tx, candidate.businessID)
		if err != nil {
			return web.EnrichmentBatch{}, err
		}
		if found {
			batch.Tasks = append(batch.Tasks, existing)
			batch.Skipped++
			continue
		}
		if !options.Force && options.StaleAfterHours > 0 {
			var lastCompleted sql.NullInt64
			if err := tx.QueryRowContext(
				ctx,
				`SELECT MAX(completed_at) FROM website_audits WHERE business_id = ?`,
				candidate.businessID,
			).Scan(&lastCompleted); err != nil {
				return web.EnrichmentBatch{}, err
			}
			cutoff := now.Add(-time.Duration(options.StaleAfterHours) * time.Hour).Unix()
			if lastCompleted.Valid && lastCompleted.Int64 >= cutoff {
				batch.Skipped++
				continue
			}
		}

		task := web.EnrichmentTask{
			ID:          uuid.NewString(),
			BusinessID:  candidate.businessID,
			JobID:       jobID,
			WebsiteURL:  candidate.websiteURL,
			State:       "queued",
			RequestedBy: requestedBy,
			Options:     options,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		var nullableJobID any
		if jobID != "" {
			nullableJobID = jobID
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO enrichment_tasks(
				id, business_id, job_id, website_url, state, requested_by,
				options, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
			task.ID,
			task.BusinessID,
			nullableJobID,
			task.WebsiteURL,
			task.RequestedBy,
			string(encodedOptions),
			now.Unix(),
			now.Unix(),
		); err != nil {
			return web.EnrichmentBatch{}, fmt.Errorf("queue enrichment task: %w", err)
		}
		batch.Tasks = append(batch.Tasks, task)
		batch.Queued++
	}

	return batch, nil
}

func activeEnrichmentTask(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
) (web.EnrichmentTask, bool, error) {
	row := tx.QueryRowContext(
		ctx,
		`SELECT id, business_id, COALESCE(job_id, ''), website_url, state, requested_by,
			options, attempts, audit_id, error, created_at, started_at, finished_at, updated_at
		FROM enrichment_tasks
		WHERE business_id = ? AND state IN ('queued', 'running')
		ORDER BY created_at, id LIMIT 1`,
		businessID,
	)
	task, err := scanEnrichmentTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.EnrichmentTask{}, false, nil
	}
	if err != nil {
		return web.EnrichmentTask{}, false, err
	}

	return task, true, nil
}

func (repo *repo) ClaimEnrichmentTask(ctx context.Context) (web.EnrichmentTask, bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.EnrichmentTask{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	// An interrupted task is safe to retry: every persisted child row is tied to
	// an immutable audit and the task is only completed after that transaction.
	staleRunning := time.Now().UTC().Add(-3 * time.Hour).Unix()
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE enrichment_tasks SET state = 'queued', started_at = NULL,
			error = 'Recovered after an interrupted local worker', updated_at = ?
		WHERE state = 'running' AND updated_at < ?`,
		time.Now().UTC().Unix(),
		staleRunning,
	); err != nil {
		return web.EnrichmentTask{}, false, fmt.Errorf("recover enrichment tasks: %w", err)
	}

	row := tx.QueryRowContext(
		ctx,
		`SELECT id, business_id, COALESCE(job_id, ''), website_url, state, requested_by,
			options, attempts, audit_id, error, created_at, started_at, finished_at, updated_at
		FROM enrichment_tasks WHERE state = 'queued' ORDER BY created_at, id LIMIT 1`,
	)
	task, err := scanEnrichmentTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return web.EnrichmentTask{}, false, err
		}
		return web.EnrichmentTask{}, false, nil
	}
	if err != nil {
		return web.EnrichmentTask{}, false, err
	}

	now := time.Now().UTC()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE enrichment_tasks SET state = 'running', attempts = attempts + 1,
			started_at = ?, finished_at = NULL, error = '', updated_at = ?
		WHERE id = ? AND state = 'queued'`,
		now.Unix(),
		now.Unix(),
		task.ID,
	)
	if err != nil {
		return web.EnrichmentTask{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return web.EnrichmentTask{}, false, err
	}
	if changed != 1 {
		return web.EnrichmentTask{}, false, fmt.Errorf("claim enrichment task %s: concurrent state change", task.ID)
	}
	if err := tx.Commit(); err != nil {
		return web.EnrichmentTask{}, false, err
	}
	task.State = "running"
	task.Attempts++
	task.StartedAt = &now
	task.UpdatedAt = now
	task.Error = ""

	return task, true, nil
}

func (repo *repo) RecoverEnrichmentTasks(ctx context.Context) (int, error) {
	now := time.Now().UTC().Unix()
	result, err := repo.db.ExecContext(
		ctx,
		`UPDATE enrichment_tasks SET state = 'queued', started_at = NULL,
			error = 'Recovered after an interrupted local worker', updated_at = ?
		WHERE state = 'running'`,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("recover enrichment tasks: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(count), nil
}

func (repo *repo) FinishEnrichmentTask(ctx context.Context, taskID string, auditID *int64, taskErr error) error {
	state := "completed"
	errorText := ""
	if taskErr != nil {
		state = "failed"
		errorText = strings.TrimSpace(taskErr.Error())
		if len(errorText) > 2000 {
			errorText = errorText[:2000]
		}
	}
	now := time.Now().UTC().Unix()
	result, err := repo.db.ExecContext(
		ctx,
		`UPDATE enrichment_tasks SET state = ?, audit_id = ?, error = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND state = 'running'`,
		state,
		auditID,
		errorText,
		now,
		now,
		taskID,
	)
	if err != nil {
		return fmt.Errorf("finish enrichment task: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", web.ErrEnrichmentTaskNotFound, taskID)
	}

	return nil
}

func (repo *repo) ListEnrichmentTasks(ctx context.Context, limit int) ([]web.EnrichmentTask, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, business_id, COALESCE(job_id, ''), website_url, state, requested_by,
			options, attempts, audit_id, error, created_at, started_at, finished_at, updated_at
		FROM enrichment_tasks ORDER BY created_at DESC, id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]web.EnrichmentTask, 0)
	for rows.Next() {
		task, err := scanEnrichmentTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

func scanEnrichmentTask(row scannable) (web.EnrichmentTask, error) {
	var task web.EnrichmentTask
	var encodedOptions string
	var auditID, startedAt, finishedAt sql.NullInt64
	var createdAt, updatedAt int64
	if err := row.Scan(
		&task.ID,
		&task.BusinessID,
		&task.JobID,
		&task.WebsiteURL,
		&task.State,
		&task.RequestedBy,
		&encodedOptions,
		&task.Attempts,
		&auditID,
		&task.Error,
		&createdAt,
		&startedAt,
		&finishedAt,
		&updatedAt,
	); err != nil {
		return web.EnrichmentTask{}, err
	}
	if err := json.Unmarshal([]byte(encodedOptions), &task.Options); err != nil {
		return web.EnrichmentTask{}, fmt.Errorf("decode enrichment options: %w", err)
	}
	if auditID.Valid {
		value := auditID.Int64
		task.AuditID = &value
	}
	task.CreatedAt = time.Unix(createdAt, 0).UTC()
	task.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if startedAt.Valid {
		value := time.Unix(startedAt.Int64, 0).UTC()
		task.StartedAt = &value
	}
	if finishedAt.Valid {
		value := time.Unix(finishedAt.Int64, 0).UTC()
		task.FinishedAt = &value
	}

	return task, nil
}

func (repo *repo) StoreWebsiteAudit(
	ctx context.Context,
	task web.EnrichmentTask,
	analysis enrichment.Result,
	startedAt time.Time,
	completedAt time.Time,
) (int64, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM businesses WHERE id = ?`, task.BusinessID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, task.BusinessID)
		}
		return 0, err
	}
	if task.ID != "" {
		var existingAuditID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM website_audits WHERE task_id = ? LIMIT 1`, task.ID,
		).Scan(&existingAuditID)
		if err == nil {
			return existingAuditID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("inspect existing website audit: %w", err)
		}
	}

	homepage := enrichment.PageResult{}
	for _, page := range analysis.Pages {
		if page.Kind == enrichment.PageHomepage {
			homepage = page
			break
		}
	}
	status := websiteAuditStatus(analysis)
	requestedURL := strings.TrimSpace(analysis.RequestedURL)
	if requestedURL == "" {
		requestedURL = task.WebsiteURL
	}
	domain := resultimport.NormalizeDomain(analysis.FinalURL)
	if domain == "" {
		domain = resultimport.NormalizeDomain(requestedURL)
	}
	redirectsJSON := mustMarshalEnrichment(analysis.RedirectChain, "[]")
	technologyNames := detectionNames(analysis.Technologies)
	trackerNames := detectionNames(analysis.Trackers)
	technologiesJSON := mustMarshalEnrichment(technologyNames, "[]")
	trackersJSON := mustMarshalEnrichment(trackerNames, "[]")
	socialsJSON := mustMarshalEnrichment(analysis.SocialProfiles, "[]")

	var previousStatus, previousFinalURL string
	_ = tx.QueryRowContext(
		ctx,
		`SELECT status, final_url FROM websites WHERE business_id = ? AND url = ?`,
		task.BusinessID,
		task.WebsiteURL,
	).Scan(&previousStatus, &previousFinalURL)

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO websites(
			business_id, url, final_url, domain, status, http_status, https,
			response_time_ms, redirect_chain, page_title, meta_description,
			language, technologies, social_links, last_checked_at, tls_valid,
			certificate_error, pages_checked, internal_links_checked,
			broken_internal_links, mixed_content, parked, coming_soon,
			placeholder, trackers
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(business_id, url) DO UPDATE SET
			final_url = excluded.final_url,
			domain = excluded.domain,
			status = excluded.status,
			http_status = excluded.http_status,
			https = excluded.https,
			response_time_ms = excluded.response_time_ms,
			redirect_chain = excluded.redirect_chain,
			page_title = excluded.page_title,
			meta_description = excluded.meta_description,
			language = excluded.language,
			technologies = excluded.technologies,
			social_links = excluded.social_links,
			last_checked_at = excluded.last_checked_at,
			tls_valid = excluded.tls_valid,
			certificate_error = excluded.certificate_error,
			pages_checked = excluded.pages_checked,
			internal_links_checked = excluded.internal_links_checked,
			broken_internal_links = excluded.broken_internal_links,
			mixed_content = excluded.mixed_content,
			parked = excluded.parked,
			coming_soon = excluded.coming_soon,
			placeholder = excluded.placeholder,
			trackers = excluded.trackers`,
		task.BusinessID,
		task.WebsiteURL,
		analysis.FinalURL,
		domain,
		status,
		analysis.StatusCode,
		boolInt(analysis.HTTPS),
		analysis.ResponseTime.Milliseconds(),
		redirectsJSON,
		homepage.Title,
		homepage.MetaDescription,
		homepage.Language,
		technologiesJSON,
		socialsJSON,
		completedAt.Unix(),
		boolInt(analysis.TLSValid),
		analysis.CertificateError,
		len(analysis.Pages),
		analysis.InternalLinksChecked,
		analysis.BrokenInternalLinkCount,
		boolInt(analysis.MixedContent),
		boolInt(analysis.Parked),
		boolInt(analysis.ComingSoon),
		boolInt(analysis.Placeholder),
		trackersJSON,
	); err != nil {
		return 0, fmt.Errorf("store current website analysis: %w", err)
	}

	var websiteID int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT id FROM websites WHERE business_id = ? AND url = ?`,
		task.BusinessID,
		task.WebsiteURL,
	).Scan(&websiteID); err != nil {
		return 0, fmt.Errorf("read stored website: %w", err)
	}

	optionsJSON := mustMarshalEnrichment(task.Options, "{}")
	rawJSON := mustMarshalEnrichment(analysis, "{}")
	auditResult, err := tx.ExecContext(
		ctx,
		`INSERT INTO website_audits(
			business_id, website_id, task_id, requested_url, final_url, reachable,
			status_code, https, tls_valid, certificate_error, response_time_ms,
			redirect_chain, internal_links_checked, broken_internal_link_count,
			broken_internal_links, mixed_content, parked, coming_soon, placeholder,
			template_indicators, technologies, trackers, emails, phones,
			social_profiles, options, raw_result, error, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.BusinessID,
		websiteID,
		task.ID,
		requestedURL,
		analysis.FinalURL,
		boolInt(analysis.Reachable),
		analysis.StatusCode,
		boolInt(analysis.HTTPS),
		boolInt(analysis.TLSValid),
		analysis.CertificateError,
		analysis.ResponseTime.Milliseconds(),
		redirectsJSON,
		analysis.InternalLinksChecked,
		analysis.BrokenInternalLinkCount,
		mustMarshalEnrichment(analysis.BrokenInternalLinks, "[]"),
		boolInt(analysis.MixedContent),
		boolInt(analysis.Parked),
		boolInt(analysis.ComingSoon),
		boolInt(analysis.Placeholder),
		mustMarshalEnrichment(analysis.TemplateIndicators, "[]"),
		mustMarshalEnrichment(analysis.Technologies, "[]"),
		mustMarshalEnrichment(analysis.Trackers, "[]"),
		mustMarshalEnrichment(analysis.Emails, "[]"),
		mustMarshalEnrichment(analysis.Phones, "[]"),
		mustMarshalEnrichment(analysis.SocialProfiles, "[]"),
		optionsJSON,
		rawJSON,
		analysis.Error,
		startedAt.Unix(),
		completedAt.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("insert website audit: %w", err)
	}
	auditID, err := auditResult.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read website audit ID: %w", err)
	}

	sourceIDs, err := storeAuditPages(ctx, tx, task, auditID, analysis.Pages, completedAt)
	if err != nil {
		return 0, err
	}
	if err := storeAuditDetections(ctx, tx, task.BusinessID, auditID, analysis); err != nil {
		return 0, err
	}
	contactsChanged, err := storeAuditContacts(ctx, tx, task, auditID, analysis, sourceIDs, completedAt)
	if err != nil {
		return 0, err
	}
	if err := recordWebsiteAuditChanges(
		ctx,
		tx,
		task.BusinessID,
		previousStatus,
		status,
		previousFinalURL,
		analysis.FinalURL,
		contactsChanged,
		completedAt,
	); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET
			emails = COALESCE((SELECT json_group_array(normalized_value) FROM (
				SELECT normalized_value FROM emails WHERE business_id = ? ORDER BY confidence DESC, rank, id
			)), '[]'),
			phone = CASE WHEN phone = '' THEN COALESCE((SELECT value FROM phones
				WHERE business_id = ? ORDER BY confidence DESC, id LIMIT 1), '') ELSE phone END,
			normalized_phone = CASE WHEN normalized_phone = '' THEN COALESCE((SELECT normalized_value FROM phones
				WHERE business_id = ? ORDER BY confidence DESC, id LIMIT 1), '') ELSE normalized_phone END,
			website_status = ?,
			website_response_ms = ?,
			updated_at = ?
		WHERE id = ?`,
		task.BusinessID,
		task.BusinessID,
		task.BusinessID,
		status,
		analysis.ResponseTime.Milliseconds(),
		completedAt.Unix(),
		task.BusinessID,
	); err != nil {
		return 0, fmt.Errorf("refresh enriched business contacts: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	repo.reclassifyProspectsForBusiness(ctx, task.BusinessID)

	return auditID, nil
}

func storeAuditPages(
	ctx context.Context,
	tx *sql.Tx,
	task web.EnrichmentTask,
	auditID int64,
	pages []enrichment.PageResult,
	completedAt time.Time,
) (map[string]int64, error) {
	sourceIDs := make(map[string]int64, len(pages))
	for _, page := range pages {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO website_audit_pages(
				audit_id, requested_url, final_url, page_kind, status_code,
				response_time_ms, size_bytes, body_truncated, content_type,
				page_title, meta_description, language, mobile_viewport,
				mixed_content, copyright_year, old_copyright, redirects, error
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			auditID,
			page.RequestedURL,
			page.FinalURL,
			page.Kind,
			page.StatusCode,
			page.ResponseTime.Milliseconds(),
			page.SizeBytes,
			boolInt(page.BodyTruncated),
			page.ContentType,
			page.Title,
			page.MetaDescription,
			page.Language,
			boolInt(page.MobileViewport),
			boolInt(page.MixedContent),
			page.CopyrightYear,
			boolInt(page.OldCopyright),
			mustMarshalEnrichment(page.Redirects, "[]"),
			page.Error,
		); err != nil {
			return nil, fmt.Errorf("insert website audit page: %w", err)
		}

		pageJSON := mustMarshalEnrichment(page, "{}")
		sourceResult, err := tx.ExecContext(
			ctx,
			`INSERT INTO business_sources(
				business_id, job_id, source_type, source_url, extraction_method,
				confidence, extracted_at, raw_json, normalized_json, record_hash, ingest_key
			) VALUES (?, ?, ?, ?, 'bounded_html_analysis', ?, ?, ?, ?, ?, ?)`,
			task.BusinessID,
			nullableText(task.JobID),
			"website_"+string(page.Kind),
			firstNonEmpty(page.FinalURL, page.RequestedURL),
			pageConfidence(page),
			completedAt.Unix(),
			pageJSON,
			pageJSON,
			hashText(pageJSON),
			fmt.Sprintf("enrichment:%s:page:%s:%s", task.ID, page.Kind, page.RequestedURL),
		)
		if err != nil {
			return nil, fmt.Errorf("insert website page source: %w", err)
		}
		sourceID, err := sourceResult.LastInsertId()
		if err != nil {
			return nil, err
		}
		sourceIDs[auditSourceKey(string(page.Kind), firstNonEmpty(page.FinalURL, page.RequestedURL))] = sourceID
		sourceIDs[auditSourceKey(string(page.Kind), page.RequestedURL)] = sourceID
	}

	return sourceIDs, nil
}

func storeAuditDetections(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	auditID int64,
	analysis enrichment.Result,
) error {
	groups := []struct {
		kind  string
		items []enrichment.Detection
	}{
		{kind: "technology", items: analysis.Technologies},
		{kind: "tracker", items: analysis.Trackers},
	}
	for _, group := range groups {
		for _, detection := range group.items {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO website_detections(
					audit_id, business_id, detection_type, name, confidence, evidence
				) VALUES (?, ?, ?, ?, ?, ?)`,
				auditID,
				businessID,
				group.kind,
				detection.Name,
				detection.Confidence,
				mustMarshalEnrichment(detection.Evidence, "[]"),
			); err != nil {
				return fmt.Errorf("insert website detection: %w", err)
			}
		}
	}

	return nil
}

func storeAuditContacts(
	ctx context.Context,
	tx *sql.Tx,
	task web.EnrichmentTask,
	auditID int64,
	analysis enrichment.Result,
	sourceIDs map[string]int64,
	completedAt time.Time,
) (bool, error) {
	changed := false
	for _, email := range analysis.Emails {
		if strings.TrimSpace(email.Address) == "" {
			continue
		}
		kind := "unknown"
		if email.RoleAddress {
			kind = "role"
		} else if email.PersonalLikely {
			kind = "personal-looking"
		}
		status := "syntax-invalid"
		if email.ValidSyntax {
			status = "syntax-valid"
		}
		switch email.MXStatus {
		case enrichment.MXPresent:
			status = "mx-present"
		case enrichment.MXMissing:
			status = "mx-missing"
		case enrichment.MXError:
			status = "mx-error"
		case enrichment.MXNotChecked:
		}
		var domainHasMX any
		if email.MXStatus == enrichment.MXPresent {
			domainHasMX = 1
		} else if email.MXStatus == enrichment.MXMissing {
			domainHasMX = 0
		}
		firstSource := enrichment.Source{}
		if len(email.Sources) > 0 {
			firstSource = email.Sources[0]
		}
		confidence := float64(email.Relevance) / 100
		var emailExists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM emails WHERE business_id = ? AND normalized_value = ?`,
			task.BusinessID, strings.ToLower(email.Address),
		).Scan(&emailExists); err != nil {
			return false, fmt.Errorf("inspect enriched email: %w", err)
		}
		_, err := tx.ExecContext(
			ctx,
			`INSERT INTO emails(
				business_id, value, normalized_value, kind, status, domain_has_mx,
				disposable, source_url, extraction_method, confidence,
				last_checked_at, valid_syntax, role, personal_likely,
				mx_status, mx_records, relevance, rank
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(business_id, normalized_value) DO UPDATE SET
				value = excluded.value,
				kind = excluded.kind,
				status = excluded.status,
				domain_has_mx = excluded.domain_has_mx,
				disposable = excluded.disposable,
				source_url = excluded.source_url,
				extraction_method = excluded.extraction_method,
				confidence = excluded.confidence,
				last_checked_at = excluded.last_checked_at,
				valid_syntax = excluded.valid_syntax,
				role = excluded.role,
				personal_likely = excluded.personal_likely,
				mx_status = excluded.mx_status,
				mx_records = excluded.mx_records,
				relevance = excluded.relevance,
				rank = excluded.rank`,
			task.BusinessID,
			email.Address,
			strings.ToLower(email.Address),
			kind,
			status,
			domainHasMX,
			boolInt(email.Disposable),
			firstSource.PageURL,
			firstSource.Method,
			confidence,
			completedAt.Unix(),
			boolInt(email.ValidSyntax),
			email.Role,
			boolInt(email.PersonalLikely),
			email.MXStatus,
			mustMarshalEnrichment(email.MXRecords, "[]"),
			email.Relevance,
			email.Rank,
		)
		if err != nil {
			return false, fmt.Errorf("store enriched email: %w", err)
		}
		if emailExists == 0 {
			changed = true
			if err := insertEnrichmentDiscovery(ctx, tx, task.BusinessID, "email", email.Address, completedAt); err != nil {
				return false, err
			}
		}
		for _, source := range email.Sources {
			if err := storeContactEvidence(ctx, tx, auditID, task.BusinessID, "email", email.Address,
				strings.ToLower(email.Address), source, confidence, email, completedAt); err != nil {
				return false, err
			}
			if err := storeContactProvenance(ctx, tx, task.BusinessID, "email", email.Address,
				strings.ToLower(email.Address), source, confidence, sourceIDs, completedAt); err != nil {
				return false, err
			}
		}
	}

	for _, phone := range analysis.Phones {
		normalized := resultimport.NormalizePhone(phone.Value, "")
		normalizedValue := normalized.Normalized
		if normalizedValue == "" {
			normalizedValue = strings.TrimSpace(phone.Value)
		}
		if normalizedValue == "" {
			continue
		}
		var phoneExists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM phones WHERE business_id = ? AND normalized_value = ?`,
			task.BusinessID, normalizedValue,
		).Scan(&phoneExists); err != nil {
			return false, fmt.Errorf("inspect enriched phone: %w", err)
		}
		firstSource := enrichment.Source{}
		if len(phone.Sources) > 0 {
			firstSource = phone.Sources[0]
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO phones(business_id, value, normalized_value, kind, source_url, confidence)
			VALUES (?, ?, ?, 'website', ?, 0.8)
			ON CONFLICT(business_id, normalized_value) DO UPDATE SET
				value = excluded.value, kind = excluded.kind,
				source_url = excluded.source_url, confidence = excluded.confidence`,
			task.BusinessID,
			phone.Value,
			normalizedValue,
			firstSource.PageURL,
		); err != nil {
			return false, fmt.Errorf("store enriched phone: %w", err)
		}
		for _, source := range phone.Sources {
			if err := storeContactEvidence(ctx, tx, auditID, task.BusinessID, "phone", phone.Value,
				normalizedValue, source, 0.8, phone, completedAt); err != nil {
				return false, err
			}
			if err := storeContactProvenance(ctx, tx, task.BusinessID, "phone", phone.Value,
				normalizedValue, source, 0.8, sourceIDs, completedAt); err != nil {
				return false, err
			}
		}
		if phoneExists == 0 {
			changed = true
			if err := insertEnrichmentDiscovery(ctx, tx, task.BusinessID, "phone", normalizedValue, completedAt); err != nil {
				return false, err
			}
		}
	}

	for _, social := range analysis.SocialProfiles {
		var socialExists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM social_profiles WHERE business_id = ? AND platform = ? AND url = ?`,
			task.BusinessID, social.Platform, social.URL,
		).Scan(&socialExists); err != nil {
			return false, fmt.Errorf("inspect enriched social profile: %w", err)
		}
		firstSource := enrichment.Source{}
		if len(social.Sources) > 0 {
			firstSource = social.Sources[0]
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO social_profiles(business_id, platform, url, source_url, confidence)
			VALUES (?, ?, ?, ?, 0.9)
			ON CONFLICT(business_id, platform, url) DO UPDATE SET
				source_url = excluded.source_url, confidence = excluded.confidence`,
			task.BusinessID,
			social.Platform,
			social.URL,
			firstSource.PageURL,
		); err != nil {
			return false, fmt.Errorf("store enriched social profile: %w", err)
		}
		for _, source := range social.Sources {
			if err := storeContactEvidence(ctx, tx, auditID, task.BusinessID, "social", social.URL,
				social.URL, source, 0.9, social, completedAt); err != nil {
				return false, err
			}
			if err := storeContactProvenance(ctx, tx, task.BusinessID, "social_profile", social.URL,
				social.URL, source, 0.9, sourceIDs, completedAt); err != nil {
				return false, err
			}
		}
		if socialExists == 0 {
			changed = true
			if err := insertEnrichmentDiscovery(ctx, tx, task.BusinessID, "social_profile", social.URL, completedAt); err != nil {
				return false, err
			}
		}
	}

	return changed, nil
}

func insertEnrichmentDiscovery(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	fieldName string,
	value string,
	detectedAt time.Time,
) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO business_changes(
			business_id, field_name, before_value, after_value, change_kind, detected_at
		) VALUES (?, ?, 'null', ?, 'discovered', ?)`,
		businessID,
		fieldName,
		mustMarshalEnrichment(value, `""`),
		detectedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("record enriched contact discovery: %w", err)
	}

	return nil
}

func storeContactEvidence(
	ctx context.Context,
	tx *sql.Tx,
	auditID int64,
	businessID string,
	contactType string,
	value string,
	normalized string,
	source enrichment.Source,
	confidence float64,
	metadata any,
	createdAt time.Time,
) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO contact_evidence(
			audit_id, business_id, contact_type, value, normalized_value,
			source_url, page_kind, extraction_method, confidence, metadata, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		auditID,
		businessID,
		contactType,
		value,
		normalized,
		source.PageURL,
		source.PageKind,
		source.Method,
		confidence,
		mustMarshalEnrichment(metadata, "{}"),
		createdAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert contact evidence: %w", err)
	}

	return nil
}

func storeContactProvenance(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	fieldName string,
	original string,
	normalized string,
	source enrichment.Source,
	confidence float64,
	sourceIDs map[string]int64,
	extractedAt time.Time,
) error {
	sourceID := sourceIDs[auditSourceKey(string(source.PageKind), source.PageURL)]
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO field_provenance(
			business_id, field_name, original_value, normalized_value, preferred,
			source_type, source_url, extraction_method, confidence, extracted_at,
			source_id, original_json, normalized_json, value_hash
		) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		businessID,
		fieldName,
		original,
		normalized,
		"website_"+string(source.PageKind),
		source.PageURL,
		source.Method,
		confidence,
		extractedAt.Unix(),
		nullableInt64(sourceID),
		mustMarshalEnrichment(original, `""`),
		mustMarshalEnrichment(normalized, `""`),
		hashText(normalized),
	)
	if err != nil {
		return fmt.Errorf("insert contact provenance: %w", err)
	}

	return nil
}

func recordWebsiteAuditChanges(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	previousStatus string,
	currentStatus string,
	previousFinalURL string,
	currentFinalURL string,
	contactsChanged bool,
	detectedAt time.Time,
) error {
	changed := false
	changes := []struct {
		field  string
		before string
		after  string
	}{
		{field: "website_status", before: previousStatus, after: currentStatus},
		{field: "website_final_url", before: previousFinalURL, after: currentFinalURL},
	}
	for _, change := range changes {
		if change.before == change.after || (change.before == "" && change.after == "") {
			continue
		}
		kind := "updated"
		if change.before == "" {
			kind = "discovered"
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO business_changes(
				business_id, field_name, before_value, after_value, change_kind, detected_at
			) VALUES (?, ?, ?, ?, ?, ?)`,
			businessID,
			change.field,
			mustMarshalEnrichment(change.before, `""`),
			mustMarshalEnrichment(change.after, `""`),
			kind,
			detectedAt.Unix(),
		); err != nil {
			return fmt.Errorf("record website change: %w", err)
		}
		changed = true
	}
	if contactsChanged {
		changed = true
	}
	if changed {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE businesses SET change_status = 'changed', last_changed_at = ?, updated_at = ? WHERE id = ?`,
			detectedAt.Unix(),
			detectedAt.Unix(),
			businessID,
		); err != nil {
			return fmt.Errorf("mark enriched business changed: %w", err)
		}
	}

	return nil
}

// AttachAuditScreenshot records the stored homepage screenshot for one audit
// and mirrors the same relative path onto the audited website row so the
// results drawer can render the preview. It only ever touches the two
// screenshot_path columns; immutable audit evidence stays untouched.
func (repo *repo) AttachAuditScreenshot(ctx context.Context, auditID int64, relativePath string) error {
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return fmt.Errorf("attach audit screenshot: empty screenshot path")
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var websiteID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT website_id FROM website_audits WHERE id = ?`, auditID).Scan(&websiteID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("attach audit screenshot: website audit %d not found", auditID)
	}
	if err != nil {
		return fmt.Errorf("read audited website: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE website_audits SET screenshot_path = ? WHERE id = ?`,
		relativePath,
		auditID,
	); err != nil {
		return fmt.Errorf("store audit screenshot path: %w", err)
	}
	if websiteID.Valid {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE websites SET screenshot_path = ? WHERE id = ?`,
			relativePath,
			websiteID.Int64,
		); err != nil {
			return fmt.Errorf("store website screenshot path: %w", err)
		}
	}

	return tx.Commit()
}

// AttachAuditErrorScreenshot stores the optional capture of a site that failed
// its audit. It is deliberately kept on the immutable audit row only: the
// current websites row keeps the last good homepage preview, so a failing
// rescan never replaces evidence of a site that used to work.
func (repo *repo) AttachAuditErrorScreenshot(ctx context.Context, auditID int64, relativePath string) error {
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return fmt.Errorf("attach audit error screenshot: empty screenshot path")
	}

	result, err := repo.db.ExecContext(
		ctx,
		`UPDATE website_audits SET error_screenshot_path = ? WHERE id = ?`,
		relativePath,
		auditID,
	)
	if err != nil {
		return fmt.Errorf("store audit error screenshot path: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store audit error screenshot path: %w", err)
	}

	if affected == 0 {
		return fmt.Errorf("attach audit error screenshot: website audit %d not found", auditID)
	}

	return nil
}

// RecordScreenshotEvent appends one auditable screenshot event, for example
// screenshot_failed or screenshot_skipped_no_driver. Events deliberately live
// in audit_logs so a capture problem never rewrites audit evidence.
func (repo *repo) RecordScreenshotEvent(ctx context.Context, action, entityID, details string) error {
	if strings.TrimSpace(details) == "" {
		details = "{}"
	}
	if _, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, 'website_audit', ?, ?, ?)`,
		action,
		entityID,
		details,
		time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("record screenshot event: %w", err)
	}

	return nil
}

func (repo *repo) WebsiteAuditHistory(
	ctx context.Context,
	businessID string,
	limit int,
) ([]web.WebsiteAuditView, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, business_id, task_id, requested_url, final_url, reachable,
			status_code, https, tls_valid, certificate_error, response_time_ms,
			redirect_chain, internal_links_checked, broken_internal_link_count,
			broken_internal_links, mixed_content, parked, coming_soon, placeholder,
			template_indicators, technologies, trackers, emails, phones,
			social_profiles, screenshot_path, error_screenshot_path, raw_result, error, started_at, completed_at
		FROM website_audits WHERE business_id = ?
		ORDER BY completed_at DESC, id DESC LIMIT ?`,
		businessID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("read website audit history: %w", err)
	}
	// The scan error inside the loop returns early, so the cursor needs an
	// unconditional release. sql.Rows.Close is idempotent, which keeps the
	// explicit close-error reporting below intact.
	defer func() { _ = rows.Close() }()

	audits := make([]web.WebsiteAuditView, 0)
	for rows.Next() {
		var audit web.WebsiteAuditView
		var reachable, https, tlsValid, mixedContent, parked, comingSoon, placeholder int
		var redirectsJSON, brokenJSON, indicatorsJSON, technologiesJSON, trackersJSON string
		var emailsJSON, phonesJSON, socialsJSON, rawJSON string
		var startedAt, completedAt int64
		if err := rows.Scan(
			&audit.ID,
			&audit.BusinessID,
			&audit.TaskID,
			&audit.RequestedURL,
			&audit.FinalURL,
			&reachable,
			&audit.StatusCode,
			&https,
			&tlsValid,
			&audit.CertificateError,
			&audit.ResponseTimeMS,
			&redirectsJSON,
			&audit.InternalLinksChecked,
			&audit.BrokenInternalLinkCount,
			&brokenJSON,
			&mixedContent,
			&parked,
			&comingSoon,
			&placeholder,
			&indicatorsJSON,
			&technologiesJSON,
			&trackersJSON,
			&emailsJSON,
			&phonesJSON,
			&socialsJSON,
			&audit.ScreenshotPath,
			&audit.ErrorScreenshotPath,
			&rawJSON,
			&audit.Error,
			&startedAt,
			&completedAt,
		); err != nil {
			return nil, fmt.Errorf("scan website audit history: %w", err)
		}
		audit.Reachable = reachable != 0
		audit.HTTPS = https != 0
		audit.TLSValid = tlsValid != 0
		audit.MixedContent = mixedContent != 0
		audit.Parked = parked != 0
		audit.ComingSoon = comingSoon != 0
		audit.Placeholder = placeholder != 0
		audit.StartedAt = time.Unix(startedAt, 0).UTC()
		audit.CompletedAt = time.Unix(completedAt, 0).UTC()
		_ = json.Unmarshal([]byte(redirectsJSON), &audit.RedirectChain)
		_ = json.Unmarshal([]byte(brokenJSON), &audit.BrokenInternalLinks)
		_ = json.Unmarshal([]byte(indicatorsJSON), &audit.TemplateIndicators)
		_ = json.Unmarshal([]byte(technologiesJSON), &audit.Technologies)
		_ = json.Unmarshal([]byte(trackersJSON), &audit.Trackers)
		_ = json.Unmarshal([]byte(emailsJSON), &audit.Emails)
		_ = json.Unmarshal([]byte(phonesJSON), &audit.Phones)
		_ = json.Unmarshal([]byte(socialsJSON), &audit.SocialProfiles)
		var raw enrichment.Result
		if json.Unmarshal([]byte(rawJSON), &raw) == nil {
			audit.Pages = raw.Pages
			// Postal addresses and the content audit round-trip through the
			// immutable raw result, so an audit stored by an older build simply
			// reports their zero values.
			audit.Addresses = raw.Addresses
			audit.ContentAudit = raw.ContentAudit
			// The crawl URL patterns that were actually applied round-trip
			// through the same immutable result, so an audit stored before the
			// control existed simply reports none.
			audit.URLPatterns = raw.URLPatterns
		}
		audits = append(audits, audit)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(audits) == 0 {
		var exists int
		if err := repo.db.QueryRowContext(ctx, `SELECT 1 FROM businesses WHERE id = ?`, businessID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, businessID)
			}
			return nil, err
		}
	}

	return audits, nil
}

func websiteAuditStatus(analysis enrichment.Result) string {
	if analysis.Reachable && analysis.StatusCode > 0 && analysis.StatusCode < 400 {
		return "active"
	}
	if analysis.Reachable {
		return "inactive"
	}
	if analysis.Error != "" {
		return "error"
	}

	return "inactive"
}

func detectionNames(detections []enrichment.Detection) []string {
	names := make([]string, 0, len(detections))
	for _, detection := range detections {
		if strings.TrimSpace(detection.Name) != "" {
			names = append(names, detection.Name)
		}
	}

	return names
}

func mustMarshalEnrichment(value any, fallback string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fallback
	}

	return string(encoded)
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}

	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func auditSourceKey(kind string, sourceURL string) string {
	return kind + "\x00" + sourceURL
}

func pageConfidence(page enrichment.PageResult) float64 {
	if page.Error != "" || page.StatusCode == 0 {
		return 0.3
	}
	if page.StatusCode >= 400 {
		return 0.5
	}

	return 0.9
}

// Website latency history bounds. The readback exists to shape one request
// timeout, so it deliberately reads a short recent window rather than the
// whole audit trail.
const (
	minimumLatencyHistoryWindow = 1
	maximumLatencyHistoryWindow = 50
)

// WebsiteLatencyHistory returns the bounded recent probe outcomes observed for
// one business website, newest first, for the adaptive timeout policy.
//
// The query is keyed on business_id and ordered by completed_at, which is
// exactly the shape of idx_website_audits_business_time, and it reads no
// payload columns. Rows are then narrowed in Go to the audits whose requested
// or final URL normalizes to the same registrable domain as websiteURL, so a
// business that changed website does not inherit the old host's latency.
//
// A business with no audits, an unknown business ID, and an unparsable
// website URL all yield an empty history and a nil error: absent history is a
// normal state that must leave the configured timeout untouched, never a
// failure that aborts an enrichment task.
func (repo *repo) WebsiteLatencyHistory(
	ctx context.Context,
	businessID string,
	websiteURL string,
	limit int,
) (enrichment.SiteHistory, error) {
	businessID = strings.TrimSpace(businessID)
	if businessID == "" {
		return enrichment.SiteHistory{}, nil
	}
	if limit < minimumLatencyHistoryWindow {
		limit = minimumLatencyHistoryWindow
	}
	if limit > maximumLatencyHistoryWindow {
		limit = maximumLatencyHistoryWindow
	}

	host := resultimport.NormalizeDomain(websiteURL)

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT requested_url, final_url, reachable, status_code, response_time_ms, error
		FROM website_audits WHERE business_id = ?
		ORDER BY completed_at DESC, id DESC LIMIT ?`,
		businessID,
		limit,
	)
	if err != nil {
		return enrichment.SiteHistory{}, fmt.Errorf("read website latency history: %w", err)
	}
	// The scan error inside the loop returns early, so the cursor needs an
	// unconditional release. sql.Rows.Close is idempotent, which keeps the
	// explicit close-error reporting below intact.
	defer func() { _ = rows.Close() }()

	history := enrichment.SiteHistory{Host: host, Observations: make([]enrichment.SiteObservation, 0, limit)}
	for rows.Next() {
		var requestedURL, finalURL, auditError string
		var reachable, statusCode int
		var responseMS int64
		if err := rows.Scan(
			&requestedURL, &finalURL, &reachable, &statusCode, &responseMS, &auditError,
		); err != nil {
			return enrichment.SiteHistory{}, fmt.Errorf("scan website latency history: %w", err)
		}
		if host != "" &&
			resultimport.NormalizeDomain(requestedURL) != host &&
			resultimport.NormalizeDomain(finalURL) != host {
			continue
		}
		history.Observations = append(history.Observations, latencyObservation(
			reachable != 0, statusCode, responseMS, auditError,
		))
	}
	if err := rows.Err(); err != nil {
		return enrichment.SiteHistory{}, fmt.Errorf("read website latency history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return enrichment.SiteHistory{}, fmt.Errorf("close website latency history: %w", err)
	}

	var status string
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT website_status FROM businesses WHERE id = ?`,
		businessID,
	).Scan(&status); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return enrichment.SiteHistory{}, fmt.Errorf("read business website status: %w", err)
	}
	history.LastStatus = strings.TrimSpace(status)

	return history, nil
}

// latencyObservation converts one persisted audit row into the policy input.
// A stored audit records either a successful probe or an error string, and the
// error text is the only surviving evidence of how the probe failed.
func latencyObservation(
	reachable bool,
	statusCode int,
	responseMS int64,
	auditError string,
) enrichment.SiteObservation {
	observation := enrichment.SiteObservation{
		ResponseTime: time.Duration(responseMS) * time.Millisecond,
		Reachable:    reachable,
	}
	if strings.TrimSpace(auditError) == "" {
		return observation
	}
	if enrichment.IsTimeoutError(auditError) {
		observation.TimedOut = true

		return observation
	}
	// A recorded error alongside a real HTTP status is a page-level problem,
	// not a transport failure: the host still answered and its latency sample
	// stays usable.
	if reachable && statusCode > 0 {
		return observation
	}
	observation.Failed = true

	return observation
}

// RecordEnrichmentEvent appends one auditable enrichment worker event, for
// example enrichment_timeout_adapted. Events live in audit_logs so worker
// telemetry can never rewrite immutable audit evidence or change an existing
// API response shape.
func (repo *repo) RecordEnrichmentEvent(ctx context.Context, action, entityID, details string) error {
	if strings.TrimSpace(details) == "" {
		details = "{}"
	}
	if _, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, 'website_audit', ?, ?, ?)`,
		action,
		entityID,
		details,
		time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("record enrichment event: %w", err)
	}

	return nil
}
