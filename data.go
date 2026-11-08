package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

const (
	recordsPerPage = 200
	maxCellWidth   = 40
	colGap         = 2
)

type orderBy struct {
	col string
	dir string
}

// tableData is a table's rows (or a query's) in a grid with a cell cursor.
// Rows load a page at a time as the cursor nears the end of what is loaded.
type tableData struct {
	name     string
	pk       string // "-" or empty: read only
	readOnly bool
	query    bool // rows came from a user view's SQL: all loaded, its own order
	columns  []db.ColumnInfo
	header   []string
	rows     [][]string
	nulls    [][]bool // a NULL, which shows as "NULL" but is not that text
	total    int
	where    string
	order    []orderBy
	lastCmd  string

	row, col   int // the cursor, in rows and header
	first, off int // first row shown, first column shown
	widths     []int
}

func newTableData(a *app, name, pk string) (*tableData, error) {
	d := &tableData{name: name, pk: pk, readOnly: pk == "" || pk == "-"}
	return d, d.reset(a)
}

const (
	queryRowsMax = 100000
	queryTimeout = 2 * time.Minute
)

// newQueryData shows a user view's answer. The view runs in a READ ONLY
// transaction: a file in the views directory is a query, never a change.
func newQueryData(a *app, name, sql string) (*tableData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	res, err := a.pg.QueryReadOnly(ctx, queryRowsMax, sql)
	if err != nil {
		return nil, err
	}
	d := &tableData{name: name, pk: "-", readOnly: true, query: true, columns: res.Columns}
	d.setHeader()
	for _, r := range res.Rows {
		d.add(r)
	}
	d.total = len(d.rows)
	d.measure()
	if res.Truncated {
		a.say(kit.Warn, "showing the first %d rows", queryRowsMax)
	}
	return d, nil
}

// add appends one row of values, nil being NULL.
func (d *tableData) add(values []any) {
	line := make([]string, len(values))
	null := make([]bool, len(values))
	for i, v := range values {
		line[i], null[i] = fmt.Sprint(v), v == nil
		if v == nil {
			line[i] = "NULL"
		}
	}
	d.rows = append(d.rows, line)
	d.nulls = append(d.nulls, null)
}

func (d *tableData) setHeader() {
	d.header = make([]string, len(d.columns))
	for i, c := range d.columns {
		d.header[i] = c.ColumnName
	}
}

func (d *tableData) orderSQL() string {
	parts := make([]string, len(d.order))
	for i, o := range d.order {
		parts[i] = db.QuoteName(o.col) + " " + o.dir
	}
	return strings.Join(parts, ", ")
}

func (d *tableData) reset(a *app) error {
	d.rows, d.nulls, d.row, d.first = nil, nil, 0, 0
	return d.load(a)
}

func (d *tableData) load(a *app) error {
	pk := d.pk
	if pk == "-" {
		pk = ""
	}
	recs, columns, total, err := a.pg.ListRecords(d.name, pk, len(d.rows), recordsPerPage, d.where, d.orderSQL())
	if err != nil {
		return err
	}
	d.columns, d.total = columns, total
	d.setHeader()
	for _, rec := range recs {
		values := make([]any, len(columns))
		for i, c := range columns {
			values[i] = rec[c.ColumnName]
		}
		d.add(values)
	}
	d.measure()
	return nil
}

func (d *tableData) measure() {
	d.widths = make([]int, len(d.header))
	for i, h := range d.header {
		d.widths[i] = min(maxCellWidth, screen.Width(h))
	}
	for _, r := range d.rows {
		for i, v := range r {
			first, _, _ := strings.Cut(v, "\n")
			d.widths[i] = max(d.widths[i], min(maxCellWidth, screen.Width(first)))
		}
	}
}

func (d *tableData) title() string {
	t := d.name
	if !d.readOnly {
		t += " · pk " + d.pk
	}
	if d.readOnly {
		t += " · read only"
	}
	return t + " · " + DBTitle
}

func (d *tableData) hints() []kit.Hint {
	h := []kit.Hint{{Key: "↑↓←→", What: "move"}}
	if !d.readOnly {
		h = append(h, kit.Hint{Key: "e", What: "edit"})
	}
	h = append(h, kit.Hint{Key: "p", What: "view"})
	if !d.query {
		h = append(h, kit.Hint{Key: "o O", What: "order"})
	}
	return append(h, kit.Hint{Key: ":", What: "command"}, kit.Hint{Key: "?", What: "help"}, kit.Hint{Key: "esc", What: "back"})
}

func (d *tableData) status() string {
	s := fmt.Sprintf("row %d of %d · col %d of %d", min(d.row+1, len(d.rows)), d.total, d.col+1, len(d.header))
	if len(d.rows) < d.total {
		s += fmt.Sprintf(" · %d loaded", len(d.rows))
	}
	if d.where != "" {
		s += " · where " + d.where
	}
	if len(d.order) > 0 {
		s += " · order " + d.orderSQL()
	}
	return s
}

