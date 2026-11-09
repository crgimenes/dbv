package db

import (
	"context"
	"fmt"
	"strings"
)

// Rel is a table or view as the server resolves its name: by search_path,
// with the quoting and the schema a SELECT would use.
type Rel struct {
	SQL    string // ready to put in a statement: oid::regclass::text
	Schema string
	Name   string
	OID    uint32
	Kind   string // pg_class.relkind: r table, p partitioned, v view, m materialized view, f foreign
	Parent bool   // other tables inherit from it (relhassubclass; partitions too)
}

// From is the relation as a SELECT reads it: a table others inherit from
// without their rows, the same rows an UPDATE reaches; a partitioned one
// with its partitions, which hold all its rows. ONLY only where it changes
// something, so the SQL shown stays plain.
func (r Rel) From() string {
	if r.Parent && r.Kind == "r" {
		return "ONLY " + r.SQL
	}
	return r.SQL
}

// Target is the relation as an UPDATE writes it: the rows From reads.
func (r Rel) Target() string { return r.From() }

// Resolve accepts a bare relation name as the catalog lists it ("Order
// Items", "Users") or a qualified one as typed ("archive.users"). A bare
// name is tried quoted first, so its case and spaces stay its own.
func (pg *Postgres) Resolve(ctx context.Context, name string) (Rel, error) {
	const q = `SELECT
	c.oid,                  -- 1
	c.oid::regclass::text,  -- 2
	n.nspname,              -- 3
	c.relname,              -- 4
	c.relkind::text,        -- 5
	c.relhassubclass        -- 6
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.oid = COALESCE(to_regclass(quote_ident($1)), to_regclass($1))`
	var r Rel
	err := pg.DB.QueryRowContext(ctx, q, name).Scan(
		&r.OID,    // 1
		&r.SQL,    // 2
		&r.Schema, // 3
		&r.Name,   // 4
		&r.Kind,   // 5
		&r.Parent, // 6
	)
	if err != nil {
		return r, fmt.Errorf("no table %q: %w", name, err)
	}
	return r, nil
}

// QuoteName quotes one identifier (a column), dots and all.
func QuoteName(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral is an escape-string literal, E'...': it reads the same
// whatever standard_conforming_strings says.
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "E'" + strings.ReplaceAll(s, "'", "''") + "'"
}
