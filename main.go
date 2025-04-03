package main

import (
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"dbv/db"
	"dbv/lua"
)

const (
	initFile       = "init.lua"
	recordsPerPage = 100
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

func runLuaFile(name string) {
	L := lua.New()
	defer L.Close()

	b, err := os.ReadFile(name)
	if err != nil {
		log.Fatal(err)
	}

	L.SetGlobal("DataBaseURL", "")
	if err := L.DoString(string(b)); err != nil {
		log.Fatal(err)
	}

	dataBaseURL := L.MustGetString("DataBaseURL")
	if dataBaseURL != "" {
		db.Storage, err = db.New(dataBaseURL)
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

	if fileExists(initFile) {
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
