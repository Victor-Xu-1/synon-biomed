package server

import (
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxWebExcelRows  = 10_000
	maxWebExcelCols  = 1_000
	maxWebExcelCells = 100_000
)

type webExcelWorkbook struct {
	Sheets []webExcelSheet `json:"sheets"`
}

type webExcelSheet struct {
	Name   string          `json:"name"`
	Data   [][]any         `json:"data"`
	Merges []webExcelMerge `json:"merges,omitempty"`
}

type webExcelMerge struct {
	Start webExcelCellRef `json:"s"`
	End   webExcelCellRef `json:"e"`
}

type webExcelCellRef struct {
	Row int `json:"r"`
	Col int `json:"c"`
}

func convertWebSpreadsheetToJSON(filePath string) (webExcelWorkbook, error) {
	if strings.EqualFold(filepath.Ext(filePath), ".csv") {
		return convertWebCSVToJSON(filePath)
	}
	return convertWebXLSXToJSON(filePath)
}

func convertWebCSVToJSON(filePath string) (webExcelWorkbook, error) {
	raw, err := readWebFSFile(filePath, maxWebFSReadBytes)
	if err != nil {
		return webExcelWorkbook{}, err
	}
	return convertWebDelimitedToJSON(bytes.NewReader(raw), strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)), ',')
}

func convertWebDelimitedToJSON(source io.Reader, sheetName string, delimiter rune) (webExcelWorkbook, error) {
	reader := csv.NewReader(source)
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false
	data := make([][]any, 0)
	cells := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return webExcelWorkbook{}, fmt.Errorf("parse CSV: %w", err)
		}
		if len(data) >= maxWebExcelRows || len(record) > maxWebExcelCols {
			return webExcelWorkbook{}, errWebFSTooMany
		}
		cells += len(record)
		if cells > maxWebExcelCells {
			return webExcelWorkbook{}, errWebFSTooMany
		}
		row := make([]any, len(record))
		for index, value := range record {
			row[index] = value
		}
		data = append(data, row)
	}
	return webExcelWorkbook{Sheets: []webExcelSheet{{
		Name: strings.TrimSpace(sheetName),
		Data: data,
	}}}, nil
}

type webXLSXWorkbookXML struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		ID   string `xml:"id,attr"`
	} `xml:"sheets>sheet"`
}

type webXLSXRelationshipsXML struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

type webXLSXSharedStringsXML struct {
	Items []webXLSXTextItem `xml:"si"`
}

type webXLSXTextItem struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

