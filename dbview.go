package main

import (
	"context"
	"errors"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"

	"github.com/crgimenes/dbv/sqlcomplete"
)

const viewEditNote = `-- The view as the database has it: save and quit to run this, quit
-- without saving and nothing runs. OR REPLACE keeps the columns it has:
-- new ones go at the end; to drop or rename one, start with DROP VIEW
-- (it fails while something depends on the view, unless CASCADE).
`

const errorMark = "-- dbv error: "

// editDBView opens a database view as the statement that makes it, and
// runs what is saved; after follows a run that worked.
func editDBView(a *app, name string, after func(a *app)) {
	var def string
	err := a.busy("reading "+name, func(ctx context.Context) error {
		rel, err := a.pg.Resolve(ctx, name)
		if err != nil {
			return err
		}
		def, err = a.pg.ViewDef(ctx, rel)
		return err
	})
	if err != nil {
		a.fail(err)
		return
	}
	// yagni: a change made meanwhile by someone else is replaced; a check
	// would read the definition again in the transaction that runs this.
	text := "-- dbv: db=" + DBTitle + "\n" + viewEditNote + "\n" + def
	runDDL(a, "replacing "+name, text, func(a *app) {
		a.say(kit.OK, "view %s replaced", name)
		after(a)
	})
}

// createDBView writes CREATE VIEW name in $EDITOR, from body (a grid's
// SELECT, or nothing), and runs what is saved.
func createDBView(a *app, name, body string, after func(a *app)) {
	name = strings.TrimSpace(name)
	if name == "" {
		a.say(kit.Warn, ":view NAME")
		return
	}
	body = strings.TrimRight(strings.TrimSpace(body), "; \n") + ";"
	if body == ";" {
		body = "SELECT "
	}
	text := "-- dbv: db=" + DBTitle + "\n" +
		"-- A new view in the database: save and quit to create it, quit\n" +
		"-- without saving and nothing is created.\n\n" +
		"CREATE VIEW " + ddlName(name) + " AS\n" + body + "\n"
	runDDL(a, "creating "+name, text, func(a *app) {
		a.say(kit.OK, "view %s created", name)
		after(a)
	})
}

// ddlName is a name typed for a new object as SQL: as typed when it is
// already qualified or quoted, else quoted when it needs to be, so its
// case and spaces stay its own.
func ddlName(name string) string {
	if strings.ContainsAny(name, `."`) {
		return name
	}
	return sqlName(name)
}

// runDDL opens text in $EDITOR and runs it when saved, changed or not (the
// text offered is the statement asked for), as one transaction that waits
// db.LockWait at most for a lock; a statement that loses data asks first.
// On an error the text opens again with the error on top, so a typo does
// not cost the edit; quitting without saving runs nothing. done follows a
// run that worked.
func runDDL(a *app, what, text string, done func(a *app)) {
	raw, wrote, err := a.externalSaved(envOr("EDITOR", "vi"), text)
	if err != nil {
		a.fail(err)
		return
	}
	saved, _ := edited(text, raw)
	if !wrote || strings.TrimSpace(stripComments(saved)) == "" {
		a.say(kit.Plain, "nothing run%s", lastError(text))
		return
	}
	run := func(a *app) {
		err := a.busy(what, func(ctx context.Context) error {
			_, err := a.pg.RunDDL(ctx, saved)
			return err
		})
		if errors.Is(err, context.Canceled) {
			a.fail(err)
			return
		}
		if err != nil {
			runDDL(a, what, withError(saved, err), done)
			return
		}
		done(a)
	}
	why := dangerous(saved)
	if why == "" {
		run(a)
		return
	}
	a.ask(why+" · run it? y/N ", "", func(a *app, line string) {
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			a.say(kit.Plain, "not run")
			return
		}
		run(a)
	})
}

// dangerous says what in sql can lose data, or "": dropping, emptying,
// deleting, or a column's type changed (a cast may round or fail, and the
// table is rewritten). It errs toward asking: a column named "type" asks.
func dangerous(sql string) string {
	w := sqlcomplete.AllWords(sql)
	switch {
	case w["DROP"]:
		return "this drops"
	case w["TRUNCATE"] || w["DELETE"]:
		return "this deletes rows"
	case w["ALTER"] && w["TYPE"]:
		return "this changes a type"
	}
	return ""
}

// lastError is the error a failed round put on top of text, for the note
// when the user gives up on it.
func lastError(text string) string {
	for line := range strings.Lines(text) {
		msg, ok := strings.CutPrefix(line, errorMark)
		if ok {
			return "; it failed with: " + strings.TrimSpace(msg)
		}
	}
	return ""
}

// withError puts err on top of text, under the "-- dbv: db=" line that
// must stay first, in place of the error an earlier round put there.
func withError(text string, err error) string {
	var head, b strings.Builder
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, errorMark) {
			continue
		}
		b.WriteString(line)
	}
	rest := b.String()
	if strings.HasPrefix(rest, "-- dbv: ") {
		first, after, _ := strings.Cut(rest, "\n")
		head.WriteString(first + "\n")
		rest = after
	}
	for line := range strings.Lines(err.Error()) {
		head.WriteString(errorMark + strings.TrimRight(line, "\r\n") + "\n")
	}
	return head.String() + rest
}

// createTable writes CREATE TABLE name in $EDITOR, a key to start from,
// and runs what is saved.
func createTable(a *app, name string, after func(a *app)) {
	name = strings.TrimSpace(name)
	text := "-- dbv: db=" + DBTitle + "\n" +
		"-- A new table: save and quit to create it, quit without saving and\n" +
		"-- nothing is created. e on it in the list adds columns later.\n\n" +
		"CREATE TABLE " + ddlName(name) + " (\n" +
		"    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY\n" +
		");\n"
	runDDL(a, "creating "+name, text, func(a *app) {
		a.say(kit.OK, "table %s created", name)
		after(a)
	})
}
