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

func configPath() string {
	p := getInitFiloPath()
	if fileExists(p) {
		return p
	}
	return "./init.filo"
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
	b, err := os.ReadFile(filepath.Clean(name)) // #nosec G703 -- the config file the user chose
	if err != nil {
		log.Fatal(err)
	}

	// Execute the Filo code that should register the databases.
	if err := f.DoString(string(b)); err != nil {
		log.Fatal(err)
	}

	return configs
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Print(usage)
		return
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
		if arg == "-s" {
			selectMode = true
			continue
		}
		files = append(files, arg)
	}

	// Files named on the command line replace the default config.
	configs := loadDBConfigs(configPath())
	if len(files) > 0 {
		configs = nil
		for _, f := range files {
			if !fileExists(f) {
				fmt.Fprintf(os.Stderr, "dbv: file not found: %q\n", f)
				os.Exit(2)
			}
			configs = append(configs, loadDBConfigs(f)...)
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
