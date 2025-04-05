package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

type ColumnInfoEx struct {
	ColumnName    string         `db:"column_name"`
	DataType      string         `db:"data_type"`
	ColumnDefault sql.NullString `db:"column_default"`
}

func (pg *Postgres) CreateInsertStatement(tableName string) (string, error) {
	// Retrieve columns with their default values.
	const sqlStatement = `
        SELECT
            column_name,
            data_type,
            column_default
        FROM information_schema.columns
        WHERE table_name = $1
        ORDER BY column_name;  -- initially ordered alphabetically
    `
	var cols []ColumnInfoEx
	if err := pg.DB.Select(&cols, sqlStatement, tableName); err != nil {
		return "", fmt.Errorf("error fetching columns: %w", err)
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("no columns found for table %s", tableName)
	}

	// Retrieve primary key column names.
	pkColumns, err := pg.GetPrimaryKeyColumns(tableName)
	if err != nil {
		return "", fmt.Errorf("error fetching primary keys: %w", err)
	}

	// Separate PK and non-PK columns.
	pkSet := make(map[string]bool, len(pkColumns))
	for _, pk := range pkColumns {
		pkSet[pk] = true
	}
	var primaryCols, nonPrimaryCols []ColumnInfoEx
	for _, c := range cols {
		if pkSet[c.ColumnName] {
			primaryCols = append(primaryCols, c)
		} else {
			nonPrimaryCols = append(nonPrimaryCols, c)
		}
	}

	// Sort non-PK columns alphabetically.
	sort.SliceStable(nonPrimaryCols, func(i, j int) bool {
		return strings.ToLower(nonPrimaryCols[i].ColumnName) < strings.ToLower(nonPrimaryCols[j].ColumnName)
	})

	// Sort PK columns based on the order returned by GetPrimaryKeyColumns.
	pkOrder := make(map[string]int, len(pkColumns))
	for i, pk := range pkColumns {
		pkOrder[pk] = i
	}
	sort.SliceStable(primaryCols, func(i, j int) bool {
		return pkOrder[primaryCols[i].ColumnName] < pkOrder[primaryCols[j].ColumnName]
	})

	// Concatenate PK and non-PK columns.
	finalCols := append(primaryCols, nonPrimaryCols...)

	type colData struct {
		baseCol string // e.g. "\t\"id\""
		baseVal string // e.g. "\tnextval('auth_group_id_seq'::regclass)"
		col     ColumnInfoEx
	}
	var dataList []colData
	for _, col := range finalCols {
		val := ""
		if col.ColumnDefault.Valid {
			val = col.ColumnDefault.String
		} else {
			lowerType := strings.ToLower(col.DataType)
			switch {
			case strings.Contains(lowerType, "char"), strings.Contains(lowerType, "text"):
				val = "''"
			case strings.Contains(lowerType, "int"):
				val = "0"
			case strings.Contains(lowerType, "numeric"),
				strings.Contains(lowerType, "real"),
				strings.Contains(lowerType, "double"):
				val = "0.0"
			case strings.Contains(lowerType, "bool"):
				val = "false"
			case strings.Contains(lowerType, "date"),
				strings.Contains(lowerType, "time"):
				val = "CURRENT_TIMESTAMP"
			default:
				val = "''"
			}
		}
		baseCol := fmt.Sprintf("\t\"%s\"", col.ColumnName)
		baseVal := fmt.Sprintf("\t%s", val)
		dataList = append(dataList, colData{baseCol: baseCol, baseVal: baseVal, col: col})
	}

	maxColWidth := 0
	maxValWidth := 0
	for _, d := range dataList {
		widthCol := len(d.baseCol) + 1 // +1 for comma
		if widthCol > maxColWidth {
			maxColWidth = widthCol
		}
		widthVal := len(d.baseVal) + 1
		if widthVal > maxValWidth {
			maxValWidth = widthVal
		}
	}

	var columnLines []string
	var valueLines []string
	for i, d := range dataList {
		colText := d.baseCol
		valText := d.baseVal
		if i != len(dataList)-1 {
			colText += ","
			valText += ","
		}
		colLine := fmt.Sprintf("%-*s -- %d", maxColWidth, colText, i+1)
		valLine := fmt.Sprintf("%-*s -- %d. %s", maxValWidth, valText, i+1, d.col.ColumnName)
		columnLines = append(columnLines, colLine)
		valueLines = append(valueLines, valLine)
	}

	columnsBlock := strings.Join(columnLines, "\n")
	valuesBlock := strings.Join(valueLines, "\n")

	stmt := fmt.Sprintf(`
INSERT INTO "%s"
(
%s
) VALUES (
%s
);
`, tableName, columnsBlock, valuesBlock)

	return stmt, nil
}
