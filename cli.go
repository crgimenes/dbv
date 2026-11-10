package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crgimenes/dbv/db"
)

const usage = `dbv [-s] [-debug] [file.filo ...]
dbv COMMAND [flags] [args]

Terminal viewer and editor for PostgreSQL. With no command it opens the
interactive interface; with a command it answers once and exits, for
scripts and agents. Connections come from init.filo in the config
directory (macOS: ~/Library/Application Support/dbv, Linux:
~/.config/dbv), or ./init.filo; files named instead replace it.

Interface flags:
  -s           selector: pick a name, struct or INSERT into /tmp/dbv_output.txt
  -debug       one line per event on stderr; redirect it (2>dbv.log), since
               the interface owns the terminal

Reading (rows and query run one statement in a READ ONLY transaction, the
others read the catalog with fixed queries):
  dbs                 configured connections
  tables              tables, views and materialized views
  columns TABLE       columns of a table, in table order
  rows TABLE          rows of a table
  query [SQL | -]     run a query; SQL from stdin when absent or "-"
  struct TABLE        Go struct for a table
  insert TABLE        INSERT template for a table (not executed)

Editor:
  lsp                 language server for .sql files (dbv lsp -h)
  show FILE           run a file's SQL, or the statement at -at, in the grid (dbv show -h)
  info [FILE]         the connection a file gets, and the server behind it (dbv info -h)

Writing (refused without -write):
  exec [SQL | -]      run SQL, all of it in one transaction
  update TABLE COLUMN VALUE
                      set one cell; -key names the whole primary key and
                      must match exactly one row, or nothing changes

Common flags (before the arguments):
  -config FILE connections from FILE instead of the default init.filo
  -db NAME     connection by title or 1-based number (default: the only one)
  -json        JSON on stdout instead of text
  -timeout D   bound for the whole command (default 30s)
  -debug       one line per step on stderr: "dbv event: OP ms=N key=value ..."
rows and query:
  -limit N     at most N rows (default 1000); a cut is reported on stderr
exec and update:
  -write       allow the change (without it the command refuses, exit 2)
update:
  -key COL=VAL a primary key column and its value; repeat for each column
rows:
  -where SQL   WHERE clause, without the keyword
  -order SQL   ORDER BY clause, without the keyword (default: primary key)
  -offset N    skip N rows (default 0)

Text output is ';'-separated with a header line; NULL prints as NULL.
JSON output for rows/query: {"columns":[{"name","type"}],"rows":[[...]],"truncated":bool}.

dbv COMMAND -h shows one command alone.

Example:
  dbv -h
  dbv rows -h
  dbv query -db 1 -json 'SELECT id, name FROM users LIMIT 5'
  echo 'SELECT now()' | dbv query -db LocalDB
  dbv update -db 1 -write -key id=7 users name 'Ana Maria'
`

type cliOpts struct {
	config  string
	write   bool
	keys    keyList
	db      string
	json    bool
	timeout time.Duration
	limit   int
	where   string
	order   string
	offset  int
}

