package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crgimenes/dbv/db"
)

// updateCell writes value into column col of row, found by its primary key.
func updateCell(ctx context.Context, pg *db.Postgres, table string, header []string, columns []db.ColumnInfo, row []string, col int, value string) error {
	for _, c := range columns {
		if c.ColumnName != header[col] || !strings.Contains(strings.ToLower(c.DataType), "timestamp") {
			continue
		}
		formatted, err := formatTimestamp(value)
		if err != nil {
			return err
		}
		value = formatted
	}

	pks, err := pg.GetPrimaryKeyColumns(ctx, table)
	if err != nil {
		return fmt.Errorf("primary key: %w", err)
	}
	if len(pks) == 0 {
		return errors.New("read only: no primary key")
	}
	at := make(map[string]int, len(header))
	for i, name := range header {
		at[name] = i
	}
	values := make([]any, len(pks))
	for i, name := range pks {
		j, ok := at[name]
		if !ok {
			return fmt.Errorf("primary key column %q is not on screen", name)
		}
		values[i] = row[j]
	}
	return pg.UpdateDataCell(ctx, table, header[col], value, pks, values)
}

func formatTimestamp(input string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, input)
	if err == nil {
		return t.Format(time.RFC3339Nano), nil
	}
	t, err = time.Parse("2006-01-02 15:04:05.999999 -0700 MST", input)
	if err == nil {
		return t.Format(time.RFC3339Nano), nil
	}
	return "", fmt.Errorf("invalid timestamp: %s", input)
}

// rowJSON is the row as one JSON object, keys in column order (a
// repeated name stays twice, as the query returned it). Each value is
// typed by its column, never guessed from its text: "00123" in a text
// column stays a string, a bigint keeps every digit, NaN is a string.
func rowJSON(columns []db.ColumnInfo, row []any) (string, error) {
	if row == nil {
		return "", errors.New("no row selected")
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, c := range columns {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(c.ColumnName)
		if err != nil {
			return "", err
		}
		b.Write(key)
		b.WriteByte(':')
		v, err := json.Marshal(row[i])
		s, ok := row[i].(string)
		if ok {
			v, err = cellJSON(c.DataType, s)
		}
		if err != nil {
			return "", err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	var out bytes.Buffer
	err := json.Indent(&out, b.Bytes(), "", "  ")
	return out.String(), err
}

// cellJSON is s, a value's text, as JSON for a column of type t: names as
// information_schema gives them (a table's grid) or as pgx does (a query's).
func cellJSON(t, s string) ([]byte, error) {
	switch t {
	case "smallint", "integer", "bigint", "numeric", "real", "double precision",
		"int2", "int4", "int8", "float4", "float8", "oid":
		if json.Valid([]byte(s)) && (s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) {
			return []byte(s), nil // exact digits; NaN and Infinity are no JSON number
		}
	case "boolean", "bool":
		switch s {
		case "true", "t":
			return []byte("true"), nil
		case "false", "f":
			return []byte("false"), nil
		}
	case "json", "jsonb":
		if json.Valid([]byte(s)) {
			return []byte(s), nil
		}
	}
	return json.Marshal(s)
}
