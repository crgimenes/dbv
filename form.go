package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	textinput "github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Parameter represents an SQL parameter for generating the form.
type Parameter struct {
	Name       string // required
	Type       string // default "string"
	Title      string // default equals Name
	ZOrder     int    // custom order; default order of occurrence
	Occurrence int    // internal order if ZOrder are equal
}

// ExtractParameters extracts tokens from the SQL string and returns ordered parameters.
func ExtractParameters(sqlStr string) []Parameter {
	re := regexp.MustCompile(`\{\{(.*?)\}\}`)
	matches := re.FindAllStringSubmatch(sqlStr, -1)

	var params []Parameter
	for i, match := range matches {
		content := match[1]
		fields := strings.Split(content, ":")
		for j, field := range fields {
			fields[j] = strings.TrimSpace(field)
		}
		if len(fields) < 1 || fields[0] == "" {
			continue
		}
		param := Parameter{
			Name:       fields[0],
			Occurrence: i,
		}
		// Default type "string"
		param.Type = "string"
		if len(fields) >= 2 && fields[1] != "" {
			param.Type = fields[1]
		}
		// Default title equals Name
		param.Title = param.Name
		if len(fields) >= 3 && fields[2] != "" {
			param.Title = fields[2]
		}
		// Default ZOrder equals the occurrence
		param.ZOrder = i
		if len(fields) >= 4 && fields[3] != "" {
			if z, err := strconv.Atoi(fields[3]); err == nil {
				param.ZOrder = z
			}
		}
		params = append(params, param)
	}
	sort.Slice(params, func(i, j int) bool {
		if params[i].ZOrder != params[j].ZOrder {
			return params[i].ZOrder < params[j].ZOrder
		}
		return params[i].Occurrence < params[j].Occurrence
	})
	return params
}

// formModel is the Bubble Tea model for the form.
// Now includes window dimensions.
type formModel struct {
	screenTitle  string
	baseSQL      string
	parameters   []Parameter
	inputs       []textinput.Model
	activeIndex  int
	submitted    bool
	abandoned    bool
	finalSQL     string
	windowWidth  int
	windowHeight int
}

// newFormModel creates a form model and instantiates a text input for each parameter.
// It aligns the prompts based on the longest title.
func newFormModel(screenTitle, baseSQL string, params []Parameter) formModel {
	// Calculate the maximum length of the field titles.
	maxLen := 0
	for _, param := range params {
		l := len(param.Title)
		if l > maxLen {
			maxLen = l
		}
	}

	inputs := make([]textinput.Model, len(params))
	for i, param := range params {
		ti := textinput.New()
		ti.Placeholder = param.Title
		// Align all prompts to the same column using padding.
		ti.Prompt = fmt.Sprintf("%-*s: ", maxLen, param.Title)
		ti.CharLimit = 64
		if i == 0 {
			ti.Focus()
		} else {
			ti.Blur()
		}
		inputs[i] = ti
	}
	return formModel{
		screenTitle: screenTitle,
		baseSQL:     baseSQL,
		parameters:  params,
		inputs:      inputs,
		activeIndex: 0,
		// Default window size can be set later via WindowSizeMsg
		windowWidth:  80,
		windowHeight: 24,
	}
}

func (m formModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles Tea messages.
func (m *formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.submitted || m.abandoned {
		return m, tea.Quit
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.abandoned = true
			return m, tea.Quit
		case tea.KeyEnter, tea.KeyCtrlS:
			// Advance focus or submit if on last input.
			if m.activeIndex < len(m.inputs)-1 {
				m.inputs[m.activeIndex].Blur()
				m.activeIndex++
				m.inputs[m.activeIndex].Focus()
			} else {
				m.submitted = true
				m.computeFinalSQL()
				return m, tea.Quit
			}
		case tea.KeyUp:
			if m.activeIndex > 0 {
				m.inputs[m.activeIndex].Blur()
				m.activeIndex--
				m.inputs[m.activeIndex].Focus()
			}
		case tea.KeyDown:
			if m.activeIndex < len(m.inputs)-1 {
				m.inputs[m.activeIndex].Blur()
				m.activeIndex++
				m.inputs[m.activeIndex].Focus()
			}
		}
	}
	var cmd tea.Cmd
	m.inputs[m.activeIndex], cmd = m.inputs[m.activeIndex].Update(msg)
	return m, cmd
}

// computeFinalSQL replaces the tokens in the SQL string with user-provided values.
func (m *formModel) computeFinalSQL() {
	final := m.baseSQL
	for i, param := range m.parameters {
		pattern := fmt.Sprintf(`\{\{\s*%s[^}]*\}\}`, regexp.QuoteMeta(param.Name))
		re := regexp.MustCompile(pattern)
		value := m.inputs[i].Value()
		final = re.ReplaceAllString(final, value)
	}
	m.finalSQL = final
}

// View renders the UI with the title at the top and the status bar and command line fixed at the bottom.
func (m formModel) View() string {
	if m.abandoned {
		return abandonedStyle.Render("Form abandoned by user.\n")
	}
	if m.submitted {
		s := submittedStyle.Render("Generated SQL with substitutions:\n\n")
		s += m.finalSQL + "\n"
		return s
	}

	// Render title and divider (at the top)
	title := formTitleStyle.Render(m.screenTitle)
	divider := lipgloss.NewStyle().Foreground(themeAccent).Render(strings.Repeat("─", m.windowWidth))

	// Build form content lines
	var formContent strings.Builder
	for _, input := range m.inputs {
		formContent.WriteString(input.View())
		formContent.WriteString("\n")
	}

	// Status bar and command line (at the bottom)
	status := statusBarStyle.Render("Ctrl+S: submit    Up/Down: navigate    Esc/Ctrl+C: cancel")
	cmdLine := "" // placeholder, pode ser usado para comandos

	// Split the existing content into lines
	headerLines := []string{title, divider, ""}
	formLines := strings.Split(formContent.String(), "\n")

	// Calculate the reserved lines for status and command line (2 linhas)
	reservedBottom := 2
	// Total used lines so far
	usedLines := len(headerLines) + len(formLines)
	// Fill remaining lines until windowHeight - reservedBottom
	var emptyLines []string
	if m.windowHeight > usedLines+reservedBottom {
		for i := 0; i < m.windowHeight-usedLines-reservedBottom; i++ {
			emptyLines = append(emptyLines, "")
		}
	}

	// Compose the final view
	allLines := append(headerLines, formLines...)
	allLines = append(allLines, emptyLines...)
	allLines = append(allLines, status, cmdLine)
	return strings.Join(allLines, "\n")
}

/*
func main() {
	baseSQL := `
		SELECT * FROM customers
		WHERE id = {{id:int:Customer ID:2}}
		  AND status = {{status::Status:1}}
		  AND created_at = {{created_at:timestamp::}}
	`
	params := ExtractParameters(baseSQL)
	// Pass screen title as parameter, for example "Customer Form"
	model := newFormModel("Customer Form", baseSQL, params)
	p := tea.NewProgram(&model)
	if _, err := p.Run(); err != nil {
		fmt.Println("Error running program:", err)
		os.Exit(1)
	}
}
*/
