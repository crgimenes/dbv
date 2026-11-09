package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/crgimenes/dbv/db"
	"github.com/crgimenes/dbv/sqlcomplete"
)

// The language server: dbv lsp, started by the editor over stdio. Spec:
// https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/
// Try it by hand:
//
//	printf 'Content-Length: 58\r\n\r\n{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | dbv lsp
//
// The database is the one dbv is on when it opened the editor (DBV_DB, its
// number, and DBV_CONFIG in the editor's environment), else the one a
// comment at the top of the file names (-- dbv: db=NAME), else none.
// Nothing here may stop the server; a loose .sql still gets its words.

const lspUsage = `dbv lsp [-config FILE] [-debug]

A language server for .sql files, on stdin and stdout, for the editor to
start. It completes table names after FROM, JOIN, UPDATE and INTO, an
alias's columns after "alias.", the statement's columns and SQL words
elsewhere.

The database is the one dbv is on when it opened the editor; a file opened
on its own may name one in its first comment lines, from the config:

  -- dbv: db=NAME        (a title or a number, as in dbv -db)

With neither, or when the database cannot be reached, SQL words only.

  -config FILE   config for files that name a database (default: dbv's)
  -debug         events on stderr (the editor's LSP log)
`

type lspMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

// catalogState is one database's catalog, read in the background: once,
// then again on dbv.reload or a save a minute after the last read.
type catalogState struct {
	cat     *sqlcomplete.Catalog
	loading bool
	read    time.Time
}

const catalogFresh = time.Minute

type lspServer struct {
	outMu sync.Mutex
	out   io.Writer

	utf8 bool // positions count bytes; otherwise UTF-16 units, the default
	docs map[string]string

	config   string // -config, for a file's own -- dbv: db=
	envFiles []string
	envDB    string

	mu       sync.Mutex
	catalogs map[string]*catalogState
	load     func(files []string, name string) (*sqlcomplete.Catalog, error)
}

