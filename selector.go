package main

import (
	"fmt"
	"regexp"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

// selectorOutputFile is where neovim/rundbv.lua watches for what was picked.
const selectorOutputFile = "/tmp/dbv_output.txt"

// selector picks a table or a column name (or a struct, an INSERT) and
// writes it to the output file, for an editor beside dbv to insert.
type selector struct {
	out    string
	tables []TableInfo
	shownT []TableInfo
	fields []db.ColumnInfo
	table  string // set while choosing a column of it
	filter string
	list   kit.Table
	keepT  int // the table cursor while the columns are up
}

func newSelector(out string) func(a *app) view {
	return func(a *app) view {
		s := &selector{out: out}
		tables, err := getTableList(a.pg)
		if err != nil {
			a.fail(err)
		}
		for _, t := range tables {
			if t.Type != userViewType {
				s.tables = append(s.tables, t)
			}
		}
		s.apply(a, "")
		return s
	}
}

func (s *selector) apply(a *app, filter string) {
	s.filter = filter
	s.shownT = s.tables
	s.list.Cursor = 0
	if filter == "" || s.table != "" {
		return
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		a.say(kit.Warn, "filter: %v", err)
		return
	}
	a.note = "" // a complaint about a half-typed expression is over
	s.shownT = nil
	for _, t := range s.tables {
		if re.MatchString(t.Name) {
			s.shownT = append(s.shownT, t)
		}
	}
}

func (s *selector) title() string {
	if s.table != "" {
		return "Columns of " + s.table + " · " + DBTitle
	}
	return "Pick a table · " + DBTitle
}

func (s *selector) hints() []kit.Hint {
	if s.table != "" {
		return []kit.Hint{{Key: "↑↓", What: "move"}, {Key: "space", What: "pick"}, {Key: "esc tab", What: "tables"}, {Key: "q", What: "quit"}}
	}
	return []kit.Hint{
		{Key: "↑↓", What: "move"}, {Key: "space", What: "pick"}, {Key: "tab", What: "columns"},
		{Key: "S", What: "struct"}, {Key: "I", What: "insert"}, {Key: "/", What: "filter"}, {Key: "q", What: "quit"},
	}
}

func (s *selector) count() int {
	if s.table != "" {
		return len(s.fields)
	}
	return len(s.shownT)
}

func (s *selector) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	if s.table != "" {
		cs := kit.FillCols([]kit.Col{{Header: "COLUMN", Width: max(12, cols/2)}, {Header: "TYPE"}}, cols)
		s.list.Draw(c, top, left, cs, len(s.fields), func(i int) []string {
			return []string{s.fields[i].ColumnName, s.fields[i].DataType}
		}, rows-2)
		return
	}
	cs := kit.FillCols([]kit.Col{{Header: "NAME", Width: max(12, cols/2)}, {Header: "TYPE", Width: 10}, {Header: "PRIMARY KEY"}}, cols)
	s.list.Draw(c, top, left, cs, len(s.shownT), func(i int) []string {
		t := s.shownT[i]
		return []string{t.Name, t.Type, t.PrimaryKey}
	}, rows-2)
}

func (s *selector) write(a *app, what, text string) {
	err := writeOutput(s.out, text)
	if err != nil {
		a.fail(err)
		return
	}
	a.say(kit.OK, "%s written to %s", what, s.out)
}

func (s *selector) current() string {
	if s.count() == 0 {
		return ""
	}
	if s.table != "" {
		return s.fields[s.list.Cursor].ColumnName
	}
	return s.shownT[s.list.Cursor].Name
}

func (s *selector) key(a *app, k screen.Key) {
	_, h := a.t.Size()
	if s.list.Move(vimKey(k), s.count(), h-8) {
		return
	}
	name := s.current()
	switch {
	case k == 'q':
		a.quit = true
	case s.table != "" && (k == screen.KeyEsc || k == screen.KeyTab || k == screen.KeyBackTab || k == screen.KeyBackspace):
		s.table, s.list.Cursor = "", s.keepT
	case k == screen.KeyEsc && s.filter != "":
		s.apply(a, "")
	case k == screen.KeyEsc:
		a.quit = true
	case name == "":
	case k == ' ' || k == 's':
		s.write(a, name, name)
	case s.table != "":
	case k == screen.KeyTab || k == screen.KeyEnter:
		s.columns(a, name)
	case k == 'S':
		s.generate(a, name, "struct", a.pg.CreateGoStructDefinition)
	case k == 'I':
		s.generate(a, name, "INSERT", a.pg.CreateInsertStatement)
	case k == '/':
		a.ask("/", s.filter, func(a *app, line string) { s.apply(a, line) })
		a.onEdit = func(a *app, line string) { s.apply(a, line) }
		a.onTab, a.onPick = tableNames(s.tables), func(name string) { s.list.Cursor = indexOfTable(s.shownT, name, s.list.Cursor) }
	}
}

func (s *selector) columns(a *app, table string) {
	fields, err := a.pg.ListColumns(table)
	if err != nil {
		a.fail(err)
		return
	}
	s.fields, s.table, s.keepT, s.list.Cursor = fields, table, s.list.Cursor, 0
}

func (s *selector) generate(a *app, table, what string, gen func(string) (string, error)) {
	text, err := gen(table)
	if err != nil {
		a.fail(err)
		return
	}
	s.write(a, fmt.Sprintf("%s of %s", what, table), text)
}
