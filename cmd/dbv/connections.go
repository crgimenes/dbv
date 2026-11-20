package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crgimenes/filo"
	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

// errConfigChanged is a config file changed since it was read.
var errConfigChanged = errors.New("the config file changed since dbv read it (another dbv, or by hand): nothing written; open dbv again")

// writeDBConfigs writes configs to path as (database url title views)
// lines, the old file kept beside it as path.bak, both 0600 and each
// replaced whole. What else the old file held (comments, variables, an
// environment lookup) is not kept. A link is followed to the file it names.
// The file must still be read (nil: it was not there); the new contents
// are what the next write expects.
// yagni: two writes in the same instant can both pass the check; a lock
// file would close that.
func writeDBConfigs(path string, configs []db.DBConfig, read []byte) ([]byte, error) {
	var b strings.Builder
	b.WriteString("; dbv connections: (database url title views), written by dbv\n")
	for _, c := range configs {
		fmt.Fprintf(&b, "(database %s %s %s)\n", filoString(c.URL), filoString(c.Title), filoString(c.ViewsPath))
	}
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = real
	}
	old, err := os.ReadFile(filepath.Clean(path))
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !bytes.Equal(old, read) || exists != (read != nil) {
		return nil, errConfigChanged
	}
	if exists {
		err = replaceFile(path+".bak", old)
		if err != nil {
			return nil, err
		}
	}
	data := []byte(b.String())
	return data, replaceFile(path, data)
}

// replaceFile puts data at path through a new 0600 file renamed over it:
// whatever was there, a link or a file open to others, is replaced, not
// written through.
func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, 0o700)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dbv-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	cerr := tmp.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // #nosec G703 -- dbv's own config path, or the file the user named
}

// filoString is s as a Filo string literal, on one line.
func filoString(s string) string { return strings.TrimSpace(filo.FormatValue(filo.VString(s))) }

