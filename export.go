package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"

	"github.com/crgimenes/dbv/db"
)

const exportMax = 1_000_000

type columnJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func columnsJSON(cols []db.ColumnInfo) []columnJSON {
	out := make([]columnJSON, 0, len(cols))
	for _, c := range cols {
		out = append(out, columnJSON{Name: c.ColumnName, Type: c.DataType})
	}
	return out
}

// resultJSON is the one JSON shape of rows, the CLI's and :export's:
// {"columns":[{"name","type"}],"rows":[[...]],"truncated":bool}, json and
// jsonb values nested instead of quoted.
func resultJSON(res db.Result) any {
	rows := make([][]any, len(res.Rows))
	for j, r := range res.Rows {
		row := make([]any, len(r))
		for i, v := range r {
			row[i] = v
			s, ok := v.(string)
			t := res.Columns[i].DataType
			if ok && (t == "json" || t == "jsonb") && json.Valid([]byte(s)) {
				row[i] = json.RawMessage(s)
			}
		}
		rows[j] = row
	}
	return struct {
		Columns   []columnJSON `json:"columns"`
		Rows      [][]any      `json:"rows"`
		Truncated bool         `json:"truncated"`
	}{columnsJSON(res.Columns), rows, res.Truncated}
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// writeCSV writes a header line and the rows, null standing for NULL.
func writeCSV(w io.Writer, comma rune, res db.Result, null string) error {
	cw := csv.NewWriter(w)
	cw.Comma = comma
	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = c.ColumnName
	}
	err := cw.Write(header)
	if err != nil {
		return err
	}
	line := make([]string, len(res.Columns))
	for _, r := range res.Rows {
		for i, v := range r {
			line[i] = null
			if v != nil {
				line[i] = fmt.Sprint(v)
			}
		}
		err = cw.Write(line)
		if err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// export is :export FILE. A table exports every row its filter and order
// select, not only the pages read; a query's answer exports what it holds.
// FILE ends in .csv or .json, and is never overwritten.
func (d *tableData) export(a *app, path string) {
	path = strings.TrimSpace(path)
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".csv" && ext != ".json" {
		a.say(kit.Err, "export to FILE.csv or FILE.json")
		return
	}
	full, err := expandHome(path)
	if err != nil {
		a.fail(err)
		return
	}

	res := db.Result{Columns: d.columns, Truncated: d.truncated}
	switch {
	case d.raw != nil:
		res.Rows = d.raw
	case d.query:
		for i := range d.rows {
			res.Rows = append(res.Rows, d.rowAt(i))
		}
	default:
		err = a.busy("reading "+d.name, func(ctx context.Context) error {
			q, err := d.selectSQL(ctx, a.pg, "*")
			if err != nil {
				return err
			}
			res, err = a.pg.QueryReadOnly(ctx, exportMax, q)
			return err
		})
		if err != nil {
			a.fail(err)
			return
		}
	}

	f, err := os.OpenFile(filepath.Clean(full), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		a.fail(err) // an existing file says "file exists": nothing is overwritten
		return
	}
	if ext == ".csv" {
		err = writeCSV(f, ',', res, "")
	} else {
		err = encodeJSON(f, resultJSON(res))
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		a.fail(fmt.Errorf("%s: %w", path, err))
		return
	}
	if res.Truncated {
		a.say(kit.Warn, "%d rows written to %s; stopped at the cap", len(res.Rows), path)
		return
	}
	a.say(kit.OK, "%d rows written to %s", len(res.Rows), path)
}

// selectSQL is the grid's query: its filter and its order, the key
// breaking ties, cols chosen by the caller.
func (d *tableData) selectSQL(ctx context.Context, pg *db.Postgres, cols string) (string, error) {
	rel, err := pg.Resolve(ctx, d.name)
	if err != nil {
		return "", err
	}
	pk, err := pg.GetPrimaryKeyColumns(ctx, rel.SQL)
	if err != nil {
		return "", err
	}
	keys := []string{}
	if len(d.order) > 0 {
		keys = append(keys, d.orderSQL())
	}
	for _, c := range pk {
		ordered := slices.ContainsFunc(d.order, func(o orderBy) bool { return o.col == c })
		if !ordered { // a key already ordered needs no tie-break
			keys = append(keys, db.QuoteName(c))
		}
	}
	q := "SELECT " + cols + "\nFROM " + rel.From()
	if d.where != "" {
		q += "\nWHERE " + d.where
	}
	if len(keys) > 0 {
		q += "\nORDER BY " + strings.Join(keys, ", ")
	}
	return q, nil
}

func expandHome(p string) (string, error) {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}
