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
		cfg := db.DBConfig{
			URL: confTbl.RawGetString("url").String(),
		}

		// Get port as number.
		//if port, ok := confTbl.RawGetString("port").(luaState.LNumber); ok {
		//	cfg.Port = int(port)
		//}
		configs = append(configs, cfg)
	})

	// print the configs
	for _, cfg := range configs {
		fmt.Printf("DBConfig: %+v\n", cfg)
	}

	// if there is only one database, use it
	if len(configs) == 1 {
		db.Storage, err = db.New(configs[0].URL)
		if err != nil {
			log.Fatal(err)
		}
	}

	// if there are multiple databases, show a list and let the user choose one
	if len(configs) > 1 {
		// TODO: show a list of databases and let the user choose one
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
