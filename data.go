package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dbv/db"
)

type externalEditorMsg struct {
	newText string
	changed bool
	err     error
}

type externalPagerMsg struct {
	err error
}

const (
	defaultWidth      = 80
	maxCellWidth      = 40
	maxCellHeight     = 1
	minTerminalWidth  = 10
	minTerminalHeight = 5
)

var (
	Background = lipgloss.Color("#282A36")
	Foreground = lipgloss.Color("#F8F8F2")
	Accent     = lipgloss.Color("#6272A4")

	statusBarStyle = lipgloss.NewStyle().
			Foreground(Accent).
			Bold(true)
	headerCellStyle = lipgloss.NewStyle().
			Foreground(Accent).
			Background(Background).
			Bold(true)
	selectedCellStyle = lipgloss.NewStyle().
				Foreground(Foreground).
				Background(Accent).
				Bold(true)
	errorStatusBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF5555")).
				Bold(true)
)

type modelData struct {
	data          [][]string
	selectedRow   int
	selectedCol   int
	colWidths     []int
	verticalOff   int
	horizontalOff int
	windowWidth   int
	windowHeight  int

	editor *modelEditor

	commandMode bool
	cmdInput    textinput.Model
	lastCommand string

	showingError bool
	errorMessage string

	tableName       string
	pk              string
	pageSize        int
	loadedRecords   [][]string
	loadedOffset    int
	totalRecords    int
	scrollThreshold int
	columnInfo      []db.ColumnInfo
	isLoading       bool
	whereCondition  string
	orderBys        map[string]string
}

func (m modelData) Init() tea.Cmd {
	return textinput.Blink
}

