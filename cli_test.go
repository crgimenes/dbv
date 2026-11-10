package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/dbv/db"
)

func TestPickConfig(t *testing.T) {
	configs := []db.DBConfig{{Title: "A", URL: "a"}, {Title: "B", URL: "b"}}
	cases := []struct {
		want, url string
		fail      bool
	}{
		{want: "", fail: true},
		{want: "2", url: "b"},
		{want: "B", url: "b"},
		{want: "3", fail: true},
		{want: "0", fail: true},
		{want: "C", fail: true},
	}
	for _, c := range cases {
		got, err := pickConfig(configs, c.want)
		if c.fail != (err != nil) || (!c.fail && got.URL != c.url) {
			t.Errorf("pickConfig(%q) = %+v, %v", c.want, got, err)
		}
	}
	got, err := pickConfig(configs[:1], "")
	if err != nil || got.URL != "a" {
		t.Errorf("single connection not chosen by default: %+v, %v", got, err)
	}
}

func TestQuoteName(t *testing.T) {
	cases := map[string]string{
		"users":     `"users"`,
		"s.t":       `"s.t"`,
		`we"ird`:    `"we""ird"`,
		"Mixed Cap": `"Mixed Cap"`,
	}
	for in, want := range cases {
		got := db.QuoteName(in)
		if got != want {
			t.Errorf("QuoteName(%q) = %s, want %s", in, got, want)
		}
	}
}

// DBV_TEST_URL points at a throwaway Postgres, e.g.
// postgres://postgres:test@localhost:55432/postgres?sslmode=disable
func TestQueryReadOnly(t *testing.T) {
	url := os.Getenv("DBV_TEST_URL")
	if url == "" {
		t.Skip("DBV_TEST_URL not set")
	}
	pg, err := db.New(url)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := pg.QueryReadOnly(ctx, 3, "SELECT g, '2020-02-29'::date AS d FROM generate_series(1, 5) g")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || !res.Truncated {
		t.Fatalf("got %d rows, truncated=%v; want 3, true", len(res.Rows), res.Truncated)
	}
	if res.Rows[0][1] != "2020-02-29" {
		t.Fatalf("date = %v, want 2020-02-29", res.Rows[0][1])
	}

	_, err = pg.QueryReadOnly(ctx, 1, "CREATE TABLE dbv_must_not_exist (x int)")
	if err == nil {
		t.Fatal("write accepted inside a read-only transaction")
	}
}

func TestCommandHelpAndStrayFlags(t *testing.T) {
	for name, c := range commands {
		u := commandUsage(name, c)
		if !strings.HasPrefix(u, "dbv "+name+": ") || !strings.Contains(u[strings.Index(u, "Example:"):], "dbv "+name) {
			t.Errorf("%s: %q", name, u)
		}
		for _, f := range c.flags {
			if flagHelp[f] == "" {
				t.Errorf("%s: no help for -%s", name, f)
			}
		}
	}
	t.Setenv("HOME", t.TempDir()) // never the machine's own connections
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, argv := range [][]string{{"tables", "-where", "x"}, {"rows", "-limit", "5", "-write", "t"}} {
		code, msg := stderrOf(t, func() int { return runCLI(argv[0], argv[1:]) })
		if code != 2 || !strings.Contains(msg, "does not apply here") {
			t.Fatalf("%v: %d %q", argv, code, msg)
		}
	}
	cases := map[string]string{"tabels": "tables", "colums": "columns", "row": "rows", "upate": "update", "zzzzzz": ""}
	for typed, want := range cases {
		got := nearCommand(typed)
		if got != want {
			t.Errorf("%s: %q, want %q", typed, got, want)
		}
	}
	if !strings.Contains(notFound("tabels"), "did you mean tables") || !strings.Contains(notFound("x.filo"), "file not found") {
		t.Fatal(notFound("tabels"))
	}
}

// stderrOf runs f with stderr caught.
func stderrOf(t *testing.T, f func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := f()
	os.Stderr = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return code, string(b)
}
