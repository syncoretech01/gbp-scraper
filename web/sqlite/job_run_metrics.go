package sqlite

import (
	"context"
	"fmt"
)

// JobOpenDuplicateCandidates counts the pairs this job produced that the
// resolver flagged as probably the same business and nobody has decided yet.
//
// Both halves of the pair must belong to this job. A pair that straddles two
// runs is a workspace-level question: attributing it to whichever run is on
// screen would report the same pair twice and make the totals across runs
// exceed the number of open pairs that exist.
//
// This is the only "duplicate" figure the job monitor may present alongside
// entity merges, and the two are different things: a candidate is a question,
// a merge is an answer. See web/run_metrics.go for the vocabulary.
func (repo *repo) JobOpenDuplicateCandidates(ctx context.Context, jobID string) (int64, error) {
	var count int64
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM duplicate_candidates
		WHERE duplicate_candidates.state = 'pending'
			AND duplicate_candidates.left_business_id IN (
				SELECT business_id FROM job_businesses WHERE job_id = ?
			)
			AND duplicate_candidates.right_business_id IN (
				SELECT business_id FROM job_businesses WHERE job_id = ?
			)`,
		jobID, jobID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count open duplicate candidates: %w", err)
	}

	return count, nil
}
