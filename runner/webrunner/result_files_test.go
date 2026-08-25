package webrunner

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gosom/google-maps-scraper/web"
)

func TestMergeResultCSVPreservesPartialRowsAndPrefersRetryRows(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	runPath := filepath.Join(directory, "job.run-retry.csv")
	header := []string{"place_id", "title", "website", "phone", "address"}
	writeMergeFixture(t, destination, header,
		[]string{"place-1", "Old Dental", "https://old.example", "+1 415 555 0101", "1 Main St"},
		[]string{"place-2", "Keep Dental", "https://keep.example", "+1 415 555 0102", "2 Main St"},
	)
	writeMergeFixture(t, runPath, header,
		[]string{"place-1", "Updated Dental", "https://updated.example", "+1 415 555 0101", "1 Main St"},
		[]string{"place-3", "New Dental", "https://new.example", "+1 415 555 0103", "3 Main St"},
		[]string{"place-3", "Duplicate New Dental", "https://new.example", "+1 415 555 0103", "3 Main St"},
	)

	summary, err := mergeResultCSV(context.Background(), destination, runPath)
	if err != nil {
		t.Fatalf("mergeResultCSV() error = %v", err)
	}
	if summary.ExistingKept != 1 || summary.ExistingReplaced != 1 || summary.RunAdded != 2 || summary.DuplicatesSkipped != 1 {
		t.Fatalf("mergeResultCSV() summary = %+v", summary)
	}
	if _, err := os.Stat(runPath); !os.IsNotExist(err) {
		t.Fatalf("run file still exists after successful merge: %v", err)
	}

	rows := readMergeFixture(t, destination)
	if len(rows) != 4 {
		t.Fatalf("merged row count including header = %d, rows = %v", len(rows), rows)
	}
	if rows[1][1] != "Keep Dental" || rows[2][1] != "Updated Dental" || rows[3][1] != "New Dental" {
		t.Fatalf("merged rows = %v", rows)
	}
}

func TestMergeResultCSVHeaderMismatchLeavesFilesUntouched(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	runPath := filepath.Join(directory, "job.run-retry.csv")
	writeMergeFixture(t, destination, []string{"place_id", "title"}, []string{"place-1", "Existing"})
	writeMergeFixture(t, runPath, []string{"title", "place_id", "extra"}, []string{"Run", "place-2", "value"})

	if _, err := mergeResultCSV(context.Background(), destination, runPath); err == nil {
		t.Fatal("mergeResultCSV() accepted mismatched headers")
	}
	if rows := readMergeFixture(t, destination); len(rows) != 2 || rows[1][1] != "Existing" {
		t.Fatalf("destination changed after rejected merge: %v", rows)
	}
	if _, err := os.Stat(runPath); err != nil {
		t.Fatalf("run file was not preserved after rejected merge: %v", err)
	}
}

func TestRecoverResultRunFilesMergesInterruptedRun(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	runPath := filepath.Join(directory, "job.run-interrupted.csv")
	header := []string{"place_id", "title"}
	writeMergeFixture(t, destination, header, []string{"place-1", "Existing"})
	writeMergeFixture(t, runPath, header, []string{"place-2", "Recovered"})

	if err := recoverResultRunFiles(context.Background(), directory); err != nil {
		t.Fatalf("recoverResultRunFiles() error = %v", err)
	}
	rows := readMergeFixture(t, destination)
	if len(rows) != 3 || rows[2][1] != "Recovered" {
		t.Fatalf("recovered rows = %v", rows)
	}
}

