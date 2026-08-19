package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/resultimport"
)

const (
	defaultResultLimit = 25
	maximumResultLimit = 250
)

var _ web.ResultRepository = (*repo)(nil)

// ImportLegacyCSV streams an untouched legacy result file into the normalized
// local schema. The file checksum and per-row ingest cursor make retries and
// restart backfills idempotent.
func (repo *repo) ImportLegacyCSV(
	ctx context.Context,
	job web.Job,
	path string,
) (importResult web.ResultFileImport, returnErr error) {
	info, checksum, err := inspectResultFile(ctx, path)
	if err != nil {
		return web.ResultFileImport{}, err
	}

	if previous, ok, err := repo.completedLegacyImport(ctx, job.ID, checksum); err != nil {
		return web.ResultFileImport{}, err
	} else if ok {
		previous.SkippedUnchanged = true

		return previous, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return web.ResultFileImport{}, fmt.Errorf("open legacy result CSV: %w", err)
	}
	defer file.Close()

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.ResultFileImport{}, fmt.Errorf("begin result import: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
		if returnErr != nil {
			failureContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = repo.recordLegacyImportFailure(
				failureContext,
				job.ID,
				filepath.Base(path),
				info,
				checksum,
				returnErr,
			)
		}
	}()

	now := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO legacy_imports(
			job_id, relative_path, file_size, file_mtime, file_checksum, state,
			last_row, row_count, imported_count, error, started_at, finished_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'running', 0, 0, 0, '', ?, NULL, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			relative_path = excluded.relative_path,
			file_size = excluded.file_size,
			file_mtime = excluded.file_mtime,
			file_checksum = excluded.file_checksum,
			state = 'running',
			last_row = 0,
			row_count = 0,
			imported_count = 0,
			error = '',
			started_at = excluded.started_at,
			finished_at = NULL,
			updated_at = excluded.updated_at`,
		job.ID,
		filepath.Base(path),
		info.Size(),
		info.ModTime().UTC().Unix(),
		checksum,
		now,
		now,
	); err != nil {
		return web.ResultFileImport{}, fmt.Errorf("start legacy result import: %w", err)
	}

	reader := resultimport.NewReader(file, resultimport.Options{
		SourceID:   job.ID,
		JobID:      job.ID,
		Query:      strings.Join(job.Data.Keywords, " | "),
		ObservedAt: job.Date,
	})
	summary := web.ResultFileImport{JobID: job.ID, Checksum: checksum}
	for {
		record, readErr := reader.Next(ctx)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return web.ResultFileImport{}, fmt.Errorf("normalize legacy result CSV: %w", readErr)
		}

		summary.Rows++
		summary.Warnings += int64(len(record.Warnings))
		_, importErr := importNormalizedRecord(ctx, tx, job, record)
		if importErr != nil {
			return web.ResultFileImport{}, importErr
		}
	}

	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM business_sources WHERE job_id = ?`,
		job.ID,
	).Scan(&summary.ImportedSources); err != nil {
		return web.ResultFileImport{}, fmt.Errorf("count imported source rows: %w", err)
	}
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM job_businesses WHERE job_id = ?`,
		job.ID,
	).Scan(&summary.UniqueBusinesses); err != nil {
		return web.ResultFileImport{}, fmt.Errorf("count imported businesses: %w", err)
	}
	summary.Duplicates = max(int64(0), summary.Rows-summary.UniqueBusinesses)

	finishedAt := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE legacy_imports SET
			state = 'completed', last_row = ?, row_count = ?, imported_count = ?,
			finished_at = ?, updated_at = ?, error = ''
		WHERE job_id = ?`,
		summary.Rows,
		summary.Rows,
		summary.ImportedSources,
		finishedAt,
		finishedAt,
		job.ID,
	); err != nil {
		return web.ResultFileImport{}, fmt.Errorf("finish legacy result import: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET
			raw_records = ?,
			unique_records = ?,
			duplicate_records = ?,
			websites_found = (
				SELECT COUNT(*) FROM job_businesses
				JOIN businesses ON businesses.id = job_businesses.business_id
				WHERE job_businesses.job_id = ? AND businesses.website <> ''
			),
			emails_found = (
				SELECT COUNT(DISTINCT emails.normalized_value) FROM job_businesses
				JOIN emails ON emails.business_id = job_businesses.business_id
				WHERE job_businesses.job_id = ?
			),
			updated_at = ?
		WHERE job_id = ?`,
		summary.Rows,
		summary.UniqueBusinesses,
		summary.Duplicates,
		job.ID,
		job.ID,
		finishedAt,
		job.ID,
	); err != nil {
		return web.ResultFileImport{}, fmt.Errorf("update result counters: %w", err)
	}

	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID:    job.ID,
		typeName: "result-import",
		severity: "information",
		stage:    jobruntime.StageDeduplicating,
		message:  fmt.Sprintf("Imported %d source rows into %d normalized businesses", summary.Rows, summary.UniqueBusinesses),
		context: map[string]any{
			"rows":              summary.Rows,
			"unique_businesses": summary.UniqueBusinesses,
			"duplicates":        summary.Duplicates,
			"warnings":          summary.Warnings,
		},
		createdAt: finishedAt,
	}); err != nil {
		return web.ResultFileImport{}, err
	}

	if err := tx.Commit(); err != nil {
		return web.ResultFileImport{}, fmt.Errorf("commit result import: %w", err)
	}

	return summary, nil
}

func (repo *repo) recordLegacyImportFailure(
	ctx context.Context,
	jobID string,
	relativePath string,
	info os.FileInfo,
	checksum string,
	importErr error,
) error {
	now := time.Now().UTC().Unix()
	_, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO legacy_imports(
			job_id, relative_path, file_size, file_mtime, file_checksum, state,
			last_row, row_count, imported_count, error, started_at, finished_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'failed', 0, 0, 0, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			relative_path = excluded.relative_path,
			file_size = excluded.file_size,
			file_mtime = excluded.file_mtime,
			file_checksum = excluded.file_checksum,
			state = 'failed',
			error = excluded.error,
			finished_at = excluded.finished_at,
			updated_at = excluded.updated_at`,
		jobID,
		relativePath,
		info.Size(),
		info.ModTime().UTC().Unix(),
		checksum,
		legacyImportErrorLabel(importErr),
		now,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("record legacy result import failure: %w", err)
	}

	return nil
}

func legacyImportErrorLabel(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "import cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "import deadline exceeded"
	case errors.Is(err, resultimport.ErrMalformedCSV):
		return "malformed CSV"
	case errors.Is(err, resultimport.ErrRecordTooLarge):
		return "CSV record exceeds configured limits"
	case errors.Is(err, resultimport.ErrInvalidHeader), errors.Is(err, resultimport.ErrDuplicateHeader):
		return "invalid CSV header"
	default:
		return "local result import failed"
	}
}

func inspectResultFile(ctx context.Context, path string) (os.FileInfo, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open legacy result CSV for checksum: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("inspect legacy result CSV: %w", err)
	}

	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}

		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, "", fmt.Errorf("checksum legacy result CSV: %w", readErr)
		}
	}

	return info, hex.EncodeToString(hash.Sum(nil)), nil
}

