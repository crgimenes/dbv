package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crgimenes/dbv/db"
	"github.com/crgimenes/dbv/sqlcomplete"
)

func frame(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(b), b)
}

// replies runs the server over in and returns what it answered, by id.
func replies(t *testing.T, s *lspServer, msgs ...any) map[string]json.RawMessage {
	t.Helper()
	var in strings.Builder
	for _, m := range msgs {
		in.WriteString(frame(m))
	}
	var out bytes.Buffer
	s.out = &out
	err := s.serve(strings.NewReader(in.String()))
	if err != nil {
		t.Fatal(err)
	}
	waitCatalogs(t, s) // a background read may still be writing its notice
	s.outMu.Lock()
	said := bytes.NewBuffer(slices.Clone(out.Bytes()))
	s.outMu.Unlock()
	got := map[string]json.RawMessage{}
	r := bufio.NewReader(said)
	for {
		body, err := readFrame(r)
		if err != nil {
			break
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(body, &m)
		if m.Error != nil {
			got[string(m.ID)] = m.Error
			continue
		}
		got[string(m.ID)] = m.Result
	}
	return got
}

func labelsOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var r struct {
		Items []struct {
			Label    string `json:"label"`
			TextEdit struct {
				NewText string `json:"newText"`
			} `json:"textEdit"`
		} `json:"items"`
	}
	err := json.Unmarshal(raw, &r)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Label)
	}
	return out
}

func didOpen(uri, text string) any {
	return map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "sql", "version": 1, "text": text}}}
}

func askCompletion(id int, uri string, line, char int) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "textDocument/completion", "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": line, "character": char}}}
}

var testCatalog = &sqlcomplete.Catalog{Tables: []sqlcomplete.Table{
	{Schema: "public", Name: "clientes", Kind: "table", Columns: []sqlcomplete.Column{{Name: "id", Type: "integer"}, {Name: "nome", Type: "text"}}},
}}

func TestLSPCompletes(t *testing.T) {
	t.Setenv("DBV_DB", "")
	var asked []string
	s := newLSPServer(nil, "/cfg.filo", func(files []string, name string) (*sqlcomplete.Catalog, error) {
		asked = append(asked, strings.Join(files, ",")+"|"+name)
		return testCatalog, nil
	})
	text := "-- dbv: db=Test\nselect * from clientes cl\nwhere cl."
	got := replies(t, s,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}}}}},
		didOpen("file:///a.sql", text))
	if !strings.Contains(string(got["1"]), `"positionEncoding":"utf-8"`) {
		t.Fatalf("%s", got["1"])
	}
	waitCatalogs(t, s)
	got = replies(t, s, askCompletion(2, "file:///a.sql", 2, 9), map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/definition", "params": map[string]any{}})
	if l := labelsOf(t, got["2"]); !slices.Equal(l, []string{"id", "nome"}) {
		t.Fatalf("cl. after FROM clientes cl: %v", l)
	}
	if !strings.Contains(string(got["3"]), "-32601") {
		t.Fatalf("unknown request: %s", got["3"])
	}
	if !slices.Equal(asked, []string{"/cfg.filo|Test"}) {
		t.Fatalf("loaded %v", asked)
	}
}

func TestLSPLooseFile(t *testing.T) {
	t.Setenv("DBV_DB", "")
	s := newLSPServer(nil, "", func([]string, string) (*sqlcomplete.Catalog, error) {
		t.Fatal("no database named: nothing to load")
		return nil, nil
	})
	got := replies(t, s, didOpen("file:///b.sql", "sel"), askCompletion(1, "file:///b.sql", 0, 3))
	if l := labelsOf(t, got["1"]); !slices.Equal(l, []string{"select"}) {
		t.Fatalf("%v", l)
	}
}

func TestLSPDatabaseFails(t *testing.T) {
	t.Setenv("DBV_DB", "")
	s := newLSPServer(nil, "", func([]string, string) (*sqlcomplete.Catalog, error) {
		return nil, fmt.Errorf("no route to host")
	})
	var out bytes.Buffer
	s.out = &out
	_ = s.catalog("-- dbv: db=gone\n")
	waitCatalogs(t, s)
	s.outMu.Lock()
	said := out.String()
	s.outMu.Unlock()
	if !strings.Contains(said, "window/showMessage") || !strings.Contains(said, "SQL words only") {
		t.Fatalf("%s", said)
	}
	if s.catalog("-- dbv: db=gone\n") != nil {
		t.Fatal("a failed database is not tried on every keystroke")
	}
}

