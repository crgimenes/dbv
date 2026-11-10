package db

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// initialisms are written whole in a Go name, as golint and the standard
// library write them.
var initialisms = map[string]bool{
	"ID": true, "URL": true, "URI": true, "UUID": true, "JSON": true, "SQL": true,
	"HTTP": true, "HTTPS": true, "API": true, "IP": true, "CPU": true, "XML": true, "HTML": true,
}

// goName is a column or table name as an exported Go identifier: every run
// of letters and digits a word, capitalized; a leading digit gets an F.
func goName(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b strings.Builder
	for _, w := range words {
		up := strings.ToUpper(w)
		if initialisms[up] {
			b.WriteString(up)
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	name := b.String()
	if name == "" {
		return "Field"
	}
	if unicode.IsDigit([]rune(name)[0]) {
		return "F" + name
	}
	return name
}

// goType is the Go type a column of PostgreSQL type t (format_type's
// spelling) scans into; nullable ones are pointers, but for the types
// whose nil already says NULL.
func goType(t string, nullable bool) string {
	base, _, _ := strings.Cut(t, "(")
	array := strings.HasSuffix(t, "[]")
	var g string
	switch {
	case array:
		g = "string" // the array as PostgreSQL writes it: {1,2}
	case base == "smallint":
		g = "int16"
	case base == "integer":
		g = "int32"
	case base == "bigint":
		g = "int64"
	case base == "real":
		g = "float32"
	case base == "double precision":
		g = "float64"
	case base == "boolean":
		g = "bool"
	case base == "date", strings.HasPrefix(base, "timestamp"):
		g = "time.Time"
	case base == "json", base == "jsonb":
		return "json.RawMessage"
	case base == "bytea":
		return "[]byte"
	default:
		g = "string" // numeric too: exact as text, where float64 would round
	}
	if nullable {
		return "*" + g
	}
	return g
}

func (pg *Postgres) CreateGoStructDefinition(ctx context.Context, tableName string) (string, error) {
	rel, err := pg.Resolve(ctx, tableName)
	if err != nil {
		return "", err
	}
	cols, err := pg.Structure(ctx, rel)
	if err != nil {
		return "", fmt.Errorf("error fetching columns: %w", err)
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("no columns found for table %s", tableName)
	}
	sort.SliceStable(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })

	lines := []string{fmt.Sprintf("type %s struct {", goName(rel.Name))}
	used := map[string]bool{}
	for _, c := range cols {
		field := goName(c.Name)
		for n := 2; used[field]; n++ {
			field = goName(c.Name) + strconv.Itoa(n)
		}
		used[field] = true
		tag := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(c.Name)
		lines = append(lines, fmt.Sprintf("\t%s %s `db:\"%s\"`", field, goType(c.Type, !c.NotNull), tag))
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n"), nil
}