func (m modelData) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.showingError {
		if _, ok := msg.(tea.KeyMsg); ok {
			m.showingError = false
			m.errorMessage = ""
		}
		return m, nil
	}

	if m.editor.IsMultiEditing() {
		_, cmd := m.editor.UpdateEditor(msg, &m)
		return m, cmd
	}

	if m.commandMode {
		var cmd tea.Cmd
		m.cmdInput, cmd = m.cmdInput.Update(msg)
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.Type {
			case tea.KeyEnter:
				m.lastCommand = m.cmdInput.Value()
				switch {
				case m.cmdInput.Value() == "q":
					return m, tea.Quit
				case strings.HasPrefix(m.cmdInput.Value(), "where"):
					m.whereCondition = strings.TrimSpace(
						strings.TrimPrefix(m.cmdInput.Value(), "where"))
					m.commandMode = false
					return m, m.resetTable()
				default:
					m.showingError = true
					m.errorMessage = fmt.Sprintf("Unrecognized command: %s", m.cmdInput.Value())
					m.commandMode = false
				}
			case tea.KeyEsc:
				m.lastCommand = m.cmdInput.Value()
				m.commandMode = false
			}
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case loadMoreRecordsMsg:
		m.totalRecords = msg.total
		newData := [][]string{}
		for _, rec := range msg.records {
			row := []string{}
			for _, col := range msg.columnInfo {
				val := rec[col.ColumnName]
				var s string
				switch v := val.(type) {
				case nil:
					s = "NULL"
				case []byte:
					if len(v) == 0 {
						s = "NULL"
					} else {
						strVal := strings.TrimSpace(string(v))
						lowerType := strings.ToLower(col.DataType)
						if strings.Contains(lowerType, "numeric") {
							if f, err := strconv.ParseFloat(strVal, 64); err == nil {
								s = fmt.Sprintf("%.2f", f)
							} else {
								s = strVal
							}
						} else {
							s = strVal
						}
					}
				default:
					s = fmt.Sprintf("%v", v)
					if s == "<nil>" {
						s = "NULL"
					}
				}
				row = append(row, s)
			}
			newData = append(newData, row)
		}
		if msg.direction == 1 {
			m.data = append(m.data, newData...)
			m.loadedRecords = append(m.loadedRecords, newData...)
		} else {
			m.loadedRecords = append(newData, m.loadedRecords...)
			m.loadedOffset = msg.offset
			m.data = append([][]string{m.data[0]}, m.loadedRecords...)
		}
		totalLoaded := m.loadedOffset + len(m.loadedRecords)
		if totalLoaded > m.totalRecords {
			m.loadedRecords = m.loadedRecords[:m.totalRecords-m.loadedOffset]
			m.data = append([][]string{m.data[0]}, m.loadedRecords...)
		}
		m.isLoading = false
		return m, nil

	case externalEditorMsg:
		if msg.err != nil {
			m.showingError = true
			m.errorMessage = msg.err.Error()
		} else if msg.changed {
			if m.pk != "" && m.pk != "-" {
				m.editor.updateCellValue(&m, msg.newText)
			} else {
				m.data[m.selectedRow][m.selectedCol] = msg.newText
			}
		}
		return m, nil

	case externalPagerMsg:
		if msg.err != nil {
			m.showingError = true
			m.errorMessage = msg.err.Error()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.selectedRow > 1 {
				m.selectedRow--
				if !m.isLoading && m.selectedRow <= m.scrollThreshold+1 && m.loadedOffset > 0 {
					loadOffset := max(0, m.loadedOffset-m.pageSize)
					loadLimit := m.loadedOffset - loadOffset
					m.isLoading = true
					return m, loadMoreRecordsCmd(
						m.tableName,
						m.pk,
						loadOffset,
						loadLimit,
						-1,
						m.whereCondition,
						generateOrderBy(m.orderBys),
					)
				}
			}
		case "down", "j":
			if m.selectedRow < len(m.data)-1 {
				m.selectedRow++
				totalLoaded := m.loadedOffset + len(m.loadedRecords)
				if !m.isLoading &&
					m.selectedRow >= len(m.data)-m.scrollThreshold &&
					totalLoaded < m.totalRecords {
					m.isLoading = true
					return m, loadMoreRecordsCmd(
						m.tableName,
						m.pk,
						totalLoaded,
						m.pageSize,
						1,
						m.whereCondition,
						generateOrderBy(m.orderBys),
					)
				}
			}
		case "left", "h":
			if m.selectedCol > 0 {
				m.selectedCol--
			}
		case "right", "l":
			if m.selectedCol < len(m.data[0])-1 {
				m.selectedCol++
			}
		case "enter", "e":
			cellContent := m.data[m.selectedRow][m.selectedCol]
			m.editor.StartMultiEditing(cellContent, m.windowWidth, m.windowHeight)
		case ":":
			m.commandMode = true
			ci := textinput.New()
			ci.Placeholder = "Command..."
			ci.Prompt = ":"
			ci.Focus()
			ci.CharLimit = 256
			ci.Width = m.windowWidth - 2
			ci.SetValue(m.lastCommand)
			m.cmdInput = ci
		case "o":
			return m, m.updateOrderBy("ASC")
		case "O":
			return m, m.updateOrderBy("DESC")
		case "v":
			if m.selectedRow == 0 {
				m.selectedRow = 1
			}
			return m, openExternalEditor(m.data[m.selectedRow][m.selectedCol])
		case "p":
			if m.selectedRow == 0 {
				m.selectedRow = 1
			}
			return m, openExternalPager(m.data[m.selectedRow][m.selectedCol])
		case "q", "ctrl+c", "esc", "backspace":
			m.lastCommand = ""
			m.orderBys = nil
			return m, func() tea.Msg { return "backToList" }
		}
	}

	m.colWidths = computeColWidths(m.data)
	tableWidth := m.windowWidth
	if tableWidth == 0 {
		tableWidth = defaultWidth
	}
	rowHeights := make([]int, len(m.data))
	for i, row := range m.data {
		rowHeights[i] = computeRowHeight(row)
	}
	headerHeight := rowHeights[0]
	tableAreaHeight := m.windowHeight - 3
	if tableAreaHeight < 1 {
		tableAreaHeight = 1
	}
	availableDataLines := tableAreaHeight - headerHeight
	if availableDataLines < 1 {
		availableDataLines = 1
	}
	if m.verticalOff < 1 {
		m.verticalOff = 1
	}

	m = m.adjustVerticalOff(rowHeights, availableDataLines)

	visStart, visEnd := visibleColumns(m.colWidths, m.horizontalOff, tableWidth)
	if m.selectedCol < visStart {
		m.horizontalOff = m.selectedCol
	} else if m.selectedCol > visEnd {
		for {
			_, visEnd = visibleColumns(m.colWidths, m.horizontalOff, tableWidth)
			if m.selectedCol <= visEnd || m.horizontalOff >= len(m.colWidths) {
				break
			}
			m.horizontalOff++
		}
	}

	if !m.isLoading {
		if m.verticalOff <= m.scrollThreshold && m.loadedOffset > 0 {
			loadOffset := max(0, m.loadedOffset-m.pageSize)
			loadLimit := m.loadedOffset - loadOffset
			m.isLoading = true
			return m, loadMoreRecordsCmd(
				m.tableName,
				m.pk,
				loadOffset,
				loadLimit,
				-1,
				m.whereCondition,
				generateOrderBy(m.orderBys),
			)
		}
		endRow := len(m.data) - 1
		visibleEndRow := m.verticalOff + availableDataLines
		totalLoaded := m.loadedOffset + len(m.loadedRecords)

		if visibleEndRow >= endRow-m.scrollThreshold && totalLoaded < m.totalRecords {
			m.isLoading = true
			return m, loadMoreRecordsCmd(
				m.tableName,
				m.pk,
				totalLoaded,
				m.pageSize,
				1,
				m.whereCondition,
				generateOrderBy(m.orderBys),
			)
		}
	}

	return m, nil
}

