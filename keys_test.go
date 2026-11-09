package main

import (
	"strings"
	"testing"

	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

func TestRowJSONTypes(t *testing.T) {
	cols := []db.ColumnInfo{
		{ColumnName: "id", DataType: "bigint"}, {ColumnName: "code", DataType: "text"},
		{ColumnName: "f", DataType: "float8"}, {ColumnName: "ok", DataType: "bool"},
		{ColumnName: "j", DataType: "jsonb"}, {ColumnName: "x", DataType: "int4"},
		{ColumnName: "x", DataType: "int4"}, {ColumnName: "n", DataType: "numeric"},
	}
	got, err := rowJSON(cols, []any{"9007199254740993", "00123", "NaN", "t", `{"a":1}`, "1", "2", nil})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id": 9007199254740993`, `"code": "00123"`, `"f": "NaN"`, `"ok": true`, `"a": 1`, `"x": 1`, `"x": 2`, `"n": null`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Index(got, `"id"`) > strings.Index(got, `"code"`) {
		t.Error("columns out of order")
	}
}

func TestGridSearch(t *testing.T) {
	d := gridOf(3)
	d.rows = [][]string{{"1", "ana", "x"}, {"2", "bia", "y"}, {"3", "ana", "z"}}
	d.nulls = [][]bool{{false, false, false}, {false, false, false}, {false, false, false}}
	a := &app{}
	d.search = "ana"
	d.find(a, 1, false)
	if d.row != 0 || d.col != 1 {
		t.Fatalf("first: %d,%d", d.row, d.col)
	}
	d.find(a, 1, true)
	if d.row != 2 || d.col != 1 {
		t.Fatalf("n: %d,%d", d.row, d.col)
	}
	d.find(a, 1, true)
	if d.row != 0 || !strings.Contains(a.note, "wrapped") {
		t.Fatalf("wrap: %d %q", d.row, a.note)
	}
	d.find(a, -1, true)
	if d.row != 2 {
		t.Fatalf("N: %d", d.row)
	}
	d.hidden = map[string]bool{d.header[1]: true}
	d.row, d.col = 0, 0
	d.find(a, 1, true)
	if !strings.Contains(a.note, "not in the 3 rows") {
		t.Fatalf("hidden column searched: %d,%d %q", d.row, d.col, a.note)
	}
}

func TestZeroColumns(t *testing.T) {
	d := &tableData{query: true, readOnly: true, rows: [][]string{{}}, nulls: [][]bool{{}}}
	a := &app{}
	for _, k := range []screen.Key{'o', 'x', 'y', 'p', 'e', screen.KeyRight, 'G', '/', 'n'} {
		d.key(a, k) // no panic, no terminal reached
	}
	if d.cell() != "" {
		t.Fatal("a cell with no column")
	}
}

func TestPageKeys(t *testing.T) {
	if vimKey(screen.Ctrl('f')) != screen.KeyPgDn || vimKey(screen.Ctrl('b')) != screen.KeyPgUp {
		t.Fatal("Ctrl-F Ctrl-B")
	}
}
