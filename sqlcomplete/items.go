package sqlcomplete

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// keywords are offered where any word may go, and close a FROM list.
var keywords = []string{
	"SELECT", "FROM", "WHERE", "AND", "OR", "NOT", "IN", "IS", "NULL",
	"LIKE", "ILIKE", "BETWEEN", "EXISTS", "AS", "ON", "USING", "JOIN",
	"LEFT", "RIGHT", "INNER", "OUTER", "FULL", "CROSS", "GROUP", "BY",
	"ORDER", "ASC", "DESC", "NULLS", "FIRST", "LAST", "HAVING", "LIMIT",
	"OFFSET", "FETCH", "DISTINCT", "UNION", "ALL", "INTERSECT", "EXCEPT",
	"WITH", "INSERT", "INTO", "VALUES", "UPDATE", "SET", "DELETE",
	"RETURNING", "CASE", "WHEN", "THEN", "ELSE", "END", "TRUE", "FALSE",
	"CREATE", "ALTER", "DROP", "TABLE", "INDEX", "VIEW", "BEGIN", "COMMIT",
	"ROLLBACK", "EXPLAIN", "ANALYZE", "COUNT", "SUM", "AVG", "MIN", "MAX",
	"COALESCE", "NULLIF", "LOWER", "UPPER", "NOW", "CURRENT_DATE",
	"CURRENT_TIMESTAMP", "INTERVAL", "CAST", "ANY", "SIMILAR", "TO",
}

var keywordSet = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range keywords {
		m[k] = true
	}
	return m
}()

// Column and Table are the catalog: what the database has.
type Column struct {
	Name, Type string
}

type Table struct {
	Schema, Name, Kind string // Kind: "table", "view"...
	Columns            []Column
}

type Catalog struct {
	Tables []Table
}

// find is the table name names, schema when given; unquoted names are
// folded the way PostgreSQL folds them, so an exact match wins first.
func (c *Catalog) find(schema, name string) *Table {
	if c == nil {
		return nil
	}
	var folded *Table
	for i := range c.Tables {
		t := &c.Tables[i]
		if schema != "" && !strings.EqualFold(t.Schema, schema) {
			continue
		}
		if t.Name == name && (schema != "" || t.Schema == "public") {
			return t
		}
		if folded == nil && strings.EqualFold(t.Name, name) {
			folded = t
		}
	}
	return folded
}

// Item is one completion.
type Item struct {
	Label  string // as shown and typed to filter
	Insert string // as it goes in the text, quoted when it must be
	Kind   string // "keyword", "table", "view", "column"
	Detail string // a column's type and table, a table's schema
}

// Complete is what fits at ctx, those starting with what was typed. With
// no catalog (no database, or not read yet) it is keywords alone: a loose
// .sql file still completes.
func Complete(cat *Catalog, ctx Context) []Item {
	var out []Item
	switch ctx.Want {
	case Tables:
		out = tables(cat, ctx.Qualifier)
	case Columns:
		out = columns(qualified(cat, ctx), false)
	case Words:
		var ts []*Table
		for _, r := range ctx.Refs {
			t := cat.find(r.Schema, r.Name)
			if t != nil && !slices.Contains(ts, t) {
				ts = append(ts, t)
			}
		}
		out = append(columns(ts, len(ts) > 1), words(ctx.Prefix)...)
	}
	return byPrefix(out, ctx.Prefix)
}

// qualified is the table x in "x.": an alias of the statement, else a
// table of that name.
func qualified(cat *Catalog, ctx Context) []*Table {
	// innermost first, an alias or an unaliased table's name alike: both
	// sides folded, so "C" and c are two names
	for _, r := range ctx.Refs {
		if (r.Alias != "" && r.Alias == ctx.Qualifier) || (r.Alias == "" && r.Name == ctx.Qualifier) {
			return nonNil(cat.find(r.Schema, r.Name))
		}
	}
	return nonNil(cat.find(ctx.Schema, ctx.Qualifier))
}

func nonNil(t *Table) []*Table {
	if t == nil {
		return nil
	}
	return []*Table{t}
}

func columns(ts []*Table, sayTable bool) []Item {
	var out []Item
	for _, t := range ts {
		for _, c := range t.Columns {
			detail := c.Type
			if sayTable {
				detail += " · " + t.Name
			}
			out = append(out, Item{Label: c.Name, Insert: Name(c.Name), Kind: "column", Detail: detail})
		}
	}
	return out
}

func tables(cat *Catalog, schema string) []Item {
	if cat == nil {
		return nil
	}
	var out []Item
	for _, t := range cat.Tables {
		if schema != "" && !strings.EqualFold(t.Schema, schema) {
			continue
		}
		insert := Name(t.Name)
		if schema == "" && t.Schema != "public" {
			insert = Name(t.Schema) + "." + insert // off the search path, the name alone would not find it
		}
		out = append(out, Item{Label: t.Name, Insert: insert, Kind: t.Kind, Detail: t.Schema})
	}
	return out
}

