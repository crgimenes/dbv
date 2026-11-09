package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

// structFields are the structure screen's columns; e edits the one under
// the cursor.
var structFields = []string{"NAME", "TYPE", "NULL", "DEFAULT", "KEY", "COMMENT"}

const (
	fName = iota
	fType
	fNull
	fDefault
	fKey
	fComment
)

// structView is a table's columns, then its constraints and indexes, each
// change an ALTER TABLE written in $EDITOR. row runs over both: past the
// columns it is a constraint.
type structView struct {
	name  string
	rel   db.Rel
	cols  []db.Column
	cons  []db.Constraint
	row   int
	field int
	first int // the first line shown, of lines()
}

var kinds = map[string]string{
	"p": "primary key", "u": "unique", "f": "foreign key", "c": "check", "x": "exclusion", "i": "index",
}

func newStructView(a *app, name string) (*structView, error) {
	s := &structView{name: name}
	return s, s.read(a)
}

func (s *structView) read(a *app) error {
	return a.busy("reading "+s.name, func(ctx context.Context) error {
		rel, err := a.pg.Resolve(ctx, s.name)
		if err != nil {
			return err
		}
		cols, err := a.pg.Structure(ctx, rel)
		if err != nil {
			return err
		}
		cons, err := a.pg.Constraints(ctx, rel)
		if err != nil {
			return err
		}
		s.rel, s.cols, s.cons = rel, cols, cons
		s.row = max(0, min(s.row, s.count()-1))
		return nil
	})
}

func (s *structView) count() int { return len(s.cols) + len(s.cons) }

// con is the constraint under the cursor, nil on a column.
func (s *structView) con() *db.Constraint {
	if s.row < len(s.cols) || s.row >= s.count() {
		return nil
	}
	return &s.cons[s.row-len(s.cols)]
}

// line is the row's line on screen: the constraints come after a blank
// line and their own header.
func (s *structView) line(row int) int {
	if row < len(s.cols) {
		return row
	}
	return row + 2
}

func (s *structView) title() string { return "Structure of " + s.name + " · " + DBTitle }

func (s *structView) hints() []kit.Hint {
	return []kit.Hint{
		{Key: "↑↓←→", What: "move"}, {Key: "e", What: "edit"}, {Key: "a", What: "add"},
		{Key: "D", What: "drop"}, {Key: ":", What: "command"}, {Key: "?", What: "help"}, {Key: "esc", What: "back"},
	}
}

func (s *structView) status() string {
	c := s.con()
	if c != nil {
		return fmt.Sprintf("%d of %d · %s", s.row-len(s.cols)+1, len(s.cons), kinds[c.Kind])
	}
	if len(s.cols) == 0 {
		return "no columns"
	}
	return fmt.Sprintf("column %d of %d · %s", s.row+1, len(s.cols), strings.ToLower(structFields[s.field]))
}

func (s *structView) cells(c db.Column) []string {
	null := "null"
	if c.NotNull {
		null = "not null"
	}
	def := c.Default
	switch {
	case c.Identity == "a":
		def = "identity always"
	case c.Identity == "d":
		def = "identity by default"
	case c.Generated == "s":
		def = "generated: " + c.Default
	}
	key := ""
	if c.Key {
		key = "pk"
	}
	return []string{c.Name, c.Type, null, def, key, c.Comment}
}

func (s *structView) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	fc := kit.FillCols([]kit.Col{
		{Header: "NAME", Width: max(10, cols/5)}, {Header: "TYPE", Width: max(10, cols/4)},
		{Header: "NULL", Width: 8}, {Header: "DEFAULT", Width: max(10, cols/5)},
		{Header: "KEY", Width: 3}, {Header: "COMMENT"},
	}, cols)
	at := left
	for _, f := range fc {
		c.Print(top, at, kit.Pad(f.Header, f.Width), kit.Soft)
		at += f.Width + 1
	}
	c.Fill(top+1, left, 1, cols, '─', kit.Dim)
	body := rows - 2
	cur := s.line(s.row)
	s.first = min(s.first, cur)
	if cur >= s.first+body {
		s.first = cur - body + 1
	}
	conW := []int{fc[0].Width, 12, max(1, cols-fc[0].Width-14)}
	lines := s.line(s.count())
	for r := 0; r < body && s.first+r < lines; r++ {
		ln, y := s.first+r, top+2+r
		switch {
		case ln < len(s.cols):
			at = left
			for j, v := range s.cells(s.cols[ln]) {
				pen := kit.Text
				if ln == s.row && j == s.field {
					pen = kit.Focus
				}
				c.Print(y, at, kit.Pad(kit.Cut(v, fc[j].Width), fc[j].Width), pen)
				at += fc[j].Width + 1
			}
		case ln == len(s.cols)+1:
			c.Print(y, left, kit.Pad("INDEX OR CONSTRAINT", conW[0]+1)+kit.Pad("KIND", conW[1]+1)+"DEFINITION", kit.Soft)
		case ln > len(s.cols)+1:
			i := ln - 2
			k := s.cons[i-len(s.cols)]
			pen := kit.Text
			if i == s.row {
				pen = kit.Focus
			}
			line := kit.Pad(kit.Cut(k.Name, conW[0]), conW[0]+1) + kit.Pad(kinds[k.Kind], conW[1]+1) + kit.Cut(k.Def, conW[2])
			c.Print(y, left, kit.Pad(line, cols), pen)
		}
	}
}

