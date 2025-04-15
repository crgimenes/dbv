package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/crgimenes/dbv/db"
)

type selectorMode int

const (
	tableSelectMode selectorMode = iota
	fieldSelectMode
)

type modelSelector struct {
	currentMode      selectorMode
	originalTables   []TableInfo
	tables           []TableInfo
	originalFields   []db.ColumnInfo
	fields           []db.ColumnInfo
	selectedTable    string
	selectedRow      int
	selectedFieldRow int
	windowWidth      int
	windowHeight     int
	offset           int
	fieldOffset      int
	textInput        textinput.Model
	textInputActive  bool
	statusMessage    string
	err              error
	outputFile       string
}

func initialModelSelector(outputFile string) modelSelector {
	tables, err := db.Storage.ListTablesAndViews()
	if err != nil {
		return modelSelector{
			err:        err,
			outputFile: outputFile,
		}
	}

	data := make([]TableInfo, len(tables))
	for i, t := range tables {
		pk := "-"
		if t.PrimaryKey.Valid {
			pk = t.PrimaryKey.String
		}
		data[i] = TableInfo{
			Name:       t.Name,
			Type:       t.Type,
			Size:       t.Size,
			PrimaryKey: pk,
		}
	}

	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "regex filter"
	ti.CharLimit = 256
	ti.Width = 20

	return modelSelector{
		currentMode:      tableSelectMode,
		originalTables:   data,
		tables:           data,
		selectedRow:      0,
		selectedFieldRow: 0,
		offset:           0,
		fieldOffset:      0,
		outputFile:       outputFile,
		textInput:        ti,
	}
}

func (m modelSelector) Init() tea.Cmd {
	return nil
}

func (m modelSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Tratar entrada de texto para filtragem
	if m.textInputActive {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				m.textInputActive = false
				return m, nil

			case "up", "down", "esc":
				m.textInputActive = false
				// Se ESC, limpar o filtro
				if msg.String() == "esc" {
					m.textInput.SetValue("")
					if m.currentMode == tableSelectMode {
						m.tables = m.originalTables
					} else {
						m.fields = m.originalFields
					}
					m.selectedRow = 0
					m.selectedFieldRow = 0
					m.offset = 0
					m.fieldOffset = 0
				}
				return m, nil
			}
		}

		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)

		filter := m.textInput.Value()
		if m.currentMode == tableSelectMode {
			if filter == "" {
				m.tables = m.originalTables
			} else {
				if re, err := regexp.Compile(filter); err == nil {
					var filtered []TableInfo
					for _, row := range m.originalTables {
						if re.MatchString(row.Name) {
							filtered = append(filtered, row)
						}
					}
					m.tables = filtered
				}
			}
			m.selectedRow = 0
			m.offset = 0
		} else {
			if filter == "" {
				m.fields = m.originalFields
			} else {
				if re, err := regexp.Compile(filter); err == nil {
					var filtered []db.ColumnInfo
					for _, field := range m.originalFields {
						if re.MatchString(field.ColumnName) {
							filtered = append(filtered, field)
						}
					}
					m.fields = filtered
				}
			}
			m.selectedFieldRow = 0
			m.fieldOffset = 0
		}

		return m, cmd
	}

	switch msg := msg.(type) {
	case errMsg:
		m.err = msg
		return m, nil

	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.err != nil {
			m.err = nil
			return m, nil
		}

		switch msg.String() {
		case "/":
			m.textInputActive = true
			m.textInput.Prompt = "/"
			m.textInput.Placeholder = "regex filter"
			m.textInput.Focus()
			m.statusMessage = ""
			return m, nil

		case "tab", "enter":
			if m.currentMode == tableSelectMode && len(m.tables) > 0 {
				tableName := m.tables[m.selectedRow].Name
				columns, err := db.Storage.ListColumns(tableName)
				if err != nil {
					m.err = err
					return m, nil
				}
				m.originalFields = columns
				m.fields = columns
				m.selectedFieldRow = 0
				m.fieldOffset = 0
				m.selectedTable = tableName
				m.currentMode = fieldSelectMode
			} else if m.currentMode == fieldSelectMode {
				m.currentMode = tableSelectMode
			}
			return m, nil

		case "up", "k":
			if m.currentMode == tableSelectMode {
				if m.selectedRow > 0 {
					m.selectedRow--
				}
				if m.selectedRow < m.offset {
					m.offset = m.selectedRow
				}
			} else {
				if m.selectedFieldRow > 0 {
					m.selectedFieldRow--
				}
				if m.selectedFieldRow < m.fieldOffset {
					m.fieldOffset = m.selectedFieldRow
				}
			}

		case "down", "j":
			if m.currentMode == tableSelectMode {
				if m.selectedRow < len(m.tables)-1 {
					m.selectedRow++
				}
				tableDataArea := m.windowHeight - 2
				if tableDataArea > 0 && m.selectedRow >= m.offset+tableDataArea {
					m.offset = m.selectedRow - tableDataArea + 1
				}
			} else {
				if m.selectedFieldRow < len(m.fields)-1 {
					m.selectedFieldRow++
				}
				fieldDataArea := m.windowHeight - 2
				if fieldDataArea > 0 && m.selectedFieldRow >= m.fieldOffset+fieldDataArea {
					m.fieldOffset = m.selectedFieldRow - fieldDataArea + 1
				}
			}

		case "S":
			if m.currentMode == tableSelectMode && len(m.tables) > 0 {
				tableName := m.tables[m.selectedRow].Name
				structDefinition, err := db.Storage.CreateGoStructDefinition(tableName)
				if err != nil {
					m.err = err
					return m, nil
				}

				err = os.WriteFile(m.outputFile, []byte(structDefinition), 0644)
				if err != nil {
					m.err = err
					return m, nil
				}

				m.statusMessage = fmt.Sprintf("Struct %s written to %s", tableName, m.outputFile)
			}
			return m, nil

		case " ", "s":
			var text string
			if m.currentMode == tableSelectMode && len(m.tables) > 0 {
				text = m.tables[m.selectedRow].Name
			} else if m.currentMode == fieldSelectMode && len(m.fields) > 0 {
				text = m.fields[m.selectedFieldRow].ColumnName
			} else {
				return m, nil
			}

			err := os.WriteFile(m.outputFile, []byte(text), 0644)
			if err != nil {
				m.err = err
				return m, nil
			}
			m.statusMessage = fmt.Sprintf("Selecionado: %s", text)
			return m, nil

		case "backspace", "esc", "shift+tab":
			if m.currentMode == fieldSelectMode {
				m.currentMode = tableSelectMode
			} else {
				return m, tea.Quit
			}

		case "q", "ctrl+c":
			return m, tea.Quit
		}

		m.statusMessage = ""
	}
	return m, nil
}

