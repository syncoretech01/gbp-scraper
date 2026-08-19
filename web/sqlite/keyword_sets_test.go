package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestKeywordSetsListNewestFirstAndBounded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for index, name := range []string{"Alpha", "Beta", "Gamma"} {
		stamp := base.Add(time.Duration(index) * time.Minute)
		if _, err := repository.SaveKeywordSet(ctx, web.KeywordSet{
			ID:        fmt.Sprintf("keyword-set-%d", index),
			Name:      name,
			Keywords:  []string{"dentists in San Francisco"},
			CreatedAt: stamp,
			UpdatedAt: stamp,
		}); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}

	sets, err := repository.ListKeywordSets(ctx, 0)
	if err != nil {
		t.Fatalf("list keyword sets: %v", err)
	}

	if len(sets) != 3 || sets[0].Name != "Gamma" || sets[1].Name != "Beta" || sets[2].Name != "Alpha" {
		t.Fatalf("newest-first order = %+v", sets)
	}

	bounded, err := repository.ListKeywordSets(ctx, 2)
	if err != nil {
		t.Fatalf("list bounded: %v", err)
	}

	if len(bounded) != 2 || bounded[0].Name != "Gamma" || bounded[1].Name != "Beta" {
		t.Fatalf("bounded listing = %+v", bounded)
	}

	// An oversized limit is clamped rather than passed through.
	clamped, err := repository.ListKeywordSets(ctx, 100000)
	if err != nil {
		t.Fatalf("list clamped: %v", err)
	}

	if len(clamped) != 3 {
		t.Fatalf("clamped listing length = %d", len(clamped))
	}
}

func TestKeywordSetsUpsertByNameKeepsIdentityAndUsage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	created := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	original, err := repository.SaveKeywordSet(ctx, web.KeywordSet{
		ID:        "keyword-set-original",
		Name:      "Bay Area dental",
		Keywords:  []string{"dentists in San Francisco"},
		CreatedAt: created,
		UpdatedAt: created,
	})
	if err != nil {
		t.Fatalf("save original: %v", err)
	}

	usedAt := created.Add(time.Hour)
	if _, err := repository.TouchKeywordSetUse(ctx, original.ID, usedAt); err != nil {
		t.Fatalf("record use: %v", err)
	}

	touched, err := repository.TouchKeywordSetUse(ctx, original.ID, usedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("record second use: %v", err)
	}

	if touched.UseCount != 2 || touched.LastUsedAt == nil || !touched.LastUsedAt.Equal(usedAt.Add(time.Minute)) {
		t.Fatalf("usage = %+v", touched)
	}

	// The name is unique case-insensitively: re-saving updates the existing
	// row's keywords and description while keeping its id, creation time,
	// and usage history.
	updated, err := repository.SaveKeywordSet(ctx, web.KeywordSet{
		ID:          "keyword-set-replacement",
		Name:        "BAY AREA DENTAL",
		Description: "Second pass",
		Keywords:    []string{"dental clinics in Oakland", "dentists in Berkeley"},
		CreatedAt:   created.Add(2 * time.Hour),
		UpdatedAt:   created.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert by name: %v", err)
	}

	if updated.ID != original.ID {
		t.Fatalf("upsert changed identity: %q -> %q", original.ID, updated.ID)
	}

	if updated.UseCount != 2 || updated.Description != "Second pass" || len(updated.Keywords) != 2 {
		t.Fatalf("upsert result = %+v", updated)
	}

	if !updated.CreatedAt.Equal(created) {
		t.Fatalf("creation time moved: %v", updated.CreatedAt)
	}

	sets, err := repository.ListKeywordSets(ctx, 0)
	if err != nil {
		t.Fatalf("list after upsert: %v", err)
	}

	if len(sets) != 1 {
		t.Fatalf("upsert duplicated the set: %+v", sets)
	}
}

func TestKeywordSetsDeleteAndMissingRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	saved, err := repository.SaveKeywordSet(ctx, web.KeywordSet{
		ID:       "keyword-set-delete",
		Name:     "Disposable",
		Keywords: []string{"coffee shops in Portland"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := repository.DeleteKeywordSet(ctx, saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	sets, err := repository.ListKeywordSets(ctx, 0)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}

	if len(sets) != 0 {
		t.Fatalf("sets after delete = %+v", sets)
	}

	if err := repository.DeleteKeywordSet(ctx, saved.ID); !errors.Is(err, web.ErrKeywordSetNotFound) {
		t.Fatalf("second delete returned %v, want not-found", err)
	}

	if _, err := repository.TouchKeywordSetUse(ctx, "missing", time.Now().UTC()); !errors.Is(err, web.ErrKeywordSetNotFound) {
		t.Fatalf("touching a missing set returned %v, want not-found", err)
	}
}
