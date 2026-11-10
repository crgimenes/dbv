package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// ErrLeftOpen says the SQL opened a transaction and did not end it; Run
// rolled it back.
var ErrLeftOpen = errors.New("the SQL left a transaction open: it was rolled back (end it with COMMIT)")

// Statement is what one statement of a script answered: its command tag
// ("UPDATE 3", "SELECT 2") and, when it returns rows, the rows as text (nil
// is NULL).
type Statement struct {
	Tag       string
	Columns   []ColumnInfo
	Rows      [][]any
	Truncated bool
}

// Run sends sqlText as it is, one or many statements, the way psql does:
// in one simple-protocol message, which PostgreSQL runs as one transaction
// unless the text opens its own. Writes are allowed; this is the user
// typing SQL. Each statement keeps at most limit rows.
func (pg *Postgres) Run(ctx context.Context, sqlText string, limit int) ([]Statement, error) {
	return pg.run(ctx, sqlText, limit, "")
}

// RunReadOnly is Run with the session read only: every transaction the
// text runs, its own included, refuses a write (IsReadOnly says so). It
// guards against a slip, not against text that turns it off on purpose.
func (pg *Postgres) RunReadOnly(ctx context.Context, sqlText string, limit int) ([]Statement, error) {
	return pg.run(ctx, sqlText, limit, "SET default_transaction_read_only = on")
}

// LockWait is how long RunDDL waits for a lock. ALTER TABLE waits for an
// exclusive one, and while it waits every query on the table queues
// behind it: on a busy table, failing soon is the lesser harm.
const LockWait = "5s"

func (pg *Postgres) RunDDL(ctx context.Context, sqlText string) ([]Statement, error) {
	w := txControl(sqlText)
	if w != "" { // a COMMIT inside would keep a DROP whose CREATE then fails
		return nil, fmt.Errorf("%s inside the SQL: it already runs as one transaction", w)
	}
	return pg.run(ctx, sqlText, 1, "SET lock_timeout = '"+LockWait+"'")
}

// IsReadOnly is the server refusing a write in a read-only transaction.
func IsReadOnly(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "25006"
}

// run sends sqlText on a connection setup prepared first; a connection
// with a setup is closed after, never handed out again.
func (pg *Postgres) run(ctx context.Context, sqlText string, limit int, setup string) ([]Statement, error) {
	conn, err := pg.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	var out []Statement
	err = conn.Raw(func(dc any) error {
		c, ok := dc.(*stdlib.Conn)
		if !ok {
			return errors.New("not a pgx connection")
		}
		if setup != "" {
			_, err := c.Conn().Exec(ctx, setup)
			if err != nil {
				return err
			}
		}
		types := c.Conn().TypeMap()
		mrr := c.Conn().PgConn().Exec(ctx, sqlText)
		for mrr.NextResult() {
			rr := mrr.ResultReader()
			var st Statement
			for _, f := range rr.FieldDescriptions() {
				name := "unknown"
				t, ok := types.TypeForOID(f.DataTypeOID)
				if ok {
					name = t.Name
				}
				st.Columns = append(st.Columns, ColumnInfo{ColumnName: f.Name, DataType: name})
			}
			size := 0
			for rr.NextRow() {
				if len(st.Rows) == limit || size > BytesMax {
					st.Truncated = true
					continue // the rest is read off the wire, not kept
				}
				values := rr.Values()
				row := make([]any, len(values))
				for i, v := range values {
					if v != nil {
						row[i] = string(v)
					}
					size += len(v)
				}
				st.Rows = append(st.Rows, row)
			}
			tag, err := rr.Close()
			if err != nil {
				_ = mrr.Close()
				return err
			}
			st.Tag = tag.String()
			out = append(out, st)
		}
		err := mrr.Close()
		if err != nil {
			return err
		}
		if c.Conn().PgConn().TxStatus() != 'I' {
			// a BEGIN with no COMMIT: the pool would take the connection
			// back mid-transaction, holding its locks until someone else
			// drew it and threw it away
			_, rerr := c.Conn().Exec(ctx, "ROLLBACK")
			return errors.Join(ErrLeftOpen, rerr)
		}
		return nil
	})
	if setup != "" {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn }) // closed: the pool must not hand out a session set up for one use
	}
	return out, err
}