// visible is the last column that fits from d.off in cols cells.
func (d *tableData) visible(cols int) int {
	used, last := 0, d.off
	for i := d.off; i < len(d.widths); i++ {
		used += d.widths[i]
		if i > d.off {
			used += colGap
		}
		if used > cols && i > d.off {
			break
		}
		last = i
	}
	return last
}

func (d *tableData) follow(rows, cols int) {
	d.first = min(d.first, d.row)
	if d.row >= d.first+rows {
		d.first = d.row - rows + 1
	}
	d.off = min(d.off, d.col)
	for d.col > d.visible(cols) && d.off < d.col {
		d.off++
	}
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func (d *tableData) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	if len(d.header) == 0 {
		c.Print(top+1, left, "no columns", kit.Dim)
		return
	}
	body := rows - 2
	d.follow(body, cols)
	last := d.visible(cols)

	at := left
	for i := d.off; i <= last; i++ {
		w := min(d.widths[i], left+cols-at)
		c.Print(top, at, kit.Pad(d.header[i], w), kit.Soft)
		at += d.widths[i] + colGap
	}
	c.Fill(top+1, left, 1, min(cols, at-left-colGap), '─', kit.Dim)

	for r := 0; r < body && d.first+r < len(d.rows); r++ {
		i := d.first + r
		at = left
		for j := d.off; j <= last; j++ {
			w := min(d.widths[j], left+cols-at)
			v := d.rows[i][j]
			first, _, multi := strings.Cut(v, "\n")
			if multi {
				first += "…"
			}
			text := kit.Pad(first, w)
			if isNumber(v) {
				text = kit.PadLeft(first, w)
			}
			pen := kit.Text
			switch {
			case i == d.row && j == d.col:
				pen = kit.Focus
			case d.nulls[i][j]:
				pen = kit.Dim
			}
			c.Print(top+2+r, at, text, pen)
			at += d.widths[j] + colGap
		}
	}
	if len(d.rows) == 0 {
		c.Print(top+2, left, "no rows", kit.Dim)
	}
	if d.off > 0 {
		c.Print(top, left-2, "‹", kit.Hot)
	}
	if last < len(d.header)-1 {
		c.Print(top, left+cols, "›", kit.Hot)
	}
}

func (d *tableData) cell() string {
	if d.row >= len(d.rows) {
		return ""
	}
	return d.rows[d.row][d.col]
}

func (d *tableData) key(a *app, k screen.Key) {
	_, h := a.t.Size()
	page := max(1, h-8)
	switch vimKey(k) {
	case screen.KeyUp:
		d.row = max(0, d.row-1)
	case screen.KeyDown:
		d.row = min(len(d.rows)-1, d.row+1)
	case screen.KeyPgUp:
		d.row = max(0, d.row-page)
	case screen.KeyPgDn:
		d.row = min(len(d.rows)-1, d.row+page)
	case screen.KeyHome:
		d.col = 0
	case screen.KeyEnd:
		d.col = len(d.header) - 1
	case screen.KeyLeft:
		d.col = max(0, d.col-1)
	case screen.KeyRight:
		d.col = min(len(d.header)-1, d.col+1)
	case 'g':
		d.row = 0
	case 'G':
		d.row = max(0, len(d.rows)-1)
	case screen.KeyEsc, 'q', screen.KeyBackspace:
		a.pop()
	case screen.KeyEnter, 'e', 'v':
		d.editCell(a)
	case 'p':
		err := a.page(d.cell())
		if err != nil {
			a.fail(err)
		}
	case 'o':
		d.orderBy(a, "ASC")
	case 'O':
		d.orderBy(a, "DESC")
	case 'I':
		d.run(a, "insert")
	case 'S':
		d.run(a, "struct")
	case 'J':
		d.run(a, "json")
	case 'U':
		d.run(a, "update")
	case ':':
		a.ask(":", d.lastCmd, d.command)
	case '?':
		a.push(&helpView{lines: dataHelp})
	}
	d.row = max(0, d.row)
	d.more(a, page)
}

// more loads the next page when the cursor comes within a page of the end
// of what is loaded.
func (d *tableData) more(a *app, page int) {
	if d.query || len(d.rows) >= d.total || d.row < len(d.rows)-page {
		return
	}
	err := d.load(a)
	if err != nil {
		a.fail(err)
	}
}

func (d *tableData) orderBy(a *app, dir string) {
	if d.query {
		a.say(kit.Warn, "a view's query has its own order")
		return
	}
	col := d.header[d.col]
	found := false
	for i := range d.order {
		if d.order[i].col == col {
			d.order[i].dir, found = dir, true
		}
	}
	if !found {
		d.order = append(d.order, orderBy{col, dir})
	}
	err := d.reset(a)
	if err != nil {
		a.fail(err)
	}
}

