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

// viewPrint is what ViewGuard compares: a view's options and its SELECT.
const viewPrint = `COALESCE(c.reloptions::text, '') || E'\n' || pg_get_viewdef(c.oid, true)`

// ViewDef is a view as a statement that puts it back as it is: CREATE OR
// REPLACE VIEW with its options (security_barrier, check_option) and its
// SELECT as the server prints it. raw is what ViewGuard wants.
func (pg *Postgres) ViewDef(ctx context.Context, rel Rel) (stmt, raw string, err error) {
	q := `SELECT 'CREATE OR REPLACE VIEW ' || c.oid::regclass::text
	|| COALESCE(' WITH (' || array_to_string(c.reloptions, ', ') || ')', '')
	|| E' AS\n' || rtrim(pg_get_viewdef(c.oid, true), E'; \n')
	|| E';\n',
	` + viewPrint + `
FROM pg_class c
WHERE c.oid = $1 AND c.relkind = 'v'`
	err = pg.DB.QueryRowContext(ctx, q, rel.OID).Scan(&stmt, &raw)
	if err != nil {
		return "", "", fmt.Errorf("no view %s: %w", rel.SQL, err)
	}
	return stmt, raw, nil
}

// MatViewDef is a materialized view as the statements that make it again,
// since a drop takes along all but its SELECT. It is made empty, given its
// indexes, comments, owner and exactly its grants (what default privileges
// would add is revoked), and only then filled: REFRESH runs as the owner,
// so the rows are the ones the owner sees, not the editor.
// yagni: the tablespaces of its indexes are left out.
func (pg *Postgres) MatViewDef(ctx context.Context, rel Rel) (script, raw string, err error) {
	q := `SELECT
	'DROP MATERIALIZED VIEW ' || n || E';\n'
	|| 'CREATE MATERIALIZED VIEW ' || n
	|| COALESCE(' WITH (' || array_to_string(c.reloptions, ', ') || ')', '')
	|| COALESCE((SELECT ' TABLESPACE ' || quote_ident(t.spcname) FROM pg_tablespace t WHERE t.oid = c.reltablespace), '')
	|| E' AS\n' || rtrim(pg_get_viewdef(c.oid, true), E'; \n') || E'\nWITH NO DATA;\n'
	|| COALESCE((SELECT string_agg(pg_get_indexdef(i.indexrelid) || E';\n', '' ORDER BY i.indexrelid)
	             FROM pg_index i WHERE i.indrelid = c.oid), '')
	|| COALESCE('COMMENT ON MATERIALIZED VIEW ' || n || ' IS ' || quote_literal(obj_description(c.oid, 'pg_class')) || E';\n', '')
	|| COALESCE((SELECT string_agg('COMMENT ON COLUMN ' || n || '.' || quote_ident(a.attname) || ' IS '
	                               || quote_literal(col_description(c.oid, a.attnum)) || E';\n', '' ORDER BY a.attnum)
	             FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attnum > 0 AND col_description(c.oid, a.attnum) IS NOT NULL), '')
	|| 'ALTER MATERIALIZED VIEW ' || n || ' OWNER TO ' || c.relowner::regrole::text || E';\n'
	|| 'REVOKE ALL ON ' || n || ' FROM ' || array_to_string(ARRAY(
	       SELECT DISTINCT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END
	       FROM pg_default_acl d, aclexplode(d.defaclacl) a
	       WHERE d.defaclobjtype = 'r' AND d.defaclrole IN (c.relowner, current_user::regrole)
	         AND d.defaclnamespace IN (0, c.relnamespace) AND a.grantee <> c.relowner
	       UNION SELECT 'PUBLIC'), ', ') || E';\n'
	|| COALESCE((SELECT string_agg('GRANT ' || g.privs || ' ON ' || n || ' TO ' || g.who || g.opt || E';\n', '' ORDER BY g.who, g.opt)
	             FROM (SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END AS who,
	                          CASE WHEN a.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END AS opt,
	                          string_agg(a.privilege_type, ', ' ORDER BY a.privilege_type) AS privs
	                   FROM aclexplode(c.relacl) a
	                   WHERE a.grantee <> c.relowner
	                   GROUP BY a.grantee, a.is_grantable) g), '')
	|| CASE WHEN c.relispopulated THEN 'REFRESH MATERIALIZED VIEW ' || n || E';\n' ELSE '' END,
	` + viewPrint + `
FROM pg_class c, LATERAL (SELECT c.oid::regclass::text AS n) r
WHERE c.oid = $1 AND c.relkind = 'm'`
	err = pg.DB.QueryRowContext(ctx, q, rel.OID).Scan(&script, &raw)
	if err != nil {
		return "", "", fmt.Errorf("no materialized view %s: %w", rel.SQL, err)
	}
	return script, raw, nil
}

// ViewGuard is a statement that fails unless rel is still what was read:
// its name still names it, its options and SELECT are still raw. It locks
// a view first, so nothing changes it between the check and the edit; put
// before the edit, in the same transaction, it keeps a change made
// meanwhile by someone else from being replaced unseen.
func ViewGuard(rel Rel, raw string) string {
	msg := "dbv: " + rel.SQL + " changed in the database since it was opened; open it again to see it"
	tag := "$dbv$"
	for n := 1; strings.Contains(raw+msg+rel.SQL, tag); n++ {
		tag = fmt.Sprintf("$dbv%d$", n)
	}
	return fmt.Sprintf("DO %[1]s BEGIN\n"+
		"IF (SELECT relkind FROM pg_class WHERE oid = %[2]d) = 'v' THEN\n"+ // LOCK takes no materialized view; its DROP locks it next
		"  EXECUTE format('LOCK TABLE %%s IN ACCESS EXCLUSIVE MODE', %[2]d::oid::regclass);\n"+
		"END IF;\n"+
		"IF to_regclass(%[3]s) IS DISTINCT FROM %[2]d::oid\n"+
		"  OR (SELECT "+viewPrint+" FROM pg_class c WHERE c.oid = %[2]d) IS DISTINCT FROM %[4]s THEN\n"+
		"  RAISE EXCEPTION USING MESSAGE = %[5]s;\n"+
		"END IF; END %[1]s;\n", tag, rel.OID, QuoteLiteral(rel.SQL), QuoteLiteral(raw), QuoteLiteral(msg))
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