// unmask puts back the passwords maskDBURL hid in typed, from old: the
// form shows "..." where a password is, and "..." left there means the
// password stays.
func unmask(typed, old string) string {
	u, err := db.ParseURL(typed)
	if err != nil || old == "" {
		return typed
	}
	o, err := db.ParseURL(old)
	if err != nil {
		return typed
	}
	if u.User != nil && o.User != nil {
		p, ok := u.User.Password()
		op, ook := o.User.Password()
		if ok && p == "..." && ook {
			u.User = url.UserPassword(u.User.Username(), op)
		}
	}
	q, oq := u.Query(), o.Query()
	for k := range q {
		if strings.Contains(strings.ToLower(k), "password") && q.Get(k) == "..." && oq.Has(k) {
			q.Set(k, oq.Get(k))
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

// connForm adds a connection (at < 0) or edits the at-th of menu's.
type connForm struct {
	menu *dbMenu
	at   int
	form *kit.Form
}

const (
	cfTitle = iota
	cfURL
	cfViews
)

func newConnForm(m *dbMenu, at int) *connForm {
	var c db.DBConfig
	if at >= 0 {
		c = m.configs[at]
	}
	shown := ""
	if c.URL != "" {
		masked, err := maskDBURL(c.URL)
		shown = masked
		if err != nil {
			shown = "" // what does not parse cannot be masked: it is not shown
		}
	}
	f := &connForm{menu: m, at: at, form: kit.NewForm([]kit.Field{
		{Kind: kit.TextField, Label: "Title", Width: 60, Max: max(200, len(c.Title)), Value: c.Title},
		{Kind: kit.TextField, Label: "URL", Width: 60, Max: max(4096, len(shown)), Value: shown},
		{Kind: kit.TextField, Label: "Queries dir", Width: 60, Max: max(1024, len(c.ViewsPath)), Value: c.ViewsPath},
	}, "Save", "Test", "Cancel")}
	if c.URL != "" && shown == "" {
		f.form.Fail(cfURL, "the stored URL does not parse; type it again")
	}
	return f
}

func (f *connForm) title() string {
	if f.at < 0 {
		return "dbv · New connection"
	}
	return "dbv · Connection " + f.menu.configs[f.at].Title
}

func (f *connForm) hints() []kit.Hint {
	return []kit.Hint{{Key: "↑↓ tab", What: "move"}, {Key: "enter", What: "next"}, {Key: "^W", What: "save"}, {Key: "esc", What: "back"}}
}

func (f *connForm) draw(_ *app, c *screen.Canvas, top, left, rows, cols int) {
	for i := range f.form.Fields {
		f.form.Fields[i].Width = max(10, min(80, cols-18))
	}
	h := f.form.Draw(c, top+1, left+1, 12)
	help := []string{
		"URL: postgres://user:password@host:5432/db?sslmode=disable, or",
		"ssh+postgres://... through a bastion. A password shown as ... stays as",
		"it is; one typed here shows while it is typed (~/.pgpass keeps it off",
		"the URL). Queries dir: your .sql files; empty, one under dbv's config.",
	}
	for i, line := range help {
		if top+h+2+i < top+rows {
			c.Print(top+h+2+i, left+1, kit.Cut(line, cols-2), kit.Dim)
		}
	}
}

func (f *connForm) key(a *app, k screen.Key) {
	switch f.form.Key(k) {
	case kit.Cancel:
		a.pop()
	case "test":
		cfg, ok := f.config()
		if !ok {
			return
		}
		a.say(kit.Wait, "connecting to %s", cfg.Title)
		a.paint()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := bounded(ctx, func() error { // an ssh dial takes no context
			pg, err := db.New(cfg.URL)
			if err == nil {
				pg.Close()
			}
			return err
		})
		if err != nil {
			f.form.Note = withoutURL(err, cfg).Error()
			a.note = ""
			return
		}
		a.say(kit.OK, "%s answers", cfg.Title)
	case "save":
		cfg, ok := f.config()
		if !ok {
			return
		}
		configs := append([]db.DBConfig(nil), f.menu.configs...)
		at := f.at
		if at < 0 {
			at = len(configs)
			configs = append(configs, db.DBConfig{})
		}
		configs[at] = cfg
		data, err := writeDBConfigs(f.menu.file, configs, f.menu.read)
		if err != nil {
			f.form.Note = err.Error()
			return
		}
		f.menu.configs, f.menu.read = configs, data
		a.pop()
		a.say(kit.OK, "%s saved in %s", cfg.Title, tildePath(f.menu.file))
	}
}

// config is what the form holds as a connection, or false with the form
// saying what is wrong.
func (f *connForm) config() (db.DBConfig, bool) {
	old := ""
	if f.at >= 0 {
		old = f.menu.configs[f.at].URL
	}
	c := db.DBConfig{
		Title:     strings.TrimSpace(f.form.Value(cfTitle)),
		URL:       unmask(strings.TrimSpace(f.form.Value(cfURL)), old),
		ViewsPath: strings.TrimSpace(f.form.Value(cfViews)),
	}
	if c.URL == "" {
		f.form.Fail(cfURL, "a connection needs a URL")
		return c, false
	}
	_, err := db.ParseURL(c.URL)
	if err != nil {
		f.form.Fail(cfURL, err.Error())
		return c, false
	}
	if c.Title == "" {
		c.Title, _ = maskDBURL(c.URL)
	}
	for i, o := range f.menu.configs {
		if i != f.at && o.Title == c.Title {
			f.form.Fail(cfTitle, "another connection has this title; a .sql file names its connection by it")
			return c, false
		}
	}
	if c.ViewsPath != "" {
		p, err := expandHome(c.ViewsPath)
		if err == nil {
			p, err = filepath.Abs(p)
		}
		info, serr := os.Stat(p)
		if err != nil || serr != nil || !info.IsDir() {
			f.form.Fail(cfViews, "not a directory: "+c.ViewsPath)
			return c, false
		}
		c.ViewsPath = p
	}
	return c, true
}