func TestLSPFromInsideDBV(t *testing.T) {
	t.Setenv("DBV_CONFIG", "/a.filo"+string(os.PathListSeparator)+"/b.filo")
	t.Setenv("DBV_DB", "Test")
	s := newLSPServer(nil, "", nil)
	files, name := s.dbFor("select 1")
	if name != "Test" || len(files) != 2 {
		t.Fatalf("%v %q", files, name)
	}
	_, name = s.dbFor("-- dbv: db=Other\nselect 1")
	if name != "Test" {
		t.Fatalf("opened by dbv, dbv's connection: %q", name)
	}
}

func TestOffsets(t *testing.T) {
	text := "ação\nselect é.x"
	off := offsetAt(text, lspPosition{1, 8}, false) // UTF-16: é is one unit
	if text[off:] != ".x" {
		t.Fatalf("%q", text[off:])
	}
	if p := positionAt(text, off, false); p != (lspPosition{1, 8}) {
		t.Fatalf("%+v", p)
	}
	off = offsetAt(text, lspPosition{1, 9}, true) // bytes: é is two
	if text[off:] != ".x" {
		t.Fatalf("%q", text[off:])
	}
}

func TestCatalogOf(t *testing.T) {
	cat := catalogOf([][]any{
		{"public", "a", "r", "id", "integer"},
		{"public", "a", "r", "x", "text"},
		{"public", "v", "v", nil, nil},
	})
	if len(cat.Tables) != 2 || len(cat.Tables[0].Columns) != 2 || cat.Tables[1].Kind != "view" {
		t.Fatalf("%+v", cat)
	}
}

// waitCatalogs waits for the background loads to end, message sent.
func waitCatalogs(t *testing.T, s *lspServer) {
	t.Helper()
	for range 200 {
		s.mu.Lock()
		done := true
		for _, c := range s.catalogs {
			done = done && !c.loading
		}
		s.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("catalog never loaded")
}

func TestLSPHoverAndReload(t *testing.T) {
	t.Setenv("DBV_DB", "")
	var loads atomic.Int32
	var fail atomic.Bool
	s := newLSPServer(nil, "/cfg.filo", func([]string, string) (*sqlcomplete.Catalog, error) {
		loads.Add(1)
		if fail.Load() {
			return nil, fmt.Errorf("down")
		}
		return testCatalog, nil
	})
	text := "-- dbv: db=Test\nselect cl.nome from clientes cl"
	replies(t, s, didOpen("file:///h.sql", text))
	waitCatalogs(t, s)
	hover := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "textDocument/hover", "params": map[string]any{"textDocument": map[string]any{"uri": "file:///h.sql"}, "position": map[string]any{"line": 1, "character": 12}}}
	got := replies(t, s, hover)
	if !strings.Contains(string(got["1"]), "nome` text") {
		t.Fatalf("%s", got["1"])
	}
	save := map[string]any{"jsonrpc": "2.0", "method": "textDocument/didSave", "params": map[string]any{"textDocument": map[string]any{"uri": "file:///h.sql"}}}
	replies(t, s, save)
	waitCatalogs(t, s)
	if loads.Load() != 1 {
		t.Fatalf("a save right after a read reads again: %d", loads.Load())
	}
	fail.Store(true)
	replies(t, s, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "workspace/executeCommand", "params": map[string]any{"command": "dbv.reload"}})
	waitCatalogs(t, s)
	if loads.Load() != 2 || s.catalog(text) == nil {
		t.Fatalf("a failed reload keeps the catalog it had: loads %d", loads.Load())
	}
}

func TestLSPRedactsAndRejects(t *testing.T) {
	for _, msg := range []string{
		`parse "postgres://u:s3cr%zz@h/db": invalid URL escape`,
		`parse "postgres://u@h:bad/db?password=s3cr3t&x=1": invalid port`,
		`parse "postgres://u@h:bad/db?sslmode=off&PASSWORD=s3cr3t": invalid port`,
		`parse "postgres://u:s3cr\"tail@h/db": invalid userinfo`,
		`host=h password = 's3cr3t' port=bad`,
		`host=h password='s3cr3t tail' port=bad`,
		`host=h password='s3cr\'3t' port=bad`,
	} {
		if got := redact(msg); strings.Contains(got, "s3cr") {
			t.Errorf("%s", got)
		}
	}
	for _, url := range []string{"postgres://u: SECRET @h/db", "postgres://h/db?pass%77ord=SECRET", "host=h password=a\\ SECRET port=x"} {
		err := withoutURL(fmt.Errorf("parse %q: bad", url), db.DBConfig{URL: url, Title: "T"})
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%v", err)
		}
	}
	t.Setenv("DBV_DB", "")
	s := newLSPServer(nil, "", nil)
	var out bytes.Buffer
	s.out = &out
	err := s.serve(strings.NewReader("Content-Length: 5\r\n\r\n{nope"))
	if err != nil || !strings.Contains(out.String(), "-32700") {
		t.Fatalf("%v %s", err, out.String())
	}
}
