package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/crgimenes/filo-term-go/screen"
)

func TestHistoryFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	p, err := historyFile()
	if err != nil {
		t.Fatal(err)
	}
	want := "/state/dbv/history.json"
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		want = filepath.Join("dbv", "history.json") // the config dir: no state dir there
	}
	if !strings.HasSuffix(filepath.ToSlash(p), filepath.ToSlash(want)) {
		t.Fatalf("%s", p)
	}
}

func TestHistorySurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dbv", "history.json")
	one := &app{views: []view{&tableData{}}, histFile: path}
	two := &app{views: []view{&tableData{}}, histFile: path}
	one.prompt, two.prompt = ":", ":"
	one.remember("where id = 1")
	two.remember("where id = 2") // a second dbv open at once keeps the first one's line
	one.remember("where id = 1")
	h, err := readHistory(path)
	key := one.histKey()
	if err != nil || !slices.Equal(h[key], []string{"where id = 1", "where id = 2", "where id = 1"}) {
		t.Fatalf("%v %v", h, err)
	}
	fi, err := os.Stat(path)
	if runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o600) {
		t.Fatalf("%v %v", fi.Mode(), err)
	}

	next := &app{views: []view{&tableData{}}, histFile: path}
	next.history, _ = readHistory(path)
	next.ask(":", "", func(*app, string) {})
	next.handle(screen.KeyUp)
	if next.input.String() != "where id = 1" {
		t.Fatalf("next run: %q", next.input.String())
	}

	err = os.WriteFile(path, []byte("{broken"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	one.remember("where id = 3") // a broken file is replaced
	h, err = readHistory(path)
	if err != nil || !slices.Equal(h[key], []string{"where id = 3"}) {
		t.Fatalf("%v %v", h, err)
	}
}

func TestFormRemembers(t *testing.T) {
	a := &app{history: map[string][]string{formKey("v", "max"): {"5", "7"}}}
	f := newParamForm(a, "v", "SELECT {{max}}", []Parameter{{Name: "max", Title: "Max"}})
	if f.form.Value(0) != "7" {
		t.Fatalf("%q", f.form.Value(0))
	}
}