// words are the keywords, in the case the user types: "sel" gets select.
func words(prefix string) []Item {
	lower := prefix != "" && strings.ToLower(prefix) == prefix
	out := make([]Item, len(keywords))
	for i, k := range keywords {
		if lower {
			k = strings.ToLower(k)
		}
		out[i] = Item{Label: k, Insert: k, Kind: "keyword"}
	}
	return out
}

func byPrefix(items []Item, prefix string) []Item {
	p := strings.ToLower(prefix)
	out := items[:0]
	for _, it := range items {
		if strings.HasPrefix(strings.ToLower(it.Label), p) {
			out = append(out, it)
		}
	}
	return out
}

// reserved are PostgreSQL 17's keywords that are not "unreserved"
// (SELECT word FROM pg_get_keywords() WHERE catcode <> 'U'): reserved, or
// a column, type or function name somewhere; a name among them is quoted.
var reserved = []string{
	"all", "analyse", "analyze", "and", "any", "array", "as", "asc",
	"asymmetric", "authorization", "between", "bigint", "binary", "bit",
	"boolean", "both", "case", "cast", "char", "character", "check", "coalesce",
	"collate", "collation", "column", "concurrently", "constraint", "create",
	"cross", "current_catalog", "current_date", "current_role",
	"current_schema", "current_time", "current_timestamp", "current_user",
	"dec", "decimal", "default", "deferrable", "desc", "distinct", "do", "else",
	"end", "except", "exists", "extract", "false", "fetch", "float", "for",
	"foreign", "freeze", "from", "full", "grant", "greatest", "group",
	"grouping", "having", "ilike", "in", "initially", "inner", "inout", "int",
	"integer", "intersect", "interval", "into", "is", "isnull", "join", "json",
	"json_array", "json_arrayagg", "json_exists", "json_object",
	"json_objectagg", "json_query", "json_scalar", "json_serialize",
	"json_table", "json_value", "lateral", "leading", "least", "left", "like",
	"limit", "localtime", "localtimestamp", "merge_action", "national",
	"natural", "nchar", "none", "normalize", "not", "notnull", "null", "nullif",
	"numeric", "offset", "on", "only", "or", "order", "out", "outer",
	"overlaps", "overlay", "placing", "position", "precision", "primary",
	"real", "references", "returning", "right", "row", "select", "session_user",
	"setof", "similar", "smallint", "some", "substring", "symmetric",
	"system_user", "table", "tablesample", "then", "time", "timestamp", "to",
	"trailing", "treat", "trim", "true", "union", "unique", "user", "using",
	"values", "varchar", "variadic", "verbose", "when", "where", "window",
	"with", "xmlattributes", "xmlconcat", "xmlelement", "xmlexists",
	"xmlforest", "xmlnamespaces", "xmlparse", "xmlpi", "xmlroot",
	"xmlserialize", "xmltable",
}

// Name is name as SQL must write it: bare when it can be, quoted when case,
// a space or a keyword would change what it means.
func Name(name string) string {
	plain := name != "" && !unicode.IsDigit(rune(name[0])) && !slices.Contains(reserved, name)
	for _, r := range name {
		plain = plain && (r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}
	if plain {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Describe is what the name at cur in stmt is, for a hover: a column's
// type and table, or a table's columns; "" when it names nothing known.
func Describe(cat *Catalog, stmt string, cur int) string {
	if cat == nil {
		return ""
	}
	end := cur
	for end < len(stmt) && end >= 0 && wordEnd(stmt, end) > end {
		end = wordEnd(stmt, end)
	}
	ctx := Analyze(stmt, end)
	word := ctx.Prefix
	if word == "" || ctx.Want < 0 {
		return ""
	}
	if ctx.Want == Tables {
		return describeTable(cat.find(ctx.Qualifier, word))
	}
	ts := qualified(cat, ctx)
	if ctx.Qualifier == "" {
		ts = nil
		for _, r := range ctx.Refs {
			if (r.Alias != "" && strings.EqualFold(r.Alias, word)) || strings.EqualFold(r.Name, word) {
				return describeTable(cat.find(r.Schema, r.Name)) // an alias or a table of the statement
			}
			t := cat.find(r.Schema, r.Name)
			if t != nil {
				ts = append(ts, t)
			}
		}
	}
	var lines []string
	for _, t := range ts {
		for _, c := range t.Columns {
			if strings.EqualFold(c.Name, word) {
				lines = append(lines, fmt.Sprintf("`%s` %s · %s.%s", c.Name, c.Type, t.Schema, t.Name))
			}
		}
	}
	if len(lines) == 0 && ctx.Qualifier == "" {
		return describeTable(cat.find("", word))
	}
	return strings.Join(lines, "\n\n")
}

func describeTable(t *Table) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s `%s.%s`\n", t.Kind, t.Schema, t.Name)
	for _, c := range t.Columns {
		fmt.Fprintf(&b, "\n- `%s` %s", c.Name, c.Type)
	}
	return b.String()
}