type command struct {
	writes  bool
	args    int // required positional arguments; -1 means optional SQL
	run     func(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error
	use     string   // the arguments, for dbv COMMAND -h
	what    string   // one sentence
	flags   []string // its own flags, beyond commonFlags
	example string
}

var commands = map[string]command{
	"dbs": {args: 0, what: "the configured connections: n;title;url (password masked);views",
		example: "dbv dbs -json"},
	"tables": {args: 0, run: cmdTables, what: "tables, views and materialized views: name;type;size;primary key",
		example: "dbv tables -db 1"},
	"columns": {args: 1, run: cmdColumns, use: "TABLE", what: "the columns of a table, in table order: name;type",
		example: "dbv columns -db 1 users"},
	"rows": {args: 1, run: cmdRows, use: "TABLE", what: "rows of a table, in a READ ONLY transaction",
		flags: []string{"limit", "where", "order", "offset"}, example: "dbv rows -db 1 -where 'id > 10' -limit 5 users"},
	"query": {args: -1, run: cmdQuery, use: "[SQL | -]", what: "one statement in a READ ONLY transaction, from stdin when SQL is absent or -",
		flags: []string{"limit"}, example: "echo 'SELECT now()' | dbv query -db 1"},
	"struct": {args: 1, run: cmdStruct, use: "TABLE", what: "a Go struct for a table",
		example: "dbv struct -db 1 users"},
	"insert": {args: 1, run: cmdInsert, use: "TABLE", what: "an INSERT template for a table, not executed",
		example: "dbv insert -db 1 users"},
	"exec": {args: -1, run: cmdExec, writes: true, use: "[SQL | -]", what: "SQL that writes, all of it in one transaction; refused without -write",
		flags: []string{"write"}, example: "dbv exec -db 1 -write 'DELETE FROM sessions WHERE expired'"},
	"update": {args: 3, run: cmdUpdate, writes: true, use: "TABLE COLUMN VALUE",
		what:  "set one cell; -key names the whole primary key and must match exactly one row, or nothing changes",
		flags: []string{"write", "key"}, example: "dbv update -db 1 -write -key id=7 users name 'Ana Maria'"},
}

var commonFlags = []string{"config", "db", "json", "timeout", "debug"}

var flagHelp = map[string]string{
	"config":  "-config FILE  connections from FILE instead of the default init.filo",
	"db":      "-db NAME      connection by title or 1-based number (default: the only one)",
	"json":    "-json         JSON on stdout instead of ';'-separated text",
	"timeout": "-timeout D    bound for the whole command (default 30s)",
	"debug":   "-debug        one line per step on stderr",
	"limit":   "-limit N      at most N rows (default 1000); a cut is reported on stderr",
	"where":   "-where SQL    WHERE clause, without the keyword",
	"order":   "-order SQL    ORDER BY clause, without the keyword (default: primary key)",
	"offset":  "-offset N     skip N rows (default 0)",
	"write":   "-write        allow the change (without it the command refuses, exit 2)",
	"key":     "-key COL=VAL  a primary key column and its value; repeat for each column",
}

func commandUsage(name string, c command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "dbv %s: %s.\n\nUsage: dbv %s [flags] %s\n\nFlags:\n", name, c.what, name, c.use)
	for _, f := range append(slices.Clone(c.flags), commonFlags...) {
		fmt.Fprintf(&b, "  %s\n", flagHelp[f])
	}
	fmt.Fprintf(&b, "\nExample: %s\n", c.example)
	return b.String()
}

