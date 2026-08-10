package webrunner

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
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
