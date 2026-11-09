package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

type TableInfo struct {
	Name       string
	Type       string
	Size       string
	PrimaryKey string
}

// queryType is a .sql file of the user's; "view" is the database's alone.
const queryType = "query"

func getTableList(pg *db.Postgres) ([]TableInfo, error) {
	lt, err := pg.ListTablesAndViews()
	if err != nil {
		return nil, err
	}

	data := make([]TableInfo, len(lt))
	for i, t := range lt {
		pk := "-"
		if t.PrimaryKey.Valid {
			pk = t.PrimaryKey.String
		}
		data[i] = TableInfo{Name: t.Name, Type: t.Type, Size: t.Size, PrimaryKey: pk}
	}

	if userViewsDir == "" {
		return data, nil
	}
	files, err := os.ReadDir(userViewsDir)
	if err != nil {
		return data, nil // a views directory that went away is not a reason to hide the tables
	}
	var views []TableInfo
	for _, f := range files {
		if !f.IsDir() && filepath.Ext(f.Name()) == ".sql" {
			views = append(views, TableInfo{
				Name:       strings.TrimSuffix(f.Name(), ".sql"),
				Type:       queryType,
				Size:       "-",
				PrimaryKey: "-",
			})
		}
	}
	return append(views, data...), nil
}

// tableList is the first screen: every table and view, a regex filter.
type tableList struct {
	all    []TableInfo
	shown  []TableInfo
	filter string
	kind   int // of kindsShown: t walks them
	table  kit.Table
}

// kindsShown are what t walks: everything, then one kind of row at a time.
var kindsShown = []struct {
	name  string
	types []string
}{
	{"", nil},
	{"tables", []string{"table"}},
	{"views", []string{"view", "materialized view"}},
	{"queries", []string{queryType}},
}

func newTableList(a *app) view {
	l := &tableList{}
	l.reload(a)
	return l
}

func (l *tableList) reload(a *app) {
	data, err := getTableList(a.pg)
	if err != nil {
		a.fail(err)
	}
	l.all = data
	l.apply(a, l.filter)
}

func (l *tableList) apply(a *app, filter string) {
	l.filter = filter
	l.table.Cursor = 0
	types := kindsShown[l.kind].types
	re, err := regexp.Compile(filter)
	if err != nil {
		a.say(kit.Warn, "filter: %v", err)
		re = nil // the kind still applies
	}
	if err == nil && filter != "" {
		a.note = "" // a complaint about a half-typed expression is over
	}
	l.shown = nil
	for _, t := range l.all {
		if (types == nil || slices.Contains(types, t.Type)) && (re == nil || re.MatchString(t.Name)) {
			l.shown = append(l.shown, t)
		}
	}
}

func (l *tableList) title() string {
	return fmt.Sprintf("dbv %s · %s", GitTag, DBTitle)
}

func (l *tableList) hints() []kit.Hint {
	return []kit.Hint{
		{Key: "↑↓", What: "move"}, {Key: "enter", What: "open"}, {Key: "/", What: "filter"}, {Key: "t", What: "kind"},
		{Key: ":", What: "command"}, {Key: "?", What: "help"}, {Key: "q", What: "quit"},
	}
}

func (l *tableList) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	cols4 := kit.FillCols([]kit.Col{
		{Header: "NAME", Width: max(12, cols*2/5)},
		{Header: "TYPE", Width: max(8, cols/6)},
		{Header: "SIZE", Width: 10},
		{Header: "PRIMARY KEY", Width: 0},
	}, cols)
	l.table.Draw(c, top, left, cols4, len(l.shown), func(i int) []string {
		t := l.shown[i]
		return []string{t.Name, t.Type, kit.PadLeft(t.Size, 10), t.PrimaryKey}
	}, rows-2)
}

func (l *tableList) status() string {
	s := fmt.Sprintf("%d of %d", min(l.table.Cursor+1, len(l.shown)), len(l.shown))
	if l.kind > 0 {
		s += " · " + kindsShown[l.kind].name
	}
	if l.filter != "" {
		s += " · /" + l.filter
	}
	return s
}