// nearCommand is the command name typed may have meant: one a prefix of
// the other, or two edits away at most.
func nearCommand(typed string) string {
	best, dist := "", 3
	for name := range commands {
		if strings.HasPrefix(name, typed) || strings.HasPrefix(typed, name) {
			return name
		}
		d := editDistance(typed, name)
		if d < dist || d == dist && name < best {
			best, dist = name, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// keyList is -key COL=VAL, repeated.
type keyList []string

func (k *keyList) String() string { return strings.Join(*k, ",") }

func (k *keyList) Set(v string) error {
	col, _, ok := strings.Cut(v, "=")
	if !ok || col == "" {
		return fmt.Errorf("want COL=VAL, got %q", v)
	}
	*k = append(*k, v)
	return nil
}

func isHelp(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

// runCLI returns the process exit status.
func runCLI(name string, argv []string) int {
	cmd := commands[name]
	var o cliOpts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.config, "config", "", "")
	fs.BoolVar(&o.write, "write", false, "")
	fs.Var(&o.keys, "key", "")
	fs.StringVar(&o.db, "db", "", "")
	fs.BoolVar(&o.json, "json", false, "")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "")
	fs.IntVar(&o.limit, "limit", 1000, "")
	fs.StringVar(&o.where, "where", "", "")
	fs.StringVar(&o.order, "order", "", "")
	fs.IntVar(&o.offset, "offset", 0, "")
	debug := fs.Bool("debug", false, "")

	err := fs.Parse(argv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(commandUsage(name, cmd))
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbv %s: %v (dbv %s -h)\n", name, err, name)
		return 2
	}
	var stray []string
	fs.Visit(func(f *flag.Flag) {
		if !slices.Contains(cmd.flags, f.Name) && !slices.Contains(commonFlags, f.Name) {
			stray = append(stray, "-"+f.Name)
		}
	})
	if len(stray) > 0 {
		fmt.Fprintf(os.Stderr, "dbv %s: %s does not apply here (dbv %s -h)\n", name, strings.Join(stray, " "), name)
		return 2
	}
	if *debug {
		debugOut = os.Stderr
	}
	cliStart := debugNow()
	args := fs.Args()
	if cmd.args >= 0 && len(args) != cmd.args {
		fmt.Fprintf(os.Stderr, "dbv %s: want %s, got %d argument(s) (dbv %s -h)\n", name, cmd.use, len(args), name)
		return 2
	}
	if cmd.writes && !o.write {
		fmt.Fprintf(os.Stderr, "dbv %s: this changes data; add -write to allow it\n", name)
		return 2
	}
	path := configPath()
	if o.config != "" {
		if !fileExists(o.config) {
			fmt.Fprintf(os.Stderr, "dbv %s: no config file %q\n", name, o.config)
			return 2
		}
		path = o.config
	}
	if o.limit < 1 || o.offset < 0 {
		fmt.Fprintf(os.Stderr, "dbv %s: -limit must be > 0 and -offset >= 0\n", name)
		return 2
	}

	// The context bounds the query; the watchdog bounds what takes no
	// context (ssh dial, ping, catalog lookups).
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	watchdog := time.AfterFunc(o.timeout+2*time.Second, func() {
		fmt.Fprintf(os.Stderr, "dbv %s: timed out after %s\n", name, o.timeout)
		os.Exit(1)
	})
	defer watchdog.Stop()

	configs := loadDBConfigs(path)
	if name == "dbs" {
		return report(name, cmdDBs(configs, o))
	}

	cfg, err := pickConfig(configs, o.db)
	if err != nil {
		return report(name, err)
	}
	connStart := debugNow()
	pg, err := db.New(cfg.URL)
	debugEvent("connect", connStart, "title", cfg.Title, "err", err)
	if err != nil {
		return report(name, fmt.Errorf("connect %q: %w", cfg.Title, err))
	}
	defer pg.Close()
	err = cmd.run(ctx, pg, o, args)
	debugEvent("command", cliStart, "name", name, "err", err)
	return report(name, err)
}

func report(name string, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(os.Stderr, "dbv %s: %v\n", name, err)
	return 1
}

func pickConfig(configs []db.DBConfig, want string) (db.DBConfig, error) {
	if len(configs) == 0 {
		return db.DBConfig{}, errors.New("no connection configured")
	}
	if want == "" {
		if len(configs) == 1 {
			return configs[0], nil
		}
		return db.DBConfig{}, errors.New("several connections: choose one with -db (see dbv dbs)")
	}
	n, err := strconv.Atoi(want)
	if err == nil && n >= 1 && n <= len(configs) {
		return configs[n-1], nil
	}
	for _, c := range configs {
		if c.Title == want {
			return c, nil
		}
	}
	return db.DBConfig{}, fmt.Errorf("no connection %q (see dbv dbs)", want)
}

func writeJSON(v any) error {
	return encodeJSON(os.Stdout, v)
}

func writeText(records [][]string) error {
	w := csv.NewWriter(os.Stdout)
	w.Comma = ';'
	err := w.WriteAll(records)
	if err != nil {
		return err
	}
	return w.Error()
}

func cmdDBs(configs []db.DBConfig, o cliOpts) error {
	type entry struct {
		N     int    `json:"n"`
		Title string `json:"title"`
		URL   string `json:"url"`
		Views string `json:"views"`
	}
	list := make([]entry, 0, len(configs))
	for i, c := range configs {
		masked, err := maskDBURL(c.URL)
		if err != nil {
			masked = "(unparsable url)"
		}
		list = append(list, entry{N: i + 1, Title: c.Title, URL: masked, Views: c.ViewsPath})
	}
	if o.json {
		return writeJSON(list)
	}
	records := [][]string{{"n", "title", "url", "views"}}
	for _, e := range list {
		records = append(records, []string{strconv.Itoa(e.N), e.Title, e.URL, e.Views})
	}
	return writeText(records)
}

func cmdTables(ctx context.Context, pg *db.Postgres, o cliOpts, _ []string) error {
	tables, err := pg.ListTablesAndViews(ctx)
	if err != nil {
		return err
	}
	type entry struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Size       string `json:"size"`
		PrimaryKey string `json:"primary_key"`
	}
	list := make([]entry, 0, len(tables))
	for _, t := range tables {
		list = append(list, entry{Name: t.Name, Type: t.Type, Size: t.Size, PrimaryKey: t.PrimaryKey.String})
	}
	if o.json {
		return writeJSON(list)
	}
	records := [][]string{{"name", "type", "size", "primary_key"}}
	for _, e := range list {
		records = append(records, []string{e.Name, e.Type, e.Size, e.PrimaryKey})
	}
	return writeText(records)
}

func cmdColumns(_ context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	cols, err := pg.ListColumns(args[0])
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("no table %q", args[0])
	}
	if o.json {
		return writeJSON(columnsJSON(cols))
	}
	records := [][]string{{"name", "type"}}
	for _, c := range cols {
		records = append(records, []string{c.ColumnName, c.DataType})
	}
	return writeText(records)
}

