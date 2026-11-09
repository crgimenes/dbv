package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestErrorUnderTheHeader(t *testing.T) {
	text := "-- dbv: db=x\nCREATE VIEW v AS SELEC 1;\n"
	once := withError(text, errors.New("ERROR: syntax error\nLINE 2: SELEC"))
	want := "-- dbv: db=x\n" + errorMark + "ERROR: syntax error\n" + errorMark + "LINE 2: SELEC\nCREATE VIEW v AS SELEC 1;\n"
	if once != want {
		t.Fatalf("%q", once)
	}
	twice := withError(once, errors.New("other"))
	if twice != "-- dbv: db=x\n"+errorMark+"other\nCREATE VIEW v AS SELEC 1;\n" {
		t.Fatalf("%q", twice)
	}
	if withError("SELECT\n", errors.New("e")) != errorMark+"e\nSELECT\n" {
		t.Fatal("no header: the error first")
	}
}

func TestQueryAndViewShareAName(t *testing.T) {
	shown := []TableInfo{{Name: "active", Type: queryType}, {Name: "users", Type: "table"}, {Name: "active", Type: "view"}}
	if indexOfTable(shown, "active", "view", 0) != 2 || indexOfTable(shown, "active", queryType, 1) != 0 {
		t.Fatal("the row of the other kind")
	}
	if indexOfTable(shown, "active", "", 1) != 0 {
		t.Fatal("any kind: the first")
	}
}

// ViewDef must give back a statement that runs, options and all, and
// replaces the view with itself.
func TestViewDefRunsBack(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP VIEW IF EXISTS "Active Users"; DROP TABLE IF EXISTS dbv_vu;
		CREATE TABLE dbv_vu (id int PRIMARY KEY, name text, on_ bool);
		CREATE VIEW "Active Users" WITH (security_barrier) AS SELECT id, name FROM dbv_vu WHERE on_
		WITH LOCAL CHECK OPTION`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := pg.Resolve(ctx, "Active Users")
	if err != nil {
		t.Fatal(err)
	}
	def, err := pg.ViewDef(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE OR REPLACE VIEW "Active Users" WITH (security_barrier=true, check_option=local) AS`
	if !strings.HasPrefix(def, want) || !strings.HasSuffix(def, "on_;\n") {
		t.Fatalf("%q", def)
	}
	_, err = pg.Run(ctx, def, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.Run(ctx, strings.Replace(def, "\n   FROM", ",\n    name AS again\n   FROM", 1), 1)
	if err != nil {
		t.Fatal(err) // a column added at the end is allowed
	}
	tab, err := pg.Resolve(ctx, "dbv_vu")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.ViewDef(ctx, tab)
	if err == nil {
		t.Fatal("a table's definition as a view")
	}
}

func TestDangerous(t *testing.T) {
	cases := map[string]string{
		"ALTER TABLE t ADD COLUMN x text":                            "",
		"ALTER TABLE t DROP COLUMN x":                                "this drops",
		"ALTER TABLE t ALTER COLUMN x TYPE int USING x::int":         "this changes a type",
		"COMMENT ON COLUMN t.x IS 'drop it later'":                   "",
		"-- DROP TABLE t\nALTER TABLE t ALTER COLUMN x SET NOT NULL": "",
		"ALTER TABLE t RENAME COLUMN \"drop\" TO x":                  "",
		"TRUNCATE t":                     "this deletes rows",
		"CREATE TYPE mood AS ENUM ('a')": "",
	}
	for sql, want := range cases {
		if got := dangerous(sql); got != want {
			t.Errorf("%q: %q, want %q", sql, got, want)
		}
	}
}

func TestStructure(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_st;
		CREATE TABLE dbv_st (
			id bigint GENERATED ALWAYS AS IDENTITY,
			code varchar(8) NOT NULL DEFAULT 'x',
			price numeric(10,2),
			twice numeric GENERATED ALWAYS AS (price * 2) STORED,
			PRIMARY KEY (id, code));
		COMMENT ON COLUMN dbv_st.price IS 'in cents';
		ALTER TABLE dbv_st DROP COLUMN twice;
		ALTER TABLE dbv_st ADD COLUMN twice numeric GENERATED ALWAYS AS (price * 2) STORED`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := pg.Resolve(ctx, "dbv_st")
	if err != nil {
		t.Fatal(err)
	}
	cols, err := pg.Structure(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(cols))
	for i, c := range cols {
		got[i] = strings.Join([]string{c.Name, c.Type, strconv.FormatBool(c.NotNull), c.Default, c.Identity, c.Generated, strconv.FormatBool(c.Key), c.Comment}, "|")
	}
	want := []string{
		"id|bigint|true||a||true|",
		"code|character varying(8)|true|'x'::character varying|||true|",
		"price|numeric(10,2)|false||||false|in cents",
		"twice|numeric|false|(price * (2)::numeric)||s|false|",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

// A lock held elsewhere fails RunDDL after db.LockWait, not never.
func TestDDLGivesUpOnALock(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_lk; CREATE TABLE dbv_lk (x int)`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pg.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "LOCK TABLE dbv_lk IN ACCESS SHARE MODE")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = pg.RunDDL(ctx, "ALTER TABLE dbv_lk ADD COLUMN y int")
	if err == nil || !strings.Contains(err.Error(), "lock timeout") || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}
