package web

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	_ "modernc.org/sqlite" // portable local database destination driver
)

const (
	// Local database destinations. MySQL/MariaDB is absent because no MySQL
	// driver exists in this module graph, and the product must build and run
	// offline; the generated mysql_sql insert transaction plus a watch folder
	// remains the supported path for those servers.
	databaseDriverSQLite   = "sqlite"
	databaseDriverPostgres = "postgres"

	defaultDestinationTable = "businesses"

	maximumDestinationIdentifier = 64
	maximumDestinationRows       = maximumExportRows
	databaseDeliveryTimeout      = 2 * time.Minute
)

// validateDatabaseDestination normalizes a local database destination and
// returns the stored configuration and the redacted public view. The DSN is
// never part of the public view.
func validateDatabaseDestination(configuration integrationConfiguration) (integrationConfiguration, integrationConfiguration, error) {
	driver := strings.ToLower(strings.TrimSpace(configuration.Driver))
	table, err := validateDestinationTable(configuration.Table)
	if err != nil {
		return integrationConfiguration{}, integrationConfiguration{}, err
	}
	normalized := integrationConfiguration{Driver: driver, Table: table}
	public := integrationConfiguration{Driver: driver, Table: table}
	switch driver {
	case databaseDriverSQLite:
		target, targetErr := validateSQLiteDestinationPath(configuration.Target)
		if targetErr != nil {
			return integrationConfiguration{}, integrationConfiguration{}, targetErr
		}
		normalized.Target = target
		public.Target = target
	case databaseDriverPostgres:
		dsn, host, dsnErr := validateLocalPostgresDSN(configuration.DSN)
		if dsnErr != nil {
			return integrationConfiguration{}, integrationConfiguration{}, dsnErr
		}
		normalized.DSN = dsn
		public.Target = host
	default:
		return integrationConfiguration{}, integrationConfiguration{},
			fmt.Errorf("database driver must be sqlite or postgres")
	}

	return normalized, public, nil
}

// validateDestinationTable bounds the destination table name. The name is
// interpolated into DDL, so only a conservative identifier is accepted.
func validateDestinationTable(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultDestinationTable, nil
	}
	if len(value) > maximumDestinationIdentifier {
		return "", fmt.Errorf("destination table name must be at most %d characters", maximumDestinationIdentifier)
	}
	for index, character := range value {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if index == 0 && !letter {
			return "", fmt.Errorf("destination table name must start with a letter")
		}
		if !letter && !digit && character != '_' {
			return "", fmt.Errorf("destination table name may only contain letters, numbers, and underscores")
		}
	}

	return value, nil
}

// validateSQLiteDestinationPath keeps the destination file inside the mounted
// local data directory, so a destination can never be used to write anywhere
// else on the host.
func validateSQLiteDestinationPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", fmt.Errorf("a SQLite destination file inside the data directory is required")
	}
	if len(value) > 512 {
		return "", fmt.Errorf("SQLite destination path is too long")
	}
	switch strings.ToLower(filepath.Ext(value)) {
	case ".sqlite", ".sqlite3", ".db":
	default:
		return "", fmt.Errorf("SQLite destination file must end in .sqlite, .sqlite3, or .db")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." ||
		strings.Contains(clean, "\x00") || filepath.IsAbs(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("SQLite destination must be a relative path inside the data directory")
	}

	return clean, nil
}

// validateLocalPostgresDSN accepts only a private or loopback PostgreSQL URL.
// It returns the DSN and a credential-free "host:port/database" summary.
func validateLocalPostgresDSN(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 {
		return "", "", fmt.Errorf("PostgreSQL DSN must contain 1 to 2048 characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", "", fmt.Errorf("PostgreSQL DSN must be a postgres:// URL")
	}
	host := parsed.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("PostgreSQL DSN must name a host")
	}
	if !localAIHostIsLocal(host) {
		return "", "", fmt.Errorf("PostgreSQL host must be a loopback or private address")
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	if value, portErr := net.LookupPort("tcp", port); portErr != nil || value < 1 || value > 65535 {
		return "", "", fmt.Errorf("PostgreSQL port is invalid")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" {
		return "", "", fmt.Errorf("PostgreSQL DSN must name a database")
	}

	return value, net.JoinHostPort(host, port) + "/" + database, nil
}

// deliverDatabaseDestination loads one completed export into a local database.
func (s *Server) deliverDatabaseDestination(
	ctx context.Context,
	configuration integrationConfiguration,
	record ExportRecord,
	path string,
) error {
	normalized, _, err := validateDatabaseDestination(configuration)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, databaseDeliveryTimeout)
	defer cancel()
	switch normalized.Driver {
	case databaseDriverSQLite:
		return s.loadExportIntoSQLite(ctx, normalized, record, path)
	case databaseDriverPostgres:
		return loadExportIntoPostgres(ctx, normalized, record, path)
	default:
		return fmt.Errorf("unsupported database driver")
	}
}

