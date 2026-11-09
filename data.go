package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"regexp"
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
	name      string
	pk        string // "-" or empty: read only
	readOnly  bool
	query     bool   // rows came from a query's SQL: all loaded, its own order
	sql       string // a query's SQL as it ran, for :sql to start from
	base      *queryRows
	columns   []db.ColumnInfo
	header    []string
	colBefore int // the cursor column when the command line opened
	hidden    map[string]bool
	rows      [][]string
	nulls     [][]bool // a NULL, which shows as "NULL" but is not that text
	raw       [][]any  // a query's rows as the driver typed them, for :export
	truncated bool     // a query's or :sql's rows stopped at the cap
	total     int
	where     string
	order     []orderBy
	search    string // the last / pattern, for n and N

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

// newQueryData shows a query's answer. The query runs in a READ ONLY
// transaction: a file in the views directory is a query, never a change.
func newQueryData(a *app, name, sql string) (*tableData, error) {
	var res db.Result
	start := debugNow()
	err := a.busy("running "+name, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()
		var err error
		res, err = a.pg.QueryReadOnly(ctx, queryRowsMax, sql)
		return err
	})
	debugEvent("view", start, "name", name, "rows", len(res.Rows), "truncated", res.Truncated, "err", err)
	if err != nil {
		return nil, err
	}
	d := &tableData{name: name, pk: "-", readOnly: true, query: true, sql: sql, columns: res.Columns}
	d.setHeader()
	for _, r := range res.Rows {
		d.add(r)
	}
	d.raw, d.truncated = res.Rows, res.Truncated
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

// reset reads the first page again; until it arrives (or if it fails, or
// is cancelled) the rows on screen stay as they were.
func (d *tableData) reset(a *app) error {
	p, err := d.page(a, 0)
	if err != nil {
		return err
	}
	d.rows, d.nulls, d.row, d.first = nil, nil, 0, 0
	d.apply(p)
	return nil
}

func (d *tableData) load(a *app) error {
	p, err := d.page(a, len(d.rows))
	if err != nil {
		return err
	}
	d.apply(p)
	return nil
}

type pageOf struct {
	rows    [][]any
	columns []db.ColumnInfo
	total   int
}

// page reads recordsPerPage rows from offset off the event loop: Esc
// cancels it. It reads d but changes nothing.
func (d *tableData) page(a *app, offset int) (pageOf, error) {
	var p pageOf
	name, where, order := d.name, d.where, d.orderSQL()
	var recs []map[string]any
	start := debugNow()
	err := a.busy("reading "+name, func(ctx context.Context) error {
		var err error
		recs, p.columns, p.total, err = a.pg.ListRecords(ctx, name, "", offset, recordsPerPage, where, order)
		return err
	})
	debugEvent("load", start, "table", name, "offset", offset, "rows", len(recs), "total", p.total,
		"where", where, "order", order, "err", err)
	if err != nil {
		return p, err
	}
	for _, rec := range recs {
		values := make([]any, len(p.columns))
		for i, c := range p.columns {
			values[i] = rec[c.ColumnName]
		}
		p.rows = append(p.rows, values)
	}
	return p, nil
}

func (d *tableData) apply(p pageOf) {
	before := d.header
	d.columns, d.total = p.columns, p.total
	d.setHeader()
	if !slices.Equal(before, d.header) {
		// the table changed shape since the rows on screen were read: they
		// no longer line up with the header
		d.rows, d.nulls, d.raw = nil, nil, nil
	}
	for _, r := range p.rows {
		d.add(r)
	}
	d.measure()
	// the table may have lost columns since (an ALTER through :sql)
	d.col = max(0, min(d.col, len(d.header)-1))
	d.off = min(d.off, d.col)
	d.row = max(0, min(d.row, len(d.rows)-1))
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
	h = append(h, kit.Hint{Key: "o O", What: "order"})
	return append(h, kit.Hint{Key: ":", What: "command"}, kit.Hint{Key: "?", What: "help"}, kit.Hint{Key: "esc", What: "back"})
}