func (repo *repo) completedLegacyImport(
	ctx context.Context,
	jobID string,
	checksum string,
) (web.ResultFileImport, bool, error) {
	var state, storedChecksum string
	var rows, imported int64
	err := repo.db.QueryRowContext(
		ctx,
		`SELECT state, file_checksum, row_count, imported_count
		FROM legacy_imports WHERE job_id = ?`,
		jobID,
	).Scan(&state, &storedChecksum, &rows, &imported)
	if errors.Is(err, sql.ErrNoRows) {
		return web.ResultFileImport{}, false, nil
	}
	if err != nil {
		return web.ResultFileImport{}, false, fmt.Errorf("read legacy import state: %w", err)
	}
	if state != "completed" || storedChecksum != checksum {
		return web.ResultFileImport{}, false, nil
	}

	summary := web.ResultFileImport{
		JobID:           jobID,
		Rows:            rows,
		ImportedSources: imported,
		Checksum:        checksum,
	}
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM job_businesses WHERE job_id = ?`,
		jobID,
	).Scan(&summary.UniqueBusinesses); err != nil {
		return web.ResultFileImport{}, false, fmt.Errorf("count existing imported businesses: %w", err)
	}
	summary.Duplicates = max(int64(0), rows-summary.UniqueBusinesses)

	return summary, true, nil
}

func importNormalizedRecord(
	ctx context.Context,
	tx *sql.Tx,
	job web.Job,
	record resultimport.Record,
) (bool, error) {
	observedAt := job.Date.UTC()
	if record.Source.ObservedAt != nil {
		observedAt = record.Source.ObservedAt.UTC()
	}
	observedUnix := observedAt.Unix()

	targetID, matchedIDs, err := resolveBusinessID(ctx, tx, record.Business)
	if err != nil {
		return false, err
	}
	if targetID == "" {
		targetID = record.Business.ID
	}

	rawJSON, err := json.Marshal(record.Raw)
	if err != nil {
		return false, fmt.Errorf("encode raw result row: %w", err)
	}
	normalizedJSON, err := json.Marshal(record.Business)
	if err != nil {
		return false, fmt.Errorf("encode normalized result row: %w", err)
	}

	targetID, wasNew, previousHash, err := ensureBusiness(
		ctx,
		tx,
		targetID,
		record.Business,
		record.Source.InputID,
		string(rawJSON),
		observedUnix,
	)
	if err != nil {
		return false, err
	}

	sourceID, inserted, err := insertBusinessSource(
		ctx,
		tx,
		targetID,
		job.ID,
		record,
		string(rawJSON),
		string(normalizedJSON),
		observedUnix,
	)
	if err != nil || !inserted {
		return inserted, err
	}

	changed := wasNew || previousHash != record.Business.RecordHash
	if err := recordBusinessVersion(
		ctx,
		tx,
		targetID,
		job.ID,
		sourceID,
		record.Business.RecordHash,
		string(normalizedJSON),
		observedUnix,
	); err != nil {
		return false, err
	}

	if err := storeIdentityKeys(ctx, tx, targetID, sourceID, record.Business.IdentityKeys, observedUnix); err != nil {
		return false, err
	}
	if err := storeContacts(ctx, tx, targetID, record, observedUnix); err != nil {
		return false, err
	}
	if err := storeProvenance(ctx, tx, targetID, sourceID, record, observedUnix); err != nil {
		return false, err
	}
	if err := linkJobBusiness(ctx, tx, job.ID, targetID, sourceID, observedUnix, wasNew, changed); err != nil {
		return false, err
	}
	if err := storeDuplicateCandidates(ctx, tx, targetID, matchedIDs, record.Business.IdentityKeys, observedUnix); err != nil {
		return false, err
	}
	if err := storeFuzzyDuplicateCandidates(ctx, tx, targetID, observedUnix); err != nil {
		return false, err
	}
	rules, err := ensureActiveQualityRules(ctx, tx)
	if err != nil {
		return false, err
	}
	if _, err := scoreBusiness(ctx, tx, targetID, rules, observedAt); err != nil {
		return false, err
	}

	return true, nil
}

func resolveBusinessID(
	ctx context.Context,
	tx *sql.Tx,
	business resultimport.Business,
) (string, []string, error) {
	matched := make([]string, 0)
	seen := make(map[string]struct{})
	for _, key := range business.IdentityKeys {
		rows, err := tx.QueryContext(
			ctx,
			`SELECT business_id FROM business_identity_keys
			WHERE key_type = ? AND key_value = ? ORDER BY created_at, business_id`,
			key.Kind,
			key.Value,
		)
		if err != nil {
			return "", nil, fmt.Errorf("match business identity: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()

				return "", nil, fmt.Errorf("scan business identity: %w", err)
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				matched = append(matched, id)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()

			return "", nil, fmt.Errorf("read business identity matches: %w", err)
		}
		if err := rows.Close(); err != nil {
			return "", nil, fmt.Errorf("close business identity rows: %w", err)
		}
	}

	if len(matched) == 0 {
		legacyID, err := matchLegacyBusiness(ctx, tx, business)
		if err != nil {
			return "", nil, err
		}
		if legacyID != "" {
			matched = append(matched, legacyID)
		}
	}

	if len(matched) == 0 {
		return "", nil, nil
	}

	return matched[0], matched, nil
}

func matchLegacyBusiness(ctx context.Context, tx *sql.Tx, business resultimport.Business) (string, error) {
	tests := []struct {
		column string
		value  string
	}{
		{column: "place_id", value: business.PlaceID},
		{column: "cid", value: business.CID},
		{column: "data_id", value: business.DataID},
	}
	if len(business.Phones) > 0 {
		tests = append(tests, struct {
			column string
			value  string
		}{column: "normalized_phone", value: business.Phones[0].Normalized})
	}
	tests = append(tests,
		struct {
			column string
			value  string
		}{column: "domain", value: business.Website.Domain},
		struct {
			column string
			value  string
		}{column: "normalized_address", value: business.Address.Normalized},
	)

	for _, test := range tests {
		if test.value == "" {
			continue
		}
		query := `SELECT id FROM businesses WHERE ` + test.column + ` = ? AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1`
		var id string
		err := tx.QueryRowContext(ctx, query, test.value).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("match migrated business by %s: %w", test.column, err)
		}

		return id, nil
	}

	return "", nil
}

func ensureBusiness(
	ctx context.Context,
	tx *sql.Tx,
	id string,
	business resultimport.Business,
	inputID string,
	rawJSON string,
	observedAt int64,
) (string, bool, string, error) {
	var previousHash string
	err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE((
			SELECT content_hash FROM business_versions
			WHERE business_id = businesses.id ORDER BY observed_at DESC, id DESC LIMIT 1
		), '')
		FROM businesses WHERE id = ?`,
		id,
	).Scan(&previousHash)
	wasNew := errors.Is(err, sql.ErrNoRows)
	if err != nil && !wasNew {
		return "", false, "", fmt.Errorf("read existing business: %w", err)
	}

	name := business.Name
	if name == "" {
		name = "Unnamed business"
	}
	result, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO businesses(
			id, canonical_key, name, normalized_name, raw_json,
			first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		business.CanonicalIdentityKey,
		name,
		business.NormalizedName,
		rawJSON,
		observedAt,
		observedAt,
		observedAt,
		observedAt,
		observedAt,
	)
	if err != nil {
		return "", false, "", fmt.Errorf("insert normalized business: %w", err)
	}
	if wasNew {
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return "", false, "", fmt.Errorf("inspect normalized business insert: %w", rowsErr)
		}
		if rows == 0 {
			if err := tx.QueryRowContext(
				ctx,
				`SELECT id FROM businesses WHERE canonical_key = ?`,
				business.CanonicalIdentityKey,
			).Scan(&id); err != nil {
				return "", false, "", fmt.Errorf("resolve canonical business collision: %w", err)
			}
			if err := tx.QueryRowContext(
				ctx,
				`SELECT COALESCE((
					SELECT content_hash FROM business_versions
					WHERE business_id = businesses.id ORDER BY observed_at DESC, id DESC LIMIT 1
				), '')
				FROM businesses WHERE id = ?`,
				id,
			).Scan(&previousHash); err != nil {
				return "", false, "", fmt.Errorf("read canonical business collision history: %w", err)
			}
			wasNew = false
		}
	}

	phones := business.Phones
	phone, normalizedPhone := "", ""
	if len(phones) > 0 {
		phone = phones[0].Raw
		normalizedPhone = phones[0].Normalized
	}
	emailValues := make([]string, 0, len(business.Emails))
	for _, email := range business.Emails {
		emailValues = append(emailValues, email.Normalized)
	}
	emailsJSON := mustJSON(emailValues, "[]")
	categoriesJSON := mustJSON(nonEmptyStrings(business.Category), "[]")
	quality, confidence := businessQuality(business)
	changeStatus := "unchanged"
	if wasNew {
		changeStatus = "new"
	} else if previousHash != "" && previousHash != business.RecordHash {
		changeStatus = "updated"
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE businesses SET
			place_id = COALESCE(NULLIF(?, ''), place_id),
			cid = COALESCE(NULLIF(?, ''), cid),
			data_id = COALESCE(NULLIF(?, ''), data_id),
			input_id = COALESCE(NULLIF(?, ''), input_id),
			maps_url = COALESCE(NULLIF(?, ''), maps_url),
			name = COALESCE(NULLIF(?, ''), name),
			normalized_name = COALESCE(NULLIF(?, ''), normalized_name),
			primary_category = COALESCE(NULLIF(?, ''), primary_category),
			categories = CASE WHEN ? <> '[]' THEN ? ELSE categories END,
			description = COALESCE(NULLIF(?, ''), description),
			business_status = COALESCE(NULLIF(?, ''), business_status),
			address = COALESCE(NULLIF(?, ''), address),
			normalized_address = COALESCE(NULLIF(?, ''), normalized_address),
			street = COALESCE(NULLIF(?, ''), street),
			city = COALESCE(NULLIF(?, ''), city),
			state = COALESCE(NULLIF(?, ''), state),
			postal_code = COALESCE(NULLIF(?, ''), postal_code),
			country = COALESCE(NULLIF(?, ''), country),
			latitude = COALESCE(?, latitude),
			longitude = COALESCE(?, longitude),
			plus_code = COALESCE(NULLIF(?, ''), plus_code),
			phone = COALESCE(NULLIF(?, ''), phone),
			normalized_phone = COALESCE(NULLIF(?, ''), normalized_phone),
			website = COALESCE(NULLIF(?, ''), website),
			domain = COALESCE(NULLIF(?, ''), domain),
			emails = CASE WHEN ? <> '[]' THEN ? ELSE emails END,
			rating = COALESCE(?, rating),
			review_count = COALESCE(?, review_count),
			reviews_per_rating = CASE WHEN ? <> '' THEN ? ELSE reviews_per_rating END,
			open_hours = CASE WHEN ? <> '' THEN ? ELSE open_hours END,
			popular_times = CASE WHEN ? <> '' THEN ? ELSE popular_times END,
			price_range = COALESCE(NULLIF(?, ''), price_range),
			quality_score = MAX(quality_score, ?),
			quality_confidence = MAX(quality_confidence, ?),
			raw_json = ?,
			last_seen_at = MAX(last_seen_at, ?),
			last_changed_at = CASE WHEN ? <> 'unchanged' THEN MAX(last_changed_at, ?) ELSE last_changed_at END,
			change_status = ?,
			updated_at = MAX(updated_at, ?)
		WHERE id = ? AND (? > last_seen_at OR (? = last_seen_at AND ? >= quality_score))`,
		business.PlaceID,
		business.CID,
		business.DataID,
		inputID,
		business.MapsURL.Canonical,
		business.Name,
		business.NormalizedName,
		business.Category,
		categoriesJSON,
		categoriesJSON,
		business.Description,
		business.Status,
		business.Address.Raw,
		business.Address.Normalized,
		business.Address.Street,
		business.Address.City,
		business.Address.State,
		business.Address.PostalCode,
		business.Address.Country,
		nullableFloat(business.Latitude),
		nullableFloat(business.Longitude),
		business.PlusCode,
		phone,
		normalizedPhone,
		business.Website.Canonical,
		business.Website.Domain,
		emailsJSON,
		emailsJSON,
		nullableFloat(business.ReviewRating),
		nullableInt(business.ReviewCount),
		canonicalJSONValue(business.Structured.ReviewsPerRating),
		canonicalJSONValue(business.Structured.ReviewsPerRating),
		canonicalJSONValue(business.Structured.OpenHours),
		canonicalJSONValue(business.Structured.OpenHours),
		canonicalJSONValue(business.Structured.PopularTimes),
		canonicalJSONValue(business.Structured.PopularTimes),
		business.PriceRange,
		quality,
		confidence,
		rawJSON,
		observedAt,
		changeStatus,
		observedAt,
		changeStatus,
		observedAt,
		id,
		observedAt,
		observedAt,
		quality,
	)
	if err != nil {
		return "", false, "", fmt.Errorf("update normalized business: %w", err)
	}

	return id, wasNew, previousHash, nil
}

func insertBusinessSource(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	jobID string,
	record resultimport.Record,
	rawJSON string,
	normalizedJSON string,
	observedAt int64,
) (int64, bool, error) {
	result, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO business_sources(
			business_id, job_id, source_type, source_url, source_query, source_cell,
			input_id, extraction_method, confidence, extracted_at, raw_json,
			normalized_json, record_hash, ingest_key
		) VALUES (?, ?, 'google_maps_csv', ?, ?, ?, ?, 'legacy_csv_import', 1, ?, ?, ?, ?, ?)`,
		businessID,
		jobID,
		record.Source.SourceURL,
		record.Source.Query,
		record.Source.GridCell,
		record.Source.InputID,
		observedAt,
		rawJSON,
		normalizedJSON,
		record.Business.RecordHash,
		record.Cursor.Token,
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert business source: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("inspect business source insert: %w", err)
	}
	if rows == 0 {
		var sourceID int64
		if err := tx.QueryRowContext(
			ctx,
			`SELECT id FROM business_sources WHERE ingest_key = ?`,
			record.Cursor.Token,
		).Scan(&sourceID); err != nil {
			return 0, false, fmt.Errorf("read existing business source: %w", err)
		}

		return sourceID, false, nil
	}

	sourceID, err := result.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("read business source id: %w", err)
	}

	return sourceID, true, nil
}

