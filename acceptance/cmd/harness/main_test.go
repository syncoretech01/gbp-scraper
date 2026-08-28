package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveExperimentsGroups(t *testing.T) {
	opts, _, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	catalog, err := buildCatalogOptions(opts)
	if err != nil {
		t.Fatalf("buildCatalogOptions: %v", err)
	}

	escalation, err := resolveExperiments("escalation", catalog)
	if err != nil || len(escalation) != 5 {
		t.Fatalf("escalation = %d err=%v", len(escalation), err)
	}
	markets, err := resolveExperiments("markets", catalog)
	if err != nil || len(markets) != 3 {
		t.Fatalf("markets = %d err=%v", len(markets), err)
	}
	widths, err := resolveExperiments("widths", catalog)
	if err != nil || len(widths) != 4 {
		t.Fatalf("widths = %d err=%v", len(widths), err)
	}

	all, err := resolveExperiments("all", catalog)
	if err != nil || len(all) != 13 {
		t.Fatalf("all = %d err=%v", len(all), err)
	}

	rung, err := resolveExperiments("W4", catalog)
	if err != nil || len(rung) != 1 || rung[0].Job.TaskWorkers != 4 {
		t.Fatalf("W4 = %+v err=%v", rung, err)
	}
	single, err := resolveExperiments("D", catalog)
	if err != nil || len(single) != 1 || single[0].ID != "D" {
		t.Fatalf("single = %+v err=%v", single, err)
	}
	if _, err := resolveExperiments("bogus", catalog); err == nil {
		t.Errorf("bogus experiment should error")
	}
}

func TestParseQueries(t *testing.T) {
	if got := parseQueries("a||b||c"); len(got) != 3 {
		t.Errorf("pipe split = %v", got)
	}
	if got := parseQueries("a\nb\n"); len(got) != 2 {
		t.Errorf("newline split = %v", got)
	}
	if got := parseQueries("   "); got != nil {
		t.Errorf("blank = %v", got)
	}
}

func TestParseLadder(t *testing.T) {
	ladder, err := parseLadder("A=1,B=2,C=4")
	if err != nil {
		t.Fatalf("parseLadder: %v", err)
	}
	if ladder["A"] != 1 || ladder["B"] != 2 || ladder["C"] != 4 {
		t.Errorf("ladder = %v", ladder)
	}
	if _, err := parseLadder("A"); err == nil {
		t.Errorf("missing = should error")
	}
	if _, err := parseLadder("A=x"); err == nil {
		t.Errorf("non-numeric should error")
	}
}

func TestBuildCatalogOptionsOverrides(t *testing.T) {
	opts, _, err := parseFlags([]string{
		"-queries", "one||two", "-grid-bbox", "1,2,3,4", "-grid-cell-km", "2.5",
		"-runtime", "600", "-market-concurrency", "3",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	catalog, err := buildCatalogOptions(opts)
	if err != nil {
		t.Fatalf("buildCatalogOptions: %v", err)
	}
	if len(catalog.Workload.Queries) != 2 {
		t.Errorf("queries = %v", catalog.Workload.Queries)
	}
	if catalog.Workload.GridBBox != "1,2,3,4" || catalog.Workload.GridCellKM != 2.5 {
		t.Errorf("grid = %q %v", catalog.Workload.GridBBox, catalog.Workload.GridCellKM)
	}
	if catalog.Workload.RuntimeSeconds != 600 || catalog.MarketConcurrency != 3 {
		t.Errorf("runtime/market = %d/%d", catalog.Workload.RuntimeSeconds, catalog.MarketConcurrency)
	}
}

func TestRunListAndDryRunNeedNoBase(t *testing.T) {
	var listOut bytes.Buffer
	if err := run([]string{"-experiment", "escalation", "-list"}, &listOut); err != nil {
		t.Fatalf("list run: %v", err)
	}
	if !strings.Contains(listOut.String(), "A ") && !strings.Contains(listOut.String(), "A\t") &&
		!strings.Contains(listOut.String(), "browser mode") {
		t.Errorf("list output missing experiments: %q", listOut.String())
	}

	var dryOut bytes.Buffer
	if err := run([]string{"-experiment", "D", "-dry-run"}, &dryOut); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !strings.Contains(dryOut.String(), "\"concurrency\": 8") {
		t.Errorf("dry-run output missing job JSON: %q", dryOut.String())
	}
}

func TestRunWithoutBaseErrors(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-experiment", "A"}, &out); err == nil {
		t.Errorf("running without -base should error")
	}
}