func (s *structView) key(a *app, k screen.Key) {
	_, h := a.t.Size()
	switch vimKey(k) {
	case screen.KeyUp:
		s.row = max(0, s.row-1)
	case screen.KeyDown:
		s.row = max(0, min(s.count()-1, s.row+1))
	case screen.KeyPgUp:
		s.row = max(0, s.row-(h-8))
	case screen.KeyPgDn:
		s.row = max(0, min(s.count()-1, s.row+(h-8)))
	case screen.KeyLeft:
		s.field = max(0, s.field-1)
	case screen.KeyRight:
		s.field = min(len(structFields)-1, s.field+1)
	case 'g', screen.KeyHome:
		s.row = 0
	case 'G', screen.KeyEnd:
		s.row = max(0, s.count()-1)
	case 'e', screen.KeyEnter:
		s.edit(a)
	case 'a':
		if s.con() != nil {
			s.addCon(a)
			return
		}
		s.add(a, "")
	case 'D':
		s.drop(a)
	case 'q', screen.KeyEsc:
		a.pop()
	case ':':
		a.ask(":", "", s.command)
		a.onTab = func(line string) completion { return commandCompletion(line, structCommands) }
	case '?':
		a.push(&helpView{lines: structHelp})
	}
}

var structCommands = []string{"add", "constraint", "reload", "sql", "quit"}

func (s *structView) command(a *app, line string) {
	cmd := strings.TrimSpace(line)
	switch {
	case cmd == "":
	case cmd == "q" || cmd == "quit":
		a.pop()
	case cmd == "reload":
		s.changed(a)
	case cmd == "sql":
		runSQL(a)
	case cmd == "constraint" || cmd == "index":
		s.addCon(a)
	case cmd == "add" || strings.HasPrefix(cmd, "add "):
		s.add(a, strings.TrimSpace(strings.TrimPrefix(cmd, "add")))
	default:
		a.say(kit.Err, "unknown command %q (?)", cmd)
	}
}

var structHelp = []string{
	"↑↓ j k        move between columns, then indexes and constraints",
	"←→ h l        move between a column's fields",
	"e enter       edit the field under the cursor as ALTER TABLE in $EDITOR;",
	"              on a constraint or index, drop and add it again, edited",
	"a :add NAME   add a column",
	"a :constraint add a key, unique, foreign key, check or index (on that list)",
	"D             drop the column, constraint or index (asks before running)",
	"",
	"Save and quit the editor to run the statement; quit without saving and",
	"nothing runs. A failed statement opens again with the error on top.",
	"A drop or a type change asks y/N first. A lock not granted in " + db.LockWait,
	"fails the statement instead of holding up the table.",
	"",
	":reload       read the structure again",
	":sql          write SQL in $EDITOR and run it",
	"esc q :q      back",
}

// alter is the head every column statement shares.
func (s *structView) alter() string { return "ALTER TABLE " + s.rel.SQL }

// edit writes the statement that changes the field under the cursor.
func (s *structView) edit(a *app) {
	k := s.con()
	if k != nil {
		s.editCon(a, *k)
		return
	}
	if len(s.cols) == 0 {
		return
	}
	c := s.cols[s.row]
	col := db.QuoteName(c.Name)
	var text string
	switch s.field {
	case fName:
		text = s.alter() + " RENAME COLUMN " + col + " TO " + col + ";\n"
	case fType:
		text = s.alter() + " ALTER COLUMN " + col + " TYPE " + c.Type + ";\n" +
			"-- where no cast is automatic, end it with USING " + col + "::NEW_TYPE\n"
	case fNull:
		set := "SET NOT NULL"
		if c.NotNull {
			set = "DROP NOT NULL"
		}
		text = s.alter() + " ALTER COLUMN " + col + " " + set + ";\n"
	case fDefault:
		if c.Identity != "" || c.Generated != "" {
			a.say(kit.Warn, "an identity or generated column: its default is not an expression to edit (:sql)")
			return
		}
		text = s.alter() + " ALTER COLUMN " + col + " SET DEFAULT " + c.Default + ";\n"
		if c.Default != "" {
			text += "-- or no default: " + s.alter() + " ALTER COLUMN " + col + " DROP DEFAULT;\n"
		}
	case fKey:
		a.say(kit.Warn, "the key is its primary key constraint, in the list below the columns")
		return
	case fComment:
		text = "COMMENT ON COLUMN " + s.rel.SQL + "." + col + " IS " + db.QuoteLiteral(c.Comment) + ";\n" +
			"-- IS NULL removes it\n"
	}
	s.run(a, "changing "+c.Name, text)
}

