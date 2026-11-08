package main

import (
	"slices"
	"testing"

	"github.com/crgimenes/filo-term-go/screen"
)

func TestComplete(t *testing.T) {
	d := &tableData{header: []string{"id", "created_at", "customer_id", "Status", "order"}}
	for _, c := range []struct {
		line, want string
		shown      int
	}{
		{"where cr", "where created_at ", 0},
		{"where c", "where c", 2},
		{"where cu", "where cu", 2}, // customer_id, current_date
		{"where cust", "where customer_id ", 0},
		{"where st", `where "Status" `, 0},
		{"where ord", `where "order" `, 0},
		{"where id = 1 an", "where id = 1 an", 2}, // and, any
		{"where id = 1 and", "where id = 1 and ", 0},
		{"where name LI", "where name LIKE ", 0},
		{"where id bet", "where id between ", 0},
		{"where id i", "where id i", 3}, // id, in, ilike, is ... share only "i"
		{"where xyz", "where xyz", 0},
	} {
		got, _, shown := complete(c.line, d.completions(c.line))
		if got != c.want || (c.shown > 0 && len(shown) < c.shown) || (c.shown == 0 && shown != nil) {
			t.Errorf("%q: got %q %v, want %q", c.line, got, shown, c.want)
		}
	}
}

func TestCompletions(t *testing.T) {
	d := &tableData{header: []string{"id"}}
	c := d.completions("where i")
	if !slices.Equal(c.names, []string{"id"}) || c.words == nil {
		t.Fatalf("%+v", c)
	}
	got, _, _ := complete("wh", d.completions("wh"))
	if got != "where " {
		t.Fatalf("%q", got)
	}
	c = d.completions("export x")
	if c.names != nil || c.words != nil {
		t.Fatalf("%+v", c)
	}
}

func TestRecallLikeAShell(t *testing.T) {
	a := &app{views: []view{&tableData{}}}
	a.ask(":", "", func(*app, string) {})
	a.input.Set("where")
	a.handle(screen.KeyUp)
	if a.input.String() != "where" {
		t.Fatalf("no history: %q", a.input.String())
	}
	a.remember("where id = 1")
	a.ask(":", "", func(*app, string) {})
	a.input.Set("where x")
	a.handle(screen.KeyUp)
	if a.input.String() != "where id = 1" {
		t.Fatalf("up: %q", a.input.String())
	}
	a.handle(screen.KeyUp)
	a.handle(screen.KeyDown)
	if a.input.String() != "where x" {
		t.Fatalf("down to the draft: %q", a.input.String())
	}
}

func TestTabMenu(t *testing.T) {
	a := &app{views: []view{&tableData{header: []string{"customer_id", "id"}}}}
	d := a.views[0].(*tableData)
	ran := ""
	a.ask(":", "", func(_ *app, line string) { ran = line })
	a.onTab = d.completions
	a.handle(screen.KeyTab) // empty line: every command, where among them
	if a.menu == nil || !slices.Contains(a.menu.items, "where") {
		t.Fatalf("menu %+v", a.menu)
	}
	a.handle(screen.KeyTab)
	if a.input.String() != "where" {
		t.Fatalf("first item: %q", a.input.String())
	}
	a.handle(screen.KeyBackTab)
	if a.input.String() != "q" {
		t.Fatalf("back wraps to the last: %q", a.input.String())
	}
	a.handle(screen.KeyTab)
	a.handle(screen.KeyEnter) // takes the word, runs nothing
	if a.input.String() != "where " || ran != "" || a.menu != nil {
		t.Fatalf("%q ran %q", a.input.String(), ran)
	}
	a.input.Set("where cu")
	a.handle(screen.KeyTab)
	a.handle(screen.KeyTab)
	a.handle(screen.KeyEsc) // back to what was typed, line still open
	if a.input.String() != "where cu" || !a.asking() {
		t.Fatalf("esc: %q", a.input.String())
	}
	a.handle(screen.KeyTab)
	a.handle(screen.KeyTab)
	a.handle(screen.Key(' ')) // typing keeps the chosen one
	a.handle(screen.KeyEnter)
	if ran != "where customer_id " {
		t.Fatalf("ran %q", ran)
	}
}

func TestTabMenuShowsTheColumn(t *testing.T) {
	d := &tableData{header: []string{"id", "customer_id", "current_x"}}
	a := &app{views: []view{d}}
	a.ask(":", "", d.command) // what ':' on the grid opens
	a.onTab, a.onPick = d.completions, d.pick
	a.input.Set("where cu")
	a.handle(screen.KeyTab)
	a.handle(screen.KeyTab)
	if d.col != 1 {
		t.Fatalf("col %d", d.col)
	}
	a.handle(screen.KeyTab)
	if d.col != 2 {
		t.Fatalf("col %d", d.col)
	}
	a.handle(screen.KeyTab) // current_date is no column: the cursor stays
	if d.col != 2 {
		t.Fatalf("col %d", d.col)
	}
	a.handle(screen.KeyEsc)
	if d.col != 0 {
		t.Fatalf("esc: col %d", d.col)
	}
}

func TestToggleOrder(t *testing.T) {
	o := toggleOrder(nil, "a", "ASC")
	o = toggleOrder(o, "b", "DESC")
	o = toggleOrder(o, "a", "DESC") // O on an o column: descending, same place
	if !slices.Equal(o, []orderBy{{"a", "DESC"}, {"b", "DESC"}}) {
		t.Fatalf("%v", o)
	}
	o = toggleOrder(o, "a", "DESC") // O again: gone, b stays
	if !slices.Equal(o, []orderBy{{"b", "DESC"}}) {
		t.Fatalf("%v", o)
	}
	o = toggleOrder(o, "c", "ASC")
	o = toggleOrder(o, "c", "ASC")
	if !slices.Equal(o, []orderBy{{"b", "DESC"}}) {
		t.Fatalf("%v", o)
	}
}
