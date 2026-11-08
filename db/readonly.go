package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Result is a query answer in column order; Truncated says rows past the
// limit were left unread.
type Result struct {
	Columns   []ColumnInfo
	Rows      [][]any
	Truncated bool
}

// QueryReadOnly runs query inside a READ ONLY transaction, so the server
// refuses any write the text carries, whatever the caller put in it.
func (pg *Postgres) QueryReadOnly(ctx context.Context, limit int, query string, args ...any) (Result, error) {
	var res Result
	tx, err := pg.DB.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	// The extended protocol takes one statement: "COMMIT; DELETE ..." is
	// refused by the server instead of ending the READ ONLY transaction,
	// even when the URL asks for the simple protocol.
	args = append([]any{pgx.QueryExecModeDescribeExec}, args...)
	rows, err := tx.QueryxContext(ctx, query, args...)
	if err != nil {
		return res, err
	}
	defer func() { _ = rows.Close() }()

	types, err := rows.ColumnTypes()
	if err != nil {
		return res, err
	}
	for _, t := range types {
		res.Columns = append(res.Columns, ColumnInfo{
			ColumnName: t.Name(),
			DataType:   strings.ToLower(t.DatabaseTypeName()),
		})
	}

	for rows.Next() {
		if len(res.Rows) == limit {
			res.Truncated = true
			break
		}
		values := make([]any, len(types))
		pointers := make([]any, len(types))
		for i := range values {
			pointers[i] = &values[i]
		}
		err = rows.Scan(pointers...)
		if err != nil {
			return res, err
		}
		for i, v := range values {
			values[i] = plain(v, res.Columns[i].DataType)
		}
		res.Rows = append(res.Rows, values)
	}
	return res, rows.Err()
}

func plain(v any, dataType string) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		if dataType == "date" {
			return x.Format(time.DateOnly)
		}
		return x.Format(time.RFC3339Nano)
	}
	return v
}
