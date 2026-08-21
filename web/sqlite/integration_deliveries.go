package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// maximumDeliveryPage bounds one page of delivery history.
const maximumDeliveryPage = 200

// ClaimIntegrationDelivery reserves one delivery. A named subject is unique per
// integration and event, so a repeated poll, a restart, or a duplicated export
// notification can never deliver the same subject twice.
func (repo *repo) ClaimIntegrationDelivery(
	ctx context.Context,
	delivery web.IntegrationDelivery,
) (bool, error) {
	createdAt := delivery.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	result, err := repo.db.ExecContext(ctx,
		`INSERT INTO integration_deliveries(
			id, integration_id, event, subject_id, state, created_at
		) VALUES (?, ?, ?, ?, 'pending', ?)
		ON CONFLICT(integration_id, event, subject_id) WHERE subject_id <> '' DO NOTHING`,
		delivery.ID, delivery.IntegrationID, delivery.Event, delivery.SubjectID, createdAt.Unix(),
	)
	if err != nil {
		return false, fmt.Errorf("claim integration delivery: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read integration delivery claim: %w", err)
	}

	return affected > 0, nil
}

// CompleteIntegrationDelivery records the final outcome of a claimed delivery.
func (repo *repo) CompleteIntegrationDelivery(
	ctx context.Context,
	delivery web.IntegrationDelivery,
) error {
	finishedAt := time.Now().UTC()
	if delivery.FinishedAt != nil {
		finishedAt = delivery.FinishedAt.UTC()
	}

	state := delivery.State
	if state == "" {
		state = "failed"
	}

	_, err := repo.db.ExecContext(ctx,
		`UPDATE integration_deliveries
		SET state = ?, attempts = ?, http_status = ?, message = ?,
			payload_bytes = ?, duration_ms = ?, finished_at = ?
		WHERE id = ?`,
		state, delivery.Attempts, delivery.HTTPStatus, delivery.Message,
		delivery.PayloadBytes, delivery.DurationMS, finishedAt.Unix(), delivery.ID,
	)
	if err != nil {
		return fmt.Errorf("record integration delivery: %w", err)
	}

	return nil
}

// ListIntegrationDeliveries returns recent delivery history, newest first.
func (repo *repo) ListIntegrationDeliveries(
	ctx context.Context,
	integrationID string,
	limit int,
) ([]web.IntegrationDelivery, error) {
	if limit < 1 || limit > maximumDeliveryPage {
		limit = maximumDeliveryPage
	}

	query := `SELECT id, integration_id, event, subject_id, state, attempts,
		http_status, message, payload_bytes, duration_ms, created_at, finished_at
		FROM integration_deliveries`

	arguments := make([]any, 0, 2)

	if strings.TrimSpace(integrationID) != "" {
		query += " WHERE integration_id = ?"

		arguments = append(arguments, integrationID)
	}

	query += " ORDER BY created_at DESC, id DESC LIMIT ?"

	arguments = append(arguments, limit)

	rows, err := repo.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list integration deliveries: %w", err)
	}
	defer rows.Close()

	deliveries := make([]web.IntegrationDelivery, 0, limit)

	for rows.Next() {
		var (
			delivery   web.IntegrationDelivery
			createdAt  int64
			finishedAt sql.NullInt64
		)

		if err := rows.Scan(
			&delivery.ID, &delivery.IntegrationID, &delivery.Event, &delivery.SubjectID,
			&delivery.State, &delivery.Attempts, &delivery.HTTPStatus, &delivery.Message,
			&delivery.PayloadBytes, &delivery.DurationMS, &createdAt, &finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan integration delivery: %w", err)
		}

		delivery.CreatedAt = time.Unix(createdAt, 0).UTC()

		if finishedAt.Valid {
			value := time.Unix(finishedAt.Int64, 0).UTC()
			delivery.FinishedAt = &value
		}

		deliveries = append(deliveries, delivery)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list integration deliveries: %w", err)
	}

	return deliveries, nil
}
