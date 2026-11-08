package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"
)

// Parameter is a {{name:type:title:order}} token in a user view's SQL.
type Parameter struct {
	Name       string // required
	Type       string // default "string"
	Title      string // default equals Name
	ZOrder     int    // custom order; default order of occurrence
	Occurrence int    // internal order if ZOrder are equal
}

var paramToken = regexp.MustCompile(`\{\{(.*?)\}\}`)

func ExtractParameters(sqlStr string) []Parameter {
	var params []Parameter
	for i, match := range paramToken.FindAllStringSubmatch(sqlStr, -1) {
		fields := strings.Split(match[1], ":")
		for j, field := range fields {
			fields[j] = strings.TrimSpace(field)
		}
		if fields[0] == "" {
			continue
		}
		p := Parameter{Name: fields[0], Occurrence: i, Type: "string", Title: fields[0], ZOrder: i}
		if len(fields) >= 2 && fields[1] != "" {
			p.Type = fields[1]
		}
		if len(fields) >= 3 && fields[2] != "" {
			p.Title = fields[2]
		}
		if len(fields) >= 4 && fields[3] != "" {
			z, err := strconv.Atoi(fields[3])
			if err == nil {
				p.ZOrder = z
			}
		}
		params = append(params, p)
	}
	sort.SliceStable(params, func(i, j int) bool {
		if params[i].ZOrder != params[j].ZOrder {
			return params[i].ZOrder < params[j].ZOrder
		}
		return params[i].Occurrence < params[j].Occurrence
	})
	return params
}

// fillParameters puts each value in place of every token of its parameter.
func fillParameters(sql string, params []Parameter, values []string) string {
	for i, p := range params {
		re := regexp.MustCompile(`\{\{\s*` + regexp.QuoteMeta(p.Name) + `\s*(:[^}]*)?\}\}`) // the whole name: {{id}} is not {{id2}}
		sql = re.ReplaceAllLiteralString(sql, values[i])
	}
	return sql
}

func openUserView(a *app, name string) {
	b, err := os.ReadFile(filepath.Join(userViewsDir, name+".sql")) // #nosec G304 -- a file the user put in their views directory
	if err != nil {
		a.fail(err)
		return
	}
	sql := string(b)
	params := ExtractParameters(sql)
	if len(params) == 0 {
		showQuery(a, name, sql)
		return
	}
	a.push(newParamForm(a, name, sql, params))
}

func showQuery(a *app, name, sql string) {
	d, err := newQueryData(a, name, sql)
	if err != nil {
		a.fail(fmt.Errorf("%s: %w", name, err))
		return
	}
	a.push(d)
}

// paramForm asks for a user view's parameters, all on screen at once.
type paramForm struct {
	name   string
	sql    string
	params []Parameter
	form   *kit.Form
	labelw int
}

// formKey is where a view's parameter keeps the values it was given.
func formKey(view, param string) string { return "form " + view + " " + param }

// newParamForm opens with the values the view ran with last time.
func newParamForm(a *app, name, sql string, params []Parameter) *paramForm {
	f := &paramForm{name: name, sql: sql, params: params}
	fields := make([]kit.Field, len(params))
	for i, p := range params {
		fields[i] = kit.Field{Kind: kit.TextField, Label: p.Title, Width: 40, Max: 256, Value: a.last(formKey(name, p.Name))}
		f.labelw = max(f.labelw, screen.Width(p.Title))
	}
	f.form = kit.NewForm(fields, "Run", "Cancel")
	return f
}

func (f *paramForm) title() string { return f.name + " · " + DBTitle }

func (f *paramForm) hints() []kit.Hint {
	return []kit.Hint{{Key: "↑↓ tab", What: "move"}, {Key: "enter", What: "next"}, {Key: "^W", What: "run"}, {Key: "esc", What: "back"}}
}

func (f *paramForm) draw(_ *app, c *screen.Canvas, top, left, _, cols int) {
	for i := range f.form.Fields {
		f.form.Fields[i].Width = max(10, min(60, cols-f.labelw-6))
	}
	f.form.Draw(c, top+1, left+1, f.labelw) // the form draws its own note
}

func (f *paramForm) key(a *app, k screen.Key) {
	switch f.form.Key(k) {
	case kit.Cancel:
		a.pop()
	case "run":
		values := make([]string, len(f.params))
		for i := range f.params {
			values[i] = f.form.Value(i)
		}
		d, err := newQueryData(a, f.name, fillParameters(f.sql, f.params, values))
		if err != nil {
			f.form.Note = err.Error() // the form stays, to fix what was typed
			return
		}
		for i, p := range f.params {
			a.rememberAt(formKey(f.name, p.Name), values[i])
		}
		a.pop()
		a.push(d)
	}
}