type webXLSXSheetXML struct {
	Rows []struct {
		Number int `xml:"r,attr"`
		Cells  []struct {
			Reference string          `xml:"r,attr"`
			Type      string          `xml:"t,attr"`
			Value     string          `xml:"v"`
			Inline    webXLSXTextItem `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
	MergeCells []struct {
		Reference string `xml:"ref,attr"`
	} `xml:"mergeCells>mergeCell"`
}

type webXLSXSheetRef struct {
	Name string
	Part string
}

func webXLSXTextValue(item webXLSXTextItem) string {
	if item.Text != "" {
		return item.Text
	}
	var output strings.Builder
	for _, run := range item.Runs {
		output.WriteString(run.Text)
	}
	return output.String()
}

func convertWebXLSXToJSON(filePath string) (webExcelWorkbook, error) {
	archive, err := openWebOOXMLArchive(filePath)
	if err != nil {
		return webExcelWorkbook{}, err
	}
	defer archive.Close()
	return convertWebXLSXArchiveToJSON(archive)
}

func convertWebXLSXArchiveToJSON(archive *webOOXMLArchive) (webExcelWorkbook, error) {
	workbookRaw, err := archive.ReadPart("xl/workbook.xml", 8<<20)
	if err != nil {
		return webExcelWorkbook{}, err
	}
	relationshipRaw, err := archive.ReadPart("xl/_rels/workbook.xml.rels", 8<<20)
	if err != nil {
		return webExcelWorkbook{}, err
	}
	var workbook webXLSXWorkbookXML
	if err := xml.Unmarshal(workbookRaw, &workbook); err != nil {
		return webExcelWorkbook{}, fmt.Errorf("parse XLSX workbook: %w", err)
	}
	var relationships webXLSXRelationshipsXML
	if err := xml.Unmarshal(relationshipRaw, &relationships); err != nil {
		return webExcelWorkbook{}, fmt.Errorf("parse XLSX relationships: %w", err)
	}
	if len(workbook.Sheets) == 0 || len(workbook.Sheets) > 1_000 {
		return webExcelWorkbook{}, errWebFSTooMany
	}
	relationshipParts := make(map[string]string, len(relationships.Relationships))
	for _, relationship := range relationships.Relationships {
		part, err := normalizeWebXLSXPart(relationship.Target)
		if err != nil {
			return webExcelWorkbook{}, err
		}
		relationshipParts[relationship.ID] = part
	}
	sharedStrings := []string{}
	if _, found := archive.parts["xl/sharedStrings.xml"]; found {
		sharedRaw, err := archive.ReadPart("xl/sharedStrings.xml", maxWebFSReadBytes)
		if err != nil {
			return webExcelWorkbook{}, err
		}
		var shared webXLSXSharedStringsXML
		if err := xml.Unmarshal(sharedRaw, &shared); err != nil {
			return webExcelWorkbook{}, fmt.Errorf("parse XLSX shared strings: %w", err)
		}
		if len(shared.Items) > maxWebExcelCells {
			return webExcelWorkbook{}, errWebFSTooMany
		}
		sharedStrings = make([]string, len(shared.Items))
		for index, item := range shared.Items {
			sharedStrings[index] = webXLSXTextValue(item)
		}
	}
	result := webExcelWorkbook{Sheets: make([]webExcelSheet, 0, len(workbook.Sheets))}
	totalCells := 0
	for _, source := range workbook.Sheets {
		part := relationshipParts[source.ID]
		if part == "" {
			return webExcelWorkbook{}, fmt.Errorf("XLSX worksheet relationship %q is missing", source.ID)
		}
		sheet, cellCount, err := parseWebXLSXSheet(archive, source.Name, part, sharedStrings)
		if err != nil {
			return webExcelWorkbook{}, err
		}
		totalCells += cellCount
		if totalCells > maxWebExcelCells {
			return webExcelWorkbook{}, errWebFSTooMany
		}
		result.Sheets = append(result.Sheets, sheet)
	}
	return result, nil
}

func normalizeWebXLSXPart(target string) (string, error) {
	target = strings.ReplaceAll(strings.TrimSpace(target), "\\", "/")
	if target == "" || strings.ContainsRune(target, 0) {
		return "", errors.New("XLSX relationship target is invalid")
	}
	if strings.HasPrefix(target, "/") {
		target = strings.TrimPrefix(target, "/")
	} else {
		target = path.Join("xl", target)
	}
	target = path.Clean(target)
	if target == "." || target == ".." || strings.HasPrefix(target, "../") ||
		!strings.HasPrefix(target, "xl/") {
		return "", errors.New("XLSX relationship escapes the workbook")
	}
	return target, nil
}

func parseWebExcelCellRef(value string) (row int, column int, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, errors.New("cell reference is empty")
	}
	index := 0
	for index < len(value) && value[index] >= 'A' && value[index] <= 'Z' {
		column = column*26 + int(value[index]-'A'+1)
		index++
	}
	if index == 0 || index == len(value) {
		return 0, 0, fmt.Errorf("cell reference %q is invalid", value)
	}
	row, err = strconv.Atoi(value[index:])
	if err != nil || row <= 0 || column <= 0 {
		return 0, 0, fmt.Errorf("cell reference %q is invalid", value)
	}
	return row - 1, column - 1, nil
}

func parseWebXLSXCellValue(cellType, value string, inline webXLSXTextItem, shared []string) (any, error) {
	switch cellType {
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || index < 0 || index >= len(shared) {
			return nil, errors.New("XLSX shared string index is invalid")
		}
		return shared[index], nil
	case "inlineStr":
		return webXLSXTextValue(inline), nil
	case "b":
		return strings.TrimSpace(value) == "1" || strings.EqualFold(strings.TrimSpace(value), "true"), nil
	case "str", "e":
		return value, nil
	default:
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil
		}
		number, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return number, nil
		}
		return value, nil
	}
}

func parseWebExcelMerge(value string) (webExcelMerge, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return webExcelMerge{}, fmt.Errorf("merge reference %q is invalid", value)
	}
	startRow, startCol, err := parseWebExcelCellRef(parts[0])
	if err != nil {
		return webExcelMerge{}, err
	}
	endRow, endCol, err := parseWebExcelCellRef(parts[1])
	if err != nil || endRow < startRow || endCol < startCol {
		return webExcelMerge{}, fmt.Errorf("merge reference %q is invalid", value)
	}
	return webExcelMerge{
		Start: webExcelCellRef{Row: startRow, Col: startCol},
		End:   webExcelCellRef{Row: endRow, Col: endCol},
	}, nil
}

func parseWebXLSXSheet(
	archive *webOOXMLArchive,
	name string,
	part string,
	shared []string,
) (webExcelSheet, int, error) {
	raw, err := archive.ReadPart(part, maxWebFSReadBytes)
	if err != nil {
		return webExcelSheet{}, 0, err
	}
	var source webXLSXSheetXML
	if err := xml.Unmarshal(raw, &source); err != nil {
		return webExcelSheet{}, 0, fmt.Errorf("parse XLSX worksheet %q: %w", name, err)
	}
	return buildWebXLSXSheet(name, source, shared)
}

type webXLSXLocatedValue struct {
	Row   int
	Col   int
	Value any
}

func buildWebXLSXSheet(
	name string,
	source webXLSXSheetXML,
	shared []string,
) (webExcelSheet, int, error) {
	if len(source.Rows) > maxWebExcelRows {
		return webExcelSheet{}, 0, errWebFSTooMany
	}
	values, rowWidths, maxRow, err := locateWebXLSXValues(source, shared)
	if err != nil {
		return webExcelSheet{}, 0, err
	}
	data := make([][]any, maxRow+1)
	for row, width := range rowWidths {
		data[row] = make([]any, width)
	}
	for _, value := range values {
		data[value.Row][value.Col] = value.Value
	}
	merges := make([]webExcelMerge, 0, len(source.MergeCells))
	if len(source.MergeCells) > maxWebExcelCells {
		return webExcelSheet{}, 0, errWebFSTooMany
	}
	for _, sourceMerge := range source.MergeCells {
		merge, err := parseWebExcelMerge(strings.ToUpper(sourceMerge.Reference))
		if err != nil {
			return webExcelSheet{}, 0, err
		}
		if merge.End.Row >= maxWebExcelRows || merge.End.Col >= maxWebExcelCols {
			return webExcelSheet{}, 0, errWebFSTooMany
		}
		merges = append(merges, merge)
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 {
		return webExcelSheet{}, 0, errors.New("XLSX worksheet name is invalid")
	}
	return webExcelSheet{Name: name, Data: data, Merges: merges}, len(values), nil
}

func locateWebXLSXValues(
	source webXLSXSheetXML,
	shared []string,
) ([]webXLSXLocatedValue, map[int]int, int, error) {
	values := make([]webXLSXLocatedValue, 0)
	seen := make(map[[2]int]struct{})
	rowWidths := make(map[int]int)
	maxRow := -1
	for sourceRowIndex, row := range source.Rows {
		rowIndex := sourceRowIndex
		if row.Number > 0 {
			rowIndex = row.Number - 1
		}
		if rowIndex < 0 || rowIndex >= maxWebExcelRows {
			return nil, nil, 0, errWebFSTooMany
		}
		nextColumn := 0
		for _, cell := range row.Cells {
			cellRow, column := rowIndex, nextColumn
			if strings.TrimSpace(cell.Reference) != "" {
				var err error
				cellRow, column, err = parseWebExcelCellRef(strings.ToUpper(cell.Reference))
				if err != nil {
					return nil, nil, 0, err
				}
			}
			if cellRow < 0 || cellRow >= maxWebExcelRows ||
				column < 0 || column >= maxWebExcelCols {
				return nil, nil, 0, errWebFSTooMany
			}
			value, err := parseWebXLSXCellValue(cell.Type, cell.Value, cell.Inline, shared)
			if err != nil {
				return nil, nil, 0, err
			}
			key := [2]int{cellRow, column}
			if _, duplicate := seen[key]; duplicate {
				return nil, nil, 0, errors.New("XLSX worksheet contains duplicate cells")
			}
			seen[key] = struct{}{}
			values = append(values, webXLSXLocatedValue{Row: cellRow, Col: column, Value: value})
			if len(values) > maxWebExcelCells {
				return nil, nil, 0, errWebFSTooMany
			}
			if column+1 > rowWidths[cellRow] {
				rowWidths[cellRow] = column + 1
			}
			if cellRow > maxRow {
				maxRow = cellRow
			}
			nextColumn = column + 1
		}
	}
	return values, rowWidths, maxRow, nil
}
