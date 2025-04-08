package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/crgimenes/dbv/db"
)

type modelEditor struct {
	multiEditing bool
	textArea     textarea.Model
	editMode     string // "cell" or "insert"
}

func newModelEditor() *modelEditor {
	return &modelEditor{
		editMode: "cell",
	}
}

func (me *modelEditor) IsMultiEditing() bool {
	return me.multiEditing
}

func (me *modelEditor) StartMultiEditing(initialValue string, width, height int, mode string) {
	ta := textarea.New()
	ta.SetWidth(width - 2)
	ta.SetHeight(height - 3)
	ta.MaxHeight = 0
	ta.Focus()

	ta.CharLimit = 9437184
	ta.SetValue(initialValue)
	ta.Prompt = ""
	ta.ShowLineNumbers = false

	me.textArea = ta
	me.multiEditing = true
	me.editMode = mode
}

func (me *modelEditor) UpdateEditor(msg tea.Msg, m *modelData) (bool, tea.Cmd) {
	if me.multiEditing {
		var cmd tea.Cmd
		me.textArea, cmd = me.textArea.Update(msg)
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.Type {
			case tea.KeyCtrlS:
				switch me.editMode {
				case "cell":
					if me.updateCellValue(m, me.textArea.Value()) {
						me.multiEditing = false
					}
				case "insert":
					sql := me.textArea.Value()
					err := db.Storage.Exec(sql)
					if err != nil {
						m.showingError = true
						m.errorMessage = fmt.Sprintf("Error executing: %v", err)
					} else {
						me.multiEditing = false
						return false, m.resetTable()
					}
				case "update":
					sql := me.textArea.Value()
					err := db.Storage.Exec(sql)
					if err != nil {
						m.showingError = true
						m.errorMessage = fmt.Sprintf("Error executing: %v", err)
					} else {
						me.multiEditing = false
						return false, m.resetTable()
					}
				}
			case tea.KeyCtrlO:
				return me.multiEditing, openExternalEditor(me.textArea.Value())
			case tea.KeyEsc:
				me.multiEditing = false
			}
		}

		switch x := msg.(type) {
		case externalEditorMsg:
			if x.err != nil {
				m.showingError = true
				m.errorMessage = x.err.Error()
			} else {
				me.textArea.SetValue(x.newText)
			}
		}
		return me.multiEditing, cmd
	}

	return false, nil
}

func (me *modelEditor) updateCellValue(m *modelData, newValue string) bool {
	if m.pk == "" || m.pk == "-" {
		m.showingError = true
		m.errorMessage = "Read-only mode"
		return false
	}

	updatedValue := newValue
	for _, colInfo := range m.columnInfo {
		if colInfo.ColumnName == m.data[0][m.selectedCol] {
			if strings.Contains(strings.ToLower(colInfo.DataType), "timestamp") {
				formatted, errConv := formatTimestamp(newValue)
				if errConv != nil {
					m.showingError = true
					m.errorMessage = fmt.Sprintf("Error converting timestamp: %v", errConv)
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
		m.errorMessage = fmt.Sprintf("Error getting primary key columns: %v", err)
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
		log.Println("Error updating cell:", err)

		m.showingError = true
		m.errorMessage = fmt.Sprintf("Error updating: %v", err)
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
	return "", fmt.Errorf("unable to parse timestamp: %s", input)
}
