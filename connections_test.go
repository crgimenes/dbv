package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/crgimenes/dbv/db"
)

// What writeDBConfigs writes reads back the same, odd characters and all,
// and the file it replaces stays as .bak, 0600 whatever was there before.
func TestConfigsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbv", "init.filo")
	views := t.TempDir()
	in := []db.DBConfig{
		{URL: `postgres://u:p"a\ss;w(o)rd@h:5432/d?sslmode=disable`, Title: `Prod "main"; (eu)`, ViewsPath: views},
		{URL: "postgres://h/d2", Title: "Two"},
	}
	read, err := writeDBConfigs(path, in[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	err = os.WriteFile(victim, []byte("keep"), 0o644)
	if err == nil {
		err = os.Symlink(victim, path+".bak") // a link where the backup goes
	}
	if err != nil {
		t.Fatal(err)
	}
	_, err = writeDBConfigs(path, in, []byte("not what is there"))
	if !errors.Is(err, errConfigChanged) {
		t.Fatalf("a file changed since it was read: %v", err)
	}
	_, err = writeDBConfigs(path, in, read)
	if err != nil {
		t.Fatal(err)
	}
	out, err := readDBConfigs(path)
	if err != nil || !slices.Equal(in, out) {
		t.Fatalf("%+v %v", out, err)
	}
	bak, err := readDBConfigs(path + ".bak")
	if err != nil || len(bak) != 1 {
		t.Fatalf("%+v %v", bak, err)
	}
	kept, err := os.ReadFile(victim)
	if err != nil || string(kept) != "keep" {
		t.Fatalf("written through the link: %q %v", kept, err)
	}
	for _, p := range []string{path, path + ".bak"} {
		info, err := os.Lstat(p)
		if err != nil || info.Mode() != 0o600 {
			t.Fatalf("%s: %v %v", p, info.Mode(), err)
		}
	}
}

// A config that is a link (to a dotfiles repository) stays a link: the file
// it names is the one rewritten.
func TestConfigLinkKept(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles.filo")
	link := filepath.Join(dir, "init.filo")
	err := os.WriteFile(target, []byte(`(database "postgres://h/a" "A")`), 0o600)
	if err == nil {
		err = os.Symlink(target, link)
	}
	if err != nil {
		t.Fatal(err)
	}
	read, _ := os.ReadFile(link)
	_, err = writeDBConfigs(link, []db.DBConfig{{URL: "postgres://h/b", Title: "B"}}, read)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	got, err := readDBConfigs(target)
	if err != nil || len(got) != 1 || got[0].Title != "B" {
		t.Fatalf("%+v %v", got, err)
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