func TestRecoverResultRunFilesRestoresInterruptedReplacementFirst(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	backupPath := filepath.Join(directory, "job.csv.replace-backup-test")
	writeMergeFixture(t, backupPath, []string{"place_id", "title"}, []string{"place-1", "Restored"})

	if err := recoverResultRunFiles(context.Background(), directory); err != nil {
		t.Fatalf("recoverResultRunFiles() error = %v", err)
	}
	rows := readMergeFixture(t, filepath.Join(directory, "job.csv"))
	if len(rows) != 2 || rows[1][1] != "Restored" {
		t.Fatalf("restored replacement rows = %v", rows)
	}
}
// TestMergeResultCSVKeepsDistinctListingsSharingWeakAttributes proves that
// distinct Google Maps listings which happen to share a website domain, a
// phone number, or a postal address are all preserved by a single merge.
// Before the identity fix these collapsed to one row (franchise locations share
// one domain; building tenants share one address), silently discarding
// committed businesses.
func TestMergeResultCSVKeepsDistinctListingsSharingWeakAttributes(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	runPath := filepath.Join(directory, "job.run-a.csv")
	header := []string{"place_id", "title", "website", "phone", "address"}
	writeMergeFixture(t, destination, header)
	writeMergeFixture(t, runPath, header,
		[]string{"place-1", "Subway Downtown", "https://www.subway.com", "+1 415 555 0000", "1 Market St"},
		[]string{"place-2", "Subway Uptown", "https://www.subway.com", "+1 415 555 0000", "1 Market St"},
		[]string{"place-3", "Subway Midtown", "https://www.subway.com", "+1 415 555 0000", "1 Market St"},
	)

	summary, err := mergeResultCSV(context.Background(), destination, runPath)
	if err != nil {
		t.Fatalf("mergeResultCSV() error = %v", err)
	}
	if summary.RunAdded != 3 || summary.DuplicatesSkipped != 0 {
		t.Fatalf("merge collapsed distinct listings, summary = %+v", summary)
	}
	rows := readMergeFixture(t, destination)
	if len(rows) != 4 {
		t.Fatalf("expected 3 distinct rows plus header, got %v", rows)
	}
}

// TestMergeResultCSVDoesNotCannibalizeCommittedRows proves that a later task
// whose run file shares only a weak attribute (here a franchise domain and a
// shared phone) with an already-committed business never replaces or drops that
// committed business. This is the multi-cell grid scenario: task A commits one
// franchise location, task B discovers a different franchise location in an
// adjacent cell.
func TestMergeResultCSVDoesNotCannibalizeCommittedRows(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	header := []string{"place_id", "title", "website", "phone", "address"}
	writeMergeFixture(t, destination, header,
		[]string{"place-1", "Subway Downtown", "https://www.subway.com", "+1 415 555 0000", "1 Market St"},
	)
	runPath := filepath.Join(directory, "job.run-b.csv")
	writeMergeFixture(t, runPath, header,
		[]string{"place-2", "Subway Uptown", "https://www.subway.com", "+1 415 555 0000", "9 Mission St"},
	)

	summary, err := mergeResultCSV(context.Background(), destination, runPath)
	if err != nil {
		t.Fatalf("mergeResultCSV() error = %v", err)
	}
	if summary.ExistingReplaced != 0 || summary.ExistingKept != 1 || summary.RunAdded != 1 {
		t.Fatalf("committed row was cannibalized, summary = %+v", summary)
	}
	rows := readMergeFixture(t, destination)
	if len(rows) != 3 {
		t.Fatalf("expected both committed and new rows, got %v", rows)
	}
	found := map[string]bool{}
	for _, row := range rows[1:] {
		found[row[0]] = true
	}
	if !found["place-1"] || !found["place-2"] {
		t.Fatalf("a distinct committed listing was dropped, rows = %v", rows)
	}
}

// TestMergeResultCSVReplacesSameListingByStrongIdentity confirms the intended
// replacement still works: a retry that re-scrapes the same listing (matched on
// cid or data_id, not only place_id) updates the row in place rather than
// duplicating it.
func TestMergeResultCSVReplacesSameListingByStrongIdentity(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	runPath := filepath.Join(directory, "job.run-retry.csv")
	header := []string{"place_id", "cid", "data_id", "title", "website"}
	writeMergeFixture(t, destination, header,
		[]string{"", "cid-9", "data-9", "Stale Name", "https://stale.example"},
	)
	writeMergeFixture(t, runPath, header,
		[]string{"", "cid-9", "data-9", "Fresh Name", "https://fresh.example"},
	)

	summary, err := mergeResultCSV(context.Background(), destination, runPath)
	if err != nil {
		t.Fatalf("mergeResultCSV() error = %v", err)
	}
	if summary.ExistingReplaced != 1 || summary.RunAdded != 1 {
		t.Fatalf("strong-identity retry did not replace in place, summary = %+v", summary)
	}
	rows := readMergeFixture(t, destination)
	if len(rows) != 2 || rows[1][3] != "Fresh Name" {
		t.Fatalf("retry did not win the strong-identity conflict, rows = %v", rows)
	}
}