func runLSP(argv []string) int {
	fs := flag.NewFlagSet("lsp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "")
	debug := fs.Bool("debug", false, "")
	err := fs.Parse(argv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(lspUsage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbv lsp: %v\n", err)
		return 2
	}
	if *debug {
		debugOut = os.Stderr
	}
	s := newLSPServer(os.Stdout, *config, loadCatalog)
	err = s.serve(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbv lsp:", err)
		return 1
	}
	return 0
}

func newLSPServer(out io.Writer, config string, load func([]string, string) (*sqlcomplete.Catalog, error)) *lspServer {
	s := &lspServer{out: out, config: config, docs: map[string]string{}, catalogs: map[string]*catalogState{}, load: load}
	env := os.Getenv("DBV_CONFIG")
	if env != "" && os.Getenv("DBV_DB") != "" {
		s.envFiles, s.envDB = filepath.SplitList(env), os.Getenv("DBV_DB")
	}
	return s
}

// serve reads messages until exit or the end of input.
func (s *lspServer) serve(in io.Reader) error {
	r := bufio.NewReader(in)
	for {
		body, err := readFrame(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var m lspMessage
		err = json.Unmarshal(body, &m)
		if err != nil {
			s.send(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error: " + err.Error()}})
			continue
		}
		if m.Method == "exit" {
			return nil
		}
		s.handle(m)
	}
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 || length > 64<<20 {
		return nil, fmt.Errorf("bad Content-Length %d", length)
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

func (s *lspServer) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		debugEvent("lsp", debugNow(), "err", err)
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, err = fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(b), b)
	if err != nil {
		debugEvent("lsp", debugNow(), "err", err)
	}
}

func (s *lspServer) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *lspServer) handle(m lspMessage) {
	start := debugNow()
	defer debugEvent("lsp", start, "method", m.Method)
	switch m.Method {
	case "initialize":
		s.reply(m.ID, s.initialize(m.Params))
	case "shutdown":
		s.reply(m.ID, nil)
	case "textDocument/didOpen":
		var p struct {
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.docs[p.TextDocument.URI] = p.TextDocument.Text
		s.catalog(p.TextDocument.Text) // read it while the user types
	case "textDocument/didChange":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if len(p.ContentChanges) > 0 {
			s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text // full sync
		}
	case "textDocument/didClose":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		delete(s.docs, p.TextDocument.URI)
	case "textDocument/completion":
		s.reply(m.ID, s.completion(m.Params))
	case "textDocument/hover":
		s.reply(m.ID, s.hover(m.Params))
	case "textDocument/didSave":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.catalogFor(s.docs[p.TextDocument.URI], "save") // a table made since shows up
	case "workspace/executeCommand":
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(m.Params, &p)
		for _, text := range s.docs {
			s.catalogFor(text, "reload")
		}
		s.reply(m.ID, nil)
	default:
		if len(m.ID) > 0 {
			s.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported: " + m.Method}})
		}
	}
}

func (s *lspServer) initialize(params json.RawMessage) any {
	var p struct {
		Capabilities struct {
			General struct {
				PositionEncodings []string `json:"positionEncodings"`
			} `json:"general"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(params, &p)
	enc := "utf-16"
	for _, e := range p.Capabilities.General.PositionEncodings {
		if e == "utf-8" {
			s.utf8, enc = true, e
		}
	}
	return map[string]any{
		"capabilities": map[string]any{
			"positionEncoding": enc,
			"textDocumentSync": map[string]any{"openClose": true, "change": 1, "save": map[string]any{}}, // the whole text on each change: a scratch file is small
			"hoverProvider":    true,
			"executeCommandProvider": map[string]any{
				"commands": []string{"dbv.reload"},
			},
			"completionProvider": map[string]any{
				"triggerCharacters": []string{"."},
			},
		},
		"serverInfo": map[string]any{"name": "dbv", "version": GitTag},
	}
}

func (s *lspServer) completion(params json.RawMessage) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position lspPosition `json:"position"`
	}
	_ = json.Unmarshal(params, &p)
	text := s.docs[p.TextDocument.URI]
	cur := offsetAt(text, p.Position, s.utf8)
	st, end := sqlcomplete.Statement(text, cur)
	ctx := sqlcomplete.Analyze(text[st:end], cur-st)
	items := sqlcomplete.Complete(s.catalog(text), ctx)

	from := positionAt(text, st+ctx.From, s.utf8)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"label":      it.Label,
			"kind":       lspKind(it.Kind),
			"detail":     it.Detail,
			"filterText": it.Label,
			"textEdit":   map[string]any{"range": lspRange{from, p.Position}, "newText": it.Insert},
		})
	}
	return map[string]any{"isIncomplete": false, "items": out}
}

func redactErr(err error) any {
	if err == nil {
		return nil
	}
	return redact(err.Error())
}

func (s *lspServer) hover(params json.RawMessage) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position lspPosition `json:"position"`
	}
	_ = json.Unmarshal(params, &p)
	text := s.docs[p.TextDocument.URI]
	cur := offsetAt(text, p.Position, s.utf8)
	st, end := sqlcomplete.Statement(text, cur)
	say := sqlcomplete.Describe(s.catalog(text), text[st:end], cur-st)
	if say == "" {
		return nil
	}
	return map[string]any{"contents": map[string]any{"kind": "markdown", "value": say}}
}

func lspKind(k string) int {
	switch k {
	case "column":
		return 5 // Field
	case "table", "view":
		return 22 // Struct
	}
	return 14 // Keyword
}

// dbFor is the config files and the connection name for text: dbv's own
// connection when dbv opened the editor (its number: titles repeat), else
// the file's -- dbv: db=, else none.
func (s *lspServer) dbFor(text string) ([]string, string) {
	if len(s.envFiles) > 0 {
		return s.envFiles, s.envDB
	}
	name := headerDB(text)
	if name == "" {
		return nil, ""
	}
	if s.config != "" {
		return []string{s.config}, name
	}
	return []string{configPath()}, name
}

// headerDB reads -- dbv: db=NAME from the comments that open the file.
func headerDB(text string) string {
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, "--")
		if !ok {
			return ""
		}
		rest, ok = strings.CutPrefix(strings.TrimSpace(rest), "dbv:")
		if !ok {
			continue
		}
		_, name, ok := strings.Cut(rest, "db=")
		if ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// catalog is text's database catalog when it has been read; nil while it
// is first being read, after that failed, or with no database at all.
func (s *lspServer) catalog(text string) *sqlcomplete.Catalog {
	return s.catalogFor(text, "")
}

// catalogFor reads the catalog again when why says so ("reload", "save");
// until the new one comes the old one serves, and a failed read keeps it.
// A failure is not tried again on every key, only on the next reason.
func (s *lspServer) catalogFor(text, why string) *sqlcomplete.Catalog {
	files, name := s.dbFor(text)
	if name == "" {
		return nil
	}
	key := strings.Join(files, string(os.PathListSeparator)) + "\x00" + name
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.catalogs[key]
	first := c == nil
	if first {
		c = &catalogState{}
		s.catalogs[key] = c
	}
	again := why == "reload" || (why == "save" && time.Since(c.read) > catalogFresh)
	if c.loading || (!first && !again) {
		return c.cat
	}
	c.loading = true
	go func() {
		start := debugNow()
		cat, err := s.load(files, name)
		debugEvent("catalog", start, "db", name, "why", why, "err", redactErr(err))
		switch {
		case err != nil:
			s.notify(2, fmt.Sprintf("dbv: %s: %v; completing SQL words only", name, err))
		case why == "reload":
			s.notify(3, fmt.Sprintf("dbv: %s: %d tables and views", name, len(cat.Tables)))
		}
		s.mu.Lock()
		if err == nil {
			c.cat = cat
		}
		c.loading, c.read = false, time.Now()
		s.mu.Unlock()
	}()
	return c.cat
}

// notify shows text in the editor; kind 2 is a warning, 3 news. A URL's
// password never goes out, whatever error carried it (url.Parse quotes
// the whole URL).
func (s *lspServer) notify(kind int, text string) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": "window/showMessage", "params": map[string]any{"type": kind, "message": redact(text)}})
}

var (
	userInfo  = regexp.MustCompile(`(://[^:/@\s]*:)[^@\s]*@`)
	passParam = regexp.MustCompile(`(?i)(password\s*=\s*)('(?:[^'\\]|\\.)*'?|[^&\s"]*)`) // URL ?password= and DSN password = '...'
)

// withoutURL is err with cfg's URL, as written or Go-quoted, put as its
// title: whatever form the password takes in it, it goes with the URL.
func withoutURL(err error, cfg db.DBConfig) error {
	quoted := strconv.Quote(cfg.URL)
	msg := strings.ReplaceAll(err.Error(), quoted[1:len(quoted)-1], "<"+cfg.Title+">")
	return errors.New(strings.ReplaceAll(msg, cfg.URL, "<"+cfg.Title+">"))
}

func redact(s string) string {
	return passParam.ReplaceAllString(userInfo.ReplaceAllString(s, "${1}****@"), "${1}****")
}

const catalogSQL = `SELECT n.nspname, c.relname, c.relkind::text, a.attname, format_type(a.atttypid, a.atttypmod)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY 1, 2, a.attnum`

// loadCatalog connects, reads every table's and view's columns in one
// read-only query, and lets go of the connection.
func loadCatalog(files []string, name string) (*sqlcomplete.Catalog, error) {
	var configs []db.DBConfig
	for _, f := range files {
		c, err := readDBConfigs(f)
		if err != nil {
			// the error may quote a URL it could not parse, password and all
			return nil, fmt.Errorf("cannot read %s (dbv dbs -config %s says why)", tildePath(f), tildePath(f))
		}
		configs = append(configs, c...)
	}
	cfg, err := pickConfig(configs, name)
	if err != nil {
		return nil, err
	}
	pg, err := db.New(cfg.URL)
	if err != nil {
		return nil, withoutURL(err, cfg)
	}
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := pg.QueryReadOnly(ctx, 2_000_000, catalogSQL)
	if err != nil {
		return nil, err
	}
	return catalogOf(res.Rows), nil
}

// catalogOf folds rows of (schema, table, relkind, column, type), sorted
// by table, into tables.
func catalogOf(rows [][]any) *sqlcomplete.Catalog {
	kinds := map[string]string{"r": "table", "p": "table", "f": "table", "v": "view", "m": "view"}
	cat := &sqlcomplete.Catalog{}
	str := func(v any) string { s, _ := v.(string); return s }
	for _, r := range rows {
		schema, table := str(r[0]), str(r[1])
		n := len(cat.Tables)
		if n == 0 || cat.Tables[n-1].Schema != schema || cat.Tables[n-1].Name != table {
			cat.Tables = append(cat.Tables, sqlcomplete.Table{Schema: schema, Name: table, Kind: kinds[str(r[2])]})
			n++
		}
		if r[3] != nil {
			t := &cat.Tables[n-1]
			t.Columns = append(t.Columns, sqlcomplete.Column{Name: str(r[3]), Type: str(r[4])})
		}
	}
	return cat
}

// offsetAt is the byte offset of pos in text, counting characters as
// UTF-8 bytes or UTF-16 units.
func offsetAt(text string, pos lspPosition, byBytes bool) int {
	off := 0
	for range pos.Line {
		i := strings.IndexByte(text[off:], '\n')
		if i < 0 {
			return len(text)
		}
		off += i + 1
	}
	if byBytes {
		end := strings.IndexByte(text[off:], '\n')
		if end < 0 {
			end = len(text) - off
		}
		return off + min(pos.Character, end)
	}
	units := 0
	for off < len(text) && text[off] != '\n' && units < pos.Character {
		r, n := utf8.DecodeRuneInString(text[off:])
		units += len(utf16.Encode([]rune{r}))
		off += n
	}
	return off
}

func positionAt(text string, off int, byBytes bool) lspPosition {
	off = min(off, len(text))
	line := strings.Count(text[:off], "\n")
	lineStart := strings.LastIndexByte(text[:off], '\n') + 1
	if byBytes {
		return lspPosition{line, off - lineStart}
	}
	units := 0
	for _, r := range text[lineStart:off] {
		units += len(utf16.Encode([]rune{r}))
	}
	return lspPosition{line, units}
}