// loadExportIntoSQLite appends a completed export into a local SQLite file.
// Only the two self-describing local formats are accepted: the portable SQLite
// export and the CSV export.
func (s *Server) loadExportIntoSQLite(
	ctx context.Context,
	configuration integrationConfiguration,
	record ExportRecord,
	path string,
) error {
	destination, err := safeDataPath(s.svc.dataFolder, configuration.Target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create SQLite destination directory: %w", err)
	}
	database, err := sql.Open("sqlite", destination)
	if err != nil {
		return fmt.Errorf("open SQLite destination: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=10000", "PRAGMA foreign_keys=ON"} {
		if _, err := database.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure SQLite destination: %w", err)
		}
	}
	switch record.Format {
	case "sqlite":
		return copySQLiteExportRows(ctx, database, configuration.Table, path)
	case "csv":
		return copyCSVExportRows(ctx, database, configuration.Table, path)
	default:
		return fmt.Errorf("SQLite destinations accept csv or sqlite exports; this export is %s", record.Format)
	}
}

// copySQLiteExportRows attaches the verified export file read-only and copies
// its businesses table into the destination table.
func copySQLiteExportRows(ctx context.Context, database *sql.DB, table, exportPath string) error {
	attach := "file:" + filepath.ToSlash(exportPath) + "?mode=ro"
	if _, err := database.ExecContext(ctx, "ATTACH DATABASE ? AS export_source", attach); err != nil {
		return fmt.Errorf("attach SQLite export: %w", err)
	}
	defer func() { _, _ = database.ExecContext(context.WithoutCancel(ctx), "DETACH DATABASE export_source") }()
	if _, err := database.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS main."+table+" AS SELECT * FROM export_source.businesses WHERE 0",
	); err != nil {
		return fmt.Errorf("create destination table: %w", err)
	}
	if _, err := database.ExecContext(ctx,
		"INSERT INTO main."+table+" SELECT * FROM export_source.businesses",
	); err != nil {
		return fmt.Errorf("load export rows into destination table: %w", err)
	}

	return nil
}

// copyCSVExportRows creates a text-typed destination table from the CSV header
// and inserts every row inside one transaction.
func copyCSVExportRows(ctx context.Context, database *sql.DB, table, exportPath string) error {
	file, err := os.Open(exportPath)
	if err != nil {
		return fmt.Errorf("open CSV export: %w", err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read CSV export header: %w", err)
	}
	columns := make([]string, 0, len(header))
	placeholders := make([]string, 0, len(header))
	for index, name := range header {
		identifier, identifierErr := validateDestinationTable(sanitizeDestinationColumn(name, index))
		if identifierErr != nil {
			return identifierErr
		}
		columns = append(columns, `"`+identifier+`"`)
		placeholders = append(placeholders, "?")
	}
	if _, err := database.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS main."+table+" ("+strings.Join(columns, " TEXT,")+" TEXT)",
	); err != nil {
		return fmt.Errorf("create destination table: %w", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start destination transaction: %w", err)
	}
	defer transaction.Rollback()
	statement, err := transaction.PrepareContext(ctx,
		"INSERT INTO main."+table+" ("+strings.Join(columns, ",")+") VALUES ("+strings.Join(placeholders, ",")+")",
	)
	if err != nil {
		return fmt.Errorf("prepare destination insert: %w", err)
	}
	defer statement.Close()
	var loaded int64
	for {
		row, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read CSV export row: %w", readErr)
		}
		if loaded++; loaded > maximumDestinationRows {
			return fmt.Errorf("export contains more than %d rows", maximumDestinationRows)
		}
		values := make([]any, len(columns))
		for index := range columns {
			if index < len(row) {
				values[index] = row[index]
			} else {
				values[index] = ""
			}
		}
		if _, execErr := statement.ExecContext(ctx, values...); execErr != nil {
			return fmt.Errorf("insert destination row: %w", execErr)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit destination transaction: %w", err)
	}

	return nil
}

// sanitizeDestinationColumn turns an exported heading into a safe identifier.
func sanitizeDestinationColumn(name string, index int) string {
	var builder strings.Builder
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	value := strings.Trim(builder.String(), "_")
	if value == "" || value[0] >= '0' && value[0] <= '9' {
		value = fmt.Sprintf("column_%d", index+1)
	}
	if len(value) > maximumDestinationIdentifier {
		value = value[:maximumDestinationIdentifier]
	}

	return value
}

// loadExportIntoPostgres executes the generated PostgreSQL insert transaction
// against a local server. The statements are the ones this workspace produced,
// so no operator-supplied SQL is ever executed.
func loadExportIntoPostgres(
	ctx context.Context,
	configuration integrationConfiguration,
	record ExportRecord,
	path string,
) error {
	if record.Format != "postgresql_sql" {
		return fmt.Errorf("PostgreSQL destinations accept postgresql_sql exports; this export is %s", record.Format)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("export artifact is unavailable")
	}
	if info.Size() > maximumPostgresScriptBytes {
		return fmt.Errorf("generated SQL is larger than the %d byte local load limit", maximumPostgresScriptBytes)
	}
	script, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read generated SQL: %w", err)
	}
	connection, err := pgx.Connect(ctx, configuration.DSN)
	if err != nil {
		return fmt.Errorf("connect to local PostgreSQL: %w", err)
	}
	defer connection.Close(context.WithoutCancel(ctx))
	if _, err := connection.Exec(ctx, string(script)); err != nil {
		return fmt.Errorf("load generated SQL into local PostgreSQL: %w", err)
	}

	return nil
}

// maximumPostgresScriptBytes bounds the generated script a destination will
// read into memory before executing it.
const maximumPostgresScriptBytes = int64(256 << 20)
