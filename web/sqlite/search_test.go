package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestGlobalSearchCoversWorkspaceEntities(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("search-job", time.Now().UTC())
	job.Name = "Dental campaign"
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "search.csv")
	writeLegacyResultRows(t, resultPath, map[string]string{
		"title": "Golden Gate Dental", "category": "Dentist", "city": "San Francisco", "place_id": "search-place",
	})
	if _, err := concrete.ImportLegacyCSV(ctx, job, resultPath); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := concrete.db.ExecContext(ctx, `INSERT INTO tags(name, created_at) VALUES ('Dental lead', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := concrete.db.ExecContext(ctx, `INSERT INTO templates(id,name,description,configuration,created_at,updated_at) VALUES ('dental-template','Dental template','Campaign template','{}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := concrete.db.ExecContext(ctx, `INSERT INTO exports(id,name,format,state,source_type,created_at) VALUES ('dental-export','Dental delivery','csv','completed','results',?)`, now); err != nil {
		t.Fatal(err)
	}

	items, err := concrete.GlobalSearch(ctx, "Dental", 25)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, item := range items {
		types[item.Type] = true
		if item.Title == "" || item.URL == "" {
			t.Fatalf("invalid search item: %+v", item)
		}
	}
	for _, expected := range []string{"Business", "Job", "Tag", "Template", "Export"} {
		if !types[expected] {
			t.Fatalf("missing %s in %+v", expected, items)
		}
	}
}

var _ interface {
	GlobalSearch(context.Context, string, int) ([]web.GlobalSearchItem, error)
} = (*repo)(nil)
