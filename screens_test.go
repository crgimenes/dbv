package main

import (
	"slices"
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