func (l *tableList) key(a *app, k screen.Key) {
	_, h := a.t.Size()
	if l.table.Move(vimKey(k), len(l.shown), h-8) {
		return
	}
	switch k {
	case 'q':
		a.quit = true
	case screen.KeyEsc:
		if l.filter != "" || l.kind != 0 {
			l.kind = 0
			l.apply(a, "")
			return
		}
		a.pop()
	case '/':
		a.ask("/", l.filter, func(a *app, line string) { l.apply(a, line) })
		a.onEdit = func(a *app, line string) { l.apply(a, line) }
		a.onTab, a.onPick = tableNames(l.all), func(name string) { l.table.Cursor = indexOfTable(l.shown, name, "", l.table.Cursor) }
	case ':':
		a.ask(":", "", l.command)
		a.onTab = func(line string) completion { return commandCompletion(line, listCommands) }
	case '?':
		a.push(&helpView{lines: listHelp})
	case 'y':
		if len(l.shown) > 0 {
			a.copyText("the name", l.shown[l.table.Cursor].Name)
		}
	case 'n':
		l.newQuery(a, "")
	case 't':
		l.nextKind(a)
	case 'e':
		l.edit(a)
	case screen.KeyEnter:
		l.open(a)
	}
}

func (l *tableList) open(a *app) {
	if len(l.shown) == 0 {
		return
	}
	t := l.shown[l.table.Cursor]
	if t.Type == queryType {
		openQuery(a, t.Name)
		return
	}
	v, err := newTableData(a, t.Name, t.PrimaryKey)
	if err != nil {
		a.fail(err)
		return
	}
	a.push(v)
}

// edit opens what is under the cursor as text: a query's file, a view's
// CREATE OR REPLACE.
func (l *tableList) edit(a *app) {
	if len(l.shown) == 0 {
		return
	}
	t := l.shown[l.table.Cursor]
	switch t.Type {
	case queryType:
		l.saved(a, t.Name, queryType, editQuery(a, t.Name, ""))
	case "view":
		editDBView(a, t.Name, func(a *app) { l.saved(a, t.Name, "view", true) })
	case "table":
		v, err := newStructView(a, t.Name)
		if err != nil {
			a.fail(err)
			return
		}
		a.push(v)
	case "materialized view":
		a.say(kit.Warn, "a materialized view has no OR REPLACE: :sql with DROP and CREATE")
	default:
		a.say(kit.Warn, "e edits a table, a view or a query")
	}
}

var listCommands = []string{"clear", "reload", "rename", "query", "view", "table", "sql", "help", "quit"}

// nextKind shows the next kind of row only, the filter kept.
func (l *tableList) nextKind(a *app) {
	l.kind = (l.kind + 1) % len(kindsShown)
	l.apply(a, l.filter)
}

// newQuery asks for a name when there is none, then opens the new query.
func (l *tableList) newQuery(a *app, name string) {
	if name != "" {
		l.saved(a, name, queryType, editQuery(a, name, ""))
		return
	}
	a.ask("new query: ", "", func(a *app, name string) {
		if strings.TrimSpace(name) != "" {
			l.saved(a, name, queryType, editQuery(a, name, ""))
		}
	})
}

// saved reads the list again after a query or a view was written, the
// cursor on it.
func (l *tableList) saved(a *app, name, typ string, ok bool) {
	if !ok {
		return
	}
	note, tone := a.note, a.tone
	l.reload(a)
	if typ == queryType {
		name, _ = queryName(name)
	}
	l.table.Cursor = indexOfTable(l.shown, name, typ, l.table.Cursor)
	a.note, a.tone = note, tone // the reload must not eat "query saved"
}

// listSaved is saved for the table list under the screens on top of it.
func listSaved(a *app, name, typ string, ok bool) {
	for _, v := range a.views {
		l, isList := v.(*tableList)
		if isList {
			l.saved(a, name, typ, ok)
		}
	}
}

// tableNames is Tab in a / filter: the names as they are, nothing after,
// for the filter to narrow to as the menu walks them.
func tableNames(tables []TableInfo) func(string) completion {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return func(string) completion { return completion{names: names} }
}

// indexOfTable is the row of name of type typ (any type when "") in
// shown, at when it is not there: a query and a view may share a name.
func indexOfTable(shown []TableInfo, name, typ string, at int) int {
	for i, t := range shown {
		if t.Name == name && (typ == "" || t.Type == typ) {
			return i
		}
	}
	return at
}

