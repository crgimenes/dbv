package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
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

	size := 0
	for rows.Next() {
		if len(res.Rows) == limit || size > BytesMax {
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
			size += sizeOf(values[i])
		}
		res.Rows = append(res.Rows, values)
	}
	return res, rows.Err()
}

// BytesMax bounds what a result keeps besides its row limit: a thousand
// rows of 10 MB cells would otherwise all be held. The row that crosses it
// is the last kept.
const BytesMax = 64 << 20

func sizeOf(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []byte:
		return len(x)
	}
	return 16
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
	case float64:
		return nonFinite(x, v)
	case float32:
		return nonFinite(float64(x), v)
	}
	return v
}

// nonFinite is NaN and the infinities as PostgreSQL writes them: JSON has
// no number for them.
func nonFinite(f float64, v any) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return v
}

// ExecTx runs sqlText, which may hold several statements, in one
// transaction: all of it commits or none does. It returns the rows the last
// statement affected.
func (pg *Postgres) ExecTx(ctx context.Context, sqlText string) (int64, error) {
	w := txControl(sqlText)
	if w != "" {
		// a COMMIT inside would end this transaction, and what follows it
		// would no longer be undone with the rest
		return 0, fmt.Errorf("%s inside the SQL: it already runs as one transaction", w)
	}
	tx, err := pg.DB.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, sqlText)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