func recordBusinessVersion(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	jobID string,
	sourceID int64,
	contentHash string,
	snapshot string,
	observedAt int64,
) error {
	var previousID, previousVersion int64
	var previousHash, previousSnapshot string
	err := tx.QueryRowContext(
		ctx,
		`SELECT id, version_no, content_hash, snapshot FROM business_versions
		WHERE business_id = ? AND observed_at <= ?
		ORDER BY observed_at DESC, id DESC LIMIT 1`,
		businessID,
		observedAt,
	).Scan(&previousID, &previousVersion, &previousHash, &previousSnapshot)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read business version: %w", err)
	}
	if err == nil && previousHash == contentHash {
		return nil
	}

	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(version_no), 0) FROM business_versions WHERE business_id = ?`,
		businessID,
	).Scan(&previousVersion); err != nil {
		return fmt.Errorf("read business version sequence: %w", err)
	}

	changedFields := changedTopLevelFields(previousSnapshot, snapshot)
	changeType := "updated"
	if errors.Is(err, sql.ErrNoRows) {
		changeType = "new"
	}
	changedJSON := mustJSON(changedFields, "[]")
	var previous any
	if previousID > 0 {
		previous = previousID
	}
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO business_versions(
			business_id, version_no, previous_version_id, job_id, source_id,
			content_hash, change_type, changed_fields, snapshot, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		businessID,
		previousVersion+1,
		previous,
		jobID,
		sourceID,
		contentHash,
		changeType,
		changedJSON,
		snapshot,
		observedAt,
	)
	if err != nil {
		return fmt.Errorf("insert business version: %w", err)
	}
	versionID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read business version id: %w", err)
	}

	if previousID > 0 {
		before, after := decodeJSONObject(previousSnapshot), decodeJSONObject(snapshot)
		for _, field := range changedFields {
			beforeJSON := mustJSON(before[field], "null")
			afterJSON := mustJSON(after[field], "null")
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO business_changes(
					business_id, from_version_id, to_version_id, field_name,
					before_value, after_value, change_kind, detected_at
				) VALUES (?, ?, ?, ?, ?, ?, 'updated', ?)`,
				businessID,
				previousID,
				versionID,
				field,
				beforeJSON,
				afterJSON,
				observedAt,
			); err != nil {
				return fmt.Errorf("insert business field change: %w", err)
			}
		}
	}

	return nil
}

func storeIdentityKeys(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	sourceID int64,
	keys []resultimport.IdentityKey,
	observedAt int64,
) error {
	for _, key := range keys {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO business_identity_keys(
				business_id, key_type, key_value, source_id, confidence, created_at
			) VALUES (?, ?, ?, ?, 1, ?)`,
			businessID,
			key.Kind,
			key.Value,
			sourceID,
			observedAt,
		); err != nil {
			return fmt.Errorf("insert business identity key: %w", err)
		}
	}

	return nil
}

func storeContacts(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	record resultimport.Record,
	observedAt int64,
) error {
	for _, phone := range record.Business.Phones {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO phones(
				business_id, value, normalized_value, kind, source_url, confidence
			) VALUES (?, ?, ?, 'business', ?, 1)`,
			businessID,
			phone.Raw,
			phone.Normalized,
			record.Source.SourceURL,
		); err != nil {
			return fmt.Errorf("insert business phone: %w", err)
		}
	}

	for _, email := range record.Business.Emails {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO emails(
				business_id, value, normalized_value, kind, status, source_url,
				extraction_method, confidence, last_checked_at
			) VALUES (?, ?, ?, ?, 'syntax-valid', ?, 'legacy_csv', 0.6, ?)`,
			businessID,
			email.Raw,
			email.Normalized,
			emailKind(email.Normalized),
			record.Source.SourceURL,
			observedAt,
		); err != nil {
			return fmt.Errorf("insert business email: %w", err)
		}
	}

	return nil
}

func storeProvenance(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	sourceID int64,
	record resultimport.Record,
	observedAt int64,
) error {
	fields := []struct {
		name       string
		original   string
		normalized string
	}{
		{name: "name", original: record.Business.Name, normalized: record.Business.NormalizedName},
		{name: "category", original: record.Business.Category, normalized: record.Business.NormalizedCategory},
		{name: "address", original: record.Business.Address.Raw, normalized: record.Business.Address.Normalized},
		{name: "website", original: record.Business.Website.Raw, normalized: record.Business.Website.Canonical},
		{name: "place_id", original: record.Business.PlaceID, normalized: record.Business.PlaceID},
		{name: "cid", original: record.Business.CID, normalized: record.Business.CID},
		{name: "data_id", original: record.Business.DataID, normalized: record.Business.DataID},
	}
	if len(record.Business.Phones) > 0 {
		fields = append(fields, struct {
			name       string
			original   string
			normalized string
		}{name: "phone", original: record.Business.Phones[0].Raw, normalized: record.Business.Phones[0].Normalized})
	}
	if len(record.Business.Emails) > 0 {
		fields = append(fields, struct {
			name       string
			original   string
			normalized string
		}{name: "email", original: record.Business.Emails[0].Raw, normalized: record.Business.Emails[0].Normalized})
	}

	for _, field := range fields {
		if strings.TrimSpace(field.normalized) == "" {
			continue
		}
		var preferredAt sql.NullInt64
		if err := tx.QueryRowContext(
			ctx,
			`SELECT MAX(extracted_at) FROM field_provenance
			WHERE business_id = ? AND field_name = ? AND preferred = 1 AND superseded_at IS NULL`,
			businessID,
			field.name,
		).Scan(&preferredAt); err != nil {
			return fmt.Errorf("read preferred field provenance: %w", err)
		}
		preferred := !preferredAt.Valid || observedAt >= preferredAt.Int64
		if preferred {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE field_provenance SET preferred = 0, superseded_at = ?
				WHERE business_id = ? AND field_name = ? AND preferred = 1 AND superseded_at IS NULL`,
				observedAt,
				businessID,
				field.name,
			); err != nil {
				return fmt.Errorf("supersede field provenance: %w", err)
			}
		}
		originalJSON := mustJSON(field.original, `""`)
		normalizedJSON := mustJSON(field.normalized, `""`)
		valueHash := hashText(field.name + "\x00" + field.normalized)
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO field_provenance(
				business_id, field_name, original_value, normalized_value, preferred,
				source_type, source_url, source_query, source_cell, extraction_method,
				confidence, extracted_at, source_id, original_json, normalized_json, value_hash
			) VALUES (?, ?, ?, ?, ?, 'google_maps_csv', ?, ?, ?, 'legacy_csv_import', 1, ?, ?, ?, ?, ?)`,
			businessID,
			field.name,
			field.original,
			field.normalized,
			boolInt(preferred),
			record.Source.SourceURL,
			record.Source.Query,
			record.Source.GridCell,
			observedAt,
			sourceID,
			originalJSON,
			normalizedJSON,
			valueHash,
		); err != nil {
			return fmt.Errorf("insert field provenance: %w", err)
		}
	}

	return nil
}

func linkJobBusiness(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	businessID string,
	sourceID int64,
	observedAt int64,
	isNew bool,
	isChanged bool,
) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_businesses(
			job_id, business_id, first_source_id, first_seen_at, last_seen_at,
			occurrence_count, is_new, is_changed
		) VALUES (?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(job_id, business_id) DO UPDATE SET
			last_seen_at = MAX(job_businesses.last_seen_at, excluded.last_seen_at),
			occurrence_count = job_businesses.occurrence_count + 1,
			is_new = MAX(job_businesses.is_new, excluded.is_new),
			is_changed = MAX(job_businesses.is_changed, excluded.is_changed)`,
		jobID,
		businessID,
		sourceID,
		observedAt,
		observedAt,
		boolInt(isNew),
		boolInt(isChanged),
	)
	if err != nil {
		return fmt.Errorf("link job business: %w", err)
	}

	return nil
}

