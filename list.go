package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

const userViewType = "User defined view"

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
				Type:       userViewType,
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
	table  kit.Table
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
	l.shown = l.all
	l.table.Cursor = 0
	if filter == "" {
		return
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		a.say(kit.Warn, "filter: %v", err)
		return
	}
	a.note = "" // a complaint about a half-typed expression is over
	l.shown = nil
	for _, t := range l.all {
		if re.MatchString(t.Name) {
			l.shown = append(l.shown, t)
		}
	}
}

func (l *tableList) title() string {
	return fmt.Sprintf("dbv %s · %s", GitTag, DBTitle)
}

func (l *tableList) hints() []kit.Hint {
	return []kit.Hint{
		{Key: "↑↓", What: "move"}, {Key: "enter", What: "open"}, {Key: "/", What: "filter"},
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
	case 'q', screen.Ctrl('c'):
		a.quit = true
	case screen.KeyEsc:
		if l.filter != "" {
			l.apply(a, "")
			return
		}
		a.pop()
	case '/':
		a.ask("/", l.filter, func(a *app, line string) { l.apply(a, line) })
		a.onEdit = func(a *app, line string) { l.apply(a, line) }
		a.onTab, a.onPick = tableNames(l.all), func(name string) { l.table.Cursor = indexOfTable(l.shown, name, l.table.Cursor) }
	case ':':
		a.ask(":", "", l.command)
		a.onTab = func(line string) completion { return commandCompletion(line, listCommands) }
	case '?':
		a.push(&helpView{lines: listHelp})
	case 'y':
		if len(l.shown) > 0 {
			a.copyText("the name", l.shown[l.table.Cursor].Name)
		}
	case screen.KeyEnter:
		l.open(a)
	}
}

func (l *tableList) open(a *app) {
	if len(l.shown) == 0 {
		return
	}
	t := l.shown[l.table.Cursor]
	if t.Type == userViewType {
		openUserView(a, t.Name)
		return
	}
	v, err := newTableData(a, t.Name, t.PrimaryKey)
	if err != nil {
		a.fail(err)
		return
	}
	a.push(v)
}

var listCommands = []string{"clear", "reload", "rename", "sql", "help", "quit"}

// tableNames is Tab in a / filter: the names as they are, nothing after,
// for the filter to narrow to as the menu walks them.
func tableNames(tables []TableInfo) func(string) completion {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return func(string) completion { return completion{names: names} }
}

// indexOfTable is name's row in shown, at when it is not there.
func indexOfTable(shown []TableInfo, name string, at int) int {
	for i, t := range shown {
		if t.Name == name {
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
		l.apply(a, "")
		a.say(kit.OK, "filter cleared")
	case "help":
		a.push(&helpView{lines: listHelp})
	case "reload":
		l.reload(a)
	case "sql":
		runSQL(a)
	case "rename":
		if len(l.shown) == 0 {
			a.say(kit.Warn, "no table selected")
			return
		}
		old := l.shown[l.table.Cursor].Name
		a.ask("rename "+old+" to: ", old, func(a *app, name string) {
			name = strings.TrimSpace(name)
			if name == "" || name == old {
				a.say(kit.Warn, "same or empty name: nothing renamed")
				return
			}
			err := a.pg.RenameTable(old, name)
			if err != nil {
				a.fail(err)
				return
			}
			l.reload(a)
			a.say(kit.OK, "%s renamed to %s", old, name)
		})
	default:
		a.say(kit.Err, "unknown command %q (:help)", cmd)
	}
}

var listHelp = []string{
	"↑↓ j k        move",
	"PgUp PgDn     a page",
	"enter         open the table or view",
	"/             filter by regular expression, live; tab completes a name",
	"y             copy the table name to the clipboard",
	":q :quit      quit",
	":clear        clear the filter",
	":reload       read the table list again",
	":rename       rename the table under the cursor",
	":sql          write SQL in $EDITOR and run it (writes too)",
	"esc           back (to the database menu)",
}

// vimKey reads h j k l as arrows, the way the old interface did.
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
