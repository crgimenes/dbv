package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"

	"github.com/crgimenes/filo"

	"github.com/crgimenes/dbv/db"
)

var (
	GitTag       = "v0.0.0"
	DBTitle      = "-"
	userViewsDir string
	// configFiles are the configs the TUI read, for the editor it opens to
	// hand to dbv lsp: the same connection, by title
	configFiles []string
	dbNumber    int // DBTitle's place in configFiles' connections, from 1
)

func fileExists(name string) bool {
	_, err := os.Stat(name) // #nosec G703 -- a path the user gave on the command line
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		panic(err)
	}
	return true
}

// getInitFiloPath is init.filo in the platform's config directory:
// ~/Library/Application Support/dbv on macOS, $XDG_CONFIG_HOME/dbv (or
// ~/.config/dbv) on Linux and the BSDs, %AppData%\dbv on Windows.
func getInitFiloPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal("no config directory: ", err)
	}
	return filepath.Join(dir, "dbv", "init.filo")
}

func configPath() string {
	p := getInitFiloPath()
	_, err := os.Stat(p) // #nosec G703 -- dbv's own config path
	if err == nil {
		return p
	}
	return "./init.filo" // also when the directory cannot be read: the server reading this must not panic
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
	q := u.Query()
	if q.Has("password") { // libpq and pgx take it from the query too
		q.Set("password", "...")
		u.RawQuery = q.Encode()
	}

	return u.String(), nil
}

// loadDBConfigs reads a Filo config file and returns the database
// configurations it declares. The file populates the list by calling the
// registered builtin (database url [title] [views]); url is required, title
// defaults to the masked url when empty, and views is an optional directory.
func loadDBConfigs(name string) []db.DBConfig {
	configs, err := readDBConfigs(name)
	if err != nil {
		log.Fatal(err)
	}
	return configs
}

// readDBConfigs is loadDBConfigs for a caller that must not exit on a bad
// file: the language server, which a loose .sql can start.
func readDBConfigs(name string) ([]db.DBConfig, error) {
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
		return nil, err
	}

	// Read the Filo file.
	b, err := os.ReadFile(filepath.Clean(name)) // #nosec G703 -- the config file the user chose
	if err != nil {
		return nil, err
	}

	// Execute the Filo code that should register the databases.
	err = f.DoString(string(b))
	if err != nil {
		return nil, err
	}
	return configs, nil
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Print(usage)
		return
	}
	if len(args) > 0 && args[0] == "lsp" {
		os.Exit(runLSP(args[1:]))
	}
	if len(args) > 0 {
		_, ok := commands[args[0]]
		if ok {
			os.Exit(runCLI(args[0], args[1:]))
		}
	}

	selectMode := false
	var files []string
	for _, arg := range args {
		switch arg {
		case "-s":
			selectMode = true
		case "-debug":
			debugOut = os.Stderr
		default:
			files = append(files, arg)
		}
	}
	if debugOut != nil && stderrIsTerminal() {
		fmt.Fprintln(os.Stderr, "dbv: -debug writes to stderr, which is this screen; redirect it: dbv -debug 2>dbv.log")
		os.Exit(2)
	}

	// Files named on the command line replace the default config, which is
	// then not read at all.
	var configs []db.DBConfig
	if len(files) == 0 {
		configFiles = []string{configPath()}
		configs = loadDBConfigs(configFiles[0])
	}
	if len(files) > 0 {
		for _, f := range files {
			if !fileExists(f) {
				fmt.Fprintf(os.Stderr, "dbv: file not found: %q\n", f)
				os.Exit(2)
			}
			configs = append(configs, loadDBConfigs(f)...)
			configFiles = append(configFiles, f)
		}
	}

	first := newTableList
	if selectMode {
		first = newSelector(selectorOutputFile)
	}
	err := runTUI(configs, first)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbv:", err)
		os.Exit(1)
	}
}