// add writes ADD COLUMN, with name when one was typed.
func (s *structView) add(a *app, name string) {
	col := "new_column"
	if name != "" {
		col = ddlName(name)
	}
	text := s.alter() + " ADD COLUMN " + col + " text;\n" +
		"-- NOT NULL on a table with rows needs a DEFAULT; constant ones do not rewrite the table\n"
	s.run(a, "adding a column", text)
}

func (s *structView) drop(a *app) {
	k := s.con()
	if k != nil {
		s.run(a, "dropping "+k.Name, s.dropCon(*k)+";\n"+dependents(*k))
		return
	}
	if len(s.cols) == 0 {
		return
	}
	text := s.alter() + " DROP COLUMN " + db.QuoteName(s.cols[s.row].Name) + ";\n" +
		"-- its data goes with it; a view that uses it fails this, unless CASCADE drops the view\n"
	s.run(a, "dropping "+s.cols[s.row].Name, text)
}

// dropCon is the statement that drops k, without its semicolon.
func (s *structView) dropCon(k db.Constraint) string {
	if k.Kind == "i" {
		return "DROP INDEX " + k.SQL
	}
	return s.alter() + " DROP CONSTRAINT " + db.QuoteName(k.Name)
}

func dependents(k db.Constraint) string {
	switch k.Kind {
	case "p", "u":
		return "-- a foreign key that references it fails this, unless CASCADE drops the foreign key\n"
	case "f", "c":
		return "-- adding it back checks every row; NOT VALID skips the rows already there\n"
	}
	return ""
}

// editCon writes k dropped and made again, in one transaction: an edit
// that fails leaves the old one.
func (s *structView) editCon(a *app, k db.Constraint) {
	text := s.dropCon(k) + ";\n" + k.Def + ";\n"
	if k.Kind != "i" {
		text = s.alter() + "\n    DROP CONSTRAINT " + db.QuoteName(k.Name) + ",\n    ADD CONSTRAINT " + db.QuoteName(k.Name) + " " + k.Def + ";\n"
	}
	s.run(a, "changing "+k.Name, text+dependents(k))
}

// addCon writes every kind of constraint as a comment, the column under the
// cursor in each: the one uncommented runs, none runs nothing.
func (s *structView) addCon(a *app) {
	col := "col"
	if s.row < len(s.cols) {
		col = db.QuoteName(s.cols[s.row].Name)
	}
	t := s.rel.SQL
	text := "-- Uncomment the one to add, and name it:\n" +
		"-- " + s.alter() + " ADD CONSTRAINT name PRIMARY KEY (" + col + ");\n" +
		"-- " + s.alter() + " ADD CONSTRAINT name UNIQUE (" + col + ");\n" +
		"-- " + s.alter() + " ADD CONSTRAINT name FOREIGN KEY (" + col + ") REFERENCES other_table (id);\n" +
		"-- " + s.alter() + " ADD CONSTRAINT name CHECK (" + col + " > 0);\n" +
		"-- CREATE INDEX name ON " + t + " (" + col + ");\n" +
		"-- CREATE INDEX CONCURRENTLY does not lock out writes, but runs only alone in the text\n"
	s.run(a, "adding to "+s.name, text)
}

func (s *structView) run(a *app, what, text string) {
	text = "-- dbv: db=" + DBTitle + "\n" +
		"-- Save and quit to run this, quit without saving and nothing runs.\n\n" + text
	runDDL(a, what, text, func(a *app) {
		a.say(kit.OK, "%s: done", s.name)
		s.changed(a)
	})
}

// changed reads the structure again, and the grids of this table and the
// table list under it, which show its columns too.
func (s *structView) changed(a *app) {
	note, tone := a.note, a.tone
	err := s.read(a)
	if err != nil {
		a.fail(err)
		return
	}
	for _, v := range a.views {
		d, ok := v.(*tableData)
		if ok && d.name == s.name && !d.query {
			d.reload(a)
		}
	}
	listSaved(a, s.name, "table", true)
	if a.tone != kit.Err {
		a.note, a.tone = note, tone
	}
}
