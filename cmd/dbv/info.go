package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/crgimenes/dbv/db"
)

const infoUsage = `dbv info: which connection a .sql file gets, and what is on the other end.

Usage: dbv info [-config FILE] [-db NAME] [-timeout 5s] [FILE]

The connection is chosen as dbv show chooses it (dbv show -h). Prints its
title, why it was chosen, the URL without its password, the views
directory, then the server's version, user, database, size and table count,
and the list of connections. With no FILE, only -db, DBV_DB or the only
connection choose. Text on stdout; exit 1 when the connection fails.

Example: dbv info report.sql
`

const infoSQL = `SELECT current_setting('server_version'),
	current_user,
	current_database(),
	pg_size_pretty(pg_database_size(current_database())),
	(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	  WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
	    AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'),
	current_setting('default_transaction_read_only')`

func runInfo(argv []string) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "")
	name := fs.String("db", "", "")
	timeout := fs.Duration("timeout", 5*time.Second, "")
	err := fs.Parse(argv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(infoUsage)
		return 0
	}
	if err != nil || fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "dbv info: want at most one file (dbv info -h)")
		return 2
	}
	text := ""
	if fs.NArg() == 1 {
		b, err := os.ReadFile(filepath.Clean(fs.Arg(0)))
		if err != nil {
			return report("info", err)
		}
		text = string(b)
	}
	files, want, why := showDB(*config, *name, text)
	var out strings.Builder
	err = writeInfo(&out, files, want, why, *timeout)
	_, werr := os.Stdout.WriteString(out.String())
	return report("info", errors.Join(err, werr))
}

// writeInfo says what showDB chose; the connection's error, if any, is the
// error, after all that could be said without it.
func writeInfo(w *strings.Builder, files []string, want, why string, timeout time.Duration) error {
	var configs []db.DBConfig
	for _, f := range files {
		c, err := readDBConfigs(f)
		if err != nil {
			return fmt.Errorf("cannot read %s (dbv dbs -config %s says why)", tildePath(f), tildePath(f))
		}
		configs = append(configs, c...)
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = tildePath(f)
	}
	cfg, pickErr := pickConfig(configs, want)
	n := slices.Index(configs, cfg) + 1
	if pickErr == nil {
		masked, err := maskDBURL(cfg.URL)
		if err != nil {
			masked = "(unparsable url)"
		}
		fmt.Fprintf(w, "Database   %s (#%d)\n", cfg.Title, n)
		fmt.Fprintf(w, "Chosen by  %s\n", why)
		fmt.Fprintf(w, "URL        %s\n", masked)
		fmt.Fprintf(w, "Views      %s\n", tildePath(viewsDirFor(cfg)))
	}
	fmt.Fprintf(w, "Config     %s\n", strings.Join(paths, ", "))
	var connErr error
	if pickErr == nil {
		connErr = writeServer(w, cfg, timeout)
	}
	fmt.Fprintln(w, "\nConnections")
	for i, c := range configs {
		mark := " "
		if i+1 == n && pickErr == nil {
			mark = "*"
		}
		fmt.Fprintf(w, "  %s %d  %s\n", mark, i+1, c.Title)
	}
	if pickErr != nil {
		return fmt.Errorf("%w; name one with -db or a first line \"-- dbv: db=NAME\"", pickErr)
	}
	return connErr
}

func writeServer(w *strings.Builder, cfg db.DBConfig, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	var pg *db.Postgres
	err := bounded(ctx, func() error { // an ssh dial takes no context
		var err error
		pg, err = db.New(cfg.URL)
		return err
	})
	if err != nil {
		fmt.Fprintln(w, "Server     not reached")
		return withoutURL(err, cfg)
	}
	defer pg.Close()
	res, err := pg.QueryReadOnly(ctx, 1, infoSQL)
	if err != nil {
		fmt.Fprintln(w, "Server     reached, but it did not answer")
		return withoutURL(err, cfg)
	}
	r := res.Rows[0]
	fmt.Fprintf(w, "Server     PostgreSQL %v, %d ms\n", r[0], time.Since(start).Milliseconds())
	fmt.Fprintf(w, "Session    %v on %v\n", r[1], r[2])
	fmt.Fprintf(w, "Size       %v, %v tables and views\n", r[3], r[4])
	fmt.Fprintf(w, "Read only  %v by default\n", r[5])
	return nil
}

// bounded runs op until ctx ends; past that op is left to finish alone.
func bounded(ctx context.Context, op func() error) error {
	done := make(chan error, 1)
	go func() { done <- op() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