func storeDuplicateCandidates(
	ctx context.Context,
	tx *sql.Tx,
	targetID string,
	matchedIDs []string,
	keys []resultimport.IdentityKey,
	observedAt int64,
) error {
	keyNames := make([]string, 0, len(keys))
	for _, key := range keys {
		keyNames = append(keyNames, key.String())
	}
	signals := mustJSON(map[string]any{"exact_identity_keys": keyNames}, "{}")
	for _, otherID := range matchedIDs {
		if otherID == targetID {
			continue
		}
		left, right := targetID, otherID
		if left > right {
			left, right = right, left
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO duplicate_candidates(
				left_business_id, right_business_id, score, signals, state, created_at
			) VALUES (?, ?, 1, ?, 'pending', ?)`,
			left,
			right,
			signals,
			observedAt,
		); err != nil {
			return fmt.Errorf("insert duplicate candidate: %w", err)
		}
	}

	return nil
}

type fuzzyBusinessCandidate struct {
	id                string
	normalizedName    string
	normalizedAddress string
	city              string
	postalCode        string
	normalizedPhone   string
	domain            string
	latitude          sql.NullFloat64
	longitude         sql.NullFloat64
}

// storeFuzzyDuplicateCandidates records conservative review suggestions. It
// never merges records: stable identity keys remain the only automatic merge
// path, while fuzzy/composite signals require an explicit future review action.
func storeFuzzyDuplicateCandidates(
	ctx context.Context,
	tx *sql.Tx,
	targetID string,
	observedAt int64,
) error {
	target, err := readFuzzyBusiness(ctx, tx, targetID)
	if err != nil {
		return err
	}
	if target.normalizedName == "" {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, normalized_name, normalized_address, city, postal_code,
			normalized_phone, domain, latitude, longitude
		FROM businesses
		WHERE id <> ? AND deleted_at IS NULL AND merged_into_id IS NULL
			AND (
				normalized_name = ?
				OR (? <> '' AND normalized_phone = ?)
				OR (? <> '' AND domain = ?)
				OR (? <> '' AND postal_code = ?)
				OR (? <> '' AND city = ? AND substr(normalized_name, 1, 6) = substr(?, 1, 6))
			)
		ORDER BY last_seen_at DESC, id
		LIMIT 100`,
		targetID,
		target.normalizedName,
		target.normalizedPhone,
		target.normalizedPhone,
		target.domain,
		target.domain,
		target.postalCode,
		target.postalCode,
		target.city,
		target.city,
		target.normalizedName,
	)
	if err != nil {
		return fmt.Errorf("find fuzzy duplicate candidates: %w", err)
	}
	candidates := make([]fuzzyBusinessCandidate, 0)
	for rows.Next() {
		var candidate fuzzyBusinessCandidate
		if err := rows.Scan(
			&candidate.id,
			&candidate.normalizedName,
			&candidate.normalizedAddress,
			&candidate.city,
			&candidate.postalCode,
			&candidate.normalizedPhone,
			&candidate.domain,
			&candidate.latitude,
			&candidate.longitude,
		); err != nil {
			return fmt.Errorf("read fuzzy duplicate candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read fuzzy duplicate candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close fuzzy duplicate candidates: %w", err)
	}

	for _, candidate := range candidates {
		score, signals := fuzzyDuplicateScore(target, candidate)
		if score < 0.62 {
			continue
		}
		left, right := targetID, candidate.id
		if left > right {
			left, right = right, left
		}
		signalJSON := mustJSON(signals, "{}")
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO duplicate_candidates(
				left_business_id, right_business_id, score, signals, state, created_at
			) VALUES (?, ?, ?, ?, 'pending', ?)
			ON CONFLICT(left_business_id, right_business_id) DO UPDATE SET
				score = MAX(duplicate_candidates.score, excluded.score),
				signals = CASE WHEN excluded.score >= duplicate_candidates.score
					THEN excluded.signals ELSE duplicate_candidates.signals END
			WHERE duplicate_candidates.state = 'pending'`,
			left,
			right,
			score,
			signalJSON,
			observedAt,
		); err != nil {
			return fmt.Errorf("store fuzzy duplicate candidate: %w", err)
		}
	}
	return nil
}

func readFuzzyBusiness(ctx context.Context, tx *sql.Tx, id string) (fuzzyBusinessCandidate, error) {
	var business fuzzyBusinessCandidate
	business.id = id
	if err := tx.QueryRowContext(ctx,
		`SELECT normalized_name, normalized_address, city, postal_code,
			normalized_phone, domain, latitude, longitude
		FROM businesses WHERE id = ?`,
		id,
	).Scan(
		&business.normalizedName,
		&business.normalizedAddress,
		&business.city,
		&business.postalCode,
		&business.normalizedPhone,
		&business.domain,
		&business.latitude,
		&business.longitude,
	); err != nil {
		return fuzzyBusinessCandidate{}, fmt.Errorf("read business for fuzzy matching: %w", err)
	}
	return business, nil
}

func fuzzyDuplicateScore(left, right fuzzyBusinessCandidate) (float64, map[string]any) {
	nameSimilarity := textDiceSimilarity(left.normalizedName, right.normalizedName)
	addressSimilarity := textDiceSimilarity(left.normalizedAddress, right.normalizedAddress)
	signals := map[string]any{
		"normalized_name_similarity":    roundedScore(nameSimilarity),
		"normalized_address_similarity": roundedScore(addressSimilarity),
	}
	samePhone := left.normalizedPhone != "" && left.normalizedPhone == right.normalizedPhone
	sameDomain := left.domain != "" && strings.EqualFold(left.domain, right.domain)
	// Nearby suites and neighbouring storefronts frequently share very similar
	// addresses. Require a meaningful name signal unless a phone or domain is
	// shared so proximity alone never creates noisy review candidates.
	if nameSimilarity < 0.55 && !samePhone && !sameDomain {
		return 0, signals
	}

	score := nameSimilarity*0.45 + addressSimilarity*0.25
	if left.city != "" && strings.EqualFold(left.city, right.city) {
		score += 0.10
		signals["same_city"] = true
	}
	if left.postalCode != "" && strings.EqualFold(left.postalCode, right.postalCode) {
		score += 0.15
		signals["same_postal_code"] = true
	}
	if samePhone {
		score += 0.55
		signals["same_normalized_phone"] = true
	}
	if sameDomain {
		score += 0.50
		signals["same_domain"] = true
	}
	if left.latitude.Valid && left.longitude.Valid && right.latitude.Valid && right.longitude.Valid {
		distance := geographicDistanceMeters(
			left.latitude.Float64,
			left.longitude.Float64,
			right.latitude.Float64,
			right.longitude.Float64,
		)
		signals["distance_metres"] = math.Round(distance)
		switch {
		case distance <= 100:
			score += 0.25
		case distance <= 500:
			score += 0.15
		case distance <= 2000:
			score += 0.05
		}
	}
	return min(1.0, roundedScore(score)), signals
}

func textDiceSimilarity(left, right string) float64 {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	leftPairs := runePairs(left)
	rightPairs := runePairs(right)
	if len(leftPairs) == 0 || len(rightPairs) == 0 {
		return 0
	}
	remaining := make(map[string]int, len(leftPairs))
	for _, pair := range leftPairs {
		remaining[pair]++
	}
	intersection := 0
	for _, pair := range rightPairs {
		if remaining[pair] > 0 {
			remaining[pair]--
			intersection++
		}
	}
	return 2 * float64(intersection) / float64(len(leftPairs)+len(rightPairs))
}

func runePairs(value string) []string {
	runes := []rune(value)
	if len(runes) == 1 {
		return []string{string(runes)}
	}
	pairs := make([]string, 0, len(runes)-1)
	for index := 0; index+1 < len(runes); index++ {
		pairs = append(pairs, string(runes[index:index+2]))
	}
	return pairs
}

func geographicDistanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusMetres = 6_371_000.0
	lat1Radians := lat1 * math.Pi / 180
	lat2Radians := lat2 * math.Pi / 180
	latitudeDelta := (lat2 - lat1) * math.Pi / 180
	longitudeDelta := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(latitudeDelta/2)*math.Sin(latitudeDelta/2) +
		math.Cos(lat1Radians)*math.Cos(lat2Radians)*
			math.Sin(longitudeDelta/2)*math.Sin(longitudeDelta/2)
	return earthRadiusMetres * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func roundedScore(value float64) float64 {
	return math.Round(value*1000) / 1000
}

// SearchBusinesses runs a parameterized FTS/filter query against preferred
// normalized records.
func (repo *repo) SearchBusinesses(ctx context.Context, search web.ResultSearch) (web.ResultPage, error) {
	if search.Limit <= 0 {
		search.Limit = defaultResultLimit
	}
	search.Limit = min(search.Limit, maximumResultLimit)
	search.Offset = max(search.Offset, 0)

	where := []string{"businesses.deleted_at IS NULL"}
	if !search.IncludeDuplicates {
		where = append(where, "businesses.merged_into_id IS NULL")
	}
	fromSQL := "FROM businesses"
	if search.IncludeDuplicates {
		fromSQL = "FROM business_sources AS selected_source JOIN businesses ON businesses.id = selected_source.business_id"
	}
	args := make([]any, 0)
	if strings.TrimSpace(search.Query) != "" {
		where = append(where, `businesses.id IN (
			SELECT business_id FROM businesses_fts WHERE businesses_fts MATCH ?
		)`)
		args = append(args, ftsMatchQuery(search.Query))
	}
	if strings.TrimSpace(search.JobID) != "" {
		if search.IncludeDuplicates {
			where = append(where, "selected_source.job_id = ?")
		} else {
			where = append(where, `EXISTS (
				SELECT 1 FROM job_businesses
				WHERE job_businesses.business_id = businesses.id AND job_businesses.job_id = ?
			)`)
		}
		args = append(args, strings.TrimSpace(search.JobID))
	}
	for _, filter := range search.Filters {
		clause, values, err := resultFilterSQL(filter)
		if err != nil {
			return web.ResultPage{}, fmt.Errorf("%w: %v", web.ErrInvalidResultQuery, err)
		}
		where = append(where, clause)
		args = append(args, values...)
	}
	if search.FilterGroup != nil {
		clause, values, err := resultFilterGroupSQL(*search.FilterGroup, 1, new(int))
		if err != nil {
			return web.ResultPage{}, fmt.Errorf("%w: %v", web.ErrInvalidResultQuery, err)
		}
		where = append(where, clause)
		args = append(args, values...)
	}

	whereSQL := strings.Join(where, " AND ")
	var total int64
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) `+fromSQL+` WHERE `+whereSQL,
		args...,
	).Scan(&total); err != nil {
		return web.ResultPage{}, fmt.Errorf("count normalized results: %w", err)
	}

	orderSQL, err := resultSortSQL(search.Sort)
	if err != nil {
		return web.ResultPage{}, fmt.Errorf("%w: %v", web.ErrInvalidResultQuery, err)
	}
	sourceIDSQL := `COALESCE((SELECT id FROM business_sources WHERE business_sources.business_id = businesses.id
		ORDER BY extracted_at DESC, id DESC LIMIT 1), 0)`
	sourceJobSQL := `COALESCE((SELECT job_id FROM business_sources WHERE business_sources.business_id = businesses.id
		ORDER BY extracted_at DESC, id DESC LIMIT 1), '')`
	sourceQuerySQL := `COALESCE((SELECT source_query FROM business_sources WHERE business_sources.business_id = businesses.id
		ORDER BY extracted_at DESC, id DESC LIMIT 1), '')`
	sourceCellSQL := `COALESCE((SELECT source_cell FROM business_sources WHERE business_sources.business_id = businesses.id
		ORDER BY extracted_at DESC, id DESC LIMIT 1), '')`
	sourceTimeSQL := `COALESCE((SELECT extracted_at FROM business_sources WHERE business_sources.business_id = businesses.id
		ORDER BY extracted_at DESC, id DESC LIMIT 1), businesses.last_seen_at)`
	if search.IncludeDuplicates {
		sourceIDSQL = "selected_source.id"
		sourceJobSQL = "COALESCE(selected_source.job_id, '')"
		sourceQuerySQL = "selected_source.source_query"
		sourceCellSQL = "selected_source.source_cell"
		sourceTimeSQL = "selected_source.extracted_at"
	}
	query := `SELECT
		businesses.id, businesses.name, businesses.primary_category, businesses.categories,
		businesses.business_status, businesses.claimed, businesses.address, businesses.city,
		businesses.state, businesses.postal_code, businesses.country, businesses.latitude,
		businesses.longitude, businesses.phone, businesses.normalized_phone,
		COALESCE((SELECT normalized_value FROM emails WHERE emails.business_id = businesses.id
			ORDER BY confidence DESC, id LIMIT 1), ''),
		businesses.website, businesses.domain, businesses.website_status,
		businesses.website_response_ms, businesses.rating, businesses.review_count,
		businesses.quality_score, businesses.quality_confidence, businesses.reviewed,
		businesses.notes, businesses.change_status, businesses.maps_url, businesses.place_id,
		businesses.cid, businesses.data_id, ` + sourceIDSQL + `, ` + sourceJobSQL + `,
		` + sourceQuerySQL + `, ` + sourceCellSQL + `, ` + sourceTimeSQL + `,
		businesses.updated_at,
		COALESCE((SELECT GROUP_CONCAT(tags.name, char(31)) FROM business_tags
			JOIN tags ON tags.id = business_tags.tag_id
			WHERE business_tags.business_id = businesses.id), '')
	` + fromSQL + `
	WHERE ` + whereSQL + `
	ORDER BY ` + orderSQL + `
	LIMIT ? OFFSET ?`
	queryArgs := append(append([]any(nil), args...), search.Limit, search.Offset)
	rows, err := repo.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return web.ResultPage{}, fmt.Errorf("search normalized results: %w", err)
	}
	defer rows.Close()

	results := make([]web.BusinessResult, 0, search.Limit)
	for rows.Next() {
		result, err := scanBusinessResult(rows)
		if err != nil {
			return web.ResultPage{}, err
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return web.ResultPage{}, fmt.Errorf("read normalized results: %w", err)
	}

	return web.ResultPage{Results: results, Total: total, Limit: search.Limit, Offset: search.Offset}, nil
}

// GetBusiness returns a normalized record with source and version history.
func (repo *repo) GetBusiness(ctx context.Context, id string) (web.BusinessDetail, error) {
	page, err := repo.SearchBusinesses(ctx, web.ResultSearch{
		Filters: []web.ResultFilter{{Field: "id", Operator: "eq", Value: id}},
		Limit:   1,
	})
	if err != nil {
		return web.BusinessDetail{}, err
	}
	if len(page.Results) == 0 {
		return web.BusinessDetail{}, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, id)
	}

	detail := web.BusinessDetail{Business: page.Results[0]}
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT raw_json FROM businesses WHERE id = ?`,
		id,
	).Scan(&detail.RawJSON); err != nil {
		return web.BusinessDetail{}, fmt.Errorf("read business raw JSON: %w", err)
	}

	sourceRows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, COALESCE(job_id, ''), COALESCE(task_id, ''), source_type, source_url, source_query,
			source_cell, input_id, extraction_method, confidence, extracted_at,
			raw_json, normalized_json, record_hash
		FROM business_sources WHERE business_id = ? ORDER BY extracted_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return web.BusinessDetail{}, fmt.Errorf("read business sources: %w", err)
	}
	for sourceRows.Next() {
		var source web.BusinessSourceView
		var extractedAt int64
		if err := sourceRows.Scan(
			&source.ID,
			&source.JobID,
			&source.TaskID,
			&source.SourceType,
			&source.SourceURL,
			&source.SourceQuery,
			&source.SourceCell,
			&source.InputID,
			&source.ExtractionMethod,
			&source.Confidence,
			&extractedAt,
			&source.RawJSON,
			&source.NormalizedJSON,
			&source.RecordHash,
		); err != nil {
			_ = sourceRows.Close()

			return web.BusinessDetail{}, fmt.Errorf("scan business source: %w", err)
		}
		source.ExtractedAt = time.Unix(extractedAt, 0).UTC()
		detail.Sources = append(detail.Sources, source)
	}
	if err := sourceRows.Err(); err != nil {
		_ = sourceRows.Close()

		return web.BusinessDetail{}, fmt.Errorf("read business sources: %w", err)
	}
	if err := sourceRows.Close(); err != nil {
		return web.BusinessDetail{}, fmt.Errorf("close business sources: %w", err)
	}

	versionRows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, version_no, previous_version_id, COALESCE(job_id, ''), source_id,
			change_type, changed_fields, snapshot, observed_at
		FROM business_versions WHERE business_id = ? ORDER BY observed_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return web.BusinessDetail{}, fmt.Errorf("read business versions: %w", err)
	}
	for versionRows.Next() {
		var version web.BusinessVersionView
		var changedFields string
		var previousVersionID, sourceID sql.NullInt64
		var observedAt int64
		if err := versionRows.Scan(
			&version.ID,
			&version.Version,
			&previousVersionID,
			&version.JobID,
			&sourceID,
			&version.ChangeType,
			&changedFields,
			&version.Snapshot,
			&observedAt,
		); err != nil {
			_ = versionRows.Close()

			return web.BusinessDetail{}, fmt.Errorf("scan business version: %w", err)
		}
		_ = json.Unmarshal([]byte(changedFields), &version.ChangedFields)
		version.PreviousVersionID = nullIntPointer(previousVersionID)
		version.SourceID = nullIntPointer(sourceID)
		version.ObservedAt = time.Unix(observedAt, 0).UTC()
		detail.Versions = append(detail.Versions, version)
	}
	if err := versionRows.Err(); err != nil {
		_ = versionRows.Close()

		return web.BusinessDetail{}, fmt.Errorf("read business versions: %w", err)
	}
	if err := versionRows.Close(); err != nil {
		return web.BusinessDetail{}, fmt.Errorf("close business versions: %w", err)
	}

	if err := repo.loadBusinessDetailRelations(ctx, id, &detail); err != nil {
		return web.BusinessDetail{}, err
	}

	duplicateRows, err := repo.db.QueryContext(
		ctx,
		`SELECT CASE WHEN left_business_id = ? THEN right_business_id ELSE left_business_id END
		FROM duplicate_candidates
		WHERE (left_business_id = ? OR right_business_id = ?) AND state = 'pending'
		ORDER BY score DESC, id`,
		id,
		id,
		id,
	)
	if err != nil {
		return web.BusinessDetail{}, fmt.Errorf("read business duplicates: %w", err)
	}
	for duplicateRows.Next() {
		var duplicateID string
		if err := duplicateRows.Scan(&duplicateID); err != nil {
			_ = duplicateRows.Close()

			return web.BusinessDetail{}, fmt.Errorf("scan business duplicate: %w", err)
		}
		detail.Duplicates = append(detail.Duplicates, duplicateID)
	}
	if err := duplicateRows.Err(); err != nil {
		_ = duplicateRows.Close()

		return web.BusinessDetail{}, fmt.Errorf("read business duplicates: %w", err)
	}
	if err := duplicateRows.Close(); err != nil {
		return web.BusinessDetail{}, fmt.Errorf("close business duplicates: %w", err)
	}
	detail.Quality, err = repo.BusinessQuality(ctx, id)
	if err != nil {
		return web.BusinessDetail{}, fmt.Errorf("read explainable business quality: %w", err)
	}

	return detail, nil
}

func (repo *repo) loadBusinessDetailRelations(ctx context.Context, id string, detail *web.BusinessDetail) error {
	if err := repo.loadBusinessProvenance(ctx, id, detail); err != nil {
		return err
	}
	if err := repo.loadBusinessWebsites(ctx, id, detail); err != nil {
		return err
	}
	if err := repo.loadBusinessContacts(ctx, id, detail); err != nil {
		return err
	}
	if err := repo.loadBusinessChanges(ctx, id, detail); err != nil {
		return err
	}
	if err := repo.loadDuplicateMatches(ctx, id, detail); err != nil {
		return err
	}

	return nil
}

func (repo *repo) loadBusinessProvenance(ctx context.Context, id string, detail *web.BusinessDetail) error {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, field_name, original_value, normalized_value, original_json, normalized_json,
			preferred, source_id, source_type, source_url, source_query, source_cell,
			extraction_method, confidence, extracted_at, superseded_at, operator, edit_reason
		FROM field_provenance
		WHERE business_id = ?
		ORDER BY field_name, preferred DESC, extracted_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business provenance: %w", err)
	}
	for rows.Next() {
		var item web.FieldProvenanceView
		var preferred int
		var sourceID, supersededAt sql.NullInt64
		var extractedAt int64
		if err := rows.Scan(
			&item.ID,
			&item.FieldName,
			&item.OriginalValue,
			&item.NormalizedValue,
			&item.OriginalJSON,
			&item.NormalizedJSON,
			&preferred,
			&sourceID,
			&item.SourceType,
			&item.SourceURL,
			&item.SourceQuery,
			&item.SourceCell,
			&item.ExtractionMethod,
			&item.Confidence,
			&extractedAt,
			&supersededAt,
			&item.Operator,
			&item.EditReason,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan business provenance: %w", err)
		}
		item.Preferred = preferred != 0
		item.SourceID = nullIntPointer(sourceID)
		item.ExtractedAt = time.Unix(extractedAt, 0).UTC()
		item.SupersededAt = nullTimePointer(supersededAt)
		detail.Provenance = append(detail.Provenance, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read business provenance: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close business provenance: %w", err)
	}

	return nil
}

func (repo *repo) loadBusinessWebsites(ctx context.Context, id string, detail *web.BusinessDetail) error {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, url, final_url, domain, status, http_status, https, response_time_ms,
			redirect_chain, page_title, meta_description, language, technologies, social_links,
			screenshot_path, last_checked_at, tls_valid, certificate_error, pages_checked,
			internal_links_checked, broken_internal_links, mixed_content, parked,
			coming_soon, placeholder, trackers
		FROM websites WHERE business_id = ? ORDER BY last_checked_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business websites: %w", err)
	}
	for rows.Next() {
		var item web.WebsiteView
		var httpStatus, https, responseTime, lastCheckedAt, tlsValid sql.NullInt64
		var mixedContent, parked, comingSoon, placeholder int
		if err := rows.Scan(
			&item.ID,
			&item.URL,
			&item.FinalURL,
			&item.Domain,
			&item.Status,
			&httpStatus,
			&https,
			&responseTime,
			&item.RedirectChain,
			&item.PageTitle,
			&item.MetaDescription,
			&item.Language,
			&item.Technologies,
			&item.SocialLinks,
			&item.ScreenshotPath,
			&lastCheckedAt,
			&tlsValid,
			&item.CertificateError,
			&item.PagesChecked,
			&item.InternalLinksChecked,
			&item.BrokenInternalLinks,
			&mixedContent,
			&parked,
			&comingSoon,
			&placeholder,
			&item.Trackers,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan business website: %w", err)
		}
		item.HTTPStatus = nullIntPointer(httpStatus)
		item.HTTPS = nullIntegerBoolPointer(https)
		item.ResponseTimeMS = nullIntPointer(responseTime)
		item.LastCheckedAt = nullTimePointer(lastCheckedAt)
		item.TLSValid = nullIntegerBoolPointer(tlsValid)
		item.MixedContent = mixedContent != 0
		item.Parked = parked != 0
		item.ComingSoon = comingSoon != 0
		item.Placeholder = placeholder != 0
		detail.Websites = append(detail.Websites, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read business websites: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close business websites: %w", err)
	}

	return nil
}

func (repo *repo) loadBusinessContacts(ctx context.Context, id string, detail *web.BusinessDetail) error {
	emailRows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, value, normalized_value, kind, status, domain_has_mx, disposable,
			source_url, extraction_method, confidence, last_checked_at, valid_syntax,
			role, personal_likely, mx_status, mx_records, relevance, rank
		FROM emails WHERE business_id = ? ORDER BY confidence DESC, id`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business emails: %w", err)
	}
	for emailRows.Next() {
		var item web.EmailView
		var domainHasMX, lastCheckedAt sql.NullInt64
		var disposable, validSyntax, personalLikely int
		if err := emailRows.Scan(
			&item.ID,
			&item.Value,
			&item.NormalizedValue,
			&item.Kind,
			&item.Status,
			&domainHasMX,
			&disposable,
			&item.SourceURL,
			&item.ExtractionMethod,
			&item.Confidence,
			&lastCheckedAt,
			&validSyntax,
			&item.Role,
			&personalLikely,
			&item.MXStatus,
			&item.MXRecords,
			&item.Relevance,
			&item.Rank,
		); err != nil {
			_ = emailRows.Close()
			return fmt.Errorf("scan business email: %w", err)
		}
		item.DomainHasMX = nullIntegerBoolPointer(domainHasMX)
		item.Disposable = disposable != 0
		item.LastCheckedAt = nullTimePointer(lastCheckedAt)
		item.ValidSyntax = validSyntax != 0
		item.PersonalLikely = personalLikely != 0
		detail.Emails = append(detail.Emails, item)
	}
	if err := emailRows.Err(); err != nil {
		_ = emailRows.Close()
		return fmt.Errorf("read business emails: %w", err)
	}
	if err := emailRows.Close(); err != nil {
		return fmt.Errorf("close business emails: %w", err)
	}

	phoneRows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, value, normalized_value, kind, source_url, confidence
		FROM phones WHERE business_id = ? ORDER BY confidence DESC, id`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business phones: %w", err)
	}
	for phoneRows.Next() {
		var item web.PhoneView
		if err := phoneRows.Scan(
			&item.ID,
			&item.Value,
			&item.NormalizedValue,
			&item.Kind,
			&item.SourceURL,
			&item.Confidence,
		); err != nil {
			_ = phoneRows.Close()
			return fmt.Errorf("scan business phone: %w", err)
		}
		detail.Phones = append(detail.Phones, item)
	}
	if err := phoneRows.Err(); err != nil {
		_ = phoneRows.Close()
		return fmt.Errorf("read business phones: %w", err)
	}
	if err := phoneRows.Close(); err != nil {
		return fmt.Errorf("close business phones: %w", err)
	}

	socialRows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, platform, url, source_url, confidence
		FROM social_profiles WHERE business_id = ? ORDER BY platform, confidence DESC, id`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business social profiles: %w", err)
	}
	for socialRows.Next() {
		var item web.SocialProfileView
		if err := socialRows.Scan(
			&item.ID,
			&item.Platform,
			&item.URL,
			&item.SourceURL,
			&item.Confidence,
		); err != nil {
			_ = socialRows.Close()
			return fmt.Errorf("scan business social profile: %w", err)
		}
		detail.SocialProfiles = append(detail.SocialProfiles, item)
	}
	if err := socialRows.Err(); err != nil {
		_ = socialRows.Close()
		return fmt.Errorf("read business social profiles: %w", err)
	}
	if err := socialRows.Close(); err != nil {
		return fmt.Errorf("close business social profiles: %w", err)
	}

	return nil
}