func (l *tableList) command(a *app, line string) {
	cmd := strings.TrimSpace(line)
	switch cmd {
	case "":
	case "q", "quit":
		a.quit = true
	case "clear":
		l.kind = 0
		l.apply(a, "")
		a.say(kit.OK, "filter cleared")
	case "help":
		a.push(&helpView{lines: listHelp})
	case "reload":
		l.reload(a)
	case "sql":
		runSQL(a)
	case "query":
		l.newQuery(a, "")
	case "view", "table":
		a.say(kit.Warn, ":%s NAME", cmd)
	case "rename":
		if len(l.shown) == 0 {
			a.say(kit.Warn, "no table selected")
			return
		}
		old, typ := l.shown[l.table.Cursor].Name, l.shown[l.table.Cursor].Type
		a.ask("rename "+old+" to: ", old, func(a *app, name string) {
			name = strings.TrimSpace(name)
			if name == "" || name == old {
				a.say(kit.Warn, "same or empty name: nothing renamed")
				return
			}
			err := a.busy("renaming "+old, func(ctx context.Context) error {
				if typ == queryType {
					return renameQuery(old, name)
				}
				return a.pg.Rename(ctx, old, name)
			})
			if err != nil {
				a.fail(err)
				return
			}
			l.reload(a)
			if typ == queryType {
				name, _ = queryName(name)
			}
			l.table.Cursor = indexOfTable(l.shown, name, typ, l.table.Cursor)
			a.say(kit.OK, "%s renamed to %s", old, name)
		})
	default:
		name, ok := strings.CutPrefix(cmd, "query ")
		if ok {
			l.newQuery(a, name)
			return
		}
		name, ok = strings.CutPrefix(cmd, "table ")
		if ok {
			createTable(a, name, func(a *app) { l.saved(a, strings.TrimSpace(name), "table", true) })
			return
		}
		name, ok = strings.CutPrefix(cmd, "view ")
		if ok {
			createDBView(a, name, "", func(a *app) { l.saved(a, strings.TrimSpace(name), "view", true) })
			return
		}
		a.say(kit.Err, "unknown command %q (:help)", cmd)
	}
}

var listHelp = []string{
	"↑↓ j k        move",
	"PgUp PgDn ^F ^B  a page",
	"enter         open the table or view; a query runs (its form first)",
	"/             filter by regular expression, live; tab completes a name",
	"t             only tables, only views, only queries, all again",
	"y             copy the table name to the clipboard",
	":q :quit      quit",
	":clear        clear the filter and the kind",
	":reload       read the table list again",
	":rename       rename the table, view or query under the cursor",
	":sql          write SQL in $EDITOR and run it (writes too)",
	"n :query NAME a new query of yours, written in $EDITOR",
	":view NAME    a new view in the database, written in $EDITOR",
	"e             edit what is under the cursor: a table's structure,",
	"              a view's CREATE OR REPLACE, a query's file",
	":table NAME   a new table, written in $EDITOR as CREATE TABLE",
	"esc           clear the filter and the kind; then back (to the database menu)",
	"^C ^Z ^L      quit, suspend (fg resumes), redraw",
}

// vimKey reads h j k l as arrows and Ctrl-F Ctrl-B as pages, as vi and
// less do.
func vimKey(k screen.Key) screen.Key {
	switch k {
	case 'j':
		return screen.KeyDown
	case 'k':
		return screen.KeyUp
	case 'h':
		return screen.KeyLeft
	case 'l':
		return screen.KeyRight
	case screen.Ctrl('f'):
		return screen.KeyPgDn
	case screen.Ctrl('b'):
		return screen.KeyPgUp
	}
	return k
}

// helpView is a page of text; any key closes it.
type helpView struct {
	lines []string
}

func (h *helpView) title() string     { return "Help" }
func (h *helpView) hints() []kit.Hint { return []kit.Hint{{Key: "any key", What: "back"}} }

func (h *helpView) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	for i, line := range h.lines {
		if i >= rows-1 {
			break
		}
		c.Print(top+1+i, left+1, kit.Cut(line, cols-2), kit.Text)
	}
}

func (h *helpView) key(a *app, _ screen.Key) { a.pop() }
