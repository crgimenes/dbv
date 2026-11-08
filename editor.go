package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

func rowJSON(header []string, row []any) (string, error) {
	if row == nil {
		return "", errors.New("no row selected")
	}
	record := make(map[string]any, len(header))
	for i, name := range header {
		record[name] = row[i]
		s, ok := row[i].(string)
		if !ok {
			continue // nil: null
		}
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && strings.Trim(s, "0123456789.eE+-") == "" {
			record[name] = f
		}
	}
	out, err := json.MarshalIndent(record, "", "  ")
	return string(out), err
}