func cmdRows(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	rel, err := pg.Resolve(ctx, args[0])
	if err != nil {
		return err
	}
	order := o.order
	if order == "" {
		pk, err := pg.GetPrimaryKeyColumns(ctx, rel.SQL)
		if err != nil {
			return err
		}
		for i, c := range pk {
			pk[i] = db.QuoteName(c)
		}
		order = strings.Join(pk, ", ")
	}
	var q strings.Builder
	q.WriteString("SELECT * FROM " + rel.From())
	if o.where != "" {
		q.WriteString(" WHERE " + o.where)
	}
	if order != "" {
		q.WriteString(" ORDER BY " + order)
	}
	fmt.Fprintf(&q, " LIMIT %d OFFSET %d", o.limit+1, o.offset) // +1 tells a cut from an exact fit
	return printResult(ctx, pg, o, q.String())
}

func cmdQuery(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	sqlText, err := readSQL(args)
	if err != nil {
		return err
	}
	return printResult(ctx, pg, o, sqlText)
}

func printResult(ctx context.Context, pg *db.Postgres, o cliOpts, query string) error {
	start := debugNow()
	res, err := pg.QueryReadOnly(ctx, o.limit, query)
	debugEvent("query", start, "rows", len(res.Rows), "truncated", res.Truncated, "err", err)
	if err != nil {
		return err
	}
	if res.Truncated && len(res.Rows) < o.limit {
		fmt.Fprintf(os.Stderr, "dbv: stopped at %d rows, %d MB read; select fewer columns, or LIMIT and OFFSET\n", len(res.Rows), db.BytesMax>>20)
	}
	if res.Truncated && len(res.Rows) >= o.limit {
		fmt.Fprintf(os.Stderr, "dbv: stopped at %d rows; raise -limit for more\n", o.limit)
	}
	if o.json {
		return encodeJSON(os.Stdout, resultJSON(res))
	}
	return writeCSV(os.Stdout, ';', res, "NULL")
}

func printSource(o cliOpts, key, text string) error {
	if o.json {
		return writeJSON(map[string]string{key: text})
	}
	_, err := fmt.Println(strings.TrimRight(text, "\n"))
	return err
}

func cmdStruct(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	s, err := pg.CreateGoStructDefinition(ctx, args[0])
	if err != nil {
		return err
	}
	return printSource(o, "go", s)
}

func cmdInsert(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	s, err := pg.CreateInsertStatement(ctx, args[0])
	if err != nil {
		return err
	}
	return printSource(o, "sql", s)
}

func readSQL(args []string) (string, error) {
	if len(args) > 1 {
		return "", errors.New("one SQL argument at most; quote it")
	}
	sqlText := ""
	if len(args) == 1 {
		sqlText = args[0]
	}
	if sqlText == "" || sqlText == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		sqlText = strings.TrimPrefix(string(b), "\uFEFF")
	}
	if strings.TrimSpace(sqlText) == "" {
		return "", errors.New("empty SQL")
	}
	return sqlText, nil
}

func cmdExec(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	sqlText, err := readSQL(args)
	if err != nil {
		return err
	}
	start := debugNow()
	n, err := pg.ExecTx(ctx, sqlText)
	debugEvent("exec", start, "rows_affected", n, "err", err)
	if err != nil {
		return err
	}
	if o.json {
		return writeJSON(map[string]int64{"rows_affected": n})
	}
	_, err = fmt.Println(n)
	return err
}

func cmdUpdate(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	table, column, value := args[0], args[1], args[2]
	pk, err := pg.GetPrimaryKeyColumns(ctx, table)
	if err != nil {
		return err
	}
	if len(pk) == 0 {
		return fmt.Errorf("%s has no primary key: use exec", table)
	}
	given := make(map[string]string, len(o.keys))
	for _, kv := range o.keys {
		col, val, _ := strings.Cut(kv, "=")
		given[col] = val
	}
	values := make([]any, len(pk))
	for i, col := range pk {
		v, ok := given[col]
		if !ok || len(given) != len(pk) {
			return fmt.Errorf("-key must name the whole primary key: %s", strings.Join(pk, ", "))
		}
		values[i] = v
	}
	start := debugNow()
	_, now, err := pg.CellNow(ctx, table, column, pk, values)
	if err == nil {
		err = pg.UpdateDataCell(ctx, table, column, value, pk, values, now)
	}
	debugEvent("update_cell", start, "table", table, "column", column, "err", err)
	if err != nil {
		return err
	}
	if o.json {
		return writeJSON(map[string]int64{"rows_affected": 1})
	}
	_, err = fmt.Println(1)
	return err
}