// TestMergeResultCSVEmptyLaterRunKeepsCommittedRows proves that a task which
// crashes and produces no rows (header-only run file) cannot drop the rows an
// earlier successful task already committed: an empty run file yields no run
// identity keys, so nothing in the destination can be replaced.
func TestMergeResultCSVEmptyLaterRunKeepsCommittedRows(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	header := []string{"place_id", "title"}
	writeMergeFixture(t, destination, header,
		[]string{"place-1", "Committed"},
		[]string{"place-2", "Also Committed"},
	)
	runPath := filepath.Join(directory, "job.run-crash.csv")
	writeMergeFixture(t, runPath, header) // header only: the task produced nothing

	summary, err := mergeResultCSV(context.Background(), destination, runPath)
	if err != nil {
		t.Fatalf("mergeResultCSV() error = %v", err)
	}
	if summary.ExistingReplaced != 0 || summary.ExistingKept != 2 || summary.RunAdded != 0 {
		t.Fatalf("empty run dropped committed rows, summary = %+v", summary)
	}
	rows := readMergeFixture(t, destination)
	if len(rows) != 3 {
		t.Fatalf("committed rows lost to an empty run, rows = %v", rows)
	}
}

// TestMergeResultCSVIsIdempotentUnderReplay reproduces recovery replaying the
// same run file (interrupted after the atomic replace but before the run file
// was removed). Re-merging must neither grow nor shrink the destination.
func TestMergeResultCSVIsIdempotentUnderReplay(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	header := []string{"place_id", "title", "website"}
	writeMergeFixture(t, destination, header,
		[]string{"place-1", "First", "https://shared.example"},
	)

	writeReplay := func(name string) string {
		runPath := filepath.Join(directory, name)
		writeMergeFixture(t, runPath, header,
			[]string{"place-1", "First Updated", "https://shared.example"},
			[]string{"place-2", "Second", "https://shared.example"},
		)

		return runPath
	}

	if _, err := mergeResultCSV(context.Background(), destination, writeReplay("job.run-1.csv")); err != nil {
		t.Fatalf("first merge error = %v", err)
	}
	first := readMergeFixture(t, destination)

	if _, err := mergeResultCSV(context.Background(), destination, writeReplay("job.run-1.csv")); err != nil {
		t.Fatalf("replayed merge error = %v", err)
	}
	second := readMergeFixture(t, destination)

	if len(first) != 3 {
		t.Fatalf("expected two distinct listings after first merge, got %v", first)
	}
	if len(second) != len(first) {
		t.Fatalf("replay changed the row count: first = %v second = %v", first, second)
	}
}

// TestMergeTaskOutputConcurrentCompletionsKeepEveryRow drives the real pool
// merge lock: many tasks finishing at once, each folding one distinct listing
// into the shared job CSV. The merge lock must serialise them so no completed
// row is lost to a lost-update race, and every distinct listing must survive.
func TestMergeTaskOutputConcurrentCompletionsKeepEveryRow(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "job.csv")
	header := []string{"place_id", "title", "website"}
	writeMergeFixture(t, destination, header)

	run := &taskPoolRun{job: &web.Job{}, outpath: destination}

	const tasks = 24
	runPaths := make([]string, tasks)
	for index := range tasks {
		runPaths[index] = filepath.Join(directory, fmt.Sprintf("job.run-%02d.csv", index))
		writeMergeFixture(t, runPaths[index], header, []string{
			fmt.Sprintf("place-%02d", index),
			fmt.Sprintf("Business %02d", index),
			"https://shared-franchise.example",
		})
	}

	var group sync.WaitGroup
	group.Add(tasks)
	for index := range tasks {
		go func() {
			defer group.Done()
			if _, err := run.mergeTaskOutput(runPaths[index], 0); err != nil {
				t.Errorf("mergeTaskOutput() error = %v", err)
			}
		}()
	}
	group.Wait()

	if got := run.committedWrites.Load(); got != tasks {
		t.Fatalf("committedWrites = %d, want %d", got, tasks)
	}
	rows := readMergeFixture(t, destination)
	if len(rows) != tasks+1 {
		t.Fatalf("expected %d distinct rows plus header, got %d", tasks, len(rows)-1)
	}
	seen := make(map[string]struct{}, tasks)
	for _, row := range rows[1:] {
		seen[row[0]] = struct{}{}
	}
	if len(seen) != tasks {
		t.Fatalf("distinct listings collapsed under concurrent merge: %d unique of %d", len(seen), tasks)
	}
}

func writeMergeFixture(t *testing.T, path string, rows ...[]string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", path, err)
	}
	writer := csv.NewWriter(file)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatalf("write fixture row: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
}

func readMergeFixture(t *testing.T, path string) [][]string {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(%s) error = %v", path, err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll(%s) error = %v", path, err)
	}

	return rows
}
