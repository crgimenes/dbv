package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/dbv/db"
)

func TestEdited(t *testing.T) {
	cases := []struct {
		old, raw, want string
		changed        bool
	}{
		{"hello", "hello\n", "hello", false},     // the editor's own newline
		{"hello\n", "hello\n", "hello\n", false}, // a value ending in newline, untouched
		{"hello\n", "hello", "hello\n", false},
		{"hello\n", "bye\n", "bye\n", true}, // keeps the value's own newline
		{"a", "b\n", "b", true},
	}
	for _, c := range cases {
		got, changed := edited(c.old, c.raw)
		if got != c.want || changed != c.changed {
			t.Errorf("edited(%q, %q) = %q, %v; want %q, %v", c.old, c.raw, got, changed, c.want, c.changed)
		}
	}
}

func TestParameterNamesAreWhole(t *testing.T) {
	sql := "WHERE a = {{id}} AND b = {{id2:int}}"
	params := ExtractParameters(sql)
	got := fillParameters(sql, params, []string{"1", "2"})
	if got != "WHERE a = 1 AND b = 2" {
		t.Fatalf("%q", got)
	}
}

func TestMaskQueryPassword(t *testing.T) {
	got, err := maskDBURL("postgres://u@host/db?password=secret&sslmode=disable")
	if err != nil || strings.Contains(got, "secret") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestWriteOutputRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the selector's guard is Unix-only (output_unix.go)")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "precious")
	err := os.WriteFile(target, []byte("keep"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "out")
	err = os.Symlink(target, link)
	if err != nil {
		t.Skip("no symlinks here")
	}
	if writeOutput(link, "x") == nil {
		t.Fatal("wrote through a symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Fatalf("target changed: %q", b)
	}
	plain := filepath.Join(dir, "plain")
	err = os.WriteFile(plain, []byte("old and longer"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if writeOutput(plain, "new") != nil {
		t.Fatal("refused a file of mine")
	}
	b, _ = os.ReadFile(plain)
	info, _ := os.Stat(plain)
	if string(b) != "new" || info.Mode().Perm() != 0o600 {
		t.Fatalf("%q %v", b, info.Mode())
	}
}

func TestNoConfigNeedsNoTerminal(t *testing.T) {
	if runTUI(nil, newTableList) == nil {
		t.Fatal("no connections must be an error, before the terminal is touched")
	}
}

// The tests below need DBV_TEST_URL (see TestQueryReadOnly).

func testDB(t *testing.T, extra string) *db.Postgres {
	t.Helper()
	url := os.Getenv("DBV_TEST_URL")
	if url == "" {
		t.Skip("DBV_TEST_URL not set")
	}
	pg, err := db.New(url + extra)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	return pg
}

func mustExec(t *testing.T, pg *db.Postgres, sql string) {
	t.Helper()
	err := pg.Exec(sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestReadOnlyHoldsWithSimpleProtocol(t *testing.T) {
	pg := testDB(t, "&default_query_exec_mode=simple_protocol")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_ro; CREATE TABLE dbv_ro (x int); INSERT INTO dbv_ro VALUES (1)`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pg.QueryReadOnly(ctx, 10, "COMMIT; DELETE FROM dbv_ro; COMMIT")
	if err == nil {
		t.Fatal("a multi-statement string ran")
	}
	var n int
	err = pg.Get(&n, "SELECT count(*) FROM dbv_ro")
	if err != nil || n != 1 {
		t.Fatalf("rows left %d, %v", n, err)
	}
}

func TestOddNamesAndKeys(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS "Order Items", dbv_child, dbv_parent CASCADE;
CREATE TABLE "Order Items" ("Price" numeric(10,3) PRIMARY KEY, "a""b" text, note text);
INSERT INTO "Order Items" VALUES (1.234, 'x', 'O''Brien'), (1.23, 'y', 'null');
CREATE TABLE dbv_parent (id int PRIMARY KEY, v text);
CREATE TABLE dbv_child () INHERITS (dbv_parent);
INSERT INTO dbv_parent VALUES (1, 'p'); INSERT INTO dbv_child VALUES (1, 'c');`)

	recs, cols, total, err := pg.ListRecords("Order Items", "", 0, 10, "", "")
	if err != nil || total != 2 || len(cols) != 3 {
		t.Fatalf("browse: %v total %d cols %d", err, total, len(cols))
	}
	if recs[0]["Price"] != "1.230" || recs[1]["Price"] != "1.234" {
		t.Fatalf("keys must keep their digits: %v %v", recs[0]["Price"], recs[1]["Price"])
	}

	err = pg.UpdateDataCell("Order Items", "note", "changed", []string{"Price"}, []any{"1.234"})
	if err != nil {
		t.Fatal(err)
	}
	var note string
	err = pg.Get(&note, `SELECT note FROM "Order Items" WHERE "Price" = 1.23`)
	if err != nil || note != "null" {
		t.Fatalf("the other row changed: %q %v", note, err)
	}

	stmt, err := pg.CreateUpdateStatement("Order Items", []string{"Price", "a\"b", "note"}, []any{"1.234", nil, "O'Brien"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stmt, ",\nWHERE") || !strings.Contains(stmt, `"a""b" = NULL`) || !strings.Contains(stmt, `E'O''Brien'`) {
		t.Fatalf("statement:\n%s", stmt)
	}
	mustExec(t, pg, stmt)

	stmt, err = pg.CreateUpdateStatement("Order Items", []string{"Price", "a\"b", "note"}, []any{"1.23", "y", "null"})
	if err != nil || !strings.Contains(stmt, `"note" = E'null'`) {
		t.Fatalf("text 'null' must stay text:\n%s %v", stmt, err)
	}

	err = pg.UpdateDataCell("dbv_parent", "v", "z", []string{"id"}, []any{"1"})
	if err != nil {
		t.Fatal(err)
	}
	var child string
	err = pg.Get(&child, `SELECT v FROM dbv_child WHERE id = 1`)
	if err != nil || child != "c" {
		t.Fatalf("the inherited row changed: %q %v", child, err)
	}

	st, err := pg.CreateGoStructDefinition("Order Items")
	if err != nil || !strings.Contains(st, "Price") {
		t.Fatalf("%s %v", st, err)
	}
	ins, err := pg.CreateInsertStatement("Order Items")
	if err != nil || !strings.Contains(ins, `INSERT INTO "Order Items"`) {
		t.Fatalf("%s %v", ins, err)
	}
}

func TestPKLastColumn(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_last; CREATE TABLE dbv_last (name text, id int PRIMARY KEY); INSERT INTO dbv_last VALUES ('n', 7)`)
	stmt, err := pg.CreateUpdateStatement("dbv_last", []string{"name", "id"}, []any{"m", "7"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pg, stmt)
}
