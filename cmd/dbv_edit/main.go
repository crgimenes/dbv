// main.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// externalEditorMsg is sent when the external editor returns.
type externalEditorMsg struct {
	newText string
	changed bool
	err     error
}

type editorModel struct {
	filePath     string         // File being edited
	textarea     textarea.Model // Multi-line text area
	statusMsg    string         // Status or error messages
	dbURL        string         // DB URL from env DBV_URL
	dbName       string         // DB name from env DBV_NAME
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

	// Handle key presses.
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+s":
			// Save file via Ctrl+S and then exit.
			if err := saveFile(m.filePath, m.textarea.Value()); err != nil {
				m.statusMsg = fmt.Sprintf("Error saving file: %v", err)
			} else {
				m.statusMsg = "File saved."
				return m, tea.Quit
			}
		case "ctrl+c", "esc":
			// Exit without saving.
			return m, tea.Quit
		case "ctrl+o":
			// Open external editor.
			return m, openExternalEditor(m.textarea.Value())
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd

	// Process message returned by external editor.
	case externalEditorMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("External editor error: %v", msg.err)
		} else if msg.changed {
			m.textarea.SetValue(msg.newText)
			m.statusMsg = "External edit applied."
		} else {
			m.statusMsg = "No changes from external editor."
		}
		return m, nil
	}
	return m, nil
}

func (m editorModel) View() string {
	// Check minimal dimensions.
	if m.windowHeight < 3 || m.windowWidth < 10 {
		return "Terminal size too small. Please resize the window."
	}

	var lines []string

	// Header (line 1)
	header := fmt.Sprintf("EDITOR - File: %s", m.filePath)
	if m.dbURL != "" || m.dbName != "" {
		header += fmt.Sprintf(" | DB: %s (%s)", m.dbName, m.dbURL)
	}
	header = lipgloss.NewStyle().Bold(true).Render(header)
	lines = append(lines, header)

	// Textarea view occupies the central area.
	taView := m.textarea.View()
	taLines := strings.Split(taView, "\n")
	textareaHeight := m.windowHeight - 3 // Reserve 1 line for header e 2 para status
	if len(taLines) < textareaHeight {
		extra := make([]string, textareaHeight-len(taLines))
		taLines = append(taLines, extra...)
	} else if len(taLines) > textareaHeight {
		taLines = taLines[:textareaHeight]
	}
	lines = append(lines, taLines...)

	// Status bar (last line)
	statusBar := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFF00")).Render(m.statusMsg)
	lines = append(lines, statusBar)

	return strings.Join(lines, "\n")
}

func saveFile(filePath, content string) error {
	return os.WriteFile(filePath, []byte(content), 0644)
}

func openExternalEditor(initialText string) tea.Cmd {
	return createExternalProcessCmd(initialText, "editor")
}

func createExternalProcessCmd(initialText, programType string) tea.Cmd {
	return func() tea.Msg {
		tmpFile, err := os.CreateTemp("", "external-"+programType+"-*.txt")
		if err != nil {
			return externalEditorMsg{err: err}
		}
		filename := tmpFile.Name()
		_, err = tmpFile.WriteString(initialText)
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(filename)
			return externalEditorMsg{err: err}
		}
		err = tmpFile.Close()
		if err != nil {
			_ = os.Remove(filename)
			return externalEditorMsg{err: err}
		}
		editor := os.Getenv("EDITOR")
		if strings.TrimSpace(editor) == "" {
			editor = "vi"
		}
		cmd := exec.Command(editor, filename)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			modifiedBytes, err2 := os.ReadFile(filepath.Clean(filename))
			_ = os.Remove(filename)
			if err2 != nil {
				return externalEditorMsg{err: err2}
			}
			newText := strings.TrimSpace(string(modifiedBytes))
			changed := (newText != initialText)
			return externalEditorMsg{newText: newText, changed: changed, err: err}
		})()
	}
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
	ta.MaxHeight = 0
	ta.Placeholder = ""
	ta.Prompt = ""
	ta.SetHeight(20)
	ta.SetValue(string(data))
	ta.SetWidth(80)
	ta.ShowLineNumbers = false
	ta.Focus()

	dbURL := os.Getenv("DBV_URL")
	dbName := os.Getenv("DBV_NAME")

	m := editorModel{
		filePath:  filePath,
		textarea:  ta,
		statusMsg: "^s: Save and exit | ^o: External editor | ESC or ^c: Exit",
		dbURL:     dbURL,
		dbName:    dbName,
	}

	p := tea.NewProgram(m)
	_, err = p.Run()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
