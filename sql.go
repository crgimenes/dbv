package main

import (
	"context"
	"slices"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"

	"github.com/crgimenes/dbv/db"
)

const sqlTemplate = `-- dbv: write SQL, then save and quit to run it. Text left as it was
-- runs nothing (to run the same again, change anything, a space will do).
-- Several statements run as one transaction; a BEGIN left open is rolled
-- back.

`

// runSQL is :sql. The text opens in $EDITOR (the last one run, or the
// template), and what is saved runs as typed, writes included: the user is
// the one typing it. The last statement that returns rows shows as a grid.
func runSQL(a *app) {
	text := a.sqlText
	if text == "" {
		text = sqlTemplate
	}
	d, ok := a.top().(*tableData)
	if ok && d.sql != "" {
		text = sqlTemplate + strings.TrimSpace(d.sql) + "\n" // the view as it ran, parameters filled
	}
	if ok && !d.query {
		err := a.busy("reading "+d.name, func(ctx context.Context) error {
			q, err := d.selectSQL(ctx, a.pg, d.selectList())
			text = sqlTemplate + q + ";\n" // what the grid shows, to start from
			return err
		})
		if err != nil {
			a.fail(err)
			return
		}
	}
	raw, err := a.edit(text)
	if err != nil {
		a.fail(err)
		return
	}
	sqlText, changed := edited(text, raw)
	if !changed || strings.TrimSpace(sqlText) == "" {
		return // quit without saving: an INSERT run twice is a second row
	}
	a.sqlText = sqlText

	var res []db.Statement
	start := debugNow()
	err = a.busy("running the SQL", func(ctx context.Context) error {
		var err error
		res, err = a.pg.Run(ctx, sqlText, queryRowsMax)
		return err
	})
	debugEvent("sql", start, "statements", len(res), "err", err)
	if err != nil {
		a.fail(err)
		return
	}

	tags := make([]string, 0, len(res))
	wrote := false
	for _, st := range res {
		tags = append(tags, st.Tag)
		wrote = wrote || !strings.HasPrefix(st.Tag, "SELECT")
	}
	if wrote {
		if ok && !d.query {
			d.reload(a) // the rows on screen may be among the changed ones
		}
	}
	pushResult(a, res, tags)
}

// pushResult shows the last statement of res that returns rows as a grid,
// and the tags in the note.
func pushResult(a *app, res []db.Statement, tags []string) {
	for _, st := range slices.Backward(res) {
		if len(st.Columns) == 0 {
			continue
		}
		g := &tableData{name: "SQL", pk: "-", readOnly: true, query: true, columns: st.Columns, truncated: st.Truncated}
		g.setHeader()
		for _, r := range st.Rows {
			g.add(r)
		}
		g.total = len(g.rows)
		g.measure()
		a.push(g)
		if st.Truncated {
			a.say(kit.Warn, "%s · showing the first %d rows", strings.Join(tags, "; "), queryRowsMax)
			return
		}
		break
	}
	a.say(kit.OK, "%s", strings.Join(tags, "; "))
}
