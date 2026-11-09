package sqlcomplete

import (
	"slices"
	"strings"
	"testing"
)

var cat = &Catalog{Tables: []Table{
	{Schema: "public", Name: "clientes", Kind: "table", Columns: []Column{{"id", "integer"}, {"nome", "text"}, {"Cidade", "text"}}},
	{Schema: "public", Name: "pedidos", Kind: "table", Columns: []Column{{"id", "integer"}, {"cliente_id", "integer"}, {"total", "numeric"}}},
	{Schema: "vendas", Name: "metas", Kind: "view", Columns: []Column{{"mes", "date"}}},
}}

// at puts the cursor where | is.
func at(s string) (string, int) {
	i := strings.Index(s, "|")
	return s[:i] + s[i+1:], i
}

func labels(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Label)
	}
	return out
}

func TestComplete(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want []string // all of these, and for "only" nothing else
		only bool
	}{
		{"select * from clientes cl where cl.|", []string{"id", "nome", "Cidade"}, true},
		{"select cl.| from clientes cl", []string{"id", "nome", "Cidade"}, true}, // the FROM comes after the cursor
		{"select cl.n| from clientes cl", []string{"nome"}, true},
		{"select * from clientes as c join pedidos p on p.| ", []string{"id", "cliente_id", "total"}, true},
		{"select * from clientes c, pedidos p where p.to|", []string{"total"}, true},
		{"select * from clientes where clientes.|", []string{"id", "nome", "Cidade"}, true},
		{"select * from |", []string{"clientes", "pedidos", "metas"}, true},
		{"select * from clientes c join |", []string{"clientes", "pedidos", "metas"}, true},
		{"select * from clientes c, |", []string{"clientes", "pedidos", "metas"}, true},
		{"select * from vendas.|", []string{"metas"}, true},
		{"select * from cli|", []string{"clientes"}, true},
		{"update pedidos set to| = 1", []string{"total"}, false},
		{"select n| from clientes", []string{"nome", "not", "null", "nulls", "nullif", "now"}, true},
		{"sel|", []string{"select"}, true},
		{"select * from clientes where nome = 'cl.|'", nil, true}, // in a literal
		{"select x.| from clientes c", nil, true},                 // no such alias or table
		{"-- dbv: db=x se|\nselect 1", nil, true},
		{"select * from clientes c where exists (select 1 from pedidos c where c.|)", []string{"id", "cliente_id", "total"}, true}, // the subquery's c
		{"select * from clientes c where exists (select 1 from pedidos p where c.|)", []string{"id", "nome", "Cidade"}, true},      // the outer c, correlated
		{"select * from (select * from pedidos p) x where p.|", nil, true},                                                         // p is not seen out here
		{"SELECT /* a /* b */ c.| */ * FROM clientes c", nil, true},                                                                // nested comment
		{"select E'it\\'s;x' from clientes c where c.no|", []string{"nome"}, true},
		{"select * from CLIENTES C where C.no|", []string{"nome"}, true}, // unquoted names fold
		{"select * from clientes pedidos where exists (select pedidos.| from pedidos)", []string{"id", "cliente_id", "total"}, true},
		{`select c.| from clientes "C", pedidos c`, []string{"id", "cliente_id", "total"}, true}, // "C" is not c
		{"select c.id from clientes c union all select c.| from pedidos c", []string{"id", "cliente_id", "total"}, true},
		{"select c.| from clientes c union all select c.id from pedidos c", []string{"id", "nome", "Cidade"}, true}, // in a comment
		{"select /* cl.| */ * from clientes cl", nil, true},
		{"-- a comment\nsel|", []string{"select"}, true},
		{"-- a comment|", nil, true},
		{"select /* x */ cl.| from clientes cl", []string{"id", "nome", "Cidade"}, true},
	} {
		sql, cur := at(c.sql)
		s, e := Statement(sql, cur)
		got := labels(Complete(cat, Analyze(sql[s:e], cur-s)))
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%q: %q missing from %v", c.sql, w, got)
			}
		}
		if c.only && len(got) != len(c.want) {
			t.Errorf("%q: got %v, want only %v", c.sql, got, c.want)
		}
	}
}

func TestQuotedAndOffPath(t *testing.T) {
	sql, cur := at("select c.Ci| from clientes c")
	items := Complete(cat, Analyze(sql, cur))
	if len(items) != 1 || items[0].Insert != `"Cidade"` {
		t.Fatalf("%+v", items)
	}
	sql, cur = at("select * from me|")
	items = Complete(cat, Analyze(sql, cur))
	if len(items) != 1 || items[0].Insert != "vendas.metas" {
		t.Fatalf("a table off the search path needs its schema: %+v", items)
	}
}

func TestNoCatalog(t *testing.T) {
	sql, cur := at("select * from clientes c where c.|")
	if got := Complete(nil, Analyze(sql, cur)); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	sql, cur = at("sel|")
	if got := labels(Complete(nil, Analyze(sql, cur))); !slices.Equal(got, []string{"select"}) {
		t.Fatalf("a loose file still completes SQL: %v", got)
	}
}

func TestStatement(t *testing.T) {
	text := "select 1;\nselect 'a;b' from t\nwhere x = 1\n\n-- next\nselect $$;$$ from u;"
	for _, c := range []struct {
		at   string
		want string
	}{
		{"select 1", "select 1"},
		{"'a;b'", "\nselect 'a;b' from t\nwhere x = 1"},
		{"where", "\nselect 'a;b' from t\nwhere x = 1"},
		{"from u", "\n-- next\nselect $$;$$ from u"},
	} {
		s, e := Statement(text, strings.Index(text, c.at))
		if text[s:e] != c.want {
			t.Errorf("at %q: got %q, want %q", c.at, text[s:e], c.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"select cl.no|me from clientes cl", "`nome` text · public.clientes"},
		{"select to|tal from pedidos", "`total` numeric · public.pedidos"},
		{"select * from clien|tes", "table `public.clientes`"},
		{"select c|l.nome from clientes cl", "table `public.clientes`"},
		{"select * from vendas.me|tas", "view `vendas.metas`"},
		{"select nada| from clientes", ""},
	} {
		sql, cur := at(c.sql)
		got := Describe(cat, sql, cur)
		if !strings.HasPrefix(got, c.want) || (c.want == "" && got != "") {
			t.Errorf("%q: got %q, want %q...", c.sql, got, c.want)
		}
	}
	if Describe(nil, "select 1", 3) != "" {
		t.Fatal("no catalog, nothing to say")
	}
}

func TestFoldedNames(t *testing.T) {
	c := &Catalog{Tables: []Table{
		{Schema: "public", Name: "Foo", Kind: "table", Columns: []Column{{"upper", "text"}}},
		{Schema: "public", Name: "foo", Kind: "table", Columns: []Column{{"lower", "text"}}},
	}}
	for sql, want := range map[string]string{
		"select f.| from Foo f":   "lower", // unquoted Foo is foo
		`select f.| from "Foo" f`: "upper",
	} {
		s, cur := at(sql)
		got := labels(Complete(c, Analyze(s, cur)))
		if !slices.Equal(got, []string{want}) {
			t.Errorf("%q: %v", sql, got)
		}
	}
}
