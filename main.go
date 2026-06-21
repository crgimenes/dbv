package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/crgimenes/filo"

	"github.com/crgimenes/dbv/db"
)

type screen int

type errMsg error

type menuModel struct {
	choices  []db.DBConfig
	selected int
	chosen   int
}

const (
	screenList screen = iota
	screenData
	screenForm

	recordsPerPage = 200
)

var (
	GitTag       = "v0.0.0"
	DBTitle      = "-"
	userViewsDir string

	themeBackground = lipgloss.Color("#000000")
	//themeForeground = lipgloss.Color("#F8F8F2")
	//Accent     = lipgloss.Color("#6272A4")
	//Background      = lipgloss.Color("#282A36")
	themeForeground = lipgloss.Color("#F8F8F2")
	themeAccent     = lipgloss.Color("#6272A4")
	themeTitle      = lipgloss.Color("#FF79C6")

	titleStyle = lipgloss.NewStyle().
			Foreground(themeTitle).
			Background(themeBackground)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(themeAccent).
			Bold(true)
	headerCellStyle = lipgloss.NewStyle().
			Foreground(themeAccent).
			Background(themeBackground).
			Bold(true)
	selectedCellStyle = lipgloss.NewStyle().
				Foreground(themeForeground).
				Background(themeAccent).
				Bold(true)
	errorStatusBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF5555")).
				Bold(true)
	formTitleStyle = lipgloss.NewStyle().
			Foreground(themeTitle).
			Bold(true)
	submittedStyle = lipgloss.NewStyle().
			Foreground(themeForeground)
	abandonedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF5555")).
			Bold(true)
)

type showUserViewDataMsg struct {
	viewName string
	sql      string
}

type showUserViewFormMsg struct {
	viewName string
	sql      string
	params   []Parameter
}

type formResultMsg struct {
	viewName string
	sql      string
}

func fileExists(name string) bool {
	_, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		panic(err)
	}
	return true
}

func getInitFiloPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal("Failed to get home directory:", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "dbv", "init.filo")
}

func maskDBURL(dbURL string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", err
	}

	if u.User != nil {
		username := u.User.Username()
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(username, "...")
		}
	}

	return u.String(), nil
}

