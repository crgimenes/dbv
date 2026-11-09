package db

import (
	"context"
)

// Column is a column as the structure screen shows it and ALTER TABLE
// names it.
type Column struct {
	Name      string
	Type      string // format_type with its modifier: varchar(80), numeric(10,2)
	NotNull   bool
	Default   string // the expression; "" none
	Identity  string // "a" always, "d" by default, "" not one
	Generated string // "s" stored, "" not one
	Key       bool   // in the primary key
	Comment   string
}

// Structure reads rel's columns in their order.
func (pg *Postgres) Structure(ctx context.Context, rel Rel) ([]Column, error) {
	const q = `SELECT
	a.attname,                                            -- 1
	format_type(a.atttypid, a.atttypmod),                 -- 2
	a.attnotnull,                                         -- 3
	COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),        -- 4
	a.attidentity::text,                                  -- 5
	a.attgenerated::text,                                 -- 6
	EXISTS (SELECT 1 FROM pg_index i
	        WHERE i.indrelid = a.attrelid AND i.indisprimary
	          AND a.attnum = ANY(i.indkey[0:i.indnkeyatts - 1])), -- 7
	COALESCE(col_description(a.attrelid, a.attnum), '')   -- 8
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`
	rows, err := pg.DB.QueryContext(ctx, q, rel.OID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Column
	for rows.Next() {
		var c Column
		err = rows.Scan(
			&c.Name,      // 1
			&c.Type,      // 2
			&c.NotNull,   // 3
			&c.Default,   // 4
			&c.Identity,  // 5
			&c.Generated, // 6
			&c.Key,       // 7
			&c.Comment,   // 8
		)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