func (m modelData) View() string {
	if len(m.data) == 0 {
		return "No data loaded\n(Press ESC or Q to go back)"
	}
	if len(m.data[0]) == 0 {
		return "No columns found\n(Press ESC or Q to go back)"
	}
	if m.windowWidth < minTerminalWidth || m.windowHeight < minTerminalHeight {
		return renderTooSmall(m.windowWidth, m.windowHeight)
	}
	if m.windowWidth == 0 {
		m.windowWidth = defaultWidth
	}
	if m.windowHeight == 0 {
		m.windowHeight = 24
	}

	if m.editor.IsMultiEditing() {
		taLines := strings.Split(m.editor.textArea.View(), "\n")
		availableTAHeight := m.windowHeight - 3
		for len(taLines) < availableTAHeight {
			taLines = append(taLines, "")
		}
		title := m.tableName
		if title == "" {
			title = "No table selected"
		}
		outputLines := []string{title}
		outputLines = append(outputLines, taLines...)
		outputLines = append(outputLines, statusBarStyle.Render("ctrl+s: save, ESC: cancel"), "")
		return strings.Join(outputLines, "\n")
	}

	var outputLines []string
	title := m.tableName
	if title == "" {
		title = "No table selected"
	}
	if m.pk != "" && m.pk != "-" {
		title += fmt.Sprintf(" pk: %s", m.pk)
	}
	if len(m.data) > 0 && len(m.data[0]) > 0 {
		title += fmt.Sprintf(" | %d columns", len(m.data[0]))
	}
	title += title + fmt.Sprintf(" [%s]", DBTitle)
	if len(title) > m.windowWidth {
		title = title[:m.windowWidth-3] + "..."
	}

	outputLines = append(outputLines, title)

	m.colWidths = computeColWidths(m.data)
	rowHeights := make([]int, len(m.data))
	for i, row := range m.data {
		rowHeights[i] = computeRowHeight(row)
	}
	headerHeight := rowHeights[0]
	tableAreaHeight := m.windowHeight - 3
	availableDataLines := tableAreaHeight - headerHeight

	headerLines := renderRow(m.data[0], m.colWidths, headerHeight, m.horizontalOff, m.windowWidth)
	for i := range headerLines {
		headerLines[i] = headerCellStyle.Render(headerLines[i])
	}
	outputLines = append(outputLines, headerLines...)

	dataRendered := 0
	i := m.verticalOff
	for i < len(m.data) && dataRendered < availableDataLines {
		rh := rowHeights[i]
		if dataRendered+rh > availableDataLines {
			break
		}
		rowLines := renderRow(
			m.data[i],
			m.colWidths,
			rh,
			m.horizontalOff,
			m.windowWidth,
		)
		if i == m.selectedRow {
			rowLines = highlightCell(
				rowLines,
				m.data[i],
				m.colWidths,
				rh,
				m.selectedCol,
				m.horizontalOff,
				m.windowWidth,
			)
		}
		outputLines = append(outputLines, rowLines...)
		dataRendered += rh
		i++
	}

	usedLines := headerHeight + dataRendered
	for j := usedLines; j < tableAreaHeight; j++ {
		outputLines = append(outputLines, "")
	}

	_, visEnd := visibleColumns(m.colWidths, m.horizontalOff, m.windowWidth)
	statusBar := ""
	if m.horizontalOff > 0 {
		statusBar += "← "
	}
	if visEnd < len(m.colWidths)-1 {
		statusBar += "→ "
	}
	totalLoaded := m.loadedOffset + len(m.loadedRecords)
	statusBar += fmt.Sprintf("Cell: row %d, col %d | Records: %d-%d",
		m.selectedRow, m.selectedCol+1, m.loadedOffset+1, totalLoaded)
	if m.isLoading {
		statusBar += " | Loading..."
	}
	if m.totalRecords > 0 {
		statusBar += fmt.Sprintf(" | Total: %d", m.totalRecords)
	}
	orderByStr := generateOrderBy(m.orderBys)
	if orderByStr != "" {
		statusBar += " | OrderBy: " + orderByStr
	}
	if m.showingError && m.errorMessage != "" {
		statusBar = errorStatusBarStyle.Render(m.errorMessage)
	} else {
		statusBar = statusBarStyle.Render(statusBar)
	}
	outputLines = append(outputLines, statusBar)

	if m.commandMode {
		outputLines = append(outputLines, m.cmdInput.View())
	} else {
		outputLines = append(outputLines, "")
	}

	return strings.Join(outputLines, "\n")
}

