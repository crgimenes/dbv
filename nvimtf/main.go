// main.go
package main

import (
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

type mode int

const (
	tableMode mode = iota
	fieldMode
)

type model struct {
	currentMode mode
	tables      []string
	fields      []string
	tableIndex  int
	fieldIndex  int
}

func initialModel() model {
	return model{
		currentMode: tableMode,
		tables:      []string{"Tabela 1", "Tabela 2", "Tabela 3"},
		fields:      []string{"Campo 1", "Campo 2", "Campo 3"},
		tableIndex:  0,
		fieldIndex:  0,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			if m.currentMode == tableMode {
				m.currentMode = fieldMode
				m.fieldIndex = 0
			} else {
				m.currentMode = tableMode
			}
		case "up", "k":
			if m.currentMode == tableMode {
				if m.tableIndex > 0 {
					m.tableIndex--
				}
			} else {
				if m.fieldIndex > 0 {
					m.fieldIndex--
				}
			}
		case "down", "j":
			if m.currentMode == tableMode {
				if m.tableIndex < len(m.tables)-1 {
					m.tableIndex++
				}
			} else {
				if m.fieldIndex < len(m.fields)-1 {
					m.fieldIndex++
				}
			}
		case "enter":
			var text string
			if m.currentMode == tableMode {
				text = m.tables[m.tableIndex]
			} else {
				text = m.fields[m.fieldIndex]
			}
			err := os.WriteFile("/tmp/app_output.txt", []byte(text+"\n"), 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error writing to file: %v\n", err)
			}
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			if m.currentMode == fieldMode {
				m.currentMode = tableMode
			} else {
				return m, tea.Quit
			}

		}
	}
	return m, nil
}

func (m model) View() string {
	s := ""
	if m.currentMode == tableMode {
		s += "Select Table:\n\n"
		for i, table := range m.tables {
			if i == m.tableIndex {
				s += fmt.Sprintf("> %s\n", table)
			} else {
				s += fmt.Sprintf("  %s\n", table)
			}
		}
		s += "\nPress Tab to switch to field selection."
	} else {
		s += fmt.Sprintf("Table Selected: %s\n", m.tables[m.tableIndex])
		s += "Select Field:\n\n"
		for i, field := range m.fields {
			if i == m.fieldIndex {
				s += fmt.Sprintf("> %s\n", field)
			} else {
				s += fmt.Sprintf("  %s\n", field)
			}
		}
		s += "\nPress Tab to switch back to table selection."
	}
	return s
}

func main() {
	p := tea.NewProgram(initialModel())
	_, err := p.Run()
	if err != nil {
		log.Fatal(err)
	}
}
