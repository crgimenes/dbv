package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crgimenes/dbv/db"
)

const usage = `dbv [file.filo ...]
dbv COMMAND [flags] [args]

Terminal viewer and editor for PostgreSQL. With no command it opens the
interactive interface; with a command it answers once and exits, for
scripts and agents. Connections come from init.filo in the config
directory (macOS: ~/Library/Application Support/dbv, Linux:
~/.config/dbv), or ./init.filo.

Commands (none writes: rows and query run one statement in a READ ONLY
transaction, the others read the catalog with fixed queries):
  dbs                 configured connections
  tables              tables, views and materialized views
  columns TABLE       columns of a table, in table order
  rows TABLE          rows of a table
  query [SQL | -]     run a query; SQL from stdin when absent or "-"
  struct TABLE        Go struct for a table
  insert TABLE        INSERT template for a table (not executed)

Common flags (before the arguments):
  -db NAME     connection by title or 1-based number (default: the only one)
  -json        JSON on stdout instead of text
  -timeout D   bound for the whole command (default 30s)
rows and query:
  -limit N     at most N rows (default 1000); a cut is reported on stderr
rows:
  -where SQL   WHERE clause, without the keyword
  -order SQL   ORDER BY clause, without the keyword (default: primary key)
  -offset N    skip N rows (default 0)

Text output is ';'-separated with a header line; NULL prints as NULL.
JSON output for rows/query: {"columns":[{"name","type"}],"rows":[[...]],"truncated":bool}.

Example:
  dbv -h
  dbv query -db 1 -json 'SELECT id, name FROM users LIMIT 5'
  echo 'SELECT now()' | dbv query -db LocalDB
`

type cliOpts struct {
	db      string
	json    bool
	timeout time.Duration
	limit   int
	where   string
	order   string
	offset  int
}

type command struct {
	args int // required positional arguments; -1 means optional SQL
	run  func(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error
}

var commands = map[string]command{
	"dbs":     {args: 0},
	"tables":  {args: 0, run: cmdTables},
	"columns": {args: 1, run: cmdColumns},
	"rows":    {args: 1, run: cmdRows},
	"query":   {args: -1, run: cmdQuery},
	"struct":  {args: 1, run: cmdStruct},
	"insert":  {args: 1, run: cmdInsert},
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
	fs.StringVar(&o.db, "db", "", "")
	fs.BoolVar(&o.json, "json", false, "")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "")
	fs.IntVar(&o.limit, "limit", 1000, "")
	fs.StringVar(&o.where, "where", "", "")
	fs.StringVar(&o.order, "order", "", "")
	fs.IntVar(&o.offset, "offset", 0, "")

	err := fs.Parse(argv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbv %s: %v\n", name, err)
		return 2
	}
	args := fs.Args()
	if cmd.args >= 0 && len(args) != cmd.args {
		fmt.Fprintf(os.Stderr, "dbv %s: want %d argument(s), got %d (dbv -h)\n", name, cmd.args, len(args))
		return 2
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

	configs := loadDBConfigs(configPath())
	if name == "dbs" {
		return report(name, cmdDBs(configs, o))
	}

	cfg, err := pickConfig(configs, o.db)
	if err != nil {
		return report(name, err)
	}
	pg, err := db.New(cfg.URL)
	if err != nil {
		return report(name, fmt.Errorf("connect %q: %w", cfg.Title, err))
	}
	defer pg.Close()
	return report(name, cmd.run(ctx, pg, o, args))
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
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
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

func cmdTables(_ context.Context, pg *db.Postgres, o cliOpts, _ []string) error {
	tables, err := pg.ListTablesAndViews()
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

type columnJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func columnsJSON(cols []db.ColumnInfo) []columnJSON {
	out := make([]columnJSON, 0, len(cols))
	for _, c := range cols {
		out = append(out, columnJSON{Name: c.ColumnName, Type: c.DataType})
	}
	return out
}

func cmdRows(ctx context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	rel, err := pg.Resolve(args[0])
	if err != nil {
		return err
	}
	order := o.order
	if order == "" {
		pk, err := pg.GetPrimaryKeyColumns(rel.SQL)
		if err != nil {
			return err
		}
		for i, c := range pk {
			pk[i] = db.QuoteName(c)
		}
		order = strings.Join(pk, ", ")
	}
	var q strings.Builder
	q.WriteString("SELECT * FROM " + rel.SQL)
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
	if len(args) > 1 {
		return errors.New("one SQL argument at most; quote it")
	}
	sqlText := ""
	if len(args) == 1 {
		sqlText = args[0]
	}
	if sqlText == "" || sqlText == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		sqlText = strings.TrimPrefix(string(b), "\uFEFF")
	}
	if strings.TrimSpace(sqlText) == "" {
		return errors.New("empty SQL")
	}
	return printResult(ctx, pg, o, sqlText)
}

func printResult(ctx context.Context, pg *db.Postgres, o cliOpts, query string) error {
	res, err := pg.QueryReadOnly(ctx, o.limit, query)
	if err != nil {
		return err
	}
	if res.Truncated {
		fmt.Fprintf(os.Stderr, "dbv: stopped at %d rows; raise -limit for more\n", o.limit)
	}
	if o.json {
		rows := res.Rows
		if rows == nil {
			rows = [][]any{}
		}
		for _, r := range rows {
			for i, v := range r {
				s, ok := v.(string)
				t := res.Columns[i].DataType
				if ok && (t == "json" || t == "jsonb") {
					r[i] = json.RawMessage(s)
				}
			}
		}
		return writeJSON(struct {
			Columns   []columnJSON `json:"columns"`
			Rows      [][]any      `json:"rows"`
			Truncated bool         `json:"truncated"`
		}{columnsJSON(res.Columns), rows, res.Truncated})
	}
	header := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		header = append(header, c.ColumnName)
	}
	records := [][]string{header}
	for _, r := range res.Rows {
		line := make([]string, len(r))
		for i, v := range r {
			line[i] = "NULL"
			if v != nil {
				line[i] = fmt.Sprint(v)
			}
		}
		records = append(records, line)
	}
	return writeText(records)
}

func printSource(o cliOpts, key, text string) error {
	if o.json {
		return writeJSON(map[string]string{key: text})
	}
	_, err := fmt.Println(strings.TrimRight(text, "\n"))
	return err
}

func cmdStruct(_ context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	s, err := pg.CreateGoStructDefinition(args[0])
	if err != nil {
		return err
	}
	return printSource(o, "go", s)
}

func cmdInsert(_ context.Context, pg *db.Postgres, o cliOpts, args []string) error {
	s, err := pg.CreateInsertStatement(args[0])
	if err != nil {
		return err
	}
	return printSource(o, "sql", s)
}
