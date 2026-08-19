package web

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type textExportWriter struct {
	destination string
	temporary   string
	format      string
	columns     []ExportColumnSelection
	options     ExportBuildOptions
	file        *os.File
	buffer      *bufio.Writer
	first       bool
	closed      bool
}

func newTextExportWriter(
	destination, format string,
	columns []ExportColumnSelection,
	options ExportBuildOptions,
) (*textExportWriter, error) {
	if _, ok := exportExtension(format); !ok || format == "xlsx" || format == "sqlite" {
		return nil, fmt.Errorf("unsupported text export format %q", format)
	}
	temporary := destination + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create export file: %w", err)
	}
	writer := &textExportWriter{
		destination: destination, temporary: temporary, format: format,
		columns: columns, options: options, file: file,
		buffer: bufio.NewWriterSize(file, 128*1024), first: true,
	}
	if err := writer.writeStart(); err != nil {
		writer.Abort()
		return nil, err
	}
	return writer, nil
}

func (writer *textExportWriter) writeStart() error {
	if writer.options.LegacyShape {
		return writeExportStart(writer.buffer, writer.format)
	}
	switch writer.format {
	case "csv":
		csvWriter := csv.NewWriter(writer.buffer)
		headings := make([]string, 0, len(writer.columns))
		for _, column := range writer.columns {
			headings = append(headings, column.Label)
		}
		if err := csvWriter.Write(headings); err != nil {
			return err
		}
		csvWriter.Flush()
		return csvWriter.Error()
	case "json":
		_, err := io.WriteString(writer.buffer, "[\n")
		return err
	case "geojson":
		_, err := io.WriteString(writer.buffer, "{\"type\":\"FeatureCollection\",\"features\":[\n")
		return err
	case "kml":
		return writeExportStart(writer.buffer, writer.format)
	case "postgresql_sql", "mysql_sql":
		transaction := "BEGIN;\n"
		if writer.format == "mysql_sql" {
			transaction = "START TRANSACTION;\n"
		}
		if _, err := io.WriteString(writer.buffer, transaction); err != nil {
			return err
		}
		definitions := make([]string, 0, len(writer.columns))
		for _, column := range writer.columns {
			definition, _ := exportColumnDefinitionFor(column.Key)
			definitions = append(definitions, exportSQLIdentifier(column.Label, writer.format)+" "+exportSQLType(definition.DataType, writer.format))
		}
		_, err := fmt.Fprintf(writer.buffer, "CREATE TABLE businesses (%s);\n", strings.Join(definitions, ","))
		return err
	default:
		return nil
	}
}

