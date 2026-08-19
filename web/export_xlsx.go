package web

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

type xlsxExportWriter struct {
	destination string
	temporary   string
	columns     []ExportColumnSelection
	file        *os.File
	archive     *zip.Writer
	sheet       io.Writer
	rowNumber   int64
	closed      bool
}

func newXLSXExportWriter(destination string, columns []ExportColumnSelection) (*xlsxExportWriter, error) {
	temporary := destination + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create XLSX export: %w", err)
	}
	writer := &xlsxExportWriter{
		destination: destination, temporary: temporary, columns: columns,
		file: file, archive: zip.NewWriter(file),
	}
	if err := writer.writePackageStart(); err != nil {
		writer.Abort()
		return nil, err
	}
	return writer, nil
}

func (writer *xlsxExportWriter) writePackageStart() error {
	parts := []struct {
		name string
		body string
	}{
		{name: "[Content_Types].xml", body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			`</Types>`},
		{name: "_rels/.rels", body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{name: "xl/workbook.xml", body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="Businesses" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{name: "xl/_rels/workbook.xml.rels", body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`</Relationships>`},
		{name: "xl/styles.xml", body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
			`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
			`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
			`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
			`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
			`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
			`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
	}
	for _, part := range parts {
		entry, err := writer.archive.Create(part.name)
		if err != nil {
			return fmt.Errorf("create XLSX part %s: %w", part.name, err)
		}
		if _, err := io.WriteString(entry, part.body); err != nil {
			return fmt.Errorf("write XLSX part %s: %w", part.name, err)
		}
	}
	sheet, err := writer.archive.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return fmt.Errorf("create XLSX worksheet: %w", err)
	}
	writer.sheet = sheet
	if _, err := io.WriteString(sheet, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`+
		`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`+
		`<sheetFormatPr defaultRowHeight="15"/><sheetData>`); err != nil {
		return fmt.Errorf("start XLSX worksheet: %w", err)
	}
	headings := make([]any, 0, len(writer.columns))
	for _, column := range writer.columns {
		headings = append(headings, column.Label)
	}
	writer.rowNumber = 1
	return writer.writeRow(headings, true)
}

func (writer *xlsxExportWriter) Add(row exportDataRow) error {
	if writer.closed {
		return fmt.Errorf("XLSX writer is closed")
	}
	if writer.rowNumber >= maximumXLSXDataRows+1 {
		return fmt.Errorf("XLSX worksheet exceeds its row limit")
	}
	values := make([]any, 0, len(writer.columns))
	for _, column := range writer.columns {
		value, err := exportColumnValue(row, column.Key)
		if err != nil {
			return err
		}
		values = append(values, value)
	}
	writer.rowNumber++
	return writer.writeRow(values, false)
}

func (writer *xlsxExportWriter) writeRow(values []any, header bool) error {
	if _, err := fmt.Fprintf(writer.sheet, `<row r="%d">`, writer.rowNumber); err != nil {
		return err
	}
	for index, value := range values {
		cell := xlsxColumnName(index+1) + strconv.FormatInt(writer.rowNumber, 10)
		style := ""
		if header {
			style = ` s="1"`
		}
		switch typed := value.(type) {
		case nil:
			if _, err := fmt.Fprintf(writer.sheet, `<c r="%s"%s/>`, cell, style); err != nil {
				return err
			}
		case bool:
			boolean := "0"
			if typed {
				boolean = "1"
			}
			if _, err := fmt.Fprintf(writer.sheet, `<c r="%s"%s t="b"><v>%s</v></c>`, cell, style, boolean); err != nil {
				return err
			}
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				if err := writeXLSXInlineString(writer.sheet, cell, style, exportValueString(typed)); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(writer.sheet, `<c r="%s"%s><v>%s</v></c>`, cell, style, strconv.FormatFloat(typed, 'g', -1, 64)); err != nil {
				return err
			}
		case int64:
			if _, err := fmt.Fprintf(writer.sheet, `<c r="%s"%s><v>%d</v></c>`, cell, style, typed); err != nil {
				return err
			}
		case int:
			if _, err := fmt.Fprintf(writer.sheet, `<c r="%s"%s><v>%d</v></c>`, cell, style, typed); err != nil {
				return err
			}
		default:
			if err := writeXLSXInlineString(writer.sheet, cell, style, exportValueString(value)); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(writer.sheet, `</row>`)
	return err
}

func writeXLSXInlineString(writer io.Writer, cell, style, value string) error {
	if _, err := fmt.Fprintf(writer, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">`, cell, style); err != nil {
		return err
	}
	if err := xml.EscapeText(writer, []byte(validXMLText(value))); err != nil {
		return err
	}
	_, err := io.WriteString(writer, `</t></is></c>`)
	return err
}

func validXMLText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\t' || character == '\n' || character == '\r' || character >= 0x20 {
			return character
		}
		return -1
	}, value)
}

func xlsxColumnName(column int) string {
	var result [8]byte
	position := len(result)
	for column > 0 {
		column--
		position--
		result[position] = byte('A' + column%26)
		column /= 26
	}
	return string(result[position:])
}

func (writer *xlsxExportWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	lastCell := xlsxColumnName(len(writer.columns)) + strconv.FormatInt(writer.rowNumber, 10)
	_, writeErr := fmt.Fprintf(writer.sheet, `</sheetData><autoFilter ref="A1:%s"/></worksheet>`, lastCell)
	if closeErr := writer.archive.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if syncErr := writer.file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := writer.file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(writer.temporary)
		return fmt.Errorf("write XLSX export: %w", writeErr)
	}
	if err := os.Rename(writer.temporary, writer.destination); err != nil {
		_ = os.Remove(writer.temporary)
		return fmt.Errorf("publish XLSX export: %w", err)
	}
	return nil
}

func (writer *xlsxExportWriter) Abort() {
	if writer == nil {
		return
	}
	if !writer.closed {
		writer.closed = true
		_ = writer.archive.Close()
		_ = writer.file.Close()
	}
	_ = os.Remove(writer.temporary)
}

func verifyXLSX(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	required := map[string]bool{
		"[Content_Types].xml":      false,
		"xl/workbook.xml":          false,
		"xl/worksheets/sheet1.xml": false,
		"xl/styles.xml":            false,
	}
	for _, file := range archive.File {
		if _, ok := required[file.Name]; !ok {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return err
		}
		decoder := xml.NewDecoder(reader)
		for {
			if _, err := decoder.Token(); err != nil {
				if err == io.EOF {
					break
				}
				_ = reader.Close()
				return fmt.Errorf("invalid XML in %s: %w", file.Name, err)
			}
		}
		if err := reader.Close(); err != nil {
			return err
		}
		required[file.Name] = true
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("XLSX package is missing %s", name)
		}
	}
	return nil
}
