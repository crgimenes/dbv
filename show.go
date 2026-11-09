package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
	"github.com/crgimenes/dbv/sqlcomplete"
)

const showUsage = `dbv show: run SQL from a file and show its rows in the grid.

Usage: dbv show [-config FILE] [-db NAME] [-at OFFSET|FROM:TO] FILE

  -at N        only the statement at byte N (cut at ; or a blank line)
  -at A:B      only bytes A to B: a selection
  -db NAME     connection, by title or number; else DBV_DB, else the
               file's "-- dbv: db=NAME", else the only one configured
  -config F    config file (default: dbv's init.filo, or DBV_CONFIG)

Reads run at once in a read-only session; a statement that writes asks
first. Rows show in the grid, where q quits; with no rows (a write, an
error) the SQL stays on screen and r runs it again. Editors call it for the
statement under the cursor: neovim/dbv.lua.

Example: dbv show -at 120 report.sql
`

func runShow(argv []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "")
	name := fs.String("db", "", "")
	at := fs.String("at", "", "")
	debug := fs.Bool("debug", false, "")
	err := fs.Parse(argv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(showUsage)
		return 0
	}
	if err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "dbv show: want one file (dbv show -h)")
		return 2
	}
	if *debug {
		if stderrIsTerminal() {
			fmt.Fprintln(os.Stderr, "dbv show: -debug writes to stderr, which is this screen; redirect it")
			return 2
		}
		debugOut = os.Stderr
	}
	b, err := os.ReadFile(filepath.Clean(fs.Arg(0)))
	if err != nil {
		return report("show", err)
	}
	text := string(b)
	sql, err := cutSQL(text, *at)
	if err != nil {
		return report("show", err)
	}
	files, want := showDB(*config, *name, text)
	var configs []db.DBConfig
	for _, f := range files {
		c, err := readDBConfigs(f)
		if err != nil {
			// a screen in the editor others may be watching; the error can quote a URL
			return report("show", fmt.Errorf("cannot read %s (dbv dbs -config %s says why)", tildePath(f), tildePath(f)))
		}
		configs = append(configs, c...)
	}
	cfg, err := pickConfig(configs, want)
	if err != nil {
		return report("show", err)
	}
	configFiles = files
	n := slices.Index(configs, cfg) + 1
	err = withApp(func(a *app) error {
		err := a.connect(cfg, n)
		if err != nil {
			return withoutURL(err, cfg)
		}
		v := &runView{sql: sql}
		a.push(v)
		v.run(a)
		return nil
	})
	return report("show", err)
}

// showDB is which configs and connection: -db and -config first; then the
// dbv this runs under (DBV_DB, DBV_CONFIG); then the file's header.
func showDB(config, name, text string) ([]string, string) {
	envFiles, envDB := os.Getenv("DBV_CONFIG"), os.Getenv("DBV_DB")
	files := []string{configPath()}
	if config != "" {
		files = []string{config}
	}
	if config == "" && envFiles != "" && envDB != "" {
		files = filepath.SplitList(envFiles)
		if name == "" {
			name = envDB // a number: only meaningful in the files it came with
		}
	}
	if name == "" {
		name = headerDB(text)
	}
	return files, name
}

// cutSQL is the part of text at says: all of it, the statement at a byte
// offset, or a FROM:TO range of bytes.
func cutSQL(text, at string) (string, error) {
	sql := text
	from, to, isRange := strings.Cut(at, ":")
	switch {
	case at == "":
	case isRange:
		a, err1 := strconv.Atoi(from)
		b, err2 := strconv.Atoi(to)
		if err1 != nil || err2 != nil || a < 0 || b < a || b > len(text) {
			return "", fmt.Errorf("-at %s: want FROM:TO within the file's %d bytes", at, len(text))
		}
		sql = text[a:b]
	default:
		n, err := strconv.Atoi(at)
		if err != nil || n < 0 || n > len(text) {
			return "", fmt.Errorf("-at %s: want a byte offset within the file's %d bytes", at, len(text))
		}
		back := strings.TrimRight(text[:n], " \t")
		if strings.HasSuffix(back, ";") {
			n = len(back) - 1 // past a ';' on its line: that statement, as in DBeaver
		}
		start, end := sqlcomplete.Statement(text, n)
		if blank(text[start:end]) && start > 0 {
			start, end = sqlcomplete.Statement(text, start-1) // the cursor just past a ';'
		}
		sql = text[start:end]
	}
	if blank(sql) {
		return "", errors.New("no SQL there")
	}
	return strings.TrimSpace(sql), nil
}

// blank is text with no statement in it: spaces and comments only.
func blank(text string) bool {
	return len(sqlcomplete.FirstWords(text)) == 0
}

// reads are statements that only read, as far as the words tell; the
// read-only session catches the rest (a WITH that deletes, a function
// that writes).
var reads = []string{"SELECT", "WITH", "VALUES", "TABLE", "SHOW", "EXPLAIN"}

func onlyReads(sql string) bool {
	for _, w := range sqlcomplete.FirstWords(sql) {
		if !slices.Contains(reads, w) {
			return false
		}
	}
	return true
}

// runView is dbv show's first screen: the SQL it runs, under the grid of
// what it answered.
type runView struct {
	sql string
}

func (v *runView) title() string { return "dbv · " + DBTitle + " · SQL" }

func (v *runView) hints() []kit.Hint {
	return []kit.Hint{{Key: "r", What: "run again"}, {Key: "q", What: "quit"}}
}

func (v *runView) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	lines := strings.Split(v.sql, "\n")
	for i, l := range lines {
		if i == rows {
			break
		}
		c.Print(top+i, left, kit.Cut(strings.ReplaceAll(l, "\t", "    "), cols), kit.Text)
	}
}

func (v *runView) key(a *app, k screen.Key) {
	switch k {
	case 'q', screen.KeyEsc, screen.Ctrl('c'):
		a.pop()
	case 'r', screen.KeyEnter:
		v.run(a)
	}
}

// run reads at once; a write waits for a yes.
func (v *runView) run(a *app) {
	if !onlyReads(v.sql) {
		v.confirm(a)
		return
	}
	var res []db.Statement
	err := a.busy("running the SQL", func(ctx context.Context) error {
		var err error
		res, err = a.pg.RunReadOnly(ctx, v.sql, queryRowsMax)
		return err
	})
	if db.IsReadOnly(err) {
		v.confirm(a)
		return
	}
	v.show(a, res, err)
}

func (v *runView) confirm(a *app) {
	a.ask("this writes · run it? y/N ", "", func(a *app, line string) {
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			a.say(kit.Plain, "not run")
			return
		}
		var res []db.Statement
		err := a.busy("running the SQL", func(ctx context.Context) error {
			var err error
			res, err = a.pg.Run(ctx, v.sql, queryRowsMax)
			return err
		})
		v.show(a, res, err)
	})
}

func (v *runView) show(a *app, res []db.Statement, err error) {
	if err != nil {
		a.fail(err)
		return
	}
	tags := make([]string, 0, len(res))
	for _, st := range res {
		tags = append(tags, st.Tag)
	}
	pushResult(a, res, tags)
	if a.top() != view(v) {
		// rows came: the grid alone, so q closes the split at once; the SQL
		// is in the editor
		a.views = slices.DeleteFunc(a.views, func(x view) bool { return x == view(v) })
	}
}
