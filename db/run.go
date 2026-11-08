package db

import (
	"context"
	"errors"

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
			for rr.NextRow() {
				if len(st.Rows) == limit {
					st.Truncated = true
					continue // the rest is read off the wire, not kept
				}
				values := rr.Values()
				row := make([]any, len(values))
				for i, v := range values {
					if v != nil {
						row[i] = string(v)
					}
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
	return out, err
}
