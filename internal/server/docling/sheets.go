package docling

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Docling's Markdown for a workbook is bare pipe tables: the sheet names are
// dropped. Its JSON keeps them (groups labelled "sheet"), so spreadsheets are
// converted via JSON and rendered with one heading per sheet — the same shape
// the built-in converter (mdconv) produces, which create_excel reads back.

func isSpreadsheet(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx", ".xls", ".ods":
		return true
	}
	return false
}

func (c *Client) convertSpreadsheet(ctx context.Context, filename string, data []byte) (string, error) {
	cr, err := c.convert(ctx, filename, data, "json")
	if err != nil {
		return "", err
	}
	md, err := renderWorkbook(cr.Document.JSONContent)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(md) == "" {
		return "", fmt.Errorf("docling: conversion produced no text")
	}
	return md, nil
}

// renderWorkbook renders each sheet group as "## Sheet i of n: name" followed
// by its tables (first row = header). Falls back to the plain flattening when
// the document has no sheet groups.
func renderWorkbook(raw json.RawMessage) (string, error) {
	var doc doclingDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("docling: parse json_content: %w", err)
	}
	var sheets []doclingNode
	for _, ch := range doc.Body.Children {
		if kind, idx, ok := parseRef(ch.Ref); ok && kind == "groups" && idx < len(doc.Groups) && doc.Groups[idx].Label == "sheet" {
			sheets = append(sheets, doc.Groups[idx])
		}
	}
	if len(sheets) == 0 {
		text, _, err := flattenDoclingJSON(raw)
		return text, err
	}
	var out []string
	for i, sh := range sheets {
		name := strings.TrimSpace(sh.Name)
		if name == "" {
			name = fmt.Sprintf("Sheet%d", i+1)
		}
		if len(sheets) > 1 {
			out = append(out, fmt.Sprintf("## Sheet %d of %d: %s", i+1, len(sheets), name))
		} else {
			out = append(out, "## "+name)
		}
		n := 0
		for _, ch := range sh.Children {
			if kind, idx, ok := parseRef(ch.Ref); ok && kind == "tables" && idx < len(doc.Tables) {
				if t := mdTable(doc.Tables[idx]); t != "" {
					out = append(out, t)
					n++
				}
			}
		}
		if n == 0 {
			out = append(out, "_(empty)_")
		}
	}
	return strings.Join(out, "\n\n"), nil
}

// mdTable renders a cell grid as a Markdown pipe table, first row as header.
func mdTable(t doclingTable) string {
	var rows [][]string
	for _, row := range t.Data.Grid {
		cells := make([]string, len(row))
		blank := true
		for i, c := range row {
			cells[i] = strings.NewReplacer("|", `\|`, "\n", " ").Replace(strings.TrimSpace(c.Text))
			blank = blank && cells[i] == ""
		}
		if !blank {
			rows = append(rows, cells)
		}
	}
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, r := range rows {
		sb.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			sb.WriteString("|" + strings.Repeat(" --- |", len(r)) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