func (repo *repo) loadBusinessChanges(ctx context.Context, id string, detail *web.BusinessDetail) error {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, from_version_id, to_version_id, field_name, before_value,
			after_value, change_kind, detected_at
		FROM business_changes WHERE business_id = ? ORDER BY detected_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business changes: %w", err)
	}
	for rows.Next() {
		var item web.BusinessChangeView
		var fromVersionID, toVersionID sql.NullInt64
		var detectedAt int64
		if err := rows.Scan(
			&item.ID,
			&fromVersionID,
			&toVersionID,
			&item.FieldName,
			&item.BeforeValue,
			&item.AfterValue,
			&item.ChangeKind,
			&detectedAt,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan business change: %w", err)
		}
		item.FromVersionID = nullIntPointer(fromVersionID)
		item.ToVersionID = nullIntPointer(toVersionID)
		item.DetectedAt = time.Unix(detectedAt, 0).UTC()
		detail.Changes = append(detail.Changes, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read business changes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close business changes: %w", err)
	}

	return nil
}

func (repo *repo) loadDuplicateMatches(ctx context.Context, id string, detail *web.BusinessDetail) error {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT candidates.id, other.id, other.name, other.primary_category, other.address,
			other.domain, candidates.score, candidates.signals, candidates.state,
			candidates.resolution_note, candidates.created_at
		FROM duplicate_candidates AS candidates
		JOIN businesses AS other ON other.id = CASE
			WHEN candidates.left_business_id = ? THEN candidates.right_business_id
			ELSE candidates.left_business_id END
		WHERE candidates.left_business_id = ? OR candidates.right_business_id = ?
		ORDER BY candidates.state = 'pending' DESC, candidates.score DESC, candidates.id`,
		id,
		id,
		id,
	)
	if err != nil {
		return fmt.Errorf("read business duplicate matches: %w", err)
	}
	for rows.Next() {
		var item web.DuplicateMatchView
		var createdAt int64
		if err := rows.Scan(
			&item.CandidateID,
			&item.BusinessID,
			&item.Name,
			&item.PrimaryCategory,
			&item.Address,
			&item.Domain,
			&item.Score,
			&item.Signals,
			&item.State,
			&item.ResolutionNote,
			&createdAt,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan business duplicate match: %w", err)
		}
		item.CreatedAt = time.Unix(createdAt, 0).UTC()
		detail.DuplicateMatches = append(detail.DuplicateMatches, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read business duplicate matches: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close business duplicate matches: %w", err)
	}

	return nil
}

// ResultOverview returns database-wide quality counts.
func (repo *repo) ResultOverview(ctx context.Context) (web.ResultOverview, error) {
	var overview web.ResultOverview
	err := repo.db.QueryRowContext(
		ctx,
		`SELECT
			(SELECT COUNT(*) FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL),
			(SELECT COUNT(*) FROM business_sources),
			(SELECT COUNT(*) FROM duplicate_candidates WHERE state = 'pending'),
			(SELECT COUNT(*) FROM business_merges),
			(SELECT COUNT(*) FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL AND website <> ''),
			(SELECT COUNT(DISTINCT normalized_value) FROM emails),
			(SELECT COUNT(DISTINCT normalized_value) FROM phones),
			(SELECT COUNT(*) FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL
				AND (reviewed = 0 OR quality_confidence < 0.6))`,
	).Scan(
		&overview.UniqueBusinesses,
		&overview.RawRecords,
		&overview.DuplicateGroups,
		&overview.DuplicatesMerged,
		&overview.Websites,
		&overview.Emails,
		&overview.Phones,
		&overview.NeedsReview,
	)
	if err != nil {
		return web.ResultOverview{}, fmt.Errorf("read result overview: %w", err)
	}

	return overview, nil
}

type resultScanner interface {
	Scan(...any) error
}

func scanBusinessResult(scanner resultScanner) (web.BusinessResult, error) {
	var result web.BusinessResult
	var categories, tags string
	var claimed sql.NullBool
	var latitude, longitude, rating sql.NullFloat64
	var websiteResponse, reviewCount sql.NullInt64
	var reviewed int
	var scrapedAt, updatedAt int64
	err := scanner.Scan(
		&result.ID,
		&result.Name,
		&result.PrimaryCategory,
		&categories,
		&result.BusinessStatus,
		&claimed,
		&result.Address,
		&result.City,
		&result.State,
		&result.PostalCode,
		&result.Country,
		&latitude,
		&longitude,
		&result.Phone,
		&result.NormalizedPhone,
		&result.PrimaryEmail,
		&result.Website,
		&result.Domain,
		&result.WebsiteStatus,
		&websiteResponse,
		&rating,
		&reviewCount,
		&result.QualityScore,
		&result.Confidence,
		&reviewed,
		&result.Notes,
		&result.ChangeStatus,
		&result.MapsURL,
		&result.PlaceID,
		&result.CID,
		&result.DataID,
		&result.SourceRecordID,
		&result.SourceJobID,
		&result.SourceQuery,
		&result.SourceCell,
		&scrapedAt,
		&updatedAt,
		&tags,
	)
	if err != nil {
		return web.BusinessResult{}, fmt.Errorf("scan normalized business: %w", err)
	}
	result.Claimed = claimed.Valid && claimed.Bool
	result.Reviewed = reviewed != 0
	result.Latitude = nullFloatPointer(latitude)
	result.Longitude = nullFloatPointer(longitude)
	result.Rating = nullFloatPointer(rating)
	result.ReviewCount = nullIntPointer(reviewCount)
	result.WebsiteResponseMS = nullIntPointer(websiteResponse)
	result.ScrapedAt = time.Unix(scrapedAt, 0).UTC()
	result.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	result.Tags = splitUnitSeparator(tags)
	result.AdditionalCategories = additionalCategories(categories, result.PrimaryCategory)

	return result, nil
}

func resultFilterSQL(filter web.ResultFilter) (string, []any, error) {
	field := strings.ToLower(strings.TrimSpace(filter.Field))
	operator := strings.ToLower(strings.TrimSpace(filter.Operator))
	value := strings.TrimSpace(filter.Value)
	if field == "id" && operator == "in" {
		values := splitFilterValues(value)
		if len(values) == 0 || len(values) > 5000 {
			return "", nil, fmt.Errorf("ID filter must contain between 1 and 5000 identifiers")
		}
		placeholders := make([]string, len(values))
		arguments := make([]any, len(values))
		for index, identifier := range values {
			if !validResultIdentifier(identifier) {
				return "", nil, fmt.Errorf("ID filter contains an invalid identifier")
			}
			placeholders[index] = "?"
			arguments[index] = identifier
		}
		return "businesses.id IN (" + strings.Join(placeholders, ",") + ")", arguments, nil
	}

	columns := map[string]string{
		"id":              "businesses.id",
		"name":            "businesses.name",
		"address":         "businesses.address",
		"city":            "businesses.city",
		"state":           "businesses.state",
		"postal_code":     "businesses.postal_code",
		"country":         "businesses.country",
		"category":        "businesses.primary_category",
		"status":          "businesses.business_status",
		"business_status": "businesses.business_status",
		"website_status":  "businesses.website_status",
		"domain":          "businesses.domain",
		"change_status":   "businesses.change_status",
		"place_id":        "businesses.place_id",
		"cid":             "businesses.cid",
		"data_id":         "businesses.data_id",
		"maps_url":        "businesses.maps_url",
	}
	if column, ok := columns[field]; ok {
		return textFilterSQL(column, operator, value)
	}

	numericColumns := map[string]string{
		"rating":              "businesses.rating",
		"reviews":             "businesses.review_count",
		"review_count":        "businesses.review_count",
		"quality_score":       "businesses.quality_score",
		"confidence":          "businesses.quality_confidence",
		"website_response_ms": "businesses.website_response_ms",
	}
	if column, ok := numericColumns[field]; ok {
		return numericFilterSQL(column, field, operator, value)
	}

	contactExpressions := map[string]string{
		"website": "businesses.website",
		"email": `(SELECT normalized_value FROM emails
			WHERE emails.business_id = businesses.id ORDER BY confidence DESC, id LIMIT 1)`,
		"phone": "businesses.normalized_phone",
	}
	if expression, ok := contactExpressions[field]; ok {
		return textFilterSQL("COALESCE("+expression+", '')", operator, value)
	}

	if field == "reviewed" || field == "claimed" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return "", nil, fmt.Errorf("invalid boolean result filter %q", field)
		}
		column := "businesses.reviewed"
		if field == "claimed" {
			column = "COALESCE(businesses.claimed, 0)"
		}
		switch operator {
		case "eq":
			return column + " = ?", []any{boolInt(parsed)}, nil
		case "neq":
			return column + " <> ?", []any{boolInt(parsed)}, nil
		}
	}
	if field == "tags" && (operator == "contains" || operator == "eq") {
		return `EXISTS (
			SELECT 1 FROM business_tags JOIN tags ON tags.id = business_tags.tag_id
			WHERE business_tags.business_id = businesses.id AND tags.name = ? COLLATE NOCASE
		)`, []any{value}, nil
	}
	if field == "tags" && operator == "not_contains" {
		return `NOT EXISTS (
			SELECT 1 FROM business_tags JOIN tags ON tags.id = business_tags.tag_id
			WHERE business_tags.business_id = businesses.id AND tags.name = ? COLLATE NOCASE
		)`, []any{value}, nil
	}
	if field == "category_member" {
		exists := `EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(businesses.categories)
			THEN businesses.categories ELSE '[]' END) WHERE value = ? COLLATE NOCASE)`
		switch operator {
		case "eq", "contains":
			return exists, []any{value}, nil
		case "neq", "not_contains":
			return "NOT " + exists, []any{value}, nil
		}
	}
	if field == "email_status" || field == "email_kind" {
		column := "emails.status"
		if field == "email_kind" {
			column = "emails.kind"
		}
		exists := `EXISTS (SELECT 1 FROM emails WHERE emails.business_id = businesses.id AND ` + column + ` = ? COLLATE NOCASE)`
		switch operator {
		case "eq", "contains":
			return exists, []any{value}, nil
		case "neq", "not_contains":
			return "NOT " + exists, []any{value}, nil
		case "empty":
			return `NOT EXISTS (SELECT 1 FROM emails WHERE emails.business_id = businesses.id)`, nil, nil
		case "not_empty":
			return `EXISTS (SELECT 1 FROM emails WHERE emails.business_id = businesses.id)`, nil, nil
		}
	}
	if field == "social" {
		switch operator {
		case "eq", "contains":
			return `EXISTS (SELECT 1 FROM social_profiles WHERE social_profiles.business_id = businesses.id
				AND social_profiles.platform = ? COLLATE NOCASE)`, []any{value}, nil
		case "neq", "not_contains":
			return `NOT EXISTS (SELECT 1 FROM social_profiles WHERE social_profiles.business_id = businesses.id
				AND social_profiles.platform = ? COLLATE NOCASE)`, []any{value}, nil
		case "empty":
			return `NOT EXISTS (SELECT 1 FROM social_profiles WHERE social_profiles.business_id = businesses.id)`, nil, nil
		case "not_empty":
			return `EXISTS (SELECT 1 FROM social_profiles WHERE social_profiles.business_id = businesses.id)`, nil, nil
		}
	}
	if field == "technology" {
		technology := `EXISTS (SELECT 1 FROM websites, json_each(CASE WHEN json_valid(websites.technologies)
			THEN websites.technologies ELSE '[]' END) AS technology
			WHERE websites.business_id = businesses.id AND technology.value = ? COLLATE NOCASE)`
		switch operator {
		case "eq", "contains":
			return technology, []any{value}, nil
		case "neq", "not_contains":
			return "NOT " + technology, []any{value}, nil
		case "empty":
			return `NOT EXISTS (SELECT 1 FROM websites, json_each(CASE WHEN json_valid(websites.technologies)
				THEN websites.technologies ELSE '[]' END) AS technology WHERE websites.business_id = businesses.id)`, nil, nil
		case "not_empty":
			return `EXISTS (SELECT 1 FROM websites, json_each(CASE WHEN json_valid(websites.technologies)
				THEN websites.technologies ELSE '[]' END) AS technology WHERE websites.business_id = businesses.id)`, nil, nil
		}
	}

	dateColumns := map[string]string{
		"updated_at":    "businesses.updated_at",
		"first_seen_at": "businesses.first_seen_at",
		"last_seen_at":  "businesses.last_seen_at",
		"scraped_at": `(SELECT extracted_at FROM business_sources WHERE business_sources.business_id = businesses.id
			ORDER BY extracted_at DESC, id DESC LIMIT 1)`,
		"last_checked_at": `(SELECT last_checked_at FROM websites WHERE websites.business_id = businesses.id
			ORDER BY last_checked_at DESC, id DESC LIMIT 1)`,
	}
	if column, ok := dateColumns[field]; ok {
		return dateFilterSQL(column, field, operator, value)
	}
	if field == "bbox" && operator == "within" {
		values, err := parseFilterNumbers(value, 4)
		if err != nil || values[0] < -90 || values[2] > 90 || values[0] > values[2] || values[1] > values[3] {
			return "", nil, fmt.Errorf("invalid bounding-box result filter")
		}
		return `businesses.latitude BETWEEN ? AND ? AND businesses.longitude BETWEEN ? AND ?`,
			[]any{values[0], values[2], values[1], values[3]}, nil
	}
	if field == "distance" && operator == "within" {
		values, err := parseFilterNumbers(value, 3)
		if err != nil || values[0] < -90 || values[0] > 90 || values[1] < -180 || values[1] > 180 ||
			values[2] <= 0 || values[2] > 1000 {
			return "", nil, fmt.Errorf("invalid radius result filter")
		}
		latitude, longitude, radiusKM := values[0], values[1], values[2]
		longitudeScale := 111.320 * math.Cos(latitude*math.Pi/180)
		return `businesses.latitude IS NOT NULL AND businesses.longitude IS NOT NULL AND
			(((businesses.latitude - ?) * 111.320) * ((businesses.latitude - ?) * 111.320) +
			 ((businesses.longitude - ?) * ?) * ((businesses.longitude - ?) * ?)) <= ?`,
			[]any{latitude, latitude, longitude, longitudeScale, longitude, longitudeScale, radiusKM * radiusKM}, nil
	}
	if field == "polygon" && operator == "within" {
		return polygonFilterSQL(value)
	}

	return "", nil, fmt.Errorf("unsupported result filter %q/%q", field, operator)
}

func validResultIdentifier(value string) bool {
	if len(value) < 5 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func resultFilterGroupSQL(group web.ResultFilterGroup, depth int, nodes *int) (string, []any, error) {
	if depth > 4 {
		return "", nil, fmt.Errorf("nested result filter is too deep")
	}
	logic := strings.ToUpper(strings.TrimSpace(group.Logic))
	if logic == "" {
		logic = "AND"
	}
	if logic != "AND" && logic != "OR" {
		return "", nil, fmt.Errorf("nested filter logic must be AND or OR")
	}
	parts := make([]string, 0, len(group.Filters)+len(group.Groups))
	args := make([]any, 0)
	for _, filter := range group.Filters {
		(*nodes)++
		if *nodes > 50 {
			return "", nil, fmt.Errorf("nested result filter has too many conditions")
		}
		clause, values, err := resultFilterSQL(filter)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "("+clause+")")
		args = append(args, values...)
	}
	for _, child := range group.Groups {
		(*nodes)++
		if *nodes > 50 {
			return "", nil, fmt.Errorf("nested result filter has too many conditions")
		}
		clause, values, err := resultFilterGroupSQL(child, depth+1, nodes)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "("+clause+")")
		args = append(args, values...)
	}
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("nested result filter group is empty")
	}
	clause := strings.Join(parts, " "+logic+" ")
	if group.Not {
		clause = "NOT (" + clause + ")"
	}
	return clause, args, nil
}

func textFilterSQL(column, operator, value string) (string, []any, error) {
	switch operator {
	case "eq":
		return column + " = ? COLLATE NOCASE", []any{value}, nil
	case "neq":
		return column + " <> ? COLLATE NOCASE", []any{value}, nil
	case "contains":
		return column + " LIKE ? ESCAPE '\\'", []any{"%" + escapeLike(value) + "%"}, nil
	case "not_contains":
		return column + " NOT LIKE ? ESCAPE '\\'", []any{"%" + escapeLike(value) + "%"}, nil
	case "starts_with":
		return column + " LIKE ? ESCAPE '\\'", []any{escapeLike(value) + "%"}, nil
	case "ends_with":
		return column + " LIKE ? ESCAPE '\\'", []any{"%" + escapeLike(value)}, nil
	case "empty":
		return "COALESCE(" + column + ", '') = ''", nil, nil
	case "not_empty":
		return "COALESCE(" + column + ", '') <> ''", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported text result operator %q", operator)
	}
}

func numericFilterSQL(column, field, operator, value string) (string, []any, error) {
	if operator == "empty" {
		return column + " IS NULL", nil, nil
	}
	if operator == "not_empty" {
		return column + " IS NOT NULL", nil, nil
	}
	if operator == "between" {
		values, err := parseFilterNumbers(value, 2)
		if err != nil || values[0] > values[1] {
			return "", nil, fmt.Errorf("invalid numeric range result filter %q", field)
		}
		return column + " BETWEEN ? AND ?", []any{values[0], values[1]}, nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", nil, fmt.Errorf("invalid numeric result filter %q", field)
	}
	symbols := map[string]string{"eq": "=", "neq": "<>", "gte": ">=", "lte": "<=", "gt": ">", "lt": "<"}
	symbol, ok := symbols[operator]
	if !ok {
		return "", nil, fmt.Errorf("unsupported numeric result operator %q", operator)
	}
	return column + " " + symbol + " ?", []any{number}, nil
}

func dateFilterSQL(column, field, operator, value string) (string, []any, error) {
	if operator == "empty" {
		return column + " IS NULL", nil, nil
	}
	if operator == "not_empty" {
		return column + " IS NOT NULL", nil, nil
	}
	if operator == "between" {
		parts := splitFilterValues(value)
		if len(parts) != 2 {
			return "", nil, fmt.Errorf("invalid date range result filter %q", field)
		}
		start, err := parseFilterTime(parts[0], false)
		if err != nil {
			return "", nil, fmt.Errorf("invalid date range result filter %q", field)
		}
		end, err := parseFilterTime(parts[1], true)
		if err != nil || start > end {
			return "", nil, fmt.Errorf("invalid date range result filter %q", field)
		}
		return column + " BETWEEN ? AND ?", []any{start, end}, nil
	}
	timestamp, err := parseFilterTime(value, operator == "lte" || operator == "before")
	if err != nil {
		return "", nil, fmt.Errorf("invalid date result filter %q", field)
	}
	symbols := map[string]string{"eq": "=", "neq": "<>", "gte": ">=", "after": ">=", "lte": "<=", "before": "<="}
	symbol, ok := symbols[operator]
	if !ok {
		return "", nil, fmt.Errorf("unsupported date result operator %q", operator)
	}
	return column + " " + symbol + " ?", []any{timestamp}, nil
}

func parseFilterTime(value string, endOfDay bool) (int64, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Unix(), nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0, err
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Second)
	}
	return parsed.UTC().Unix(), nil
}

func parseFilterNumbers(value string, count int) ([]float64, error) {
	parts := splitFilterValues(value)
	if len(parts) != count {
		return nil, fmt.Errorf("expected %d numeric values", count)
	}
	values := make([]float64, count)
	for index, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, fmt.Errorf("invalid numeric value")
		}
		values[index] = parsed
	}
	return values, nil
}

func splitFilterValues(value string) []string {
	separator := ","
	if strings.Contains(value, "..") {
		separator = ".."
	}
	parts := strings.Split(value, separator)
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func polygonFilterSQL(value string) (string, []any, error) {
	var geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(value), &geometry); err != nil || !strings.EqualFold(geometry.Type, "Polygon") {
		return "", nil, fmt.Errorf("polygon filter must be a GeoJSON Polygon")
	}
	var rings [][][]float64
	if err := json.Unmarshal(geometry.Coordinates, &rings); err != nil || len(rings) == 0 || len(rings[0]) < 4 || len(rings[0]) > 100 {
		return "", nil, fmt.Errorf("polygon filter has invalid coordinates")
	}
	ring := rings[0]
	parts := make([]string, 0, len(ring)-1)
	args := make([]any, 0, (len(ring)-1)*6)
	for index := 0; index < len(ring)-1; index++ {
		left, right := ring[index], ring[index+1]
		if len(left) < 2 || len(right) < 2 || left[0] < -180 || left[0] > 180 || right[0] < -180 || right[0] > 180 ||
			left[1] < -90 || left[1] > 90 || right[1] < -90 || right[1] > 90 {
			return "", nil, fmt.Errorf("polygon filter has invalid coordinates")
		}
		parts = append(parts, `CASE WHEN ((? > businesses.latitude) <> (? > businesses.latitude))
			AND businesses.longitude < ((? - ?) * (businesses.latitude - ?) / NULLIF((? - ?), 0) + ?)
			THEN 1 ELSE 0 END`)
		args = append(args, left[1], right[1], right[0], left[0], left[1], right[1], left[1], left[0])
	}
	return `businesses.latitude IS NOT NULL AND businesses.longitude IS NOT NULL AND ((` +
		strings.Join(parts, "+") + `) % 2) = 1`, args, nil
}

func resultSortSQL(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "updated_desc":
		return "businesses.updated_at DESC, businesses.id", nil
	case "name_asc":
		return "businesses.normalized_name, businesses.id", nil
	case "rating_desc":
		return "businesses.rating IS NULL, businesses.rating DESC, businesses.id", nil
	case "reviews_desc":
		return "businesses.review_count IS NULL, businesses.review_count DESC, businesses.id", nil
	case "quality_desc":
		return "businesses.quality_score DESC, businesses.id", nil
	default:
		return "", fmt.Errorf("unsupported result sort %q", value)
	}
}

func ftsMatchQuery(value string) string {
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return `""`
	}
	for index := range parts {
		parts[index] = `"` + strings.ReplaceAll(parts[index], `"`, `""`) + `"`
	}

	return strings.Join(parts, " AND ")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	value = strings.ReplaceAll(value, "_", `\_`)

	return value
}

