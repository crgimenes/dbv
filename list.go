package main

import (
	"fmt"
	"regexp"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dbv/db"
)

var (
	themeBackground = lipgloss.Color("#000000")
	themeForeground = lipgloss.Color("#F8F8F2")
	themeAccent     = lipgloss.Color("#6272A4")
)

type TableInfo struct {
	Name       string
	Type       string
	Size       string
	PrimaryKey string
}

type modelList struct {
	quitting        bool
	err             error
	originalData    []TableInfo
	tableData       []TableInfo
	selected        int
	offset          int
	width           int
	height          int
	textInput       textinput.Model
	textInputActive bool
}

var (
	rowHighlight = lipgloss.NewStyle().
			Foreground(themeForeground).
			Background(themeAccent).
			Bold(true)

	headerStyle = lipgloss.NewStyle().
			Foreground(themeAccent).
			Background(themeBackground).
			Bold(true)

	tableCellStyle = lipgloss.NewStyle().
			Foreground(themeForeground).
			Background(themeBackground)
)

func fitText(s string, width int) string {
	if len(s) > width {
		if width > 3 {
			return s[:width-3] + "..."
		}
		return s[:width]
	}
	return s
}

func formatLeft(s string, width int) string {
	return fmt.Sprintf("%-*s", width, fitText(s, width))
}

func formatRight(s string, width int) string {
	return fmt.Sprintf("%*s", width, fitText(s, width))
}

func initialModelList() modelList {
	lt, err := db.Storage.ListTablesAndViews()
	if err != nil {
		return modelList{err: err}
	}

	data := make([]TableInfo, len(lt))
	for i, t := range lt {
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
	ti.Placeholder = "regex filter"
	ti.CharLimit = 256
	ti.Width = 20

	return modelList{
		originalData:    data,
		tableData:       data,
		selected:        0,
		offset:          0,
		textInput:       ti,
		textInputActive: false,
	}
}

func (m modelList) Init() tea.Cmd {
	return nil
}

func (m modelList) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.textInputActive {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter", "up", "down":
				m.textInputActive = false
				return m, nil
			case "esc":
				m.textInputActive = false
				m.textInput.SetValue("")
				m.tableData = m.originalData
				m.selected = 0
				m.offset = 0
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		filter := m.textInput.Value()
		if filter == "" {
			m.tableData = m.originalData
		} else {
			if re, err := regexp.Compile(filter); err != nil {
				m.tableData = m.originalData
			} else {
				var filtered []TableInfo
				for _, row := range m.originalData {
					if re.MatchString(row.Name) {
						filtered = append(filtered, row)
					}
				}
				m.tableData = filtered
			}
		}
		m.selected = 0
		m.offset = 0
		return m, cmd
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "/":
			m.textInputActive = true
			m.textInput.Focus()
			return m, nil
		case "esc":
			if len(m.tableData) != len(m.originalData) {
				m.tableData = m.originalData
				m.selected = 0
				m.offset = 0
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
			if m.selected < m.offset {
				m.offset = m.selected
			}
		case "down", "j":
			if m.selected < len(m.tableData)-1 {
				m.selected++
			}
			tableDataArea := m.height - 4
			if tableDataArea > 0 && m.selected >= m.offset+tableDataArea {
				m.offset = m.selected - tableDataArea + 1
			}
		case "enter":
			selectedTable := ""
			pk := ""
			if m.selected >= 0 && m.selected < len(m.tableData) {
				selectedTable = m.tableData[m.selected].Name
				pk = m.tableData[m.selected].PrimaryKey
			}

			return m, func() tea.Msg {
				return showRecordsMsg{
					tableName: selectedTable,
					pk:        pk,
				}
			}
		}
		return m, nil

	case errMsg:
		m.err = msg
		return m, nil

	default:
		return m, nil
	}
}

func (m modelList) View() string {
	if m.err != nil {
		return m.err.Error()
	}

	width := m.width
	if width == 0 {
		width = 80
	}

	selWidth := 2
	gaps := 4
	remaining := width - selWidth - gaps
	if remaining < 0 {
		remaining = 0
	}
	nameWidth := int(0.4 * float64(remaining))
	typeWidth := int(0.15 * float64(remaining))
	sizeWidth := int(0.15 * float64(remaining))
	pkWidth := remaining - nameWidth - typeWidth - sizeWidth

	s := fmt.Sprintf("dbv %s - Database Viewer [%s]\n", GitTag, DBTitle)
	if len(s) > m.width {
		if m.width > 3 {
			s = s[:m.width-3] + "..."
		}
	}

	header := fmt.Sprintf("  %s %s %s %s",
		formatLeft("NAME", nameWidth),
		formatLeft("TYPE", typeWidth),
		formatRight("SIZE", sizeWidth),
		formatLeft("PRIMARY KEY", pkWidth))
	header = headerStyle.Render(header)
	s += "\033[1m" + header + "\033[0m\n"

	tableDataArea := m.height - 4
	visibleEnd := m.offset + tableDataArea
	if visibleEnd > len(m.tableData) {
		visibleEnd = len(m.tableData)
	}
	for i := m.offset; i < visibleEnd; i++ {
		row := m.tableData[i]
		selIndicator := " "
		if i == m.selected {
			selIndicator = ""
		}
		line := fmt.Sprintf("%s %s %s %s %s",
			selIndicator,
			formatLeft(row.Name, nameWidth),
			formatLeft(row.Type, typeWidth),
			formatRight(row.Size, sizeWidth),
			formatLeft(row.PrimaryKey, pkWidth))
		if i == m.selected {
			line = rowHighlight.Render(line)
		} else {
			line = tableCellStyle.Render(line)
		}
		s += line + "\n"
	}
	for i := visibleEnd - m.offset; i < tableDataArea; i++ {
		s += "\n"
	}

	status := fmt.Sprintf("Row %d of %d", m.selected+1, len(m.tableData))
	s += statusBarStyle.Render(status) + "\n"

	if m.textInputActive {
		s += m.textInput.View()
	} else {
		s += ""
	}
	return s
}
