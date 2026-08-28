package web

import "context"

// Run counting vocabulary.
//
// A Maps run counts four different things and, until this file existed, showed
// three of them under one word. "Duplicates merged" on the job monitor was the
// number of repeated rows inside the committed CSV — a file the merge has
// already de-repeated, so the figure is structurally zero — while the coverage
// strip beside it reported "555 rows added / 224 rows replaced" for the same
// run. Neither phrase describes an entity merge, and no operator can be
// expected to reconcile them.
//
// The five names below are the whole vocabulary. Every surface that counts a
// run must use these words and no others:
//
//	Maps observations            a search returned a business and the merge
//	                             accepted the row. The same business found by
//	                             four searches is four observations.
//	Repeated observations        observations of a business an earlier search
//	                             in this run had already collected. The stored
//	                             row was refreshed, not added.
//	Unique businesses            distinct businesses the run kept. This is
//	                             observations minus repeated observations.
//	Entity merges                separate stored business records an operator
//	                             or the resolver folded into one. This is the
//	                             only thing that may be called a merge.
//	Unresolved duplicate         pairs the resolver flagged as probably the
//	  candidates                 same business and nobody has decided yet.
//
// Checkpoint replacement is a repeated observation, never an entity merge:
// re-finding a business and refreshing its row does not fold two records
// together, and calling it a merge tells the operator that data was combined
// when nothing was.

// RunObservations is one run counted in the vocabulary above.
//
// Available separates "the run made no repeated observations" from "nothing
// has reported observation counts yet". Consumers must not print zeros for the
// second case.
type RunObservations struct {
	Available bool `json:"available"`
	// Observations is every accepted Maps observation across the run.
	Observations int64 `json:"observations"`
	// RepeatObservations is the part of Observations that re-found a business
	// an earlier search in the same run had already collected.
	RepeatObservations int64 `json:"repeat_observations"`
	// InFileDuplicateRows counts rows a single search's own result file
	// repeated and the merge dropped outright. It is separate from
	// RepeatObservations, which is overlap *between* searches.
	InFileDuplicateRows int64 `json:"in_file_duplicate_rows"`
	// UniqueBusinesses is what the run kept.
	UniqueBusinesses int64 `json:"unique_businesses"`
	// EntityMerges counts stored records folded into another record.
	EntityMerges int64 `json:"entity_merges"`
	// HasEntityMerges separates a real zero from missing evidence, exactly as
	// Available does for the observation counts.
	HasEntityMerges bool `json:"has_entity_merges"`
	// UnresolvedDuplicates counts flagged pairs still awaiting a decision;
	// HasUnresolvedDuplicates separates zero from unknown.
	UnresolvedDuplicates    int64 `json:"unresolved_duplicates"`
	HasUnresolvedDuplicates bool  `json:"has_unresolved_duplicates"`
}

// NewRunObservations derives the run vocabulary from the durable coverage
// totals. The totals are per-task checkpoint sums, which is the only place the
// run records how often a search re-found something an earlier search had
// already collected.
//
// A plan with no finished task yields an unavailable value rather than zeros,
// because "no search has reported yet" and "no search found anything" are
// different facts and the monitor must not conflate them.
func NewRunObservations(totals CoverageTotals) RunObservations {
	observations := RunObservations{
		Available:           totals.TasksDone > 0 || totals.RowsAdded > 0,
		Observations:        max(totals.RowsAdded, 0),
		RepeatObservations:  max(totals.RowsReplaced, 0),
		InFileDuplicateRows: max(totals.DuplicatesSkipped, 0),
	}
	observations.UniqueBusinesses = max(observations.Observations-observations.RepeatObservations, 0)

	return observations
}

// WithUniqueBusinesses replaces the derived unique count with the committed
// one. The committed result file is the better authority whenever it exists:
// the derived subtraction assumes every replacement landed on a row this run
// wrote, which stops holding once a run is resumed from a checkpoint written
// by an earlier attempt.
func (observations RunObservations) WithUniqueBusinesses(unique int64) RunObservations {
	if unique > 0 {
		observations.UniqueBusinesses = unique
	}

	return observations
}

// WithEntityMerges records how many stored records were folded together.
func (observations RunObservations) WithEntityMerges(merges int64) RunObservations {
	observations.EntityMerges = max(merges, 0)
	observations.HasEntityMerges = true

	return observations
}

// WithUnresolvedDuplicates records how many flagged pairs await a decision.
func (observations RunObservations) WithUnresolvedDuplicates(pairs int64) RunObservations {
	observations.UnresolvedDuplicates = max(pairs, 0)
	observations.HasUnresolvedDuplicates = true

	return observations
}

// RepeatSharePercent is the share of observations that re-found a business the
// run already had, rounded to one decimal. It is the honest answer to "how
// much did my searches overlap".
func (observations RunObservations) RepeatSharePercent() float64 {
	return sharePercent(int(observations.RepeatObservations), int(observations.Observations))
}

// jobDuplicateRepository is the optional capability behind the unresolved
// duplicate count. It is separate from the rest of the repository interface so
// an installation without duplicate review simply reports the count as
// unknown instead of failing the page.
type jobDuplicateRepository interface {
	JobOpenDuplicateCandidates(ctx context.Context, jobID string) (int64, error)
}

// JobOpenDuplicateCandidates counts the flagged pairs from this job that
// nobody has decided yet.
//
// Only pairs whose both halves this job collected are counted. A pair that
// straddles two runs is a workspace-level question, and attributing it to
// whichever run happens to be on screen would make the same pair appear twice.
func (s *Service) JobOpenDuplicateCandidates(ctx context.Context, jobID string) (int64, bool) {
	if s.repo == nil {
		return 0, false
	}

	repository, ok := s.repo.(jobDuplicateRepository)
	if !ok {
		return 0, false
	}

	count, err := repository.JobOpenDuplicateCandidates(ctx, jobID)
	if err != nil {
		return 0, false
	}

	return count, true
}
