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

// QuoteLiteral is an escape-string literal, E'...': it reads the same
// whatever standard_conforming_strings says.
func QuoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "E'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ViewDef is a view as a statement that puts it back as it is: CREATE OR
// REPLACE VIEW with its options (security_barrier, check_option) and its
// SELECT as the server prints it. raw is that SELECT as ViewGuard wants it.
func (pg *Postgres) ViewDef(ctx context.Context, rel Rel) (stmt, raw string, err error) {
	const q = `SELECT 'CREATE OR REPLACE VIEW ' || c.oid::regclass::text
	|| COALESCE(' WITH (' || array_to_string(c.reloptions, ', ') || ')', '')
	|| E' AS\n' || rtrim(pg_get_viewdef(c.oid, true), E'; \n')
	|| E';\n',
	pg_get_viewdef(c.oid, true)
FROM pg_class c
WHERE c.oid = $1 AND c.relkind = 'v'`
	err = pg.DB.QueryRowContext(ctx, q, rel.OID).Scan(&stmt, &raw)
	if err != nil {
		return "", "", fmt.Errorf("no view %s: %w", rel.SQL, err)
	}
	return stmt, raw, nil
}

// MatViewDef is a materialized view as the statements that make it again:
// DROP, then CREATE with its options and its SELECT, its indexes, comment,
// grants and owner, since a drop takes them along. raw is as for ViewDef.
// yagni: column comments and the tablespace are left out.
func (pg *Postgres) MatViewDef(ctx context.Context, rel Rel) (script, raw string, err error) {
	const q = `SELECT
	'DROP MATERIALIZED VIEW ' || c.oid::regclass::text || E';\n'
	|| 'CREATE MATERIALIZED VIEW ' || c.oid::regclass::text
	|| COALESCE(' WITH (' || array_to_string(c.reloptions, ', ') || ')', '')
	|| E' AS\n' || rtrim(pg_get_viewdef(c.oid, true), E'; \n')
	|| CASE WHEN c.relispopulated THEN E'\nWITH DATA;\n' ELSE E'\nWITH NO DATA;\n' END
	|| COALESCE((SELECT string_agg(pg_get_indexdef(i.indexrelid) || E';\n', '' ORDER BY i.indexrelid)
	             FROM pg_index i WHERE i.indrelid = c.oid), '')
	|| COALESCE('COMMENT ON MATERIALIZED VIEW ' || c.oid::regclass::text || ' IS '
	            || quote_literal(obj_description(c.oid, 'pg_class')) || E';\n', '')
	|| COALESCE((SELECT string_agg('GRANT ' || g.privs || ' ON ' || c.oid::regclass::text || ' TO ' || g.who || E';\n', '' ORDER BY g.who)
	             FROM (SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END AS who,
	                          string_agg(a.privilege_type, ', ' ORDER BY a.privilege_type) AS privs
	                   FROM aclexplode(c.relacl) a
	                   WHERE a.grantee <> c.relowner
	                   GROUP BY a.grantee) g), '')
	|| 'ALTER MATERIALIZED VIEW ' || c.oid::regclass::text || ' OWNER TO ' || c.relowner::regrole::text || E';\n',
	pg_get_viewdef(c.oid, true)
FROM pg_class c
WHERE c.oid = $1 AND c.relkind = 'm'`
	err = pg.DB.QueryRowContext(ctx, q, rel.OID).Scan(&script, &raw)
	if err != nil {
		return "", "", fmt.Errorf("no materialized view %s: %w", rel.SQL, err)
	}
	return script, raw, nil
}

// ViewGuard is a statement that fails unless rel's SELECT is still raw: put
// before an edit of it, in the same transaction, it keeps a change made
// meanwhile by someone else from being replaced unseen.
func ViewGuard(rel Rel, raw string) string {
	tag := "$dbv$"
	for n := 1; strings.Contains(raw+rel.SQL, tag); n++ {
		tag = fmt.Sprintf("$dbv%d$", n)
	}
	return fmt.Sprintf("DO %[1]s BEGIN IF pg_get_viewdef(%[2]d::oid, true) IS DISTINCT FROM %[3]s THEN "+
		"RAISE EXCEPTION 'dbv: %[4]s changed in the database since it was opened; open it again to see it'; "+
		"END IF; END %[1]s;\n", tag, rel.OID, QuoteLiteral(raw), strings.NewReplacer("'", "''", "%", "%%").Replace(rel.SQL))
}

// Rename renames a table, view or materialized view; to is the new name as
// it should read in the catalog, case and spaces kept.
func (pg *Postgres) Rename(ctx context.Context, name, to string) error {
	rel, err := pg.Resolve(ctx, name)
	if err != nil {
		return err
	}
	kind := map[string]string{"r": "TABLE", "p": "TABLE", "v": "VIEW", "m": "MATERIALIZED VIEW", "f": "FOREIGN TABLE"}[rel.Kind]
	if kind == "" {
		return fmt.Errorf("%s: not a table or a view", rel.SQL)
	}
	_, err = pg.RunDDL(ctx, "ALTER "+kind+" "+rel.SQL+" RENAME TO "+QuoteName(to))
	return err
}
