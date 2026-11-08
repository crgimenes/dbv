package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/filo-term-go/screen"
)

func TestHiddenColumns(t *testing.T) {
	d := &tableData{header: []string{"id", "Name", "b", "c"}, widths: []int{2, 4, 1, 1}}
	a := &app{views: []view{d}}
	d.col = 1
	d.hide(a)
	if d.col != 2 || d.shown(1) {
		t.Fatalf("hide moves on: col %d", d.col)
	}
	d.col = d.next(d.col, -1)
	if d.col != 0 {
		t.Fatalf("left skips the hidden: col %d", d.col)
	}
	if got := d.selectList(); got != "id, b, c" {
		t.Fatalf("%q", got)
	}
	d.col = 3
	d.hide(a)
	d.col = 2
	d.hide(a)
	if d.col != 0 || d.hiddenCount() != 3 {
		t.Fatalf("col %d hidden %d", d.col, d.hiddenCount())
	}
	d.hide(a) // the last column stays
	if d.hiddenCount() != 3 {
		t.Fatalf("hid the last one")
	}
	c := screen.New(40, 6)
	d.draw(a, c, 0, 2, 6, 36) // only id painted, no ›
	s := c.String()
	if !strings.Contains(s, "id") || strings.Contains(s, "Name") || strings.Contains(s, "›") {
		t.Fatalf("\n%s", s)
	}
	d.command(a, "show")
	if d.hiddenCount() != 0 || d.selectList() != "*" {
		t.Fatalf("show left %d", d.hiddenCount())
	}
}

func TestGridSelect(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_sel; CREATE TABLE dbv_sel (id int PRIMARY KEY, "Name" text, n int)`)
	d := &tableData{name: "dbv_sel", where: "n > 1", order: []orderBy{{"Name", "DESC"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q, err := d.selectSQL(ctx, pg, `"Name", n`)
	want := "SELECT \"Name\", n\nFROM dbv_sel\nWHERE n > 1\nORDER BY \"Name\" DESC, \"id\""
	if err != nil || q != want {
		t.Fatalf("%v\ngot  %q\nwant %q", err, q, want)
	}
	_, err = pg.QueryReadOnly(ctx, 10, q)
	if err != nil {
		t.Fatal(err)
	}
}
