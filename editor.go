package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crgimenes/dbv/db"
)

type ExternalEditType string

const (
	CellEdit   ExternalEditType = "cell"
	InsertEdit ExternalEditType = "insert"
	UpdateEdit ExternalEditType = "update"
	StructEdit ExternalEditType = "struct"
	JsonEdit   ExternalEditType = "json"
)

func handleExternalEditorReturn(m *modelData, msg externalEditorMsg, editType ExternalEditType) bool {
	if msg.err != nil {
		m.showingError = true
		m.errorMessage = fmt.Sprintf("Erro no editor externo: %v", msg.err)
		return false
	}

	if !msg.changed {
		// nothing changed
		return false
	}

	switch editType {
	case CellEdit:
		return updateCellValue(m, msg.newText)
	case InsertEdit, UpdateEdit:
		sql := msg.newText
		err := db.Storage.Exec(sql)
		if err != nil {
			m.showingError = true
			m.errorMessage = fmt.Sprintf("Erro executando: %v", err)
			return false
		}
		m.resetTable()
		return true
	default:
		// StructEdit and JsonEdit (noting to do)
		return true
	}
}

func updateCellValue(m *modelData, newValue string) bool {
	if m.pk == "" || m.pk == "-" {
		m.showingError = true
		m.errorMessage = "Readonly, primary key not detected"
		return false
	}

	updatedValue := newValue
	for _, colInfo := range m.columnInfo {
		if colInfo.ColumnName == m.data[0][m.selectedCol] {
			if strings.Contains(strings.ToLower(colInfo.DataType), "timestamp") {
				formatted, errConv := formatTimestamp(newValue)
				if errConv != nil {
					m.showingError = true
					m.errorMessage = fmt.Sprintf("Erro convertendo timestamp: %v", errConv)
					return false
				}
				updatedValue = formatted
			}
			break
		}
	}

	idxMap := make(map[string]int)
	for i, colName := range m.data[0] {
		idxMap[colName] = i
	}

	pks, err := db.Storage.GetPrimaryKeyColumns(m.tableName)
	if err != nil {
		m.showingError = true
		m.errorMessage = fmt.Sprintf("Error getting primary keys: %v", err)
		return false
	}

	values := make([]any, len(pks))
	for i, pkName := range pks {
		colIndex, ok := idxMap[pkName]
		if !ok {
			return false
		}
		values[i] = m.data[m.selectedRow][colIndex]
	}

	err = db.Storage.UpdateDataCell(
		m.tableName,
		m.data[0][m.selectedCol],
		updatedValue,
		pks,
		values,
	)

	if err != nil {
		m.showingError = true
		m.errorMessage = fmt.Sprintf("Error updating cell: %v", err)
		return false
	}

	m.data[m.selectedRow][m.selectedCol] = updatedValue
	return true
}

func formatTimestamp(input string) (string, error) {
	if t, err := time.Parse(time.RFC3339Nano, input); err == nil {
		return t.Format(time.RFC3339), nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999 -0700 MST", input); err == nil {
		return t.Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("invalid timestamp format for: %s", input)
}

func openEditorWithContent(content string, editType ExternalEditType) tea.Cmd {
	return func() tea.Msg {
		return openExternalEditor(content)()
	}
}

func createJSONStructure(m *modelData) (string, error) {
	if m.selectedRow < 1 || m.selectedRow >= len(m.data) {
		return "", fmt.Errorf("nenhum registro selecionado")
	}

	cols := m.data[0]
	row := m.data[m.selectedRow]
	record := make(map[string]any)

	for i, colName := range cols {
		val := row[i]
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			record[colName] = f
		} else if val == "NULL" {
			record[colName] = nil
		} else {
			record[colName] = val
		}
	}

	output, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	return string(output), nil
}
