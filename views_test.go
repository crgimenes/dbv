package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crgimenes/dbv/db"
)

func TestViewName(t *testing.T) {
	for in, want := range map[string]string{" vendas ": "vendas", "vendas.sql": "vendas", "por mês": "por mês"} {
		got, err := queryName(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a/b", `a\b`, "../x", ".hidden"} {
		_, err := queryName(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestViewsDir(t *testing.T) {
	if got := viewsDirFor(db.DBConfig{ViewsPath: "/v", Title: "x"}); got != "/v" {
		t.Fatalf("%q", got)
	}
	got := viewsDirFor(db.DBConfig{Title: "Test at gauss (copy)"})
	if filepath.Base(got) != "Test_at_gauss__copy_" || filepath.Base(filepath.Dir(got)) != "views" {
		t.Fatalf("%q", got)
	}
	if slug("..") != "default" {
		t.Fatalf("%q", slug(".."))
	}
}

func TestWriteView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "views", "a.sql")
	err := writeView(path, "SELECT 1\n", false)
	if err != nil {
		t.Fatal(err)
	}
	err = writeView(path, "SELECT 2\n", false) // a new one never overwrites
	if err == nil {
		t.Fatal("overwrote an existing view")
	}
	err = writeView(path, "SELECT 3\n", true)
	b, _ := os.ReadFile(path)
	if err != nil || string(b) != "SELECT 3\n" {
		t.Fatalf("%q %v", b, err)
	}
	if params := ExtractParameters(queryTemplate("")); len(params) != 0 {
		t.Fatalf("the template asks for %v", params)
	}
}

func TestFreeName(t *testing.T) {
	dir := t.TempDir()
	if got := filepath.Base(freeName(dir, "v")); got != "v-2.sql" {
		t.Fatalf("%s", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "v-2.sql"), nil, 0o600)
	if got := filepath.Base(freeName(dir, "v")); got != "v-3.sql" {
		t.Fatalf("%s", got)
	}
}
