package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"

	"github.com/crgimenes/dbv/db"
)

// viewsDirFor is where a connection's views live: the directory its config
// names, else one of its own under dbv's config directory, made when the
// first view is saved.
func viewsDirFor(cfg db.DBConfig) string {
	if cfg.ViewsPath != "" {
		return cfg.ViewsPath
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "dbv", "views", slug(cfg.Title))
}

// slug makes a title a directory name: what is not a letter, a digit,
// '.', '-' or '_' becomes '_'.
func slug(title string) string {
	var b strings.Builder
	for _, r := range title {
		ok := r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			r = '_'
		}
		b.WriteRune(r)
	}
	s := strings.Trim(b.String(), ".")
	if s == "" {
		return "default"
	}
	return s
}

// viewName checks a name typed for a view: a file name in the views
// directory, never a path out of it.
func viewName(name string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".sql")
	switch {
	case name == "":
		return "", errors.New("a view needs a name")
	case strings.ContainsAny(name, `/\`+"\x00") || strings.HasPrefix(name, "."):
		return "", fmt.Errorf("%q: a view's name is a file name, no / and no leading dot", name)
	}
	return name, nil
}

// viewTemplate leaves the view's name out: a name may hold braces, which
// would ask for a parameter.
func viewTemplate(body string) string {
	if body == "" {
		body = "SELECT "
	}
	return "-- dbv: db=" + DBTitle + "\n" +
		"-- A view of yours: save and quit to keep it, quit without saving and\n" +
		"-- nothing is kept. Double-brace parameter tokens become a form.\n\n" +
		body + "\n"
}

// editView opens view name in $EDITOR, a new one starting from body (a
// grid's SELECT, or nothing), and keeps what is saved. It says whether a
// file was written.
func editView(a *app, name, body string) bool {
	name, err := viewName(name)
	if err != nil {
		a.fail(err)
		return false
	}
	if userViewsDir == "" {
		a.say(kit.Err, "no views directory: the config directory is unknown")
		return false
	}
	path := filepath.Join(userViewsDir, name+".sql")
	old, err := os.ReadFile(filepath.Clean(path))
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.fail(err)
		return false
	}
	text := string(old)
	if !exists {
		text = viewTemplate(body)
	}
	raw, wrote, err := a.externalSaved(envOr("EDITOR", "vi"), text)
	if err != nil {
		a.fail(err)
		return false
	}
	saved, _ := edited(text, raw)
	sql := strings.TrimSpace(stripComments(saved))
	if !wrote || sql == "" || (!exists && sql == "SELECT") {
		a.say(kit.Plain, "nothing saved") // quit without saving, or the template left empty
		return false
	}
	if exists {
		now, err := os.ReadFile(filepath.Clean(path))
		if err == nil && string(now) != text {
			// changed meanwhile, by another dbv or by hand: both kept. yagni:
			// two saves in the same instant can still pass this check
			path = freeName(userViewsDir, name)
			exists = false
		}
	}
	err = writeView(path, saved, exists)
	if err != nil {
		a.fail(err)
		return false
	}
	a.say(kit.OK, "view %s saved in %s", strings.TrimSuffix(filepath.Base(path), ".sql"), tildePath(userViewsDir))
	return true
}

// freeName is the first NAME-2.sql, NAME-3.sql... not taken in dir.
func freeName(dir, name string) string {
	for n := 2; ; n++ {
		p := filepath.Join(dir, fmt.Sprintf("%s-%d.sql", name, n))
		_, err := os.Stat(p)
		if err != nil {
			return p // not there, or not readable: the exclusive create says which
		}
	}
}

// stripComments drops -- comment lines, to tell a template left as it was.
func stripComments(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
		}
	}
	return b.String()
}

// writeView writes a new view only where none is (two dbv may be at it),
// and an edited one whole or not at all.
func writeView(path, text string, exists bool) error {
	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, 0o700)
	if err != nil {
		return err
	}
	if !exists {
		f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(text)
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
		return err
	}
	tmp, err := os.CreateTemp(dir, ".view-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.WriteString(text)
	cerr := tmp.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// tildePath writes a path under home as ~/..., to fit a note.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	rest, ok := strings.CutPrefix(p, home+string(filepath.Separator))
	if !ok {
		return p
	}
	return filepath.Join("~", rest)
}
