package web

import (
	"strings"
	"testing"
)

// TestStatusFilterCarriesTheUnavailableStatusNotice is the issue-H regression
// on the server side.
//
// Measured against the live workspace: 372 of 372 stored businesses carry an
// empty business_status, and 331 of 331 rows in the Thorough acceptance CSV
// have an empty status column, because Maps no longer returns one. A stored
// status rule therefore narrows the result view to nothing, and the plan every
// surface is built from has to say so instead of presenting an empty view as a
// finding about the businesses.
func TestStatusFilterCarriesTheUnavailableStatusNotice(t *testing.T) {
	t.Parallel()

	data := JobData{ResultFilters: &JobResultFilters{Statuses: []string{JobStatusOperational}}}

	plan := BuildJobCollectionPlan("job-1", data)

	if !plan.Filters.StatusFiltered() {
		t.Fatal("a stored status rule was not reported as a status filter")
	}

	if !hasNotice(plan.Notices, JobStatusFilterNotice) {
		t.Fatalf("plan notices %q do not explain the unavailable status", plan.Notices)
	}

	// A plan with no status rule must not carry the warning.
	quiet := BuildJobCollectionPlan("job-1", JobData{
		ResultFilters: &JobResultFilters{NameContains: "clinic"},
	})

	if hasNotice(quiet.Notices, JobStatusFilterNotice) {
		t.Fatalf("a plan with no status rule carried the status notice: %q", quiet.Notices)
	}
}

// TestReviewBoundCarriesTheUncapturedCountNotice is the issue-K regression on
// the filter side: a review count Fast mode could not capture is stored as
// unknown, not zero, and a numeric bound excludes it. Saying so is what stops
// "0 reviews" from being read as a fact about the business.
func TestReviewBoundCarriesTheUncapturedCountNotice(t *testing.T) {
	t.Parallel()

	minimum := int64(10)

	plan := BuildJobCollectionPlan("job-1", JobData{
		ResultFilters: &JobResultFilters{ReviewsMin: &minimum},
	})

	if !plan.Filters.ReviewCountFiltered() {
		t.Fatal("a stored review bound was not reported as a review filter")
	}

	if !hasNotice(plan.Notices, JobReviewFilterNotice) {
		t.Fatalf("plan notices %q do not explain uncaptured review counts", plan.Notices)
	}

	quiet := BuildJobCollectionPlan("job-1", JobData{
		ResultFilters: &JobResultFilters{NameContains: "clinic"},
	})

	if hasNotice(quiet.Notices, JobReviewFilterNotice) {
		t.Fatalf("a plan with no review bound carried the review notice: %q", quiet.Notices)
	}
}

// TestFilterPredicatesTolerateNoFilters keeps the two new predicates safe on
// the nil filter set every unfiltered job carries.
func TestFilterPredicatesTolerateNoFilters(t *testing.T) {
	t.Parallel()

	var filters *JobResultFilters

	if filters.StatusFiltered() || filters.ReviewCountFiltered() {
		t.Fatal("a nil filter set reported a filter")
	}

	empty := &JobResultFilters{}
	if empty.StatusFiltered() || empty.ReviewCountFiltered() {
		t.Fatal("an empty filter set reported a filter")
	}

	// Whitespace-only statuses normalize away and must not count.
	blank := &JobResultFilters{Statuses: []string{"  ", ""}}
	if blank.StatusFiltered() {
		t.Fatal("blank statuses reported as a status filter")
	}
}

func hasNotice(notices []string, want string) bool {
	for _, notice := range notices {
		if strings.TrimSpace(notice) == strings.TrimSpace(want) {
			return true
		}
	}

	return false
}
