package webrunner

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type resultMergeSummary struct {
	ExistingKept      int64
	ExistingReplaced  int64
	RunAdded          int64
	DuplicatesSkipped int64
}

// mergeResultCSV atomically folds a completed run file into the job's legacy
// CSV. Rows from the new run win exact identity conflicts, while unrelated
// rows already collected by an earlier partial run remain intact.
func mergeResultCSV(ctx context.Context, destination, runPath string) (resultMergeSummary, error) {
	var summary resultMergeSummary
	runInfo, err := os.Stat(runPath)
	if err != nil {
		return summary, fmt.Errorf("inspect run result CSV: %w", err)
	}
	if runInfo.Size() == 0 {
		if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(runPath, destination); err != nil {
				return summary, fmt.Errorf("preserve empty result CSV: %w", err)
			}
		} else if err != nil {
			return summary, fmt.Errorf("inspect existing result CSV: %w", err)
		} else if err := os.Remove(runPath); err != nil {
			return summary, fmt.Errorf("remove empty run result CSV: %w", err)
		}

		return summary, nil
	}

	runHeader, err := csvHeader(runPath)
	if err != nil {
		return summary, err
	}
	runKeys := make(map[string]struct{})
	if err := walkCSV(ctx, runPath, runHeader, func(row []string) error {
		for _, key := range resultIdentityKeys(runHeader, row) {
			runKeys[key] = struct{}{}
		}

		return nil
	}); err != nil {
		return summary, err
	}

	directory := filepath.Dir(destination)
	merged, err := os.CreateTemp(directory, filepath.Base(destination)+".merge-*")
	if err != nil {
		return summary, fmt.Errorf("create merged result CSV: %w", err)
	}
	mergedPath := merged.Name()
	keepMerged := false
	defer func() {
		_ = merged.Close()
		if !keepMerged {
			_ = os.Remove(mergedPath)
		}
	}()

	writer := csv.NewWriter(merged)
	seen := make(map[string]struct{})
	destinationExists := false
	if info, statErr := os.Stat(destination); statErr == nil && info.Size() > 0 {
		destinationExists = true
		existingHeader, headerErr := csvHeader(destination)
		if headerErr != nil {
			return summary, headerErr
		}
		if !sameCSVHeader(existingHeader, runHeader) {
			return summary, errors.New("existing and run result CSV headers do not match")
		}
		if err := writer.Write(existingHeader); err != nil {
			return summary, fmt.Errorf("write merged CSV header: %w", err)
		}
		if err := walkCSV(ctx, destination, existingHeader, func(row []string) error {
			keys := resultIdentityKeys(existingHeader, row)
			if identityIntersects(keys, runKeys) {
				summary.ExistingReplaced++

				return nil
			}
			if identityIntersects(keys, seen) {
				summary.DuplicatesSkipped++

				return nil
			}
			if err := writer.Write(row); err != nil {
				return fmt.Errorf("write existing merged CSV row: %w", err)
			}
			rememberIdentityKeys(seen, keys)
			summary.ExistingKept++

			return nil
		}); err != nil {
			return summary, err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return summary, fmt.Errorf("inspect existing result CSV: %w", statErr)
	}
	if !destinationExists {
		if err := writer.Write(runHeader); err != nil {
			return summary, fmt.Errorf("write result CSV header: %w", err)
		}
	}

	if err := walkCSV(ctx, runPath, runHeader, func(row []string) error {
		keys := resultIdentityKeys(runHeader, row)
		if identityIntersects(keys, seen) {
			summary.DuplicatesSkipped++

			return nil
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("write run merged CSV row: %w", err)
		}
		rememberIdentityKeys(seen, keys)
		summary.RunAdded++

		return nil
	}); err != nil {
		return summary, err
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return summary, fmt.Errorf("flush merged result CSV: %w", err)
	}
	if err := merged.Sync(); err != nil {
		return summary, fmt.Errorf("sync merged result CSV: %w", err)
	}
	if err := merged.Close(); err != nil {
		return summary, fmt.Errorf("close merged result CSV: %w", err)
	}
	if err := replaceResultFile(mergedPath, destination); err != nil {
		return summary, fmt.Errorf("replace result CSV atomically: %w", err)
	}
	keepMerged = true
	if err := os.Remove(runPath); err != nil {
		return summary, fmt.Errorf("remove merged run result CSV: %w", err)
	}

	return summary, nil
}

func replaceResultFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if _, err := os.Stat(destination); err != nil {
		return err
	}

	backupFile, err := os.CreateTemp(filepath.Dir(destination), filepath.Base(destination)+".replace-backup-*")
	if err != nil {
		return err
	}
	backupPath := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		_ = os.Remove(backupPath)

		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(destination, backupPath); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if rollbackErr := os.Rename(backupPath, destination); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore prior result CSV: %w", rollbackErr))
		}

		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("remove prior result CSV backup: %w", err)
	}

	return nil
}