func (writer *textExportWriter) Add(row exportDataRow) error {
	if writer.closed {
		return fmt.Errorf("export writer is closed")
	}
	if writer.options.LegacyShape {
		if err := writeExportRow(writer.buffer, writer.format, row.Business, writer.first); err != nil {
			return err
		}
		writer.first = false
		return nil
	}
	values := make([]any, 0, len(writer.columns))
	for _, column := range writer.columns {
		value, err := exportColumnValue(row, column.Key)
		if err != nil {
			return err
		}
		values = append(values, value)
	}
	switch writer.format {
	case "csv":
		record := make([]string, 0, len(values))
		for _, value := range values {
			record = append(record, exportValueString(value))
		}
		csvWriter := csv.NewWriter(writer.buffer)
		if err := csvWriter.Write(record); err != nil {
			return err
		}
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			return err
		}
	case "json":
		if !writer.first {
			if _, err := io.WriteString(writer.buffer, ",\n"); err != nil {
				return err
			}
		}
		if err := writeOrderedExportObject(writer.buffer, writer.columns, values); err != nil {
			return err
		}
	case "jsonl":
		if err := writeOrderedExportObject(writer.buffer, writer.columns, values); err != nil {
			return err
		}
		if err := writer.buffer.WriteByte('\n'); err != nil {
			return err
		}
	case "geojson":
		if !writer.first {
			if _, err := io.WriteString(writer.buffer, ",\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(writer.buffer, "{\"type\":\"Feature\",\"geometry\":"); err != nil {
			return err
		}
		geometry := any(nil)
		if row.Business.Latitude != nil && row.Business.Longitude != nil {
			geometry = map[string]any{"type": "Point", "coordinates": []float64{*row.Business.Longitude, *row.Business.Latitude}}
		}
		if err := json.NewEncoder(writer.buffer).Encode(geometry); err != nil {
			return err
		}
		// Encoder adds a newline, which remains valid insignificant whitespace.
		if _, err := io.WriteString(writer.buffer, ",\"properties\":"); err != nil {
			return err
		}
		if err := writeOrderedExportObject(writer.buffer, writer.columns, values); err != nil {
			return err
		}
		if err := writer.buffer.WriteByte('}'); err != nil {
			return err
		}
	case "kml":
		if err := writeKMLBusiness(writer.buffer, row.Business); err != nil {
			return err
		}
	case "vcard":
		if err := writeExportRow(writer.buffer, writer.format, row.Business, writer.first); err != nil {
			return err
		}
	case "txt":
		for index, value := range values {
			if index > 0 {
				if err := writer.buffer.WriteByte('\t'); err != nil {
					return err
				}
			}
			replaced := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(exportValueString(value))
			if _, err := io.WriteString(writer.buffer, replaced); err != nil {
				return err
			}
		}
		if err := writer.buffer.WriteByte('\n'); err != nil {
			return err
		}
	case "postgresql_sql", "mysql_sql":
		identifiers := make([]string, 0, len(writer.columns))
		literals := make([]string, 0, len(values))
		for index, column := range writer.columns {
			identifiers = append(identifiers, exportSQLIdentifier(column.Label, writer.format))
			literals = append(literals, exportSQLLiteral(values[index], writer.format))
		}
		if _, err := fmt.Fprintf(writer.buffer, "INSERT INTO businesses (%s) VALUES (%s);\n",
			strings.Join(identifiers, ","), strings.Join(literals, ",")); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported export format %q", writer.format)
	}
	writer.first = false
	return nil
}

func (writer *textExportWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	var writeErr error
	if writer.options.LegacyShape {
		writeErr = writeExportEnd(writer.buffer, writer.format)
	} else {
		switch writer.format {
		case "json":
			_, writeErr = io.WriteString(writer.buffer, "\n]\n")
		case "geojson":
			_, writeErr = io.WriteString(writer.buffer, "\n]}\n")
		case "kml":
			writeErr = writeExportEnd(writer.buffer, writer.format)
		case "postgresql_sql", "mysql_sql":
			_, writeErr = io.WriteString(writer.buffer, "COMMIT;\n")
		}
	}
	if flushErr := writer.buffer.Flush(); writeErr == nil {
		writeErr = flushErr
	}
	if syncErr := writer.file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := writer.file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(writer.temporary)
		return fmt.Errorf("write export file: %w", writeErr)
	}
	if err := os.Rename(writer.temporary, writer.destination); err != nil {
		_ = os.Remove(writer.temporary)
		return fmt.Errorf("publish export file: %w", err)
	}
	return nil
}

func (writer *textExportWriter) Abort() {
	if writer == nil {
		return
	}
	if !writer.closed {
		writer.closed = true
		_ = writer.file.Close()
	}
	_ = os.Remove(writer.temporary)
}

func writeOrderedExportObject(writer io.Writer, columns []ExportColumnSelection, values []any) error {
	if _, err := io.WriteString(writer, "{"); err != nil {
		return err
	}
	for index, column := range columns {
		if index > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		label, err := json.Marshal(column.Label)
		if err != nil {
			return err
		}
		value, err := json.Marshal(values[index])
		if err != nil {
			return err
		}
		if _, err := writer.Write(label); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, ":"); err != nil {
			return err
		}
		if _, err := writer.Write(value); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "}")
	return err
}

func exportSQLIdentifier(label, format string) string {
	if format == "mysql_sql" {
		return "`" + strings.ReplaceAll(label, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(label, `"`, `""`) + `"`
}

func exportSQLType(dataType, format string) string {
	switch dataType {
	case "number":
		if format == "mysql_sql" {
			return "DOUBLE"
		}
		return "DOUBLE PRECISION"
	case "integer", "boolean":
		if dataType == "boolean" && format != "mysql_sql" {
			return "BOOLEAN"
		}
		return "BIGINT"
	default:
		return "TEXT"
	}
}

func exportSQLLiteral(value any, format string) string {
	if value == nil {
		return "NULL"
	}
	switch typed := value.(type) {
	case bool:
		if format == "mysql_sql" {
			if typed {
				return "1"
			}
			return "0"
		}
		if typed {
			return "TRUE"
		}
		return "FALSE"
	case float64:
		return exportValueString(typed)
	case int64:
		return exportValueString(typed)
	case int:
		return exportValueString(typed)
	default:
		return sqlExportValue(exportValueString(value))
	}
}
