package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"dbv/db"
	"dbv/lua"

	tea "github.com/charmbracelet/bubbletea"
	luaState "github.com/yuin/gopher-lua"
)

const (
	recordsPerPage = 1000
)

type errMsg error

type menuModel struct {
	choices  []db.DBConfig
	selected int
	chosen   int
}

var (
	GitTag = "v0.0.0"
)

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

func getInitLuaPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal("Failed to get home directory:", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "dbv", "init.lua")
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
	s := statusBarStyle.Render("Select a database") + "\n\n"
	for i, cfg := range m.choices {
		if m.selected == i {
			title := cfg.Title
			if title == "" {
				title = cfg.URL
			}
			s += fmt.Sprintf("> %d ", i+1) +
				selectedCellStyle.Render(fmt.Sprintf("%s", title)) + "\n"
		} else {
			title := cfg.Title
			if title == "" {
				title = cfg.URL
			}
			s += fmt.Sprintf("  %d ", i+1) +
				statusBarStyle.Render(fmt.Sprintf("%s", title)) + "\n"
		}
	}
	s += "\n" + statusBarStyle.Render(
		"Enter to select, q or esc to quit",
	)
	return s
}

func runLuaFile(name string) {
	// Create a new Lua state.
	L := lua.New()
	defer L.Close()

	// Read the Lua file.
	b, err := os.ReadFile(name)
	if err != nil {
		log.Fatal(err)
	}

	// Pre-declare DataBases as an empty table.
	emptyTable := L.GetState().NewTable()
	L.SetGlobalTable("DataBases", emptyTable)

	// Execute the Lua code that should populate DataBases.
	if err := L.DoString(string(b)); err != nil {
		log.Fatal(err)
	}

	// Retrieve the DataBases table.
	table := L.GetGlobalTable("DataBases")
	if table == nil {
		log.Fatal("DataBases is not a table")
	}

	// Convert the Lua table to a slice of db.DBConfig.
	var configs []db.DBConfig
	table.ForEach(func(_, value luaState.LValue) {
		confTbl, ok := value.(*luaState.LTable)
		if !ok {
			return
		}

		var (
			title luaState.LValue
			url   luaState.LValue
		)

		title = confTbl.RawGetString("title")
		if title == luaState.LNil {
			title = confTbl.RawGetString("url")
		}

		url = confTbl.RawGetString("url")
		if url == luaState.LNil {
			// if url is nil, error message
			log.Fatalf("url is nil for table: %q", confTbl)
		}

		cfg := db.DBConfig{
			URL:   url.String(),
			Title: title.String(),
		}

		configs = append(configs, cfg)
	})

	// print the configs
	//for _, cfg := range configs {
	//	fmt.Printf("DBConfig: %+v\n", cfg)
	//}

	// if there is only one database, use it
	if len(configs) == 1 {
		db.Storage, err = db.New(configs[0].URL)
		if err != nil {
			log.Fatal(err)
		}
	}

	// if there are multiple databases, show a menu to select one
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

		db.Storage, err = db.New(configs[chosenMenu.chosen].URL)
		if err != nil {
			log.Fatal(err)
		}
	}

}

type screen int

const (
	screenList screen = iota
	screenData
)

type rootModel struct {
	currentScreen screen
	modelList     modelList
	modelData     modelData
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

		m.modelData.windowWidth = m.modelList.width
		m.modelData.windowHeight = m.modelList.height
		m.modelData.editor = newModelEditor()

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
	}
	return m, nil
}

func (m rootModel) View() string {
	switch m.currentScreen {
	case screenList:
		return m.modelList.View()
	case screenData:
		return m.modelData.View()
	default:
		return "Something went wrong..."
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Llongfile)

	initFile := getInitLuaPath()
	if fileExists(initFile) {
		runLuaFile(initFile)
	} else {
		// load local config
		initFile = "./init.lua"
		runLuaFile(initFile)
	}

	defer func() {
		if db.Storage != nil {
			db.Storage.Close()
		}
	}()

	if len(os.Args) > 1 {
		for _, runFile := range os.Args[1:] {
			if !fileExists(runFile) {
				log.Fatalf("file not found: %s", runFile)
			}
			log.Printf("running %s", runFile)
			runLuaFile(runFile)
		}
	}

	p := tea.NewProgram(
		rootModel{
			currentScreen: screenList,
			modelList:     initialModelList(),
			modelData:     modelData{},
		},
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	if err != nil {
		fmt.Println("Error:", err)
	}
}