func recoverResultRunFiles(ctx context.Context, dataFolder string) error {
	recoveryFailures := make([]error, 0)
	if err := recoverResultReplacementBackups(dataFolder); err != nil {
		recoveryFailures = append(recoveryFailures, err)
	}

	pattern := filepath.Join(dataFolder, "*.run-*.csv")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("find interrupted result runs: %w", err)
	}
	type pendingRun struct {
		path    string
		modTime int64
	}
	pending := make([]pendingRun, 0, len(paths))
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return fmt.Errorf("inspect interrupted result run: %w", statErr)
		}
		pending = append(pending, pendingRun{path: path, modTime: info.ModTime().UnixNano()})
	}
	sort.Slice(pending, func(left, right int) bool {
		if pending[left].modTime == pending[right].modTime {
			return pending[left].path < pending[right].path
		}

		return pending[left].modTime < pending[right].modTime
	})

	for _, run := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		base := filepath.Base(run.path)
		marker := strings.Index(base, ".run-")
		if marker <= 0 {
			continue
		}
		destination := filepath.Join(dataFolder, base[:marker]+".csv")
		if _, mergeErr := mergeResultCSV(ctx, destination, run.path); mergeErr != nil {
			recoveryFailures = append(recoveryFailures, fmt.Errorf("recover %s: %w", base, mergeErr))
		}
	}

	return errors.Join(recoveryFailures...)
}

func recoverResultReplacementBackups(dataFolder string) error {
	paths, err := filepath.Glob(filepath.Join(dataFolder, "*.csv.replace-backup-*"))
	if err != nil {
		return fmt.Errorf("find interrupted result replacements: %w", err)
	}
	failures := make([]error, 0)
	for _, backupPath := range paths {
		base := filepath.Base(backupPath)
		marker := strings.Index(base, ".replace-backup-")
		if marker <= 0 {
			continue
		}
		destination := filepath.Join(dataFolder, base[:marker])
		if _, statErr := os.Stat(destination); errors.Is(statErr, os.ErrNotExist) {
			if renameErr := os.Rename(backupPath, destination); renameErr != nil {
				failures = append(failures, fmt.Errorf("restore %s: %w", base, renameErr))
			}

			continue
		} else if statErr != nil {
			failures = append(failures, fmt.Errorf("inspect replacement target %s: %w", base, statErr))

			continue
		}

		orphanPath := backupPath + ".orphan"
		if renameErr := os.Rename(backupPath, orphanPath); renameErr != nil {
			failures = append(failures, fmt.Errorf("preserve prior replacement %s: %w", base, renameErr))
		}
	}

	return errors.Join(failures...)
}

func csvHeader(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open result CSV: %w", err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("result CSV has no header")
	}
	if err != nil {
		return nil, fmt.Errorf("read result CSV header: %w", err)
	}
	if len(header) == 0 {
		return nil, errors.New("result CSV has an empty header")
	}
	header[0] = strings.TrimPrefix(header[0], "\ufeff")

	return header, nil
}

func walkCSV(ctx context.Context, path string, expectedHeader []string, consume func([]string) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open result CSV rows: %w", err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read result CSV header: %w", err)
	}
	header[0] = strings.TrimPrefix(header[0], "\ufeff")
	if !sameCSVHeader(header, expectedHeader) {
		return errors.New("result CSV header changed while reading")
	}
	rowNumber := int64(1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		rowNumber++
		if readErr != nil {
			return fmt.Errorf("read result CSV row %d: malformed CSV", rowNumber)
		}
		if err := consume(row); err != nil {
			return err
		}
	}
}

func sameCSVHeader(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if canonicalCSVHeader(left[index]) != canonicalCSVHeader(right[index]) {
			return false
		}
	}

	return true
}

// resultIdentityKeys derives the identity of one CSV row for merge dedup and
// replacement. Only the authoritative Google identifiers (place_id, cid,
// data_id) identify a distinct Maps listing; a row that carries none falls back
// to a hash of its whole content so only byte-identical rows collapse.
//
// Weak attributes (phone, website domain, postal address) are deliberately not
// identity here. Distinct listings routinely share them — franchise locations
// share one website domain, tenants of one building share an address, and a
// shared reception line spans several businesses — so folding rows on a weak
// attribute silently discards distinct, already-committed businesses. The
// authoritative SQLite entity resolution performs weak-signal deduplication
// non-destructively (it records review candidates and honours keep-separate
// rules) and is the correct place for it; the per-job CSV must preserve every
// distinct listing it ever committed.
func resultIdentityKeys(header, row []string) []string {
	indexes := make(map[string]int, len(header))
	for index, value := range header {
		indexes[canonicalCSVHeader(value)] = index
	}
	value := func(name string) string {
		index, ok := indexes[name]
		if !ok || index >= len(row) {
			return ""
		}

		return strings.TrimSpace(row[index])
	}

	keys := make([]string, 0, 3)
	appendKey := func(kind, normalized string) {
		if normalized != "" {
			keys = append(keys, kind+":"+normalized)
		}
	}
	appendKey("place", strings.ToLower(value("place_id")))
	appendKey("cid", strings.ToLower(value("cid")))
	appendKey("data", strings.ToLower(value("data_id")))
	if len(keys) == 0 {
		hash := sha256.New()
		for _, cell := range row {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(cell))
		}
		keys = append(keys, "row:"+hex.EncodeToString(hash.Sum(nil)))
	}

	return keys
}

func canonicalCSVHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "\ufeff")))
}

func identityIntersects(keys []string, set map[string]struct{}) bool {
	for _, key := range keys {
		if _, ok := set[key]; ok {
			return true
		}
	}

	return false
}

func rememberIdentityKeys(set map[string]struct{}, keys []string) {
	for _, key := range keys {
		set[key] = struct{}{}
	}
}