func (d *tableData) status() string {
	s := fmt.Sprintf("row %d of %d · col %d of %d", min(d.row+1, len(d.rows)), d.total, d.col+1, len(d.header))
	if len(d.rows) < d.total {
		s += fmt.Sprintf(" · %d loaded", len(d.rows))
	}
	hidden := d.hiddenCount()
	if hidden > 0 {
		s += fmt.Sprintf(" · %d hidden", hidden)
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
	used, last, n := 0, d.off, 0
	for i := d.off; i < len(d.widths); i++ {
		if !d.shown(i) {
			continue
		}
		w := d.widths[i]
		if n > 0 {
			w += colGap
		}
		if used+w > cols && n > 0 {
			break
		}
		used, last, n = used+w, i, n+1
	}
	return last
}

func (d *tableData) shown(i int) bool { return !d.hidden[d.header[i]] }

// next is the first shown column from from in dir, from itself when there
// is none that way.
func (d *tableData) next(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(d.header); i += dir {
		if d.shown(i) {
			return i
		}
	}
	return from
}

// fixCol keeps the cursor off a hidden column.
func (d *tableData) fixCol() {
	d.col = max(0, min(d.col, len(d.header)-1))
	if len(d.header) > 0 && !d.shown(d.col) {
		d.col = d.next(d.col, 1)
	}
	if len(d.header) > 0 && !d.shown(d.col) {
		d.col = d.next(d.col, -1)
	}
}

// hide takes the cursor's column off the grid; the last one stays.
func (d *tableData) hide(a *app) {
	if len(d.header)-d.hiddenCount() <= 1 {
		a.say(kit.Warn, "the last column stays")
		return
	}
	if d.hidden == nil {
		d.hidden = map[string]bool{}
	}
	d.hidden[d.header[d.col]] = true
	d.fixCol()
	a.say(kit.Plain, "%d hidden · :show brings them back", d.hiddenCount())
}

// hiddenCount counts by the header: a column :sql dropped no longer counts.
func (d *tableData) hiddenCount() int {
	n := 0
	for i := range d.header {
		if !d.shown(i) {
			n++
		}
	}
	return n
}

// selectList is the shown columns for a SELECT, * when none is hidden.
func (d *tableData) selectList() string {
	if d.hiddenCount() == 0 {
		return "*"
	}
	var cols []string
	for i, h := range d.header {
		if d.shown(i) {
			cols = append(cols, sqlName(h))
		}
	}
	return strings.Join(cols, ", ")
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
	d.fixCol() // a cursor past the header must never reach the paint
	d.off = min(d.off, d.col)
	body := rows - 2
	d.follow(body, cols)
	last := d.visible(cols)

	at := left
	for i := d.off; i <= last; i++ {
		if !d.shown(i) {
			continue
		}
		w := min(d.widths[i], left+cols-at)
		c.Print(top, at, kit.Pad(d.header[i], w), kit.Soft)
		at += d.widths[i] + colGap
	}
	c.Fill(top+1, left, 1, min(cols, at-left-colGap), '─', kit.Dim)

	for r := 0; r < body && d.first+r < len(d.rows); r++ {
		i := d.first + r
		at = left
		for j := d.off; j <= last; j++ {
			if !d.shown(j) {
				continue
			}
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
	if d.next(d.off, -1) != d.off {
		c.Print(top, left-2, "‹", kit.Hot)
	}
	if d.next(last, 1) != last {
		c.Print(top, left+cols, "›", kit.Hot)
	}
}

func (d *tableData) cell() string {
	if d.row >= len(d.rows) || d.col >= len(d.rows[d.row]) {
		return ""
	}
	return d.rows[d.row][d.col]
}

func (d *tableData) key(a *app, k screen.Key) {
	if len(d.header) == 0 && k != screen.KeyEsc && k != 'q' && k != screen.KeyBackspace && k != ':' && k != '?' {
		return // SELECT FROM t: rows, no columns; nothing for a cell key to act on
	}
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
		d.col = d.next(-1, 1)
	case screen.KeyEnd:
		d.col = d.next(len(d.header), -1)
	case screen.KeyLeft:
		d.col = d.next(d.col, -1)
	case screen.KeyRight:
		d.col = d.next(d.col, 1)
	case 'x':
		d.hide(a)
	case 'g':
		d.row = 0
	case 'G':
		d.row = max(0, len(d.rows)-1)
	case screen.KeyEsc, 'q', screen.KeyBackspace:
		a.pop()
	case 'e', 'v': // v: less's key for $EDITOR
		d.editCell(a)
	case '/':
		a.ask("/", d.search, func(a *app, line string) {
			if line != "" {
				d.search = line
			}
			d.find(a, 1, false)
		})
	case 'n':
		d.find(a, 1, true)
	case 'N':
		d.find(a, -1, true)
	case 'y':
		if d.row < len(d.rows) {
			a.copyText(d.header[d.col], d.cell())
		}
	case 'Y':
		if d.row < len(d.rows) {
			a.copyText("the row", d.rowTSV())
		}
	case 'p', screen.KeyEnter: // Enter opens, never writes
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
		a.ask(":", "", d.command) // ↑ brings the last one back
		a.onTab, a.onPick = d.completions, d.pick
		d.colBefore = d.col
	case '?':
		a.push(&helpView{lines: dataHelp})
	}
	d.row = max(0, d.row)
	d.more(a, page)
}

// find moves the cursor to the next cell, in dir, that the search's regular
// expression matches, as / n N do in less and vi: row by row, the shown
// columns left to right, wrapping at the end. A new search may stay on the
// cell it starts from; n and N move off it first. Only the rows read so far
// are searched.
func (d *tableData) find(a *app, dir int, next bool) {
	if d.search == "" {
		a.say(kit.Warn, "no search yet: / starts one")
		return
	}
	re, err := regexp.Compile(d.search)
	if err != nil {
		a.say(kit.Err, "/%s: %v", d.search, err)
		return
	}
	cols := []int{}
	for i := range d.header {
		if d.shown(i) {
			cols = append(cols, i)
		}
	}
	n := len(d.rows) * len(cols)
	if n == 0 {
		return
	}
	at := d.row*len(cols) + max(0, slices.Index(cols, d.col))
	start := 0
	if next {
		start = 1
	}
	for k := start; k < n+start; k++ {
		i := ((at+dir*k)%n + n) % n
		r, c := i/len(cols), cols[i%len(cols)]
		if !re.MatchString(d.rows[r][c]) {
			continue
		}
		d.row, d.col = r, c
		wrapped := (dir > 0 && i < at) || (dir < 0 && i > at)
		if wrapped {
			a.say(kit.Plain, "/%s · wrapped", d.search)
		}
		return
	}
	more := ""
	if !d.query && len(d.rows) < d.total {
		more = "; :where searches the whole table"
	}
	a.say(kit.Warn, "/%s: not in the %d rows read%s", d.search, len(d.rows), more)
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

// queryRows are a query's rows as they came, for sortHere to start from.
type queryRows struct {
	rows  [][]string
	nulls [][]bool
	raw   [][]any
}

// sortHere orders a query's rows in memory: they are all here, and running
// the query again could run its writes again. NULLs sort as PostgreSQL's
// do, last going up and first going down; no order is the order they came.
func (d *tableData) sortHere() {
	if d.base == nil {
		d.base = &queryRows{d.rows, d.nulls, d.raw}
	}
	b := d.base
	type key struct {
		col  int
		desc bool
		nums []num
	}
	var keys []key
	for _, o := range d.order {
		i := slices.Index(d.header, o.col)
		if i >= 0 {
			keys = append(keys, key{i, o.dir == "DESC", numbers(b, i)})
		}
	}
	perm := make([]int, len(b.rows))
	for i := range perm {
		perm[i] = i
	}
	slices.SortStableFunc(perm, func(x, y int) int {
		for _, k := range keys {
			c := compareCells(b, x, y, k.col, k.nums)
			if k.desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return 0
	})
	d.rows, d.nulls = make([][]string, len(perm)), make([][]bool, len(perm))
	if b.raw != nil {
		d.raw = make([][]any, len(perm))
	}
	for i, p := range perm {
		d.rows[i], d.nulls[i] = b.rows[p], b.nulls[p]
		if b.raw != nil {
			d.raw[i] = b.raw[p]
		}
	}
}

// decimal has its exponent capped: 1e1000000000 as an exact number would
// take the sort's memory and time.
var decimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d{1,4})?$`)

// numbers is column col read as exact numbers when every value in it is
// one (bigints past 2^53 included), nil when any is not: one column sorts
// one way, so the order is a true order.
func numbers(b *queryRows, col int) []num {
	out := make([]num, len(b.rows))
	for i, r := range b.rows {
		if b.nulls[i][col] {
			continue
		}
		v := r[col]
		switch v {
		case "-Infinity":
			out[i] = num{rank: -1}
			continue
		case "Infinity":
			out[i] = num{rank: 1}
			continue
		case "NaN":
			out[i] = num{rank: 2}
			continue
		}
		if !decimal.MatchString(v) { // not "1/2" or "0x10", which Rat would take
			return nil
		}
		n, ok := new(big.Rat).SetString(v)
		if !ok {
			return nil
		}
		out[i] = num{r: n}
	}
	return out
}

// num is an exact number or, by rank, PostgreSQL's numeric specials in
// their order: -Infinity, the numbers, Infinity, NaN.
type num struct {
	rank int
	r    *big.Rat
}

func (a num) cmp(b num) int {
	if a.rank != 0 || b.rank != 0 {
		return cmp.Compare(a.rank, b.rank)
	}
	return a.r.Cmp(b.r)
}

// compareCells compares a column's cells, NULLs last, as numbers when nums
// has them, else as text.
func compareCells(b *queryRows, x, y, col int, nums []num) int {
	nx, ny := b.nulls[x][col], b.nulls[y][col]
	switch {
	case nx && ny:
		return 0
	case nx:
		return 1
	case ny:
		return -1
	}
	if nums != nil {
		return nums[x].cmp(nums[y])
	}
	return strings.Compare(b.rows[x][col], b.rows[y][col])
}

// toggleOrder is o and O on col: a new key goes last, the same key again
// undoes it and the other direction flips it, the other keys untouched.
func toggleOrder(order []orderBy, col, dir string) []orderBy {
	order = slices.Clone(order)
	i := slices.IndexFunc(order, func(o orderBy) bool { return o.col == col })
	switch {
	case i < 0:
		return append(order, orderBy{col, dir})
	case order[i].dir == dir:
		return slices.Delete(order, i, i+1)
	}
	order[i].dir = dir
	return order
}

func (d *tableData) orderBy(a *app, dir string) {
	col := d.header[d.col]
	before := slices.Clone(d.order)
	d.order = toggleOrder(before, col, dir)
	if d.query {
		d.sortHere()
		return
	}
	err := d.reset(a)
	if err != nil {
		d.order = before // the rows on screen are still in the old order
		a.fail(err)
	}
}

func (d *tableData) command(a *app, line string) {
	cmd := strings.TrimSpace(line)
	switch {
	case cmd == "":
	case cmd == "q" || cmd == "quit":
		a.pop()
	case cmd == "order" && d.query:
		d.order = nil
		d.sortHere()
	case (cmd == "where" || strings.HasPrefix(cmd, "where ")) && d.query && d.sql == "":
		a.say(kit.Warn, "a :sql result is not run again (it may write): :sql with a WHERE")
	case (cmd == "where" || strings.HasPrefix(cmd, "where ")) && d.query:
		d.whereQuery(a, strings.TrimSpace(strings.TrimPrefix(cmd, "where")))
	case cmd == "where" || strings.HasPrefix(cmd, "where "):
		before := d.where
		d.where = strings.TrimSpace(strings.TrimPrefix(cmd, "where"))
		err := d.reset(a)
		if err != nil {
			d.where = before // the rows on screen are still the old filter's
			a.fail(err)
		}
	case cmd == "order":
		before := d.order
		d.order = nil
		err := d.reset(a)
		if err != nil {
			d.order = before
			a.fail(err)
		}
	case cmd == "insert" || cmd == "struct" || cmd == "json" || cmd == "update":
		d.run(a, cmd)
	case cmd == "show":
		d.hidden = nil
	case cmd == "sql":
		runSQL(a)
	case cmd == "connections":
		toConnections(a)
	case cmd == "structure" && !d.query:
		v, err := newStructView(a, d.name)
		if err != nil {
			a.fail(err)
			return
		}
		a.push(v)
	case cmd == "query" || strings.HasPrefix(cmd, "query "):
		d.keep(a, strings.TrimSpace(strings.TrimPrefix(cmd, "query")), queryType, func(a *app, name, body string, after func(a *app)) {
			if editQuery(a, name, body) {
				after(a)
			}
		})
	case cmd == "view" || strings.HasPrefix(cmd, "view "):
		d.keep(a, strings.TrimSpace(strings.TrimPrefix(cmd, "view")), "view", createDBView)
	case strings.HasPrefix(cmd, "export"):
		d.export(a, strings.TrimPrefix(cmd, "export"))
	default:
		a.say(kit.Err, "unknown command %q (?)", cmd)
	}
}

var dataCommands = []string{"where", "order", "show", "insert", "struct", "json", "update", "sql", "query", "view", "structure", "export", "connections", "quit"}

// querySQL is a query's SQL as the grid shows it: its own, inside a
// subselect when a :where filters it.
func (d *tableData) querySQL() string {
	if d.where == "" || d.sql == "" {
		return d.sql
	}
	return "SELECT * FROM (\n" + strings.TrimRight(strings.TrimSpace(d.sql), "; \t\n") + "\n) AS q\nWHERE " + d.where
}

// whereQuery runs a query again, read only, its rows filtered by cond (none
// when ""); the order the grid was sorted in stays.
func (d *tableData) whereQuery(a *app, cond string) {
	before := d.where
	d.where = cond
	nd, err := newQueryData(a, d.name, d.querySQL())
	if err != nil {
		d.where = before // the rows on screen are still the old filter's
		a.fail(err)
		return
	}
	d.columns, d.rows, d.nulls, d.raw = nd.columns, nd.rows, nd.nulls, nd.raw
	d.truncated, d.total, d.base = nd.truncated, nd.total, nil
	d.row, d.first = 0, 0
	d.setHeader()
	d.measure()
	if len(d.order) > 0 {
		d.sortHere()
	}
}

// keep starts a query of yours, or a view in the database, from what the
// grid shows: a table's SELECT (shown columns, filter, order), a query's
// own SQL.
func (d *tableData) keep(a *app, name, typ string, start func(a *app, name, body string, after func(a *app))) {
	if name == "" {
		a.say(kit.Warn, "a name: :query NAME, :view NAME")
		return
	}
	body := d.querySQL()
	if !d.query {
		err := a.busy("reading "+d.name, func(ctx context.Context) error {
			q, err := d.selectSQL(ctx, a.pg, d.selectList())
			body = q
			return err
		})
		if err != nil {
			a.fail(err)
			return
		}
	}
	if body == "" {
		a.say(kit.Warn, "a :sql result has no SQL of its own to keep; :sql shows the last one")
		return
	}
	start(a, name, strings.TrimSpace(body), func(a *app) { listSaved(a, name, typ, true) })
}

// completions are what Tab offers at the grid's ":": a command first, then
// in a :where the columns and the words between them.
func (d *tableData) completions(line string) completion {
	line = strings.TrimLeft(line, " ")
	if strings.HasPrefix(line, "where ") && (!d.query || d.sql != "") {
		names := make([]string, len(d.header))
		for i, h := range d.header {
			names[i] = sqlName(h)
		}
		return completion{names: names, words: whereWords, sep: " "}
	}
	return commandCompletion(line, dataCommands)
}

// pick puts the cursor on the column the Tab menu chose, so its data
// shows while the :where is written; ESC puts it back.
func (d *tableData) pick(item string) {
	if item == "" {
		d.col = d.colBefore
		return
	}
	for i, h := range d.header {
		if sqlName(h) == item {
			d.col = i
			return
		}
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
		text, err = rowJSON(d.columns, d.rowValues())
	case "update":
		if d.readOnly {
			a.say(kit.Err, "no primary key: update not allowed")
			return
		}
		err = a.busy("reading the row", func(ctx context.Context) error {
			var err error
			text, err = a.pg.CreateUpdateStatement(ctx, d.name, d.header, d.rowValues())
			return err
		})
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
	start := debugNow()
	var n int64
	err = a.busy("running the "+what, func(ctx context.Context) error {
		var err error
		n, err = a.pg.ExecTx(ctx, out)
		return err
	})
	debugEvent("exec", start, "what", what, "table", d.name, "err", err)
	if err != nil {
		a.fail(err)
		return
	}
	a.say(kit.OK, "%s done", what)
	if what == "update" && n == 0 {
		a.say(kit.Warn, "UPDATE 0: the row changed since it was read, or is gone; nothing written")
	}
	err = d.reset(a)
	if err != nil {
		a.fail(err)
	}
}

// rowTSV is the row under the cursor as tab-separated text, NULL empty: what
// a spreadsheet takes on paste. Tabs and newlines inside a value become
// spaces, or they would make cells of their own.
func (d *tableData) rowTSV() string {
	cells := make([]string, len(d.rows[d.row]))
	for i, v := range d.rows[d.row] {
		if !d.nulls[d.row][i] {
			cells[i] = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(v)
		}
	}
	return strings.Join(cells, "\t")
}

// rowValues is the row under the cursor with nil for NULL; nil when there
// is no row.
func (d *tableData) rowValues() []any {
	if d.row >= len(d.rows) {
		return nil
	}
	return d.rowAt(d.row)
}

func (d *tableData) rowAt(r int) []any {
	out := make([]any, len(d.rows[r]))
	for i, v := range d.rows[r] {
		if !d.nulls[r][i] {
			out[i] = v
		}
	}
	return out
}

func (d *tableData) editCell(a *app) {
	if d.row >= len(d.rows) {
		return
	}
	if d.readOnly { // before the editor: what is typed there would be lost
		why := "no primary key"
		if d.query {
			why = "the rows of a query"
		}
		a.say(kit.Warn, "%s: nothing to save a cell to; p views it", why)
		return
	}
	// the editor starts from the cell as it is now, and the save checks it
	// still is: another session's change is neither hidden nor overwritten
	row, col, at := d.rows[d.row], d.col, d.row
	name := d.header[col]
	var now any
	var token sql.NullString
	err := a.busy("reading "+name, func(ctx context.Context) error {
		pks, values, err := rowKey(ctx, a.pg, d.name, d.header, row)
		if err != nil {
			return err
		}
		now, token, err = a.pg.CellNow(ctx, d.name, name, pks, values)
		return err
	})
	if err != nil {
		a.fail(err)
		return
	}
	stale := d.setCell(at, col, now)
	old := d.cell()
	raw, err := a.edit(old)
	if err != nil {
		a.fail(err)
		return
	}
	value, changed := edited(old, raw)
	if !changed {
		if stale {
			a.say(kit.Warn, "%s had changed in the database; the grid shows it now", name)
		}
		return
	}
	start := debugNow()
	err = a.busy("saving "+name, func(ctx context.Context) error {
		return updateCell(ctx, a.pg, d.name, d.header, d.columns, row, col, value, token)
	})
	debugEvent("update_cell", start, "table", d.name, "column", name, "err", err)
	if errors.Is(err, db.ErrChanged) {
		d.refreshCell(a, at, col)
		a.copyText("what you typed", value)
		a.say(kit.Warn, "%s changed meanwhile in the database: not saved; your text is on the clipboard", name)
		return
	}
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

// setCell puts v (nil is NULL) in the cell, saying whether it was not
// already there.
func (d *tableData) setCell(row, col int, v any) bool {
	s, null := "", v == nil
	if !null {
		s = fmt.Sprint(v)
	}
	if d.rows[row][col] == s && d.nulls[row][col] == null {
		return false
	}
	d.rows[row][col], d.nulls[row][col] = s, null
	d.measure()
	return true
}

// refreshCell reads one cell again.
func (d *tableData) refreshCell(a *app, row, col int) {
	var now any
	err := a.busy("reading "+d.header[col], func(ctx context.Context) error {
		pks, values, err := rowKey(ctx, a.pg, d.name, d.header, d.rows[row])
		if err != nil {
			return err
		}
		now, _, err = a.pg.CellNow(ctx, d.name, d.header[col], pks, values)
		return err
	})
	if err == nil {
		d.setCell(row, col, now)
	}
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
	d.row, d.col = min(row, max(0, len(d.rows)-1)), min(col, max(0, len(d.header)-1))
	d.off = min(d.off, d.col)
}

var dataHelp = []string{
	"↑↓←→ h j k l   move          g G       first, last row",
	"PgUp PgDn ^F ^B  a page      Home End  first, last column",
	"enter p        view the cell in $PAGER",
	"e v            edit the cell in $EDITOR (saved to the table)",
	"/ n N          search the rows read (regular expression), next, previous",
	"y Y            copy the cell, the row (tab-separated) to the clipboard",
	"o O            order by this column, ascending or descending; again undoes",
	"x              hide this column (:show brings them all back)",
	"I S J U        INSERT, Go struct, JSON of the row, UPDATE of the row",
	":where COND    filter rows (:where alone clears); tab completes; a query's",
	"               rows are read again, its SQL inside a subselect",
	":order         clear the order",
	":insert :struct :json :update  the same as I S J U",
	":sql           write SQL in $EDITOR and run it (writes too); starts as the grid's SELECT",
	":query NAME    keep the grid's SELECT as a query of yours, edited first",
	":view NAME     create a view in the database from the grid's SELECT",
	":structure     the table's columns: add, rename, type, default, drop",
	":connections   the connections menu: open, add, edit, remove",
	":export F.csv  every row of the filter (or .json), to a new file",
	"esc q :q       back to the tables",
	"^C ^Z ^L       quit, suspend (fg resumes), redraw",
}
