package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/dbv/db"
)

func TestCutSQL(t *testing.T) {
	text := "-- dbv: db=Test\nSELECT 1;\nSELECT 2\n\nSELECT 'é';\n"
	cases := []struct {
		at, want string
	}{
		{"", "-- dbv: db=Test\nSELECT 1;\nSELECT 2\n\nSELECT 'é';"},
		{"17", "-- dbv: db=Test\nSELECT 1"}, // the header is a comment of the first statement
		{"25", "-- dbv: db=Test\nSELECT 1"}, // just past the ";": that statement
		{"28", "SELECT 2"},
		{"40", "SELECT 'é'"},
		{"26:34", "SELECT 2"},
	}
	for _, c := range cases {
		got, err := cutSQL(text, c.at)
		if err != nil || got != c.want {
			t.Errorf("cutSQL(-at %q) = %q, %v; want %q", c.at, got, err, c.want)
		}
	}
	for _, bad := range []string{"x", "-1", "999", "5:2", "0:999", "a:b"} {
		_, err := cutSQL(text, bad)
		if err == nil {
			t.Errorf("-at %q passed", bad)
		}
	}
	_, err := cutSQL("-- nothing\n\n", "")
	if err == nil {
		t.Error("a file of comments ran")
	}
}

func TestShowDB(t *testing.T) {
	t.Setenv("DBV_CONFIG", "/a.filo"+string(os.PathListSeparator)+"/b.filo")
	t.Setenv("DBV_DB", "2")
	files, name, _ := showDB("", "", "-- dbv: db=Other\n")
	if !slices.Equal(files, []string{"/a.filo", "/b.filo"}) || name != "2" {
		t.Fatalf("inside dbv: %v %q", files, name)
	}
	files, name, _ = showDB("", "Prod", "")
	if len(files) != 2 || name != "Prod" {
		t.Fatalf("-db inside dbv: %v %q", files, name)
	}
	files, name, _ = showDB("/c.filo", "", "-- dbv: db=Other\n")
	if !slices.Equal(files, []string{"/c.filo"}) || name != "Other" {
		t.Fatalf("-config: %v %q", files, name)
	}
	t.Setenv("DBV_DB", "")
	files, name, _ = showDB("", "", "-- dbv: db=Other\n")
	if !slices.Equal(files, []string{configPath()}) || name != "Other" {
		t.Fatalf("loose file: %v %q", files, name)
	}
}

func TestOnlyReads(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT 1":                                          true,
		"(select 1) union (select 2)":                       true,
		"with x as (select 1) table x":                      true,
		"explain select 1":                                  true,
		"update t set a = 1":                                false,
		"select 1; delete from t":                           false,
		"select 1; commit":                                  false,
		"set default_transaction_read_only = off; select 1": false,
	} {
		if got := onlyReads(sql); got != want {
			t.Errorf("onlyReads(%q) = %v", sql, got)
		}
	}
}

func TestRunReadOnly(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_ro; CREATE TABLE dbv_ro (v int); INSERT INTO dbv_ro VALUES (0)`)
	ctx := context.Background()
	for _, sql := range []string{
		"WITH d AS (DELETE FROM dbv_ro RETURNING v) SELECT * FROM d",
		"SELECT 1; COMMIT; UPDATE dbv_ro SET v = 1", // a new transaction is read only too
		"EXPLAIN ANALYZE UPDATE dbv_ro SET v = 1",
	} {
		_, err := pg.RunReadOnly(ctx, sql, 10)
		if !db.IsReadOnly(err) {
			t.Errorf("%q: err = %v, want read only", sql, err)
		}
	}
	res, err := pg.RunReadOnly(ctx, "SELECT v FROM dbv_ro", 10)
	if err != nil || len(res) != 1 || res[0].Rows[0][0] != "0" {
		t.Fatalf("read: %+v %v", res, err)
	}
	for range 10 { // the pool must not hand a read-only session to a write
		mustExec(t, pg, `UPDATE dbv_ro SET v = v`)
		_, err = pg.Run(ctx, "UPDATE dbv_ro SET v = v", 1)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestShowFindsTheConnection(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "init.filo") // never reached: each case fails before connecting
	err := os.WriteFile(cfg, []byte(`(database "postgres://u@127.0.0.1:1/x" "Test")`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	sql := filepath.Join(t.TempDir(), "q.sql")
	err = os.WriteFile(sql, []byte("-- dbv: db=Nope\nSELECT 1\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBV_DB", "")
	if got := runShow([]string{"-config", cfg, sql}); got != 1 {
		t.Fatalf("unknown connection: exit %d, want 1", got)
	}
	if got := runShow([]string{"-config", cfg, "-at", "x", sql}); got != 1 {
		t.Fatalf("bad -at: exit %d, want 1", got)
	}
	if got := runShow([]string{"-h"}); got != 0 {
		t.Fatalf("-h: exit %d", got)
	}
}

func TestInfo(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "init.filo")
	err := os.WriteFile(cfg, []byte(`(database "postgres://u:s3cret@127.0.0.1:1/x?sslpassword=k3y" "Down")
(database "postgres://u@127.0.0.1:1/y" "Other")`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	err = writeInfo(&b, []string{cfg}, "", "the only connection configured", time.Second)
	if err == nil || !strings.Contains(b.String(), "1  Down") || strings.Contains(b.String(), "Database") {
		t.Fatalf("two connections, none named: %v\n%s", err, b.String())
	}
	b.Reset()
	err = writeInfo(&b, []string{cfg}, "Down", "-db", time.Second)
	out := b.String() + fmt.Sprint(err)
	if err == nil || !strings.Contains(out, "* 1  Down") || !strings.Contains(out, "not reached") {
		t.Fatalf("unreachable: %s", out)
	}
	if strings.Contains(out, "s3cret") || strings.Contains(out, "k3y") {
		t.Fatalf("a password shows: %s", out)
	}
}

func TestInfoServer(t *testing.T) {
	cfg := testConfig(t)
	var b strings.Builder
	err := writeInfo(&b, []string{cfg}, "", "the only connection configured", 5*time.Second)
	if err != nil || !strings.Contains(b.String(), "Server     PostgreSQL") || !strings.Contains(b.String(), "Read only  off") {
		t.Fatalf("%v\n%s", err, b.String())
	}
}

func TestURLErrorsHideThePassword(t *testing.T) {
	for _, u := range []string{"postgres://u:s3cret@host:bad/db", "ssh+postgres://u:s3cret@host:bad/db", "postgres://u:s3cret@host/%zz"} {
		_, err := db.New(u)
		if err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("db.New(%q): %v", u, err)
		}
		_, err = maskDBURL(u)
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("maskDBURL(%q): %v", u, err)
		}
	}
	cfg := filepath.Join(t.TempDir(), "init.filo")
	err := os.WriteFile(cfg, []byte(`(database "postgres://u:s3cret@host:bad/db")`), 0o600) // no title: the URL masked is the title
	if err != nil {
		t.Fatal(err)
	}
	_, err = readDBConfigs(cfg)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("readDBConfigs: %v", err)
	}
}

func TestReadOnlyCellRefusedBeforeTheEditor(t *testing.T) {
	d := &tableData{readOnly: true, query: true, header: []string{"x"}, rows: [][]string{{"1"}}, nulls: [][]bool{{false}}}
	a := &app{} // no terminal: reaching the editor would panic
	d.editCell(a)
	if !strings.Contains(a.note, "p views it") {
		t.Fatalf("note %q", a.note)
	}
}