func (m *modelData) resetTable() tea.Cmd {
	m.loadedOffset = 0
	m.loadedRecords = [][]string{}
	m.data = [][]string{m.data[0]}
	m.selectedRow = 1
	m.verticalOff = 1
	m.isLoading = true
	return loadMoreRecordsCmd(
		m.tableName,
		m.pk,
		0,
		m.pageSize,
		1,
		m.whereCondition,
		generateOrderBy(m.orderBys),
	)
}

func (m *modelData) updateOrderBy(order string) tea.Cmd {
	if m.orderBys == nil {
		m.orderBys = make(map[string]string)
	}
	colName := m.data[0][m.selectedCol]
	m.orderBys[colName] = order
	return m.resetTable()
}

func (m modelData) adjustVerticalOff(rowHeights []int, availableDataLines int) modelData {
	if m.selectedRow < m.verticalOff {
		m.verticalOff = m.selectedRow
		return m
	}
	cum := 0
	for i := m.verticalOff; i <= m.selectedRow && i < len(m.data); i++ {
		cum += rowHeights[i]
	}
	for m.verticalOff < m.selectedRow && cum > availableDataLines {
		cum -= rowHeights[m.verticalOff]
		m.verticalOff++
	}
	return m
}

func computeColWidths(data [][]string) []int {
	if len(data) == 0 {
		return nil
	}
	numCols := len(data[0])
	widths := make([]int, numCols)
	for _, row := range data {
		for i, cell := range row {
			lines := strings.Split(cell, "\n")
			for _, line := range lines {
				w := len(line)
				if w > maxCellWidth {
					w = maxCellWidth
				}
				if w > widths[i] {
					widths[i] = w
				}
			}
		}
	}
	return widths
}

