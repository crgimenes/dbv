package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type ColumnInfoEx struct {
	ColumnName    string         `db:"column_name"`
	DataType      string         `db:"data_type"`
	ColumnDefault sql.NullString `db:"column_default"`
}

func (pg *Postgres) CreateInsertStatement(tableName string) (string, error) {
	const sqlStatement = `
		SELECT column_name, data_type, column_default
		FROM information_schema.columns
		WHERE table_name = $1;
	`
	var columns []ColumnInfoEx
	err := pg.DB.Select(&columns, sqlStatement, tableName)
	if err != nil {
		return "", err
	}
	if len(columns) == 0 {
		return "", fmt.Errorf("no columns found for table %s", tableName)
	}

	var columnNames []string
	var valueList []string

	for i, col := range columns {
		columnNames = append(columnNames, fmt.Sprintf("\"%s\"", col.ColumnName))

		var value string
		if col.ColumnDefault.Valid {
			value = col.ColumnDefault.String
		} else {
			lowerType := strings.ToLower(col.DataType)
			switch {
			case strings.Contains(lowerType, "char") || strings.Contains(lowerType, "text"):
				value = "''"
			case strings.Contains(lowerType, "int"):
				value = "0"
			case strings.Contains(lowerType, "numeric") ||
				strings.Contains(lowerType, "real") ||
				strings.Contains(lowerType, "double"):
				value = "0.0"
			case strings.Contains(lowerType, "bool"):
				value = "false"
			case strings.Contains(lowerType, "date") || strings.Contains(lowerType, "time"):
				value = "CURRENT_TIMESTAMP"
			default:
				value = "''"
			}
		}
		valueList = append(valueList, fmt.Sprintf("%s -- %d", value, i+1))
	}

	var columnsStr string
	for i, colName := range columnNames {
		if i < len(columnNames)-1 {
			columnsStr += fmt.Sprintf("    %s,\n", colName)
			continue
		}
		columnsStr += fmt.Sprintf("    %s\n", colName)
	}

	var valuesStr string
	for i, val := range valueList {
		if i < len(valueList)-1 {
			valuesStr += fmt.Sprintf("    %s,\n", val)
			continue
		}
		valuesStr += fmt.Sprintf("    %s\n", val)
	}

	stmt := fmt.Sprintf(
		"INSERT INTO \"%s\"\n(\n%s) VALUES (\n%s);",
		tableName,
		columnsStr,
		valuesStr,
	)
	return stmt, nil
}
