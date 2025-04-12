package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type editorModel struct {
	filePath     string         // File being edited
	textarea     textarea.Model // Multi-line text area
	commandMode  bool           // Indicates if command mode is active
	statusMsg    string         // Status or error messages
	dbURL        string         // Database URL from environment variable DBV_URL
	dbName       string         // Database name from environment variable DBV_NAME
	windowWidth  int            // Terminal width (updated via WindowSizeMsg)
	windowHeight int            // Terminal height (updated via WindowSizeMsg)
}

func (m editorModel) Init() tea.Cmd {
	return tea.EnterAltScreen
}

func (m editorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		if m.windowHeight >= 3 {
			m.textarea.SetWidth(m.windowWidth)
			m.textarea.SetHeight(m.windowHeight - 3)
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+s":
			// Save file via ctrl+s.
			err := saveFile(m.filePath, m.textarea.Value())
			if err != nil {
				m.statusMsg = fmt.Sprintf("Error saving file: %v", err)
			} else {
				// if save is successful, quit the Program
				return m, tea.Quit
			}
		case "ctrl+c", "esc":
			// Exit the program via ctrl+c or esc.
			return m, tea.Quit
		}

		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m editorModel) View() string {
	if m.windowHeight < 3 || m.windowWidth < 10 {
		return "Terminal size too small. Please resize the window."
	}

	var lines []string

	header := fmt.Sprintf("EDITOR - File: %s", m.filePath)
	if m.dbURL != "" || m.dbName != "" {
		header += fmt.Sprintf(" | DB: %s (%s)", m.dbName, m.dbURL)
	}
	header = lipgloss.NewStyle().Bold(true).Render(header)
	lines = append(lines, header)

	taView := m.textarea.View()
	taLines := strings.Split(taView, "\n")
	textareaHeight := m.windowHeight - 3
	if len(taLines) < textareaHeight {
		extra := make([]string, textareaHeight-len(taLines))
		taLines = append(taLines, extra...)
	} else if len(taLines) > textareaHeight {
		taLines = taLines[:textareaHeight]
	}
	lines = append(lines, taLines...)

	statusBar := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFF00")).Render(m.statusMsg)
	lines = append(lines, statusBar)

	return strings.Join(lines, "\n")
}

func saveFile(filePath, content string) error {
	return os.WriteFile(filePath, []byte(content), 0644)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: editor <file>")
		os.Exit(1)
	}
	filePath := os.Args[1]

	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	ta := textarea.New()
	ta.SetValue(string(data))
	ta.SetWidth(80)
	ta.SetHeight(20)
	ta.MaxHeight = 0
	ta.Prompt = ""
	ta.Placeholder = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Focus()

	dbURL := os.Getenv("DBV_URL")
	dbName := os.Getenv("DBV_NAME")

	m := editorModel{
		filePath:    filePath,
		textarea:    ta,
		commandMode: false,
		statusMsg:   "Ctrl+S: Save and exit | ^C or ESC: Exit",
		dbURL:       dbURL,
		dbName:      dbName,
	}

	// Create and start the Bubble Tea program.
	p := tea.NewProgram(m)
	_, err = p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