func computeRowHeight(row []string) int {
	height := 1
	for _, cell := range row {
		lines := strings.Split(cell, "\n")
		h := len(lines)
		if h > maxCellHeight {
			h = maxCellHeight
		}
		if h > height {
			height = h
		}
	}
	return height
}

func renderCell(cell string, width, height, tableWidth int) []string {
	allowedWidth := width
	screenLimit := tableWidth - 5
	if screenLimit < allowedWidth {
		allowedWidth = screenLimit
	}
	lines := strings.Split(cell, "\n")

	if len(lines) > 1 {
		base := lines[0]
		suffix := "..."
		available := allowedWidth - len(suffix)
		if available < 0 {
			available = 0
		}
		var display string
		if len(base) > available {
			display = base[:available] + suffix
		} else {
			display = base + suffix
		}
		if _, err := strconv.ParseFloat(display, 64); err == nil {
			display = fmt.Sprintf("%*s", allowedWidth, display)
		} else {
			display = fmt.Sprintf("%-*s", allowedWidth, display)
		}
		ret := []string{display}
		for i := 1; i < height; i++ {
			ret = append(ret, fmt.Sprintf("%-*s", allowedWidth, ""))
		}
		return ret
	}

	line := lines[0]
	if len(line) > allowedWidth {
		if allowedWidth > 3 {
			line = line[:allowedWidth-3] + "..."
		} else {
			line = line[:allowedWidth]
		}
	}
	if _, err := strconv.ParseFloat(line, 64); err == nil {
		line = fmt.Sprintf("%*s", allowedWidth, line)
	} else {
		line = fmt.Sprintf("%-*s", allowedWidth, line)
	}
	result := []string{line}
	for i := 1; i < height; i++ {
		result = append(result, fmt.Sprintf("%-*s", allowedWidth, ""))
	}
	return result
}

func renderRow(row []string, colWidths []int, rowHeight, horizontalOff, tableWidth int) []string {
	visStart, visEnd := visibleColumns(colWidths, horizontalOff, tableWidth)
	cells := make([][]string, 0)
	for j := visStart; j <= visEnd && j < len(row); j++ {
		cells = append(cells, renderCell(row[j], colWidths[j], rowHeight, tableWidth))
	}
	rendered := make([]string, rowHeight)
	for i := 0; i < rowHeight; i++ {
		parts := make([]string, len(cells))
		for j, cellLines := range cells {
			parts[j] = cellLines[i]
		}
		rendered[i] = strings.Join(parts, "  ")
	}
	return rendered
}

func highlightCell(rowLines []string, row []string, colWidths []int, rowHeight, selectedCol, horizontalOff, tableWidth int) []string {
	visStart, visEnd := visibleColumns(colWidths, horizontalOff, tableWidth)
	if selectedCol < visStart || selectedCol > visEnd {
		return rowLines
	}
	cells := make([][]string, 0)
	for j := visStart; j <= visEnd && j < len(row); j++ {
		cellLines := renderCell(row[j], colWidths[j], rowHeight, tableWidth)
		if j == selectedCol {
			for i := range cellLines {
				cellLines[i] = selectedCellStyle.Render(cellLines[i])
			}
		}
		cells = append(cells, cellLines)
	}
	newRow := make([]string, rowHeight)
	for i := 0; i < rowHeight; i++ {
		parts := make([]string, len(cells))
		for j, cellLines := range cells {
			parts[j] = cellLines[i]
		}
		newRow[i] = strings.Join(parts, "  ")
	}
	return newRow
}

