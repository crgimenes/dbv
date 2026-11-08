package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/filo-term-go/kit"

	"github.com/crgimenes/dbv/db"
)

func testConfig(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DBV_TEST_URL")
	if url == "" {
		t.Skip("DBV_TEST_URL not set")
	}
	path := filepath.Join(t.TempDir(), "init.filo")
	err := os.WriteFile(path, []byte(`(database "`+url+`" "Test")`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWritesNeedTheFlag(t *testing.T) {
	for _, argv := range [][]string{{"SELECT 1"}, {"-key", "id=1", "t", "c", "v"}} {
		name := "exec"
		if len(argv) > 1 {
			name = "update"
		}
		if got := runCLI(name, argv); got != 2 {
			t.Errorf("%s without -write: exit %d, want 2", name, got)
		}
	}
	if got := runCLI("tables", []string{"-config", "/nonexistent/init.filo"}); got != 2 {
		t.Errorf("missing -config file: exit %d, want 2", got)
	}
}

func TestExecAndUpdate(t *testing.T) {
	cfg := testConfig(t)
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_w; CREATE TABLE dbv_w (id int PRIMARY KEY, name text); INSERT INTO dbv_w VALUES (1, 'a'), (2, 'b')`)

	// a failing statement takes the ones before it back
	if got := runCLI("exec", []string{"-config", cfg, "-write", "UPDATE dbv_w SET name = 'x'; SELECT 1/0"}); got != 1 {
		t.Fatalf("exec with an error: exit %d, want 1", got)
	}
	var n int
	err := pg.Get(&n, `SELECT count(*) FROM dbv_w WHERE name = 'x'`)
	if err != nil || n != 0 {
		t.Fatalf("partial exec committed %d rows (%v)", n, err)
	}

	if got := runCLI("update", []string{"-config", cfg, "-write", "-key", "id=2", "dbv_w", "name", "B"}); got != 0 {
		t.Fatalf("update: exit %d", got)
	}
	var name string
	err = pg.Get(&name, `SELECT name FROM dbv_w WHERE id = 2`)
	if err != nil || name != "B" {
		t.Fatalf("name %q (%v)", name, err)
	}
	if got := runCLI("update", []string{"-config", cfg, "-write", "-key", "nope=2", "dbv_w", "name", "C"}); got != 1 {
		t.Fatalf("a key that is not the primary key: exit %d, want 1", got)
	}
	if got := runCLI("update", []string{"-config", cfg, "-write", "-key", "id=9", "dbv_w", "name", "C"}); got != 1 {
		t.Fatalf("a key that finds no row: exit %d, want 1", got)
	}
	if got := runCLI("exec", []string{"-config", cfg, "-write", "DELETE FROM dbv_w WHERE id = 1"}); got != 0 {
		t.Fatalf("exec: exit %d", got)
	}
	if got := runCLI("rows", []string{"-config", cfg, "dbv_w"}); got != 0 {
		t.Fatalf("rows through -config: exit %d", got)
	}
}

func TestRunScript(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_s; CREATE TABLE dbv_s (id int PRIMARY KEY, v text); INSERT INTO dbv_s VALUES (1, NULL), (2, 'b')`)
	ctx := context.Background()

	res, err := pg.Run(ctx, "UPDATE dbv_s SET v = 'x' WHERE id = 2; SELECT id, v FROM dbv_s ORDER BY id", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Tag != "UPDATE 1" || res[1].Tag != "SELECT 2" {
		t.Fatalf("%+v", res)
	}
	if res[1].Rows[0][1] != nil || res[1].Rows[1][1] != "x" || res[1].Columns[0].DataType != "int4" {
		t.Fatalf("rows %+v columns %+v", res[1].Rows, res[1].Columns)
	}

	_, err = pg.Run(ctx, "UPDATE dbv_s SET v = 'y'; SELECT 1/0", 10)
	if err == nil {
		t.Fatal("division by zero passed")
	}
	var n int
	_ = pg.Get(&n, `SELECT count(*) FROM dbv_s WHERE v = 'y'`)
	if n != 0 {
		t.Fatal("a failed script committed its first statement")
	}

	res, err = pg.Run(ctx, "SELECT generate_series(1, 5)", 2)
	if err != nil || len(res[0].Rows) != 2 || !res[0].Truncated {
		t.Fatalf("limit: %+v %v", res, err)
	}

	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = pg.Run(cctx, "SELECT pg_sleep(10)", 1)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancel took %s (%v)", time.Since(start), err)
	}
}

func TestExecRefusesTransactionControl(t *testing.T) {
	cfg := testConfig(t)
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_c; CREATE TABLE dbv_c (v int); INSERT INTO dbv_c VALUES (0)`)
	if got := runCLI("exec", []string{"-config", cfg, "-write", "UPDATE dbv_c SET v = 1; COMMIT; SELECT 1/0"}); got != 1 {
		t.Fatalf("exit %d, want 1", got)
	}
	var v int
	_ = pg.Get(&v, `SELECT v FROM dbv_c`)
	if v != 0 {
		t.Fatal("the statement before COMMIT was committed")
	}
}

func TestRunRollsBackAnOpenTransaction(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_o; CREATE TABLE dbv_o (v int); INSERT INTO dbv_o VALUES (0)`)
	_, err := pg.Run(context.Background(), "BEGIN; UPDATE dbv_o SET v = 1", 1)
	if !errors.Is(err, db.ErrLeftOpen) {
		t.Fatalf("err = %v", err)
	}
	var v int
	_ = pg.Get(&v, `SELECT v FROM dbv_o`)
	if v != 0 {
		t.Fatal("the open transaction's update stayed")
	}
	mustExec(t, pg, `UPDATE dbv_o SET v = 2`) // not blocked by a leaked lock
}

func TestColumnsShrink(t *testing.T) {
	d := gridOf(3)
	d.col, d.off = 2, 2
	d.apply(pageOf{columns: []db.ColumnInfo{{ColumnName: "id"}}})
	if d.col != 0 || d.off != 0 {
		t.Fatalf("col %d off %d", d.col, d.off)
	}
}

func TestExportSaysTruncated(t *testing.T) {
	d := &tableData{query: true, truncated: true, columns: []db.ColumnInfo{{ColumnName: "n", DataType: "int4"}}, raw: [][]any{{int64(1)}}}
	path := filepath.Join(t.TempDir(), "out.json")
	a := &app{}
	d.export(a, path)
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"truncated":true`) || a.tone != kit.Warn {
		t.Fatalf("%s %v tone %v", b, err, a.tone)
	}
}
