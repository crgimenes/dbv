package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/dbv/db"
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
	def, raw, err := pg.ViewDef(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.RunDDL(ctx, db.ViewGuard(rel, raw)+def)
	if err != nil {
		t.Fatal(err) // unchanged: the guard lets it through
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
	_, err = pg.RunDDL(ctx, db.ViewGuard(rel, raw)+def)
	if err == nil || !strings.Contains(err.Error(), "changed in the database") {
		t.Fatalf("a view changed meanwhile was replaced: %v", err)
	}
	_, _, err = pg.ViewDef(ctx, tab)
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
		got := dangerous(sql)
		if got != want {
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

func TestConstraints(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_ck, dbv_cp;
		CREATE TABLE dbv_cp (id int PRIMARY KEY);
		CREATE TABLE dbv_ck (
			id int,
			code text UNIQUE,
			parent int REFERENCES dbv_cp (id),
			n int CHECK (n > 0),
			PRIMARY KEY (id));
		CREATE INDEX dbv_ck_n ON dbv_ck (n) WHERE n > 10`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := pg.Resolve(ctx, "dbv_ck")
	if err != nil {
		t.Fatal(err)
	}
	cons, err := pg.Constraints(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(cons))
	for i, c := range cons {
		got[i] = c.Kind + "|" + c.Name + "|" + c.Def + "|" + c.SQL
	}
	want := []string{
		"p|dbv_ck_pkey|PRIMARY KEY (id)|",
		"u|dbv_ck_code_key|UNIQUE (code)|",
		"f|dbv_ck_parent_fkey|FOREIGN KEY (parent) REFERENCES dbv_cp(id)|",
		"c|dbv_ck_n_check|CHECK (n > 0)|",
		"i|dbv_ck_n|CREATE INDEX dbv_ck_n ON public.dbv_ck USING btree (n) WHERE (n > 10)|dbv_ck_n",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

// A query filtered by :where runs inside a subselect: its ;, a comment on
// its last line and its ORDER BY must not break it.
func TestWhereOnAQuery(t *testing.T) {
	for _, sql := range []string{
		"-- dbv: db=x\nSELECT n FROM generate_series(1, 5) n\nORDER BY n DESC -- newest first\n;\n",
		"-- dbv: db=x\n\nSELECT n FROM generate_series(1, 5) n; -- a note after\n",
		"SELECT n FROM generate_series(1, 5) n WHERE ';' <> '' -- no ; at the end",
	} {
		whereOnAQuery(t, sql)
	}
}

func whereOnAQuery(t *testing.T, sql string) {
	t.Helper()
	d := &tableData{sql: sql}
	if d.querySQL() != d.sql {
		t.Fatal("no where: the query as it is")
	}
	d.where = "n > 2"
	pg := testDB(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := pg.QueryReadOnly(ctx, 10, d.querySQL())
	if err != nil {
		t.Fatalf("%v\n%s", err, d.querySQL())
	}
	if len(res.Rows) != 3 {
		t.Fatalf("%v", res.Rows)
	}
}

// A materialized view made again keeps what a drop takes along.
func TestMatViewDefRunsBack(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP MATERIALIZED VIEW IF EXISTS "Sales %"; DROP TABLE IF EXISTS dbv_ms;
		DO $$ BEGIN CREATE ROLE dbv_reader; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		CREATE TABLE dbv_ms (id int, v int);
		INSERT INTO dbv_ms VALUES (1, 10), (2, 20);
		CREATE MATERIALIZED VIEW "Sales %" WITH (fillfactor = 90) AS SELECT id, v FROM dbv_ms;
		CREATE UNIQUE INDEX dbv_ms_id ON "Sales %" (id);
		COMMENT ON MATERIALIZED VIEW "Sales %" IS 'it''s daily';
		ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO PUBLIC;
		REVOKE ALL ON "Sales %" FROM PUBLIC;
		COMMENT ON COLUMN "Sales %".v IS 'value';
		GRANT SELECT ON "Sales %" TO dbv_reader WITH GRANT OPTION`)
	t.Cleanup(func() { mustExec(t, pg, `ALTER DEFAULT PRIVILEGES REVOKE SELECT ON TABLES FROM PUBLIC`) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := pg.Resolve(ctx, "Sales %")
	if err != nil {
		t.Fatal(err)
	}
	script, raw, err := pg.MatViewDef(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`DROP MATERIALIZED VIEW "Sales %";`,
		`CREATE MATERIALIZED VIEW "Sales %" WITH (fillfactor=90) AS`,
		"WITH NO DATA;",
		`CREATE UNIQUE INDEX dbv_ms_id ON public."Sales %" USING btree (id);`,
		`COMMENT ON MATERIALIZED VIEW "Sales %" IS 'it''s daily';`,
		`GRANT SELECT ON "Sales %" TO dbv_reader WITH GRANT OPTION;`,
		`ALTER MATERIALIZED VIEW "Sales %" OWNER TO postgres;`,
		`REVOKE ALL ON "Sales %" FROM PUBLIC;`,
		`COMMENT ON COLUMN "Sales %".v IS 'value';`,
		`REFRESH MATERIALIZED VIEW "Sales %";`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("no %q in\n%s", want, script)
		}
	}
	_, err = pg.RunDDL(ctx, db.ViewGuard(rel, raw)+strings.Replace(script, "SELECT id,", "SELECT id + 0 AS id,", 1))
	if err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
	var idx, rows int
	var grants, public bool
	var colComment string
	var comment string
	err = pg.Get(&idx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'dbv_ms_id'`)
	if err == nil {
		err = pg.Get(&comment, `SELECT obj_description('"Sales %"'::regclass, 'pg_class')`)
	}
	if err == nil {
		err = pg.Get(&grants, `SELECT has_table_privilege('dbv_reader', '"Sales %"', 'SELECT WITH GRANT OPTION')`)
	}
	if err == nil {
		err = pg.Get(&public, `SELECT has_table_privilege('public', '"Sales %"', 'SELECT')`)
	}
	if err == nil {
		err = pg.Get(&colComment, `SELECT col_description('"Sales %"'::regclass, 2)`)
	}
	if err == nil {
		err = pg.Get(&rows, `SELECT count(*) FROM "Sales %"`)
	}
	if err != nil || idx != 1 || comment != "it's daily" || !grants || public || colComment != "value" || rows != 2 {
		t.Fatalf("index %d, comment %q, grants %v, public %v, column %q, rows %d: %v", idx, comment, grants, public, colComment, rows, err)
	}
}

// A table's DDL runs back: it makes the same table elsewhere.
func TestDDLRunsBack(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP SCHEMA IF EXISTS dbv_copy CASCADE; DROP TABLE IF EXISTS dbv_dd, dbv_dp;
		CREATE TABLE dbv_dp (id int PRIMARY KEY);
		CREATE TABLE dbv_dd (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			"Code" varchar(8) NOT NULL DEFAULT 'x' UNIQUE,
			parent int REFERENCES dbv_dp (id),
			n numeric(10,2) CHECK (n > 0),
			twice numeric GENERATED ALWAYS AS (n * 2) STORED);
		CREATE INDEX dbv_dd_n ON dbv_dd (n) WHERE n > 1;
		COMMENT ON TABLE dbv_dd IS 'it''s a table';
		COMMENT ON COLUMN dbv_dd.n IS 'cents'`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := pg.Resolve(ctx, "dbv_dd")
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := pg.DDL(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "CREATE TABLE dbv_dd (\n    id ") || !strings.Contains(ddl, "\n    \"Code\" character varying(8) DEFAULT 'x'::character varying NOT NULL,\n    parent integer,") {
		t.Fatalf("layout:\n%s", ddl)
	}
	copied := strings.NewReplacer("TABLE dbv_dd ", "TABLE dbv_copy.dbv_dd ", "ON public.dbv_dd ", "ON dbv_copy.dbv_dd ",
		"COLUMN dbv_dd.", "COLUMN dbv_copy.dbv_dd.").Replace(ddl)
	_, err = pg.RunDDL(ctx, "CREATE SCHEMA dbv_copy;\n"+copied)
	if err != nil {
		t.Fatalf("%v\n%s", err, copied)
	}
	mustExec(t, pg, "DROP SCHEMA dbv_copy CASCADE")
}

func TestDDLPartitions(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_part, dbv_kid CASCADE;
		CREATE TABLE dbv_part (id int, at date) PARTITION BY RANGE (at);
		CREATE TABLE dbv_part_2026 PARTITION OF dbv_part FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
		DROP TABLE IF EXISTS dbv_mom_x CASCADE; CREATE TABLE dbv_mom_x (v text);
		CREATE TABLE dbv_kid () INHERITS (dbv_mom_x)`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for name, want := range map[string]string{
		"dbv_part":      ") PARTITION BY RANGE (at);",
		"dbv_part_2026": "ALTER TABLE dbv_part ATTACH PARTITION dbv_part_2026 FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');",
		"dbv_kid":       ") INHERITS (dbv_mom_x);",
	} {
		rel, err := pg.Resolve(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		ddl, err := pg.DDL(ctx, rel)
		if err != nil || !strings.Contains(ddl, want) {
			t.Fatalf("%s: %v\n%s", name, err, ddl)
		}
	}
}

// The guard holds the view to the object that was opened: renamed away and
// replaced by another of the same name, nothing runs.
func TestViewGuardIdentity(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP VIEW IF EXISTS dbv_gv, dbv_gv_old; CREATE VIEW dbv_gv AS SELECT 1 AS x`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := pg.Resolve(ctx, "dbv_gv")
	if err != nil {
		t.Fatal(err)
	}
	def, raw, err := pg.ViewDef(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pg, `ALTER VIEW dbv_gv RENAME TO dbv_gv_old; CREATE VIEW dbv_gv AS SELECT 1 AS x`)
	_, err = pg.RunDDL(ctx, db.ViewGuard(rel, raw)+def)
	if err == nil || !strings.Contains(err.Error(), "changed in the database") {
		t.Fatalf("replaced another view: %v", err)
	}
	_, err = pg.RunDDL(ctx, "BEGIN; "+def+" COMMIT;")
	if err == nil || !strings.Contains(err.Error(), "BEGIN inside") {
		t.Fatalf("a transaction of its own in DDL: %v", err)
	}
}

// A column named dbv_row does not stand for the row in U's guard.
func TestUpdateGuardColumnNamedRow(t *testing.T) {
	pg := testDB(t, "")
	mustExec(t, pg, `DROP TABLE IF EXISTS dbv_rw; CREATE TABLE dbv_rw (id int PRIMARY KEY, dbv_row text, v text);
		INSERT INTO dbv_rw VALUES (1, 'same', 'a')`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stmt, err := pg.CreateUpdateStatement(ctx, "dbv_rw", []string{"id", "dbv_row", "v"}, []any{"1", "same", "a"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pg, `UPDATE dbv_rw SET v = 'someone else'`)
	n, err := pg.ExecTx(ctx, stmt)
	if err != nil || n != 0 {
		t.Fatalf("a change since was written over: %d %v\n%s", n, err, stmt)
	}
}
