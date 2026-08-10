package web

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSummarizeResults(t *testing.T) {
	t.Parallel()

	csv := strings.Join([]string{
		"place_id,title,address,website,phone,emails",
		"place-1,Alpha,1 Main St,https://alpha.test,+1 555 1000,[alpha@example.test]",
		"place-1,Alpha duplicate,1 Main St,,,[]",
		"place-2,Beta,2 Main St,,+1 555 2000,null",
		",No stable ID,3 Main St,https://third.test,,{}",
	}, "\n")

	stats, err := summarizeResults(context.Background(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("summarizeResults: %v", err)
	}

	if stats.Rows != 4 || stats.UniqueBusinesses != 3 || stats.Duplicates != 1 {
		t.Fatalf("row stats = %+v", stats)
	}

	if stats.WithWebsite != 2 || stats.WithPhone != 2 || stats.WithEmail != 1 {
		t.Fatalf("contact stats = %+v", stats)
	}
}

func TestSummarizeResultsHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := summarizeResults(ctx, strings.NewReader("title\nAlpha\n"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestGetResultStatsRejectsTraversal(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, t.TempDir())

	if _, err := svc.GetResultStats(context.Background(), "../outside"); err == nil {
		t.Fatal("expected traversal error")
	}
}
