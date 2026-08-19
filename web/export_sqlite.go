package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite" // portable export database driver
)

type sqliteExportWriter struct {
	destination  string
	temporary    string
	columns      []ExportColumnSelection
	options      ExportBuildOptions
	database     *sql.DB
	transaction  *sql.Tx
	insert       *sql.Stmt
	detailInsert *sql.Stmt
	closed       bool
}

func newSQLiteExportWriter(
	destination string,
	columns []ExportColumnSelection,
	options ExportBuildOptions,
) (*sqliteExportWriter, error) {
	temporary := destination + ".tmp"
	if _, err := os.Stat(temporary); err == nil {
		return nil, fmt.Errorf("temporary SQLite export already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	database, err := sql.Open("sqlite", temporary)
	if err != nil {
		return nil, fmt.Errorf("open SQLite export: %w", err)
	}
	database.SetMaxOpenConns(1)
	writer := &sqliteExportWriter{
		destination: destination, temporary: temporary, columns: columns,
		options: options, database: database,
	}
	if err := writer.initialize(); err != nil {
		writer.Abort()
		return nil, err
	}
	return writer, nil
}

func (writer *sqliteExportWriter) initialize() error {
	pragmas := []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA user_version=1",
	}
	for _, statement := range pragmas {
		if _, err := writer.database.Exec(statement); err != nil {
			return fmt.Errorf("configure SQLite export: %w", err)
		}
	}
	definitions := make([]string, 0, len(writer.columns))
	identifiers := make([]string, 0, len(writer.columns))
	placeholders := make([]string, 0, len(writer.columns))
	for _, column := range writer.columns {
		definition, _ := exportColumnDefinitionFor(column.Key)
		identifier := sqliteExportIdentifier(column.Label)
		identifiers = append(identifiers, identifier)
		definitions = append(definitions, identifier+" "+sqliteExportType(definition.DataType))
		placeholders = append(placeholders, "?")
	}
	if _, err := writer.database.Exec("CREATE TABLE businesses (" + strings.Join(definitions, ",") + ")"); err != nil {
		return fmt.Errorf("create SQLite businesses table: %w", err)
	}
	if _, err := writer.database.Exec(
		"CREATE TABLE export_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);" +
			"INSERT INTO export_metadata(key,value) VALUES ('format','google-maps-scraper-portable-v1');" +
			"INSERT INTO export_metadata(key,value) VALUES ('created_at',datetime('now'));",
	); err != nil {
		return fmt.Errorf("create SQLite export metadata: %w", err)
	}
	if writer.options.IncludeRaw || writer.options.IncludeSources || writer.options.IncludeProvenance || writer.options.IncludeChanges {
		if _, err := writer.database.Exec(
			"CREATE TABLE export_details (business_id TEXT NOT NULL, raw_json TEXT NOT NULL DEFAULT '', " +
				"sources_json TEXT NOT NULL DEFAULT '[]', provenance_json TEXT NOT NULL DEFAULT '[]', " +
				"changes_json TEXT NOT NULL DEFAULT '[]')",
		); err != nil {
			return fmt.Errorf("create SQLite export detail table: %w", err)
		}
	}
	transaction, err := writer.database.Begin()
	if err != nil {
		return fmt.Errorf("begin SQLite export: %w", err)
	}
	writer.transaction = transaction
	insert, err := transaction.Prepare("INSERT INTO businesses (" + strings.Join(identifiers, ",") + ") VALUES (" + strings.Join(placeholders, ",") + ")")
	if err != nil {
		return fmt.Errorf("prepare SQLite business insert: %w", err)
	}
	writer.insert = insert
	if writer.options.IncludeRaw || writer.options.IncludeSources || writer.options.IncludeProvenance || writer.options.IncludeChanges {
		detailInsert, err := transaction.Prepare(
			"INSERT INTO export_details(business_id,raw_json,sources_json,provenance_json,changes_json) VALUES (?,?,?,?,?)",
		)
		if err != nil {
			return fmt.Errorf("prepare SQLite detail insert: %w", err)
		}
		writer.detailInsert = detailInsert
	}
	return nil
}

func (writer *sqliteExportWriter) Add(row exportDataRow) error {
	if writer.closed {
		return fmt.Errorf("SQLite export writer is closed")
	}
	values := make([]any, 0, len(writer.columns))
	for _, column := range writer.columns {
		value, err := exportColumnValue(row, column.Key)
		if err != nil {
			return err
		}
		switch typed := value.(type) {
		case json.RawMessage:
			value = string(typed)
		case []string:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			value = string(encoded)
		case bool:
			if typed {
				value = int64(1)
			} else {
				value = int64(0)
			}
		}
		values = append(values, value)
	}
	if _, err := writer.insert.Exec(values...); err != nil {
		return fmt.Errorf("insert SQLite export row: %w", err)
	}
	if writer.detailInsert != nil {
		rawJSON := ""
		sourcesJSON := "[]"
		provenanceJSON := "[]"
		changesJSON := "[]"
		if row.Detail == nil {
			return fmt.Errorf("optional SQLite detail was requested without business detail")
		}
		if writer.options.IncludeRaw {
			rawJSON = row.Detail.RawJSON
		}
		var err error
		if writer.options.IncludeSources {
			sourcesJSON, err = marshalJSONString(row.Detail.Sources)
		}
		if err == nil && writer.options.IncludeProvenance {
			provenanceJSON, err = marshalJSONString(row.Detail.Provenance)
		}
		if err == nil && writer.options.IncludeChanges {
			changesJSON, err = marshalJSONString(row.Detail.Changes)
		}
		if err != nil {
			return fmt.Errorf("encode SQLite export detail: %w", err)
		}
		if _, err := writer.detailInsert.Exec(row.Business.ID, rawJSON, sourcesJSON, provenanceJSON, changesJSON); err != nil {
			return fmt.Errorf("insert SQLite export detail: %w", err)
		}
	}
	return nil
}

func (writer *sqliteExportWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	var closeErr error
	if writer.insert != nil {
		closeErr = writer.insert.Close()
	}
	if writer.detailInsert != nil {
		if err := writer.detailInsert.Close(); closeErr == nil {
			closeErr = err
		}
	}
	if closeErr == nil {
		closeErr = writer.transaction.Commit()
	} else {
		_ = writer.transaction.Rollback()
	}
	if databaseErr := writer.database.Close(); closeErr == nil {
		closeErr = databaseErr
	}
	if closeErr != nil {
		removeSQLiteExportFiles(writer.temporary)
		return fmt.Errorf("close SQLite export: %w", closeErr)
	}
	// Windows FlushFileBuffers requires write access, so the durability flush
	// must not use a read-only handle.
	file, err := os.OpenFile(writer.temporary, os.O_RDWR, 0)
	if err == nil {
		err = file.Sync()
		if closeFileErr := file.Close(); err == nil {
			err = closeFileErr
		}
	}
	if err != nil {
		removeSQLiteExportFiles(writer.temporary)
		return fmt.Errorf("sync SQLite export: %w", err)
	}
	if err := os.Rename(writer.temporary, writer.destination); err != nil {
		removeSQLiteExportFiles(writer.temporary)
		return fmt.Errorf("publish SQLite export: %w", err)
	}
	return nil
}

func (writer *sqliteExportWriter) Abort() {
	if writer == nil {
		return
	}
	if !writer.closed {
		writer.closed = true
		if writer.insert != nil {
			_ = writer.insert.Close()
		}
		if writer.detailInsert != nil {
			_ = writer.detailInsert.Close()
		}
		if writer.transaction != nil {
			_ = writer.transaction.Rollback()
		}
		if writer.database != nil {
			_ = writer.database.Close()
		}
	}
	removeSQLiteExportFiles(writer.temporary)
}

func removeSQLiteExportFiles(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-journal")
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

func sqliteExportIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqliteExportType(dataType string) string {
	switch dataType {
	case "number":
		return "REAL"
	case "integer", "boolean":
		return "INTEGER"
	default:
		return "TEXT"
	}
}

func marshalJSONString(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func verifySQLiteExport(path string) error {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err := database.Exec("PRAGMA query_only=ON"); err != nil {
		return err
	}
	var integrity string
	if err := database.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite integrity check returned %q", integrity)
	}
	var tableCount int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('businesses','export_metadata')",
	).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount != 2 {
		return fmt.Errorf("portable SQLite export is missing required tables")
	}
	return nil
}