func changedTopLevelFields(beforeJSON, afterJSON string) []string {
	before := decodeJSONObject(beforeJSON)
	after := decodeJSONObject(afterJSON)
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}

	changed := make([]string, 0)
	for key := range keys {
		beforeValue := mustJSON(before[key], "null")
		afterValue := mustJSON(after[key], "null")
		if beforeValue != afterValue {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)

	return changed
}

func decodeJSONObject(value string) map[string]any {
	result := make(map[string]any)
	_ = json.Unmarshal([]byte(value), &result)

	return result
}

func businessQuality(business resultimport.Business) (float64, float64) {
	score := 0.0
	if business.Name != "" {
		score += 10
	}
	if business.Address.Normalized != "" {
		score += 15
	}
	if business.Website.Valid {
		score += 20
	}
	if len(business.Phones) > 0 {
		score += 15
	}
	if len(business.Emails) > 0 {
		score += 20
	}
	if business.ReviewRating != nil {
		score += 8
	}
	if business.ReviewCount != nil {
		score += 7
	}
	if business.Latitude != nil && business.Longitude != nil {
		score += 5
	}
	confidence := min(1, 0.35+float64(len(business.IdentityKeys))*0.12)

	return min(100, score), confidence
}

func canonicalJSONValue(value resultimport.JSONValue) string {
	if !value.Valid {
		return ""
	}

	return string(value.Value)
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}

	return *value
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}

	return *value
}

func nullFloatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}

	result := value.Float64

	return &result
}

func nullIntPointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}

	result := value.Int64

	return &result
}

func nullIntegerBoolPointer(value sql.NullInt64) *bool {
	if !value.Valid {
		return nil
	}

	result := value.Int64 != 0

	return &result
}

func mustJSON(value any, fallback string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fallback
	}

	return string(encoded)
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}

	return result
}

func boolInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func emailKind(value string) string {
	parts := strings.SplitN(value, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "unknown"
	}
	local := strings.ToLower(parts[0])
	switch local {
	case "info", "sales", "support", "contact", "admin", "billing", "careers", "jobs", "hello", "office":
		return "role"
	default:
		return "personal-looking"
	}
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))

	return hex.EncodeToString(digest[:])
}

func splitUnitSeparator(value string) []string {
	if value == "" {
		return nil
	}

	return strings.Split(value, string(rune(31)))
}

func additionalCategories(value, primary string) string {
	var categories []string
	if json.Unmarshal([]byte(value), &categories) != nil {
		return ""
	}
	result := make([]string, 0, len(categories))
	for _, category := range categories {
		if !strings.EqualFold(category, primary) && strings.TrimSpace(category) != "" {
			result = append(result, category)
		}
	}

	return strings.Join(result, ", ")
}