func visibleColumns(widths []int, offset int, tableWidth int) (start, end int) {
	total := 0
	start = offset
	end = offset
	for i := offset; i < len(widths); i++ {
		if i == offset {
			total = widths[i]
		} else {
			if total+2+widths[i] > tableWidth {
				break
			}
			total += 2 + widths[i]
		}
		end = i
	}
	return start, end
}

func renderTooSmall(width, height int) string {
	message := "Terminal is too small"
	leftPad := 0
	if width > len(message) {
		leftPad = (width - len(message)) / 2
	}
	topPad := (height - 1) / 2
	result := strings.Repeat("\n", topPad)
	result += fmt.Sprintf("%s%s\n", strings.Repeat(" ", leftPad), message)
	result += strings.Repeat("\n", height-topPad-1)
	return result
}

func loadMoreRecordsCmd(
	tableName string,
	pk string,
	offset, limit, direction int,
	whereCondition string,
	orderBy string,
) tea.Cmd {
	return func() tea.Msg {
		if offset < 0 {
			offset = 0
		}
		recs, columnInfo, totalRecords, err := db.Storage.ListRecords(
			tableName,
			pk,
			offset,
			limit,
			whereCondition,
			orderBy,
		)
		if err != nil {
			return errMsg(err)
		}
		return loadMoreRecordsMsg{
			records:    recs,
			total:      totalRecords,
			columnInfo: columnInfo,
			offset:     offset,
			direction:  direction,
		}
	}
}

func generateOrderBy(orderBys map[string]string) string {
	var clauses []string
	for col, order := range orderBys {
		clauses = append(clauses, fmt.Sprintf("%s %s", col, order))
	}
	if len(clauses) > 0 {
		return strings.Join(clauses, ", ")
	}
	return ""
}

func createExternalProcessCmd(initialText, programType string) tea.Cmd {
	return func() tea.Msg {
		tmpFile, err := os.CreateTemp("", "external-"+programType+"-*.txt")
		if err != nil {
			if programType == "editor" {
				return externalEditorMsg{err: err}
			}
			return externalPagerMsg{err: err}
		}
		filename := tmpFile.Name()
		if _, err := tmpFile.WriteString(initialText); err != nil {
			tmpFile.Close()
			os.Remove(filename)
			if programType == "editor" {
				return externalEditorMsg{err: err}
			}
			return externalPagerMsg{err: err}
		}
		if err := tmpFile.Close(); err != nil {
			os.Remove(filename)
			if programType == "editor" {
				return externalEditorMsg{err: err}
			}
			return externalPagerMsg{err: err}
		}
		program := ""
		if programType == "editor" {
			program = os.Getenv("EDITOR")
			if strings.TrimSpace(program) == "" {
				program = "vi"
			}
		} else {
			program = os.Getenv("PAGER")
			if strings.TrimSpace(program) == "" {
				program = "less"
			}
		}
		cmd := exec.Command(program, filename)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if programType == "editor" {
			return tea.ExecProcess(cmd, func(err error) tea.Msg {
				modifiedBytes, err2 := os.ReadFile(filename)
				os.Remove(filename)
				if err2 != nil {
					return externalEditorMsg{err: err2}
				}
				newText := strings.TrimRight(string(modifiedBytes), "\n\r")
				changed := (newText != initialText)
				return externalEditorMsg{newText: newText, changed: changed, err: err}
			})()
		}
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			os.Remove(filename)
			return externalPagerMsg{err: err}
		})()
	}
}

func openExternalEditor(initialText string) tea.Cmd {
	return createExternalProcessCmd(initialText, "editor")
}
func openExternalPager(initialText string) tea.Cmd {
	return createExternalProcessCmd(initialText, "pager")
}
