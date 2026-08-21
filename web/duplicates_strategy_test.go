package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The merge decision now carries a preferred-value rule. It is validated at the
// service boundary and only ever reaches the repository for a merge, so a
// mistyped rule can never quietly rewrite fields.

// duplicateDecisionRecorder captures what the service handed the repository.
type duplicateDecisionRecorder struct {
	decision DuplicateDecision
	calls    int
}

type duplicateStrategyRepository struct {
	*fixedJobRepository
	recorder duplicateDecisionRecorder
}

func (r *duplicateStrategyRepository) ResolveDuplicateCandidate(
	_ context.Context,
	decision DuplicateDecision,
) (DuplicateResolution, error) {
	r.recorder.calls++
	r.recorder.decision = decision

	return DuplicateResolution{
		CandidateID: decision.CandidateID, Action: decision.Action, State: "merged",
		KeptBusinessID: "biz_keep", MergedBusinessID: "biz_merge",
		FieldStrategy: decision.FieldStrategy, PreferredFields: []string{"name"},
	}, nil
}

func (r *duplicateStrategyRepository) ListDuplicateCandidates(
	context.Context,
	int,
) ([]DuplicateReviewPair, error) {
	return nil, nil
}

func TestResolveDuplicateValidatesThePreferredValueRule(t *testing.T) {
	t.Parallel()

	repository := &duplicateStrategyRepository{fixedJobRepository: &fixedJobRepository{}}
	service := NewService(repository, t.TempDir())

	for _, strategy := range DuplicateFieldStrategies() {
		resolution, err := service.ResolveDuplicateCandidate(context.Background(), DuplicateDecision{
			CandidateID: 1, Action: "merge", FieldStrategy: strategy,
		})
		if err != nil {
			t.Fatalf("merge with %q rule error = %v", strategy, err)
		}

		if resolution.FieldStrategy != strategy {
			t.Fatalf("resolution rule = %q, want %q", resolution.FieldStrategy, strategy)
		}
	}

	_, err := service.ResolveDuplicateCandidate(context.Background(), DuplicateDecision{
		CandidateID: 1, Action: "merge", FieldStrategy: "coin-flip",
	})
	if !errors.Is(err, ErrInvalidDuplicateDecision) {
		t.Fatalf("unknown rule error = %v, want ErrInvalidDuplicateDecision", err)
	}

	// Keeping both records is not a merge, so it must not accept a value rule.
	_, err = service.ResolveDuplicateCandidate(context.Background(), DuplicateDecision{
		CandidateID: 1, Action: "keep_both", FieldStrategy: "recency",
	})
	if !errors.Is(err, ErrInvalidDuplicateDecision) {
		t.Fatalf("keep_both with a rule error = %v, want ErrInvalidDuplicateDecision", err)
	}
}

func TestMergeRouteForwardsThePreferredValueRule(t *testing.T) {
	t.Parallel()

	repository := &duplicateStrategyRepository{fixedJobRepository: &fixedJobRepository{}}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := url.Values{}
	form.Set("candidate_id", "7")
	form.Set("keep_business_id", "biz_keep")
	form.Set("field_strategy", "recency")
	form.Set("csrf_token", server.csrfToken)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/duplicates/merge",
		strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("merge status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if repository.recorder.decision.FieldStrategy != "recency" {
		t.Fatalf("forwarded rule = %q, want recency", repository.recorder.decision.FieldStrategy)
	}

	if !strings.Contains(recorder.Body.String(), `"preferred_fields"`) {
		t.Fatalf("merge response = %s, want the adopted fields reported", recorder.Body.String())
	}
}

func TestDuplicateDrawerOffersThePreferredValueRule(t *testing.T) {
	t.Parallel()

	base := &fixedResultRepository{fixedJobRepository: &fixedJobRepository{}}
	base.detail = BusinessDetail{
		Business: BusinessResult{ID: "biz_abcde", Name: "Harbor Dental"},
		DuplicateMatches: []DuplicateMatchView{{
			CandidateID: 7, BusinessID: "biz_other", Name: "Harbor Dental Care",
			Score: .82, State: "pending", Signals: `["name","address"]`,
		}},
	}

	server, err := New(NewService(&duplicateCapableResultRepository{
		fixedResultRepository: base,
	}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/results/biz_abcde/drawer", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("drawer status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, expected := range []string{
		`name="field_strategy"`, `value="confidence"`, `value="recency"`, `value="completeness"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("duplicate drawer is missing %q", expected)
		}
	}
}

// duplicateCapableResultRepository serves business detail and accepts duplicate
// decisions, which is what the drawer's merge controls are gated on.
type duplicateCapableResultRepository struct {
	*fixedResultRepository
}

func (r *duplicateCapableResultRepository) ResolveDuplicateCandidate(
	_ context.Context,
	decision DuplicateDecision,
) (DuplicateResolution, error) {
	return DuplicateResolution{CandidateID: decision.CandidateID, State: "merged"}, nil
}

func (r *duplicateCapableResultRepository) ListDuplicateCandidates(
	context.Context,
	int,
) ([]DuplicateReviewPair, error) {
	return nil, nil
}
