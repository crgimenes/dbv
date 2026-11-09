package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	for _, u := range []string{
		"postgres://u@host/db?password=secret&sslmode=disable",
		"postgres://u@host/db?sslpassword=secret", // the client key's
	} {
		got, err := maskDBURL(u)
		if err != nil || strings.Contains(got, "secret") {
			t.Fatalf("%q %v", got, err)
		}
	}
}

func TestWriteOutputRefusesSymlink(t *testing.T) {
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

	recs, cols, total, err := pg.ListRecords(context.Background(), "Order Items", "", 0, 10, "", "")
	if err != nil || total != 2 || len(cols) != 3 {
		t.Fatalf("browse: %v total %d cols %d", err, total, len(cols))
	}
	if recs[0]["Price"] != "1.230" || recs[1]["Price"] != "1.234" {
		t.Fatalf("keys must keep their digits: %v %v", recs[0]["Price"], recs[1]["Price"])
	}

	err = updateNow(context.Background(), pg, "Order Items", "note", "changed", []string{"Price"}, []any{"1.234"})
	if err != nil {
		t.Fatal(err)
	}
	var note string
	err = pg.Get(&note, `SELECT note FROM "Order Items" WHERE "Price" = 1.23`)
	if err != nil || note != "null" {
		t.Fatalf("the other row changed: %q %v", note, err)
	}

	mustExec(t, pg, `UPDATE "Order Items" SET note = 'O''Brien', "a""b" = NULL WHERE "Price" = 1.234`)
	stmt, err := pg.CreateUpdateStatement(context.Background(), "Order Items", []string{"Price", "a\"b", "note"}, []any{"1.234", "stale", "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stmt, ",\nWHERE") || !strings.Contains(stmt, `"a""b" = NULL`) || !strings.Contains(stmt, `E'O''Brien'`) {
		t.Fatalf("statement:\n%s", stmt)
	}
	mustExec(t, pg, stmt)

	stmt, err = pg.CreateUpdateStatement(context.Background(), "Order Items", []string{"Price", "a\"b", "note"}, []any{"1.23", "y", "stale"})
	if err != nil || !strings.Contains(stmt, `"note" = E'null'`) {
		t.Fatalf("text 'null' must stay text:\n%s %v", stmt, err)
	}

	err = updateNow(context.Background(), pg, "dbv_parent", "v", "z", []string{"id"}, []any{"1"})
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
	stmt, err := pg.CreateUpdateStatement(context.Background(), "dbv_last", []string{"name", "id"}, []any{"m", "7"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pg, stmt)
}

func TestParentsAndPartitions(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_ip, dbv_pp CASCADE;
CREATE TABLE dbv_ip (id int PRIMARY KEY, v text);
CREATE TABLE dbv_ic () INHERITS (dbv_ip);
INSERT INTO dbv_ip VALUES (1, 'parent'); INSERT INTO dbv_ic VALUES (1, 'child');
CREATE TABLE dbv_pp (id int PRIMARY KEY, v text) PARTITION BY RANGE (id);
CREATE TABLE dbv_pp1 PARTITION OF dbv_pp FOR VALUES FROM (0) TO (100);
INSERT INTO dbv_pp VALUES (5, 'p');`)
	ctx := context.Background()

	// the parent's grid holds the rows its edits reach: its own
	recs, _, total, err := pg.ListRecords(ctx, "dbv_ip", "", 0, 10, "", "")
	if err != nil || total != 1 || len(recs) != 1 || recs[0]["v"] != "parent" {
		t.Fatalf("parent rows: %v %d %v", recs, total, err)
	}

	// a partitioned table's rows are its partitions'
	recs, _, total, err = pg.ListRecords(ctx, "dbv_pp", "", 0, 10, "", "")
	if err != nil || total != 1 || len(recs) != 1 {
		t.Fatalf("partitioned rows: %v %d %v", recs, total, err)
	}
	err = updateNow(ctx, pg, "dbv_pp", "v", "edited", []string{"id"}, []any{"5"})
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := pg.CreateUpdateStatement(ctx, "dbv_pp", []string{"id", "v"}, []any{"5", "stale"})
	if err != nil || strings.Contains(stmt, "ONLY") || !strings.Contains(stmt, `"v" = E'edited'`) {
		t.Fatalf("partitioned update:\n%s %v", stmt, err)
	}
	n, err := pg.ExecTx(ctx, strings.Replace(stmt, "E'edited'", "E'again'", 1))
	if err != nil || n != 1 {
		t.Fatalf("%d %v\n%s", n, err, stmt)
	}
	stmt, err = pg.CreateUpdateStatement(ctx, "dbv_pp", []string{"id", "v"}, []any{"5", "x"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pg, `UPDATE dbv_pp SET v = 'someone else' WHERE id = 5`)
	n, err = pg.ExecTx(ctx, strings.Replace(stmt, "E'again'", "E'mine'", 1))
	if err != nil || n != 0 {
		t.Fatalf("a row changed since was written over: %d %v", n, err)
	}
	mustExec(t, pg, `UPDATE dbv_pp SET v = 'again' WHERE id = 5`)
	var v string
	err = pg.Get(&v, `SELECT v FROM dbv_pp WHERE id = 5`)
	if err != nil || v != "again" {
		t.Fatalf("partition row: %q %v", v, err)
	}
}

func TestCatalogEdges(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP MATERIALIZED VIEW IF EXISTS dbv_mv; DROP TABLE IF EXISTS dbv_inc, dbv_gen;
CREATE MATERIALIZED VIEW dbv_mv AS SELECT 1 AS n, 'a'::text AS s;
CREATE TABLE dbv_inc (id int, note text, CONSTRAINT dbv_inc_pk PRIMARY KEY (id) INCLUDE (note));
INSERT INTO dbv_inc VALUES (1, NULL);
CREATE TABLE dbv_gen (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, n int, d int GENERATED ALWAYS AS (n * 2) STORED, tags text[]);`)
	ctx := context.Background()

	cols, err := pg.ListColumns("dbv_mv")
	if err != nil || len(cols) != 2 || cols[0].ColumnName != "n" || cols[0].DataType != "integer" {
		t.Fatalf("materialized view columns: %+v %v", cols, err)
	}
	recs, _, total, err := pg.ListRecords(ctx, "dbv_mv", "", 0, 10, "", "")
	if err != nil || total != 1 || recs[0]["s"] != "a" {
		t.Fatalf("materialized view rows: %v %d %v", recs, total, err)
	}

	pk, err := pg.GetPrimaryKeyColumns(ctx, "dbv_inc")
	if err != nil || len(pk) != 1 || pk[0] != "id" {
		t.Fatalf("INCLUDE is no part of the key: %v %v", pk, err)
	}
	err = updateNow(ctx, pg, "dbv_inc", "note", "set", []string{"id"}, []any{"1"})
	if err != nil {
		t.Fatal(err)
	}

	gcols, err := pg.ListColumns("dbv_gen")
	if err != nil || gcols[3].DataType != "ARRAY" {
		t.Fatalf("array type: %+v %v", gcols, err)
	}
	ins, err := pg.CreateInsertStatement("dbv_gen")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(ins, "DEFAULT") != 2 {
		t.Fatalf("identity and generated take DEFAULT:\n%s", ins)
	}
	mustExec(t, pg, ins) // the template as it comes runs
}

func TestQueryNonFinite(t *testing.T) {
	pg := testDB(t, "")
	res, err := pg.QueryReadOnly(context.Background(), 10, `SELECT 'NaN'::float8 AS a, '-Infinity'::float4 AS b, 1.5::float8 AS c`)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	err = encodeJSON(&b, resultJSON(res))
	if err != nil || !strings.Contains(b.String(), `["NaN","-Infinity",1.5]`) {
		t.Fatalf("%s %v", b.String(), err)
	}
}

// updateNow writes a cell as dbv update does: guarded by what it reads now.
func updateNow(ctx context.Context, pg *db.Postgres, table, column string, value any, pk []string, values []any) error {
	_, now, err := pg.CellNow(ctx, table, column, pk, values)
	if err != nil {
		return err
	}
	return pg.UpdateDataCell(ctx, table, column, value, pk, values, now)
}

// A cell another session changed after it was read is not overwritten,
// json (no = operator) and NULL included.
func TestCellChangedMeanwhile(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_cc;
		CREATE TABLE dbv_cc (id int PRIMARY KEY, doc json, n int, at timestamptz);
		INSERT INTO dbv_cc VALUES (1, '{"a": 1}', NULL, '2026-11-09 10:00:00+00')`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key, vals := []string{"id"}, []any{"1"}
	for _, c := range []struct{ col, other, mine string }{
		{"doc", `UPDATE dbv_cc SET doc = '{"a": 2}'`, `{"a": 3}`},
		{"n", `UPDATE dbv_cc SET n = 5`, "7"},
		{"at", `UPDATE dbv_cc SET at = at + interval '1 second'`, "2026-11-09T12:00:00Z"},
	} {
		shown, old, err := pg.CellNow(ctx, "dbv_cc", c.col, key, vals)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, pg, c.other)
		err = pg.UpdateDataCell(ctx, "dbv_cc", c.col, c.mine, key, vals, old)
		if !errors.Is(err, db.ErrChanged) {
			t.Fatalf("%s (was %v): %v", c.col, shown, err)
		}
		_, old, err = pg.CellNow(ctx, "dbv_cc", c.col, key, vals)
		if err == nil {
			err = pg.UpdateDataCell(ctx, "dbv_cc", c.col, c.mine, key, vals, old)
		}
		if err != nil {
			t.Fatalf("%s read again: %v", c.col, err)
		}
	}
	_, _, err := pg.CellNow(ctx, "dbv_cc", "n", key, []any{"2"})
	if err == nil || !strings.Contains(err.Error(), "row is gone") {
		t.Fatalf("a missing row: %v", err)
	}
}
