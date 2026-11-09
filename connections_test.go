package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/crgimenes/dbv/db"
)

// What writeDBConfigs writes reads back the same, odd characters and all,
// and the file it replaces stays as .bak.
func TestConfigsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbv", "init.filo")
	views := t.TempDir()
	in := []db.DBConfig{
		{URL: `postgres://u:p"a\ss;w(o)rd@h:5432/d?sslmode=disable`, Title: `Prod "main"; (eu)`, ViewsPath: views},
		{URL: "postgres://h/d2", Title: "Two"},
	}
	err := writeDBConfigs(path, in[:1])
	if err != nil {
		t.Fatal(err)
	}
	err = writeDBConfigs(path, in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := readDBConfigs(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in, out) {
		t.Fatalf("%+v", out)
	}
	bak, err := readDBConfigs(path + ".bak")
	if err != nil || len(bak) != 1 {
		t.Fatalf("%+v %v", bak, err)
	}
	for _, p := range []string{path, path + ".bak"} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, info.Mode(), err)
		}
	}
}

func TestUnmask(t *testing.T) {
	old := "postgres://u:secret@h/d?sslpassword=key&sslmode=require"
	cases := map[string]string{
		"postgres://u:...@h/d?sslmode=require&sslpassword=...":  "postgres://u:secret@h/d?sslmode=require&sslpassword=key",
		"postgres://u:...@h2/d?sslmode=require&sslpassword=...": "postgres://u:secret@h2/d?sslmode=require&sslpassword=key",
		"postgres://u:new@h/d":                                  "postgres://u:new@h/d",
	}
	for typed, want := range cases {
		got, err := db.ParseURL(unmask(typed, old))
		w, _ := db.ParseURL(want)
		if err != nil || got.User.String() != w.User.String() || got.Host != w.Host || got.Query().Encode() != w.Query().Encode() {
			t.Errorf("%s: %s", typed, unmask(typed, old))
		}
	}
	if unmask("postgres://u:...@h/d", "") != "postgres://u:...@h/d" {
		t.Error("a new connection: nothing to put back")
	}
}
