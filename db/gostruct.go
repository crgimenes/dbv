package db

import (
	"fmt"
	"strings"
	"unicode"
)

func toTitle(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[i] = string(runes)
	}
	return strings.Join(words, "")
}

func (pg *Postgres) CreateGoStructDefinition(tableName string) (string, error) {
	structName := toTitle(strings.ReplaceAll(tableName, "_", " "))

	const sqlStatement = `
        SELECT
            column_name,
            data_type
        FROM information_schema.columns
        WHERE table_name = $1
        ORDER BY column_name;
    `
	type simpleColumnInfo struct {
		ColumnName string `db:"column_name"`
		DataType   string `db:"data_type"`
	}
	var cols []simpleColumnInfo
	if err := pg.DB.Select(&cols, sqlStatement, tableName); err != nil {
		return "", fmt.Errorf("error fetching columns: %w", err)
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("no columns found for table %s", tableName)
	}

	lines := []string{fmt.Sprintf("type %s struct {", structName)}
	for _, c := range cols {
		var goType string
		lowerType := strings.ToLower(c.DataType)
		switch {
		// Integer types.
		case strings.Contains(lowerType, "int"):
			switch {
			case lowerType == "bigint" || strings.Contains(lowerType, "bigserial"):
				goType = "int64"
			case lowerType == "smallint" || strings.Contains(lowerType, "smallserial"):
				goType = "int16"
			default:
				goType = "int"
			}
		// Boolean type.
		case strings.Contains(lowerType, "bool"):
			goType = "bool"
		// Floating point and numeric types.
		case strings.Contains(lowerType, "real"),
			strings.Contains(lowerType, "double precision"),
			strings.Contains(lowerType, "numeric"),
			strings.Contains(lowerType, "decimal"):
			goType = "float64"
		// Time types.
		case strings.Contains(lowerType, "timestamp"),
			strings.Contains(lowerType, "date"):
			goType = "time.Time"
		// JSON types.
		case strings.Contains(lowerType, "json"):
			goType = "json.RawMessage"
		// UUID type.
		case strings.Contains(lowerType, "uuid"):
			goType = "string"
		// Binary data.
		case strings.Contains(lowerType, "bytea"):
			goType = "[]byte"
		// Default to string.
		default:
			goType = "string"
		}
		// Convert column name to TitleCase for the struct field.
		fieldName := toTitle(c.ColumnName)
		line := fmt.Sprintf("\t%s %s `db:\"%s\"`", fieldName, goType, c.ColumnName)
		lines = append(lines, line)
	}
	lines = append(lines, "}")

	return strings.Join(lines, "\n"), nil
}