func (m modelSelector) View() string {
	if m.err != nil {
		return errorStatusBarStyle.Render(fmt.Sprintf(
			"Error: %s (press any key to continue)",
			m.err))
	}

	width := m.windowWidth
	if width == 0 {
		width = 80
	}

	height := m.windowHeight
	if height == 0 {
		height = 24
	}

	var sb strings.Builder

	if m.currentMode == tableSelectMode {
		tableDataArea := height - 2
		visibleEnd := m.offset + tableDataArea
		visibleEnd = min(visibleEnd, len(m.tables))

		selWidth := 2
		nameWidth := width - selWidth - 1

		for i := m.offset; i < visibleEnd; i++ {
			table := m.tables[i]
			selIndicator := "  "
			if i == m.selectedRow {
				selIndicator = "> "
				sb.WriteString(fmt.Sprintf("%s%s\n", selIndicator, rowHighlight.Render(
					fitText(table.Name, nameWidth))))
			} else {
				sb.WriteString(fmt.Sprintf("%s%s\n", selIndicator, tableCellStyle.Render(
					fitText(table.Name, nameWidth))))
			}
		}

		for i := visibleEnd - m.offset; i < tableDataArea; i++ {
			sb.WriteString("\n")
		}

		if m.statusMessage != "" {
			sb.WriteString(statusBarStyle.Render(m.statusMessage))
		} else {
			sb.WriteString(statusBarStyle.Render(fmt.Sprintf(
				"Table %d : %d | space: select | Esc: exit | /: search",
				m.selectedRow+1,
				len(m.tables))))
		}
	} else {
		fieldDataArea := height - 2
		visibleEnd := m.fieldOffset + fieldDataArea
		visibleEnd = min(visibleEnd, len(m.fields))

		selWidth := 2
		colGap := 2
		nameWidth := (width - selWidth - colGap) * 2 / 3
		typeWidth := width - selWidth - colGap - nameWidth

		maxNameWidth := 0
		for _, field := range m.fields {
			if len(field.ColumnName) > maxNameWidth {
				maxNameWidth = len(field.ColumnName)
			}
		}
		//maxNameWidth = min(maxNameWidth, nameWidth)

		for i := m.fieldOffset; i < visibleEnd; i++ {
			field := m.fields[i]
			selIndicator := "  "

			if i == m.selectedFieldRow {
				selIndicator = "> "
				sb.WriteString(fmt.Sprintf("%s%s  %s\n",
					selIndicator,
					rowHighlight.Render(formatLeft(field.ColumnName, nameWidth)),
					rowHighlight.Render(formatLeft(field.DataType, typeWidth))))
				continue
			}
			sb.WriteString(fmt.Sprintf("%s%s  %s\n",
				selIndicator,
				tableCellStyle.Render(formatLeft(field.ColumnName, nameWidth)),
				tableCellStyle.Render(formatLeft(field.DataType, typeWidth))))
		}

		for i := visibleEnd - m.fieldOffset; i < fieldDataArea; i++ {
			sb.WriteString("\n")
		}

		if m.statusMessage != "" {
			sb.WriteString(statusBarStyle.Render(m.statusMessage))
		} else {
			sb.WriteString(statusBarStyle.Render(fmt.Sprintf(
				"Field %d : %d | space: select | Esc: exit | /: search",
				m.selectedFieldRow+1, len(m.fields))))
		}
	}

	if m.textInputActive {
		sb.WriteString("\n")
		sb.WriteString(m.textInput.View())
	}

	return sb.String()
}
