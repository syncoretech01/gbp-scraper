package web

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// parquetRowGroupSize bounds how many rows are buffered before a row group is
// flushed. Parquet is columnar, so a writer must hold a group in memory; a
// bounded group keeps a million-row export within a predictable footprint.
const parquetRowGroupSize = 20_000

// parquetExportWriter writes the selected columns as a typed Parquet file.
// Column types follow the same catalogue the other formats use, so a value is
// a number in Parquet exactly when it is a number in the SQLite export.
type parquetExportWriter struct {
	destination string
	temporary   string
	columns     []ExportColumnSelection
	options     ExportBuildOptions
	file        *os.File
	writer      *parquet.GenericWriter[map[string]any]
	buffer      []map[string]any
	closed      bool
}

func newParquetExportWriter(
	destination string,
	columns []ExportColumnSelection,
	options ExportBuildOptions,
) (*parquetExportWriter, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("a Parquet export needs at least one column")
	}
	schema, err := parquetSchemaFor(columns)
	if err != nil {
		return nil, err
	}
	temporary := destination + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create Parquet export: %w", err)
	}

	return &parquetExportWriter{
		destination: destination,
		temporary:   temporary,
		columns:     columns,
		options:     options,
		file:        file,
		writer: parquet.NewGenericWriter[map[string]any](file,
			schema,
			parquet.Compression(&zstd.Codec{}),
			parquet.CreatedBy("google-maps-scraper", "local", "parquet-v1"),
		),
		buffer: make([]map[string]any, 0, parquetRowGroupSize),
	}, nil
}

// parquetSchemaFor builds the file schema from the selected columns. Every
// column is optional so a missing local value is a real Parquet null rather
// than an empty string that a downstream query would treat as present.
func parquetSchemaFor(columns []ExportColumnSelection) (*parquet.Schema, error) {
	group := make(parquet.Group, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		definition, ok := exportColumnDefinitionFor(column.Key)
		if !ok {
			return nil, fmt.Errorf("unknown export column %q", column.Key)
		}
		name := column.Label
		if name == "" {
			name = column.Key
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("Parquet columns must have unique headings; %q is repeated", name)
		}
		seen[name] = struct{}{}
		group[name] = parquet.Optional(parquetNodeFor(definition.DataType))
	}

	return parquet.NewSchema("business", group), nil
}

// parquetNodeFor maps a local column data type onto a Parquet leaf. Datetime
// is stored as a UTC millisecond timestamp, which every Parquet reader
// understands, and JSON columns keep their encoded text under the JSON
// logical type so a reader knows the string is structured.
func parquetNodeFor(dataType string) parquet.Node {
	switch dataType {
	case "boolean":
		return parquet.Leaf(parquet.BooleanType)
	case "integer":
		return parquet.Int(64)
	case "number":
		return parquet.Leaf(parquet.DoubleType)
	case "datetime":
		return parquet.Timestamp(parquet.Millisecond)
	case "json":
		return parquet.JSON()
	default:
		return parquet.String()
	}
}

func (writer *parquetExportWriter) Add(row exportDataRow) error {
	record := make(map[string]any, len(writer.columns))
	for _, column := range writer.columns {
		definition, ok := exportColumnDefinitionFor(column.Key)
		if !ok {
			return fmt.Errorf("unknown export column %q", column.Key)
		}
		value, err := exportColumnValue(row, column.Key)
		if err != nil {
			return err
		}
		name := column.Label
		if name == "" {
			name = column.Key
		}
		record[name] = parquetColumnValue(definition.DataType, value)
	}
	writer.buffer = append(writer.buffer, record)
	if len(writer.buffer) < parquetRowGroupSize {
		return nil
	}

	return writer.flush()
}

// parquetColumnValue converts one exported value into the Go type the schema
// leaf expects, returning nil for an absent value so the column stays null.
func parquetColumnValue(dataType string, value any) any {
	if value == nil {
		return nil
	}
	switch dataType {
	case "boolean":
		if typed, ok := value.(bool); ok {
			return typed
		}
	case "integer":
		switch typed := value.(type) {
		case int:
			return int64(typed)
		case int32:
			return int64(typed)
		case int64:
			return typed
		case float64:
			return int64(typed)
		}
	case "number":
		switch typed := value.(type) {
		case float64:
			return typed
		case float32:
			return float64(typed)
		case int64:
			return float64(typed)
		case int:
			return float64(typed)
		}
	case "datetime":
		switch typed := value.(type) {
		case time.Time:
			if typed.IsZero() {
				return nil
			}

			return typed.UTC()
		case string:
			parsed, err := time.Parse(time.RFC3339, typed)
			if err != nil {
				return nil
			}

			return parsed.UTC()
		}
	}
	text := exportValueString(value)
	if text == "" {
		return nil
	}

	return text
}

// flush writes the buffered rows as one row group.
func (writer *parquetExportWriter) flush() error {
	if len(writer.buffer) == 0 {
		return nil
	}
	if _, err := writer.writer.Write(writer.buffer); err != nil {
		return fmt.Errorf("write Parquet row group: %w", err)
	}
	writer.buffer = writer.buffer[:0]

	return nil
}

func (writer *parquetExportWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	err := writer.flush()
	if closeErr := writer.writer.Close(); err == nil {
		err = closeErr
	}
	if syncErr := writer.file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := writer.file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(writer.temporary)

		return fmt.Errorf("finish Parquet export: %w", err)
	}
	if err := os.Rename(writer.temporary, writer.destination); err != nil {
		_ = os.Remove(writer.temporary)

		return fmt.Errorf("publish Parquet export: %w", err)
	}

	return nil
}

func (writer *parquetExportWriter) Abort() {
	if writer.closed {
		return
	}
	writer.closed = true
	_ = writer.writer.Close()
	_ = writer.file.Close()
	_ = os.Remove(writer.temporary)
}

// verifyParquetExport re-opens the generated file with the Parquet reader and
// checks that the footer, schema, and row count are all readable. A file that
// cannot be read back is never registered as a completed export.
func verifyParquetExport(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("generated Parquet export is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	parquetFile, err := parquet.OpenFile(file, info.Size())
	if err != nil {
		return fmt.Errorf("read Parquet footer: %w", err)
	}
	if len(parquetFile.Schema().Fields()) == 0 {
		return errors.New("Parquet export declares no columns")
	}
	if parquetFile.NumRows() < 0 {
		return errors.New("Parquet export declares a negative row count")
	}

	return nil
}
