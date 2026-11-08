package main

import (
	"context"
	"os"
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

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"users":     `"users"`,
		"s.t":       `"s"."t"`,
		`we"ird`:    `"we""ird"`,
		"Mixed Cap": `"Mixed Cap"`,
	}
	for in, want := range cases {
		got := db.QuoteIdent(in)
		if got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
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
