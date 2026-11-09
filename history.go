package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// historyFile keeps what was typed between runs. It is state, not config:
// macOS has no state directory, so Application Support (Caches may be
// purged); elsewhere $XDG_STATE_HOME, ~/.local/state by default.
func historyFile() (string, error) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "dbv", "history.json"), nil
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "dbv", "history.json"), nil
}

func readHistory(path string) (map[string][]string, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	h := map[string][]string{}
	err = json.Unmarshal(b, &h)
	if h == nil {
		h = map[string][]string{} // a file holding null
	}
	return h, err
}

// addLine appends line to key's list, a repeat of the newest and an empty
// line left out, the oldest dropped past historyMax.
func addLine(h map[string][]string, key, line string) {
	l := h[key]
	if line == "" || (len(l) > 0 && l[len(l)-1] == line) {
		return
	}
	l = append(l, line)
	if len(l) > historyMax {
		l = l[len(l)-historyMax:]
	}
	h[key] = l
}

// saveLine adds one line to the file as it is now, not this session's
// copy, so two dbv open at once both keep what they were given. The file
// is 0600: a :where holds values from the data. yagni: two saves in the
// same instant can still lose one line (read, read, rename, rename); a
// lock file when that is ever seen.
func saveLine(path, key, line string) error {
	h, err := readHistory(path)
	if err != nil {
		h = map[string][]string{} // a broken file is replaced, not a reason to lose this line
	}
	addLine(h, key, line)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a :where's > stays readable
	enc.SetIndent("", " ")
	err = enc.Encode(h)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	err = os.MkdirAll(dir, 0o700)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".history-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(b.Bytes())
	cerr := tmp.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
