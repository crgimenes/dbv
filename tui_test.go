package main

import (
	"strings"
	"testing"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"
)

func TestParameters(t *testing.T) {
	sql := "SELECT * FROM t WHERE b = {{b::Second:2}} AND a = {{a:int:First:1}} OR a = {{a}}"
	params := ExtractParameters(sql)
	if len(params) != 2 || params[0].Name != "a" || params[0].Title != "First" || params[1].Name != "b" {
		t.Fatalf("%+v", params)
	}
	got := fillParameters(sql, params, []string{"7", "$1 x"})
	want := "SELECT * FROM t WHERE b = $1 x AND a = 7 OR a = 7"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func gridOf(rows int) *tableData {
	d := &tableData{name: "t", pk: "id", header: []string{"id", "name", "note"}}
	for i := range rows {
		d.add([]any{strings.Repeat("9", 1+i%3), "ação 日本", "two\nlines"})
	}
	d.total = rows
	d.measure()
	return d
}

func TestGridDraw(t *testing.T) {
	d := gridOf(3)
	c := screen.New(40, 6)
	d.draw(nil, c, 0, 0, 6, 40)
	lines := strings.Split(c.String(), "\n")
	if lines[0] != "id   name       note" || !strings.HasPrefix(lines[1], "────") {
		t.Fatalf("header:\n%s", c)
	}
	if lines[2] != "  9  ação 日本  two…" || lines[4] != "999  ação 日本  two…" {
		t.Fatalf("rows (numbers right, multi-line cut):\n%s", c)
	}
	if c.At(2, 2).Pen != kit.Focus {
		t.Fatal("the cell under the cursor is orange")
	}
}

func TestGridFollowsCursor(t *testing.T) {
	d := gridOf(50)
	d.row, d.col = 30, 2
	c := screen.New(12, 6)
	d.draw(nil, c, 0, 0, 6, 12)
	if d.first > 30 || d.first+4 <= 30 {
		t.Fatalf("row 30 off screen: first %d", d.first)
	}
	if d.off == 0 {
		t.Fatal("column 2 needs the grid scrolled right in 12 cells")
	}
	if !strings.Contains(c.String(), "two…") {
		t.Fatalf("selected column not shown:\n%s", c)
	}
}
