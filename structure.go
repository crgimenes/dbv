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

// structView is a table's columns, each change an ALTER TABLE written in
// $EDITOR.
type structView struct {
	name  string
	rel   db.Rel
	cols  []db.Column
	row   int
	field int
	first int
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
		s.rel, s.cols = rel, cols
		s.row = max(0, min(s.row, len(cols)-1))
		return nil
	})
}

func (s *structView) title() string { return "Structure of " + s.name + " · " + DBTitle }

func (s *structView) hints() []kit.Hint {
	return []kit.Hint{
		{Key: "↑↓←→", What: "move"}, {Key: "e", What: "edit"}, {Key: "a", What: "add"},
		{Key: "D", What: "drop"}, {Key: ":", What: "command"}, {Key: "?", What: "help"}, {Key: "esc", What: "back"},
	}
}

func (s *structView) status() string {
	if len(s.cols) == 0 {
		return "no columns"
	}
	return fmt.Sprintf("%d of %d · %s", s.row+1, len(s.cols), strings.ToLower(structFields[s.field]))
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
	s.first = min(s.first, s.row)
	if s.row >= s.first+body {
		s.first = s.row - body + 1
	}
	for r := 0; r < body && s.first+r < len(s.cols); r++ {
		i := s.first + r
		at = left
		for j, v := range s.cells(s.cols[i]) {
			pen := kit.Text
			if i == s.row && j == s.field {
				pen = kit.Focus
			}
			c.Print(top+2+r, at, kit.Pad(kit.Cut(v, fc[j].Width), fc[j].Width), pen)
			at += fc[j].Width + 1
		}
	}
}

func (s *structView) key(a *app, k screen.Key) {
	_, h := a.t.Size()
	switch vimKey(k) {
	case screen.KeyUp:
		s.row = max(0, s.row-1)
	case screen.KeyDown:
		s.row = max(0, min(len(s.cols)-1, s.row+1))
	case screen.KeyPgUp:
		s.row = max(0, s.row-(h-8))
	case screen.KeyPgDn:
		s.row = max(0, min(len(s.cols)-1, s.row+(h-8)))
	case screen.KeyLeft:
		s.field = max(0, s.field-1)
	case screen.KeyRight:
		s.field = min(len(structFields)-1, s.field+1)
	case 'g', screen.KeyHome:
		s.row = 0
	case 'G', screen.KeyEnd:
		s.row = max(0, len(s.cols)-1)
	case 'e', screen.KeyEnter:
		s.edit(a)
	case 'a':
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

var structCommands = []string{"add", "reload", "sql", "quit"}

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
	case cmd == "add" || strings.HasPrefix(cmd, "add "):
		s.add(a, strings.TrimSpace(strings.TrimPrefix(cmd, "add")))
	default:
		a.say(kit.Err, "unknown command %q (?)", cmd)
	}
}

var structHelp = []string{
	"↑↓ j k        move between columns of the table",
	"←→ h l        move between fields",
	"e enter       edit the field under the cursor as ALTER TABLE in $EDITOR",
	"a :add NAME   add a column",
	"D             drop the column (asks before running)",
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
		a.say(kit.Warn, "the primary key is not edited here yet (:sql)")
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
	if len(s.cols) == 0 {
		return
	}
	text := s.alter() + " DROP COLUMN " + db.QuoteName(s.cols[s.row].Name) + ";\n" +
		"-- its data goes with it; a view that uses it fails this, unless CASCADE drops the view\n"
	s.run(a, "dropping "+s.cols[s.row].Name, text)
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