func (m menuModel) Init() tea.Cmd {
	return nil
}

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// select a database by number
		if len(msg.String()) == 1 {
			r := msg.String()[0]
			if r >= '0' && r <= '9' {
				var index int
				if r == '0' {
					index = 9
				} else {
					index = int(r - '1')
				}
				if index < len(m.choices) && index < 10 {
					m.chosen = index
					return m, tea.Quit
				}
			}
		}
		switch msg.String() {
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected < len(m.choices)-1 {
				m.selected++
			}
		case "enter":
			m.chosen = m.selected
			return m, tea.Quit
		case "q", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m menuModel) View() string {
	var sb strings.Builder
	sb.WriteString(statusBarStyle.Render("Select a database"))
	sb.WriteString("\n\n")
	for i, cfg := range m.choices {
		title := cfg.Title
		if m.selected == i {
			sb.WriteString(fmt.Sprintf("> %d ", i+1))
			sb.WriteString(selectedCellStyle.Render(title))
			sb.WriteString("\n")
			continue
		}
		sb.WriteString(fmt.Sprintf("  %d ", i+1))
		sb.WriteString(statusBarStyle.Render(title))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(statusBarStyle.Render(
		"Enter to select, q or esc to quit",
	))
	return sb.String()
}

// loadDBConfigs reads a Filo config file and returns the database
// configurations it declares. The file populates the list by calling the
// registered builtin (database url [title] [views]); url is required, title
// defaults to the masked url when empty, and views is an optional directory.
func loadDBConfigs(name string) []db.DBConfig {
	f := filo.New()
	defer f.Close()

	var configs []db.DBConfig

	err := f.RegisterBuiltin("database", func(_ context.Context, args []filo.Value) (filo.Value, error) {
		if len(args) < 1 {
			return filo.VBool(false), fmt.Errorf("database: url is required")
		}

		dbURL, err := args[0].AsString()
		if err != nil {
			return filo.VBool(false), fmt.Errorf("database: url must be a string: %w", err)
		}
		if dbURL == "" {
			return filo.VBool(false), fmt.Errorf("database: url must not be empty")
		}

		title := ""
		if len(args) >= 2 {
			title, err = args[1].AsString()
			if err != nil {
				return filo.VBool(false), fmt.Errorf("database: title must be a string: %w", err)
			}
		}
		if title == "" {
			title, err = maskDBURL(dbURL)
			if err != nil {
				return filo.VBool(false), err
			}
		}

		viewsPath := ""
		if len(args) >= 3 {
			viewsPath, err = args[2].AsString()
			if err != nil {
				return filo.VBool(false), fmt.Errorf("database: views must be a string: %w", err)
			}
		}
		if viewsPath != "" {
			info, statErr := os.Stat(viewsPath)
			if statErr != nil {
				if os.IsNotExist(statErr) {
					return filo.VBool(false), fmt.Errorf("views directory not found: %s", viewsPath)
				}
				return filo.VBool(false), fmt.Errorf("failed to access views directory: %w", statErr)
			}
			if !info.IsDir() {
				return filo.VBool(false), fmt.Errorf("views path is not a directory: %s", viewsPath)
			}
			viewsPath, err = filepath.Abs(viewsPath)
			if err != nil {
				return filo.VBool(false), fmt.Errorf("failed to get absolute path for views directory: %s", viewsPath)
			}
		}

		configs = append(configs, db.DBConfig{
			URL:       dbURL,
			Title:     title,
			ViewsPath: viewsPath,
		})
		return filo.VBool(true), nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Read the Filo file.
	b, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		log.Fatal(err)
	}

	// Execute the Filo code that should register the databases.
	if err := f.DoString(string(b)); err != nil {
		log.Fatal(err)
	}

	return configs
}

func runFiloFile(name string) {
	configs := loadDBConfigs(name)

	// if there is only one database, use it
	if len(configs) == 1 {
		DBTitle = configs[0].Title
		var err error
		db.Storage, err = db.New(configs[0].URL)
		if err != nil {
			log.Fatal(err)
		}
		userViewsDir = configs[0].ViewsPath
	}

	if len(configs) > 1 {
		menu := menuModel{choices: configs, selected: 0, chosen: -1}
		finalModel, err := tea.NewProgram(menu).Run()
		if err != nil {
			log.Fatal(err)
		}
		chosenMenu := finalModel.(menuModel)
		if chosenMenu.chosen == -1 {
			os.Exit(0)
		}

		DBTitle = configs[chosenMenu.chosen].Title
		db.Storage, err = db.New(configs[chosenMenu.chosen].URL)
		if err != nil {
			log.Fatal(err)
		}
		userViewsDir = configs[chosenMenu.chosen].ViewsPath
	}

}

type rootModel struct {
	currentScreen screen
	modelList     modelList
	modelData     modelData
	formModel     formModel
}

type showRecordsMsg struct {
	tableName string
	pk        string
}

type loadMoreRecordsMsg struct {
	records    []map[string]any
	columnInfo []db.ColumnInfo
	offset     int
	direction  int
	total      int
}

func (m rootModel) Init() tea.Cmd {
	return m.modelList.Init()
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case showRecordsMsg:
		pk := msg.pk
		if pk == "-" {
			pk = ""
		}

		recs, columnInfo, totalCount, err := db.Storage.ListRecords(
			msg.tableName,
			pk,
			0,
			recordsPerPage,
			"",
			"",
		)
		if err != nil {
			return m, tea.Cmd(func() tea.Msg {
				return errMsg(err)
			})
		}

		data := [][]string{}
		columns := []string{}
		for _, col := range columnInfo {
			columns = append(columns, col.ColumnName)
		}

		data = append(data, columns)
		for _, rec := range recs {
			row := []string{}
			for _, col := range columnInfo {
				row = append(row, fmt.Sprintf("%v", rec[col.ColumnName]))
			}
			data = append(data, row)
		}

		m.modelData.data = data
		m.modelData.selectedRow = 1
		m.modelData.selectedCol = 0
		m.modelData.verticalOff = 1
		m.modelData.horizontalOff = 0

		m.modelData.tableName = msg.tableName
		m.modelData.pk = msg.pk
		m.modelData.pageSize = recordsPerPage
		m.modelData.loadedRecords = data[1:]
		m.modelData.loadedOffset = 0
		m.modelData.totalRecords = totalCount
		m.modelData.scrollThreshold = 10
		m.modelData.columnInfo = columnInfo

		m.modelData.windowWidth = m.modelList.windowWidth
		m.modelData.windowHeight = m.modelList.windowHeight

		m.currentScreen = screenData
		return m, nil

	case userViewMsg:
		// Load the SQL file content
		sqlContent, err := os.ReadFile(msg.sqlPath)
		if err != nil {
			return m, tea.Cmd(func() tea.Msg {
				return errMsg(fmt.Errorf("error reading SQL file: %w", err))
			})
		}

		sql := string(sqlContent)
		params := ExtractParameters(sql)

		if len(params) > 0 {
			// If the view has parameters, display the form
			m.formModel = newFormModel(msg.name, sql, params)
			m.formModel.windowWidth = m.modelList.windowWidth
			m.formModel.windowHeight = m.modelList.windowHeight
			m.currentScreen = screenForm
			return m, nil
		}

		return m, func() tea.Msg {
			return showUserViewDataMsg{
				viewName: msg.name,
				sql:      sql,
			}
		}

	case formResultMsg:
		return m, func() tea.Msg {
			return showUserViewDataMsg{
				viewName: msg.viewName,
				sql:      msg.sql,
			}
		}

	case showUserViewDataMsg:
		records, columnInfo, err := db.Storage.QuerySQL(msg.sql)
		if err != nil {
			m.currentScreen = screenList
			return m, tea.Cmd(func() tea.Msg {
				return errMsg(fmt.Errorf("error executing query: %w", err))
			})
		}

		data := [][]string{}
		columns := []string{}
		for _, col := range columnInfo {
			columns = append(columns, col.ColumnName)
		}

		data = append(data, columns)
		for _, rec := range records {
			row := []string{}
			for _, col := range columnInfo {
				row = append(row, fmt.Sprintf("%v", rec[col.ColumnName]))
			}
			data = append(data, row)
		}

		m.modelData.data = data
		m.modelData.selectedRow = 1
		m.modelData.selectedCol = 0
		m.modelData.verticalOff = 1
		m.modelData.horizontalOff = 0

		m.modelData.tableName = msg.viewName
		m.modelData.pk = "-" // Read-only mode
		m.modelData.pageSize = recordsPerPage
		m.modelData.loadedRecords = data[1:]
		m.modelData.loadedOffset = 0
		m.modelData.totalRecords = len(data) - 1
		m.modelData.scrollThreshold = 10
		m.modelData.columnInfo = columnInfo

		m.modelData.windowWidth = m.modelList.windowWidth
		m.modelData.windowHeight = m.modelList.windowHeight

		m.currentScreen = screenData
		return m, nil

	case string:
		if msg == "backToList" {
			m.currentScreen = screenList
			return m, nil
		}
	}

	switch m.currentScreen {
	case screenList:
		newList, cmd := m.modelList.Update(msg)
		m.modelList = newList.(modelList)
		return m, cmd
	case screenData:
		newData, cmd := m.modelData.Update(msg)
		m.modelData = newData.(modelData)
		return m, cmd
	case screenForm:
		newForm, cmd := m.formModel.Update(msg)
		formModel := newForm.(formModel)
		m.formModel = formModel

		if formModel.submitted {
			m.currentScreen = screenData
			return m, func() tea.Msg {
				return showUserViewDataMsg{
					viewName: formModel.screenTitle,
					sql:      formModel.finalSQL,
				}
			}
		}

		if formModel.abandoned {
			m.currentScreen = screenList
		}

		return m, cmd
	}
	return m, nil
}

func (m rootModel) View() string {
	switch m.currentScreen {
	case screenList:
		return m.modelList.View()
	case screenData:
		return m.modelData.View()
	case screenForm:
		return m.formModel.View()
	default:
		return "Something went wrong..."
	}
}

func main() {
	const selectorOutputFile = "/tmp/dbv_output.txt"
	log.SetFlags(log.LstdFlags | log.Llongfile)

	useSelectorMode := false

	if slices.Contains(os.Args[1:], "-s") {
		index := slices.Index(os.Args[1:], "-s")
		if index != -1 {
			os.Args = slices.Delete(os.Args, index, index+1)
		}
		useSelectorMode = true
	}

	initFile := getInitFiloPath()
	if fileExists(initFile) {
		runFiloFile(initFile)
	} else {
		// load local config
		initFile = "./init.filo"
		runFiloFile(initFile)
	}

	defer func() {
		if db.Storage != nil {
			db.Storage.Close()
		}
	}()

	if len(os.Args) > 1 {
		for _, runFile := range os.Args[1:] {
			if !fileExists(runFile) {
				log.Fatalf("file not found: %q", runFile)
			}
			log.Printf("running %s", runFile)
			runFiloFile(runFile)
		}
	}

	var p *tea.Program
	if useSelectorMode {

		// create output file if it does not exist
		_, err := os.Stat(selectorOutputFile)
		if os.IsNotExist(err) {
			f, err := os.Create(selectorOutputFile)
			if err != nil {
				log.Fatalf("failed to create file: %v", err)
			}
			f.Close()
		}

		// truncate the file
		err = os.Truncate(selectorOutputFile, 0)
		if err != nil {
			log.Fatalf("failed to truncate file: %v", err)
		}

		// Selector mode
		p = tea.NewProgram(
			initialModelSelector(selectorOutputFile),
			tea.WithAltScreen(),
		)
	} else {
		// Default mode
		p = tea.NewProgram(
			rootModel{
				currentScreen: screenList,
				modelList:     initialModelList(),
				modelData:     modelData{},
				formModel:     formModel{},
			},
			tea.WithAltScreen(),
		)
	}

	_, err := p.Run()
	if err != nil {
		fmt.Println("Error:", err)
	}
}
