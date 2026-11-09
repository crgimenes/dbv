package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/crgimenes/filo-term-go/screen"
)

func TestListTab(t *testing.T) {
	l := &tableList{all: []TableInfo{{Name: "orders"}, {Name: "order_items"}, {Name: "users"}}}
	l.shown = l.all
	a := &app{views: []view{l}}
	a.ask("/", "", func(a *app, line string) { l.apply(a, line) })
	a.onEdit = func(a *app, line string) { l.apply(a, line) }
	a.onTab, a.onPick = tableNames(l.all), func(name string) { l.table.Cursor = indexOfTable(l.shown, name, l.table.Cursor) }
	a.input.Set("ord")
	a.handle(screen.KeyTab) // common part, no space: the filter follows
	if a.input.String() != "order" || len(l.shown) != 2 {
		t.Fatalf("%q shown %d", a.input.String(), len(l.shown))
	}
	a.handle(screen.KeyTab)
	a.handle(screen.KeyTab) // order_items
	if a.input.String() != "order_items" || l.shown[l.table.Cursor].Name != "order_items" {
		t.Fatalf("%q cursor on %v", a.input.String(), l.shown)
	}
	got, _, _ := complete("rel", commandCompletion("rel", listCommands))
	if got != "reload " {
		t.Fatalf("%q", got)
	}
}

func TestQuerySortHere(t *testing.T) {
	d := &tableData{query: true, header: []string{"n", "s"}}
	for _, r := range [][]any{{int64(10), "b"}, {nil, "a"}, {int64(9), "c"}, {int64(10), "a"}} {
		d.add(r)
	}
	d.raw = [][]any{{int64(10), "b"}, {nil, "a"}, {int64(9), "c"}, {int64(10), "a"}}
	col := func(i int) []string {
		var out []string
		for _, r := range d.rows {
			out = append(out, r[i])
		}
		return out
	}
	a := &app{views: []view{d}}
	d.orderBy(a, "ASC") // n up: numbers as numbers, NULL last
	if !slices.Equal(col(0), []string{"9", "10", "10", "NULL"}) {
		t.Fatalf("%v", col(0))
	}
	d.col = 1
	d.orderBy(a, "DESC") // then s down
	if !slices.Equal(col(1), []string{"c", "b", "a", "a"}) || d.raw[0][1] != "c" {
		t.Fatalf("%v %v", col(1), d.raw)
	}
	d.col = 0
	d.orderBy(a, "DESC") // n flips: NULL first
	if col(0)[0] != "NULL" {
		t.Fatalf("%v", col(0))
	}
	d.command(a, "order") // back to how they came
	if !slices.Equal(col(1), []string{"b", "a", "c", "a"}) {
		t.Fatalf("%v", col(1))
	}
}

func TestQuerySortExact(t *testing.T) {
	d := &tableData{query: true, header: []string{"n"}}
	for _, v := range []string{"9007199254740993", "9007199254740992", "10", "2"} {
		d.add([]any{v})
	}
	a := &app{views: []view{d}}
	d.orderBy(a, "ASC")
	got := []string{d.rows[0][0], d.rows[1][0], d.rows[2][0], d.rows[3][0]}
	if !slices.Equal(got, []string{"2", "10", "9007199254740992", "9007199254740993"}) {
		t.Fatalf("bigints past 2^53: %v", got)
	}
	m := &tableData{query: true, header: []string{"s"}}
	for _, v := range []string{"2", "10", "1a"} {
		m.add([]any{v})
	}
	m.orderBy(&app{views: []view{m}}, "ASC") // one non-number: all text, a true order
	if got := []string{m.rows[0][0], m.rows[1][0], m.rows[2][0]}; !slices.Equal(got, []string{"10", "1a", "2"}) {
		t.Fatalf("%v", got)
	}
}

func TestQuerySortHuge(t *testing.T) {
	d := &tableData{query: true, header: []string{"n"}}
	huge := "1" + strings.Repeat("0", 400)
	for _, v := range []string{huge, "10", "2"} {
		d.add([]any{v})
	}
	d.orderBy(&app{views: []view{d}}, "ASC")
	if d.rows[0][0] != "2" || d.rows[2][0] != huge {
		t.Fatalf("%v", d.rows)
	}
}

func TestQuerySortSpecials(t *testing.T) {
	d := &tableData{query: true, header: []string{"v"}}
	for _, v := range []string{"NaN", "10", "Infinity", "2", "-Infinity"} {
		d.add([]any{v})
	}
	d.orderBy(&app{views: []view{d}}, "ASC")
	var got []string
	for _, r := range d.rows {
		got = append(got, r[0])
	}
	if !slices.Equal(got, []string{"-Infinity", "2", "10", "Infinity", "NaN"}) {
		t.Fatalf("%v", got)
	}
}
