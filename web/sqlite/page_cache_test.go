package sqlite

import (
	"path/filepath"
	"testing"
)

// TestConnectionKeepsAWorkspaceSizedPageCache pins the page-cache pragma. On
// the Docker bind mount every page miss is a round trip through the host
// file-sharing layer; with the old 1000-page cache a 372-business workspace
// rendered its dashboard in 10-50 s against 0.03 s on a native volume. The
// cache is the difference, so a regression here is a product regression.
func TestConnectionKeepsAWorkspaceSizedPageCache(t *testing.T) {
	t.Parallel()

	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	var cacheSize int64
	if err := concrete.db.QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("read cache_size: %v", err)
	}
	if cacheSize != -65536 {
		t.Fatalf("cache_size = %d, want -65536 (64 MiB)", cacheSize)
	}

	var tempStore int64
	if err := concrete.db.QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil {
		t.Fatalf("read temp_store: %v", err)
	}
	if tempStore != 2 {
		t.Fatalf("temp_store = %d, want 2 (MEMORY)", tempStore)
	}
}
