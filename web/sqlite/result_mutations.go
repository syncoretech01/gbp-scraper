package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

const maximumResultMutationIDs = 250

func (repo *repo) MutateBusinesses(ctx context.Context, mutation web.ResultMutation) (int64, error) {
	mutation.Action = strings.ToLower(strings.TrimSpace(mutation.Action))
	mutation.Value = strings.TrimSpace(mutation.Value)
	mutation.IDs = uniqueMutationIDs(mutation.IDs)
	if len(mutation.IDs) == 0 || len(mutation.IDs) > maximumResultMutationIDs {
		return 0, fmt.Errorf("%w: select between 1 and %d businesses", web.ErrInvalidResultMutation, maximumResultMutationIDs)
	}
	for _, id := range mutation.IDs {
		if len(id) < 5 || len(id) > 128 {
			return 0, fmt.Errorf("%w: invalid business ID", web.ErrInvalidResultMutation)
		}
	}
	if mutation.Action != "tag" && mutation.Action != "untag" && mutation.Action != "reviewed" &&
		mutation.Action != "unreviewed" && mutation.Action != "notes" {
		return 0, fmt.Errorf("%w: unsupported action", web.ErrInvalidResultMutation)
	}
	if (mutation.Action == "tag" || mutation.Action == "untag") &&
		(mutation.Value == "" || len(mutation.Value) > 64) {
		return 0, fmt.Errorf("%w: tag must be between 1 and 64 characters", web.ErrInvalidResultMutation)
	}
	if mutation.Action == "notes" && (len(mutation.IDs) != 1 || len(mutation.Value) > 5000) {
		return 0, fmt.Errorf("%w: notes require one business and at most 5,000 characters", web.ErrInvalidResultMutation)
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("start business workflow update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()
	var changed int64
	switch mutation.Action {
	case "tag", "untag":
		if mutation.Action == "tag" {
			if _, err := tx.ExecContext(ctx,
				"INSERT OR IGNORE INTO tags(name, created_at) VALUES (?, ?)",
				mutation.Value,
				now,
			); err != nil {
				return 0, fmt.Errorf("save business tag: %w", err)
			}
		}
		var tagID int64
		tagErr := tx.QueryRowContext(ctx, "SELECT id FROM tags WHERE name = ? COLLATE NOCASE", mutation.Value).Scan(&tagID)
		if tagErr != nil && !(mutation.Action == "untag" && errors.Is(tagErr, sql.ErrNoRows)) {
			return 0, fmt.Errorf("read business tag: %w", tagErr)
		}
		if tagErr != nil {
			break
		}
		for _, id := range mutation.IDs {
			var result sql.Result
			if mutation.Action == "tag" {
				result, err = tx.ExecContext(ctx,
					"INSERT OR IGNORE INTO business_tags(business_id, tag_id) SELECT id, ? FROM businesses WHERE id = ? AND deleted_at IS NULL",
					tagID,
					id,
				)
			} else {
				result, err = tx.ExecContext(ctx, "DELETE FROM business_tags WHERE business_id = ? AND tag_id = ?", id, tagID)
			}
			if err != nil {
				return 0, fmt.Errorf("%s business tag: %w", mutation.Action, err)
			}
			rows, _ := result.RowsAffected()
			changed += rows
		}
	case "reviewed", "unreviewed":
		value := mutation.Action == "reviewed"
		for _, id := range mutation.IDs {
			result, err := tx.ExecContext(ctx,
				"UPDATE businesses SET reviewed = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND reviewed <> ?",
				value,
				now,
				id,
				value,
			)
			if err != nil {
				return 0, fmt.Errorf("mark business reviewed: %w", err)
			}
			rows, _ := result.RowsAffected()
			changed += rows
		}
	case "notes":
		result, err := tx.ExecContext(ctx,
			"UPDATE businesses SET notes = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND notes <> ?",
			mutation.Value,
			now,
			mutation.IDs[0],
			mutation.Value,
		)
		if err != nil {
			return 0, fmt.Errorf("update business notes: %w", err)
		}
		changed, _ = result.RowsAffected()
		if changed > 0 {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO notes(entity_type, entity_id, body, created_at, updated_at) VALUES ('business', ?, ?, ?, ?)",
				mutation.IDs[0],
				mutation.Value,
				now,
				now,
			); err != nil {
				return 0, fmt.Errorf("record business note history: %w", err)
			}
		}
	}

	details, _ := json.Marshal(map[string]any{
		"action":  mutation.Action,
		"count":   len(mutation.IDs),
		"changed": changed,
	})
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, details, created_at) VALUES ('business_workflow_updated', 'business', ?, ?)",
		string(details),
		now,
	); err != nil {
		return 0, fmt.Errorf("audit business workflow update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit business workflow update: %w", err)
	}
	return changed, nil
}

func uniqueMutationIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