func (d *tableData) command(a *app, line string) {
	d.lastCmd = line
	cmd := strings.TrimSpace(line)
	switch {
	case cmd == "":
	case cmd == "q":
		a.pop()
	case (cmd == "where" || strings.HasPrefix(cmd, "where ") || cmd == "order") && d.query:
		a.say(kit.Warn, "a view's query has its own where and order")
	case cmd == "where" || strings.HasPrefix(cmd, "where "):
		d.where = strings.TrimSpace(strings.TrimPrefix(cmd, "where"))
		err := d.reset(a)
		if err != nil {
			a.fail(err)
		}
	case cmd == "order":
		d.order = nil
		err := d.reset(a)
		if err != nil {
			a.fail(err)
		}
	case cmd == "insert" || cmd == "struct" || cmd == "json" || cmd == "update":
		d.run(a, cmd)
	default:
		a.say(kit.Err, "unknown command %q (?)", cmd)
	}
}

// run opens the generated text in the editor; insert and update run what
// comes back from it, struct and json are only for reading and copying.
func (d *tableData) run(a *app, what string) {
	var text string
	var err error
	switch what {
	case "insert":
		text, err = a.pg.CreateInsertStatement(d.name)
	case "struct":
		text, err = a.pg.CreateGoStructDefinition(d.name)
	case "json":
		text, err = rowJSON(d.header, d.rowValues())
	case "update":
		if d.readOnly {
			a.say(kit.Err, "no primary key: update not allowed")
			return
		}
		text, err = a.pg.CreateUpdateStatement(d.name, d.header, d.rowValues())
	}
	if err != nil {
		a.fail(err)
		return
	}
	out, err := a.edit(text)
	if err != nil {
		a.fail(err)
		return
	}
	_, changed := edited(text, out)
	if (what != "insert" && what != "update") || !changed {
		return
	}
	err = a.pg.Exec(out)
	if err != nil {
		a.fail(err)
		return
	}
	a.say(kit.OK, "%s done", what)
	err = d.reset(a)
	if err != nil {
		a.fail(err)
	}
}

// rowValues is the row under the cursor with nil for NULL; nil when there
// is no row.
func (d *tableData) rowValues() []any {
	if d.row >= len(d.rows) {
		return nil
	}
	out := make([]any, len(d.rows[d.row]))
	for i, v := range d.rows[d.row] {
		if !d.nulls[d.row][i] {
			out[i] = v
		}
	}
	return out
}

func (d *tableData) editCell(a *app) {
	if d.row >= len(d.rows) {
		return
	}
	old := d.cell()
	raw, err := a.edit(old)
	if err != nil {
		a.fail(err)
		return
	}
	value, changed := edited(old, raw)
	if !changed {
		return
	}
	if d.readOnly {
		a.say(kit.Err, "read only: no primary key")
		return
	}
	err = updateCell(a.pg, d.name, d.header, d.columns, d.rows[d.row], d.col, value)
	if err != nil {
		a.fail(err)
		return
	}
	d.rows[d.row][d.col], d.nulls[d.row][d.col] = value, false
	a.say(kit.OK, "%s saved", d.header[d.col])
	if d.ordersBy(d.header[d.col]) {
		d.reload(a)
	}
}

// edited says what the editor's file means for a cell that held old: the
// editor's own final newline is not an edit, and a value that ended in a
// newline keeps it.
func edited(old, raw string) (string, bool) {
	if raw == old || strings.TrimRight(raw, "\r\n") == strings.TrimRight(old, "\r\n") {
		return old, false
	}
	value := strings.TrimRight(raw, "\r\n")
	return value + old[len(strings.TrimRight(old, "\r\n")):], true
}

// ordersBy says the rows are ordered by col, so an edit of it moves its row
// among the pages already read.
func (d *tableData) ordersBy(col string) bool {
	for _, o := range d.order {
		if o.col == col {
			return true
		}
	}
	return len(d.order) == 0 && slices.Contains(strings.Split(d.pk, ", "), col)
}

// reload reads the pages again, as many rows as were loaded, keeping the
// cursor where it was.
func (d *tableData) reload(a *app) {
	n, row, col := len(d.rows), d.row, d.col
	err := d.reset(a)
	for err == nil && len(d.rows) < min(n, d.total) {
		before := len(d.rows)
		err = d.load(a)
		if len(d.rows) == before {
			break
		}
	}
	if err != nil {
		a.fail(err)
	}
	d.row, d.col = min(row, max(0, len(d.rows)-1)), col
}

var dataHelp = []string{
	"↑↓←→ h j k l   move          g G       first, last row",
	"PgUp PgDn      a page        Home End  first, last column",
	"enter e v      edit the cell in $EDITOR (saved to the table)",
	"p              view the cell in $PAGER",
	"o O            order by this column, ascending or descending",
	"I S J U        INSERT, Go struct, JSON of the row, UPDATE of the row",
	":where COND    filter rows (:where alone clears)",
	":order         clear the order",
	":insert :struct :json :update  the same as I S J U",
	"esc q          back to the tables",
}
