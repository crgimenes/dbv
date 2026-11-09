// Package sqlcomplete finds what fits at a cursor in SQL being typed: the
// statement around it, the tables it names and their aliases, and whether
// a table, a column of one of them or any word goes there. It reads text,
// never a database; the catalog is handed in.
package sqlcomplete

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type kind int

const (
	tWord   kind = iota // a bare identifier or keyword
	tQuoted             // a "quoted identifier"
	tString             // a 'literal', $tag$body$tag$, a number
	tPunct              // one character: . , ( ) ; and the rest
)

type token struct {
	kind     kind
	text     string // a quoted identifier's name, unquoted
	pos, end int    // byte offsets in the text read
}

// tokens reads SQL leniently: an unclosed quote or comment runs to the end,
// since the text is being typed. Comments are no tokens; their spans come
// apart, for a cursor in one to get nothing.
func tokens(s string) ([]token, [][2]int) { return lex(s, reading{vtab: true}) }

// reading is how to take what the server's settings and version decide:
// with backslashes a plain ” string reads \' as an escaped quote
// (standard_conforming_strings=off); with vtab a vertical tab counts as
// horizontal space where a string is continued on the next line.
type reading struct {
	backslashes, vtab bool
}

// lex is tokens read as r says.
func lex(s string, r reading) ([]token, [][2]int) {
	var out []token
	var comments [][2]int
	for i := 0; i < len(s); {
		c := s[i]
		start := i
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case strings.HasPrefix(s[i:], "--"):
			i = lineEnd(s, i)                                 // at \n or \r, as PostgreSQL ends one
			comments = append(comments, [2]int{start, i + 1}) // to the line's end, not the next line
		case strings.HasPrefix(s[i:], "/*"):
			i = skipBlock(s, i)
			comments = append(comments, [2]int{start, i})
		case c == '\'' && r.backslashes:
			i = skipEscapedOn(s, i, r)
			out = append(out, token{tString, s[start:i], start, i})
		case c == '\'':
			i = skipQuoted(s, i, '\'')
			out = append(out, token{tString, s[start:i], start, i})
		case c == '"':
			i = skipQuoted(s, i, '"')
			name := strings.TrimSuffix(strings.TrimPrefix(s[start:i], `"`), `"`)
			out = append(out, token{tQuoted, strings.ReplaceAll(name, `""`, `"`), start, i})
		case c == '$' && dollarTag(s[i:]) != "":
			tag := dollarTag(s[i:])
			i = skipTo(s, i+len(tag), tag)
			out = append(out, token{tString, s[start:i], start, i})
		case (c == 'E' || c == 'e') && i+1 < len(s) && s[i+1] == '\'':
			i = skipEscapedOn(s, i+1, r) // E'it\'s': a backslash escapes the quote
			out = append(out, token{tString, s[start:i], start, i})
		case isWordStart(s, i):
			i = wordEnd(s, i)
			out = append(out, token{tWord, s[start:i], start, i})
		case c >= '0' && c <= '9':
			for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
				i++
			}
			out = append(out, token{tString, s[start:i], start, i})
		default:
			i++
			out = append(out, token{tPunct, s[start:i], start, i})
		}
	}
	return out, comments
}

// identStart and identPart are PostgreSQL's identifier characters: any
// byte past ASCII counts, as the server's lexer takes it.
func identStart(r rune) bool { return r == '_' || r >= 0x80 || unicode.IsLetter(r) }

func identPart(r rune) bool { return identStart(r) || r == '$' || (r >= '0' && r <= '9') }

func isWordStart(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	return identStart(r)
}

func wordEnd(s string, i int) int {
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !identPart(r) {
			break
		}
		i += n
	}
	return i
}

// lineEnd is the \n or \r ending the line at i, or the end of s.
func lineEnd(s string, i int) int {
	k := strings.IndexAny(s[i:], "\n\r")
	if k < 0 {
		return len(s)
	}
	return i + k
}

// skipEscapedOn is an escaped string and the parts it is continued with
// on the following lines, all one string: E'a' -- note<newline>'b\'c'.
func skipEscapedOn(s string, i int, r reading) int {
	i = skipEscaped(s, i)
	for {
		next, ok := continued(s, i, r)
		if !ok {
			return i
		}
		i = skipEscaped(s, next)
	}
}

// continued is where a string literal goes on after the one ending at i,
// by scan.l's quotecontinue: horizontal space and -- comments, a newline
// (LF or CR), then any space and -- comments ending in a newline, then a
// quote.
func continued(s string, i int, r reading) (int, bool) {
	horiz := " \t\f"
	if r.vtab {
		horiz += "\v"
	}
	j := i
	for j < len(s) {
		if strings.IndexByte(horiz, s[j]) >= 0 {
			j++
			continue
		}
		if strings.HasPrefix(s[j:], "--") {
			j = lineEnd(s, j)
			continue
		}
		break
	}
	if j >= len(s) || (s[j] != '\n' && s[j] != '\r') {
		return i, false
	}
	for j < len(s) {
		if strings.IndexByte(" \t\n\r\f\v", s[j]) >= 0 {
			j++
			continue
		}
		if strings.HasPrefix(s[j:], "--") && lineEnd(s, j) < len(s) {
			j = lineEnd(s, j)
			continue
		}
		break
	}
	return j, j < len(s) && s[j] == '\''
}

func skipQuoted(s string, i int, q byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] != q {
			continue
		}
		if j+1 < len(s) && s[j+1] == q {
			j++
			continue
		}
		return j + 1
	}
	return len(s)
}

// skipEscaped is skipQuoted for an E” string, where \' does not close it.
func skipEscaped(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch {
		case s[j] == '\\':
			j++
		case s[j] == '\'' && j+1 < len(s) && s[j+1] == '\'':
			j++
		case s[j] == '\'':
			return j + 1
		}
	}
	return len(s)
}

// skipBlock is the /* comment */ at i, nested ones inside, as PostgreSQL
// reads them.
func skipBlock(s string, i int) int {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}

// FirstWords is the first word of each statement, upper case, statements
// cut at ';' alone: what a guard against COMMIT inside a script reads, so
// it errs toward finding one. The text is read every way the server's
// settings could make it read (see reading), and a statement's first word
// is its first word token whatever punctuation comes before it.
func FirstWords(text string) []string {
	var out []string
	for _, r := range []reading{{false, true}, {true, true}, {false, false}, {true, false}} {
		want := true
		toks, _ := lex(text, r)
		for k, t := range toks {
			switch {
			case t.kind == tPunct && t.text == ";":
				want = true
			case want && t.kind == tWord:
				w := strings.ToUpper(t.text)
				if w == "PREPARE" && k+2 < len(toks) && isKeyword(toks[k+1], "TRANSACTION") && toks[k+2].kind == tString {
					w = "PREPARE TRANSACTION" // PREPARE TRANSACTION 'id' ends the transaction; PREPARE transaction AS is a name
				}
				out = append(out, w)
				want = false
			}
		}
	}
	return out
}

func skipTo(s string, i int, end string) int {
	k := strings.Index(s[i:], end)
	if k < 0 {
		return len(s)
	}
	return i + k + len(end)
}

// dollarTag is the $tag$ opening a dollar-quoted body at s; a tag is an
// identifier without '$', non-ASCII letters included ($é$).
func dollarTag(s string) string {
	for j := 1; j < len(s); {
		r, n := utf8.DecodeRuneInString(s[j:])
		if r == '$' {
			return s[:j+1]
		}
		digit := j > 1 && r >= '0' && r <= '9'
		if !identStart(r) && !digit {
			return ""
		}
		j += n
	}
	return ""
}

// Statement is the span [start, end) of the statement at offset in text.
// Statements end at a ';' or a blank line outside quotes and comments, as
// a scratch file of queries is written: one after another, often with no
// semicolon.
func Statement(text string, offset int) (start, end int) {
	offset = max(0, min(offset, len(text)))
	cuts := []int{}
	toks, _ := tokens(text)
	for _, t := range toks {
		if t.kind == tPunct && t.text == ";" {
			cuts = append(cuts, t.pos)
		}
	}
	// a blank line between two tokens cuts too
	for k := 1; k < len(toks); k++ {
		gap := text[toks[k-1].end:toks[k].pos]
		if blankLine(gap) {
			cuts = append(cuts, toks[k-1].end)
		}
	}
	slices.Sort(cuts)
	start, end = 0, len(text)
	for _, c := range cuts {
		if c < offset {
			start = c + 1
			continue
		}
		end = c
		break
	}
	if start > end {
		start = end
	}
	return start, end
}

// blankLine is a gap holding a line with nothing on it; a comment in the
// gap counts as nothing, and a blank line inside a /* comment */ cuts
// nothing.
func blankLine(gap string) bool {
	var b strings.Builder
	for i := 0; i < len(gap); {
		switch {
		case strings.HasPrefix(gap[i:], "--"):
			end := lineEnd(gap, i)
			b.WriteString(gap[i:end])
			i = end
		case strings.HasPrefix(gap[i:], "/*"):
			i = skipBlock(gap, i)
			b.WriteByte(' ')
		default:
			b.WriteByte(gap[i])
			i++
		}
	}
	lines := strings.Split(b.String(), "\n")
	if len(lines) < 3 {
		return false // no line both starts and ends in the gap
	}
	for _, l := range lines[1 : len(lines)-1] {
		if strings.TrimSpace(l) == "" {
			return true
		}
	}
	return false
}

// Want is what fits at the cursor.
type Want int

const (
	Words   Want = iota // a keyword, or a column of the statement's tables
	Tables              // a table or a view
	Columns             // a column of the table Qualifier names
)

// Ref is a table the statement reads or writes, with its alias ("" when
// it has none).
type Ref struct {
	Schema, Name, Alias string // unquoted names folded to lower case, as PostgreSQL does
	lo, hi              int    // the parentheses it is named in: where it can be seen
}

// Context is what the cursor sits in.
type Context struct {
	Want      Want
	Qualifier string // the name before the dot: an alias, a table or, for Tables, a schema
	Schema    string // for Columns, a schema before the qualifier (s.t.col)
	Prefix    string // what is typed of the word so far
	From      int    // where that word starts, its opening quote included
	Refs      []Ref
}

// tableKeywords are what a table name follows.
var tableKeywords = []string{"FROM", "JOIN", "UPDATE", "INTO", "TABLE"}

// Analyze reads stmt (one statement) with the cursor at offset cur.
func Analyze(stmt string, cur int) Context {
	cur = max(0, min(cur, len(stmt)))
	toks, comments := tokens(stmt)
	for _, c := range comments {
		if c[0] < cur && cur < c[1] {
			return Context{Want: -1} // in a comment
		}
	}
	ctx := Context{Refs: visible(refs(toks, len(stmt)), cur), From: cur}

	p := len(toks) // the token the cursor is in or before
	for i, t := range toks {
		if t.kind == tString && t.pos < cur && cur < t.end {
			return Context{Want: -1} // in a literal: nothing to offer
		}
		if t.pos >= cur {
			p = i
			break
		}
		if (t.kind == tWord || t.kind == tQuoted) && cur <= t.end {
			ctx.Prefix, ctx.From = stmt[t.pos:cur], t.pos
			if t.kind == tQuoted {
				ctx.Prefix = strings.TrimPrefix(ctx.Prefix, `"`)
			}
			p = i
			break
		}
	}

	before := p // first token of the dotted name the cursor ends
	if isDot(toks, before-1) && isName(toks, before-2) {
		ctx.Qualifier = fold(toks[before-2])
		before -= 2
		if isDot(toks, before-1) && isName(toks, before-2) {
			ctx.Schema = fold(toks[before-2])
			before -= 2
		}
	}

	tables := afterTableKeyword(toks, before)
	switch {
	case tables:
		ctx.Want = Tables // with a qualifier, FROM public.| : a schema's tables
	case ctx.Qualifier != "":
		ctx.Want = Columns
	default:
		ctx.Want = Words
	}
	return ctx
}

func isDot(toks []token, i int) bool {
	return i >= 0 && i < len(toks) && toks[i].kind == tPunct && toks[i].text == "."
}

func isName(toks []token, i int) bool {
	return i >= 0 && i < len(toks) && (toks[i].kind == tWord || toks[i].kind == tQuoted)
}

func isKeyword(t token, words ...string) bool {
	return t.kind == tWord && slices.Contains(words, strings.ToUpper(t.text))
}

// afterTableKeyword says whether the token before i opens a table name:
// FROM, JOIN and the like, or a comma in a FROM list.
func afterTableKeyword(toks []token, i int) bool {
	if i == 0 {
		return false
	}
	prev := toks[i-1]
	if isKeyword(prev, tableKeywords...) {
		return true
	}
	if prev.kind != tPunct || prev.text != "," {
		return false
	}
	depth := 0
	for k := i - 2; k >= 0; k-- {
		t := toks[k]
		switch {
		case t.kind == tPunct && t.text == ")":
			depth++
		case t.kind == tPunct && t.text == "(":
			if depth == 0 {
				return false
			}
			depth--
		case depth == 0 && isKeyword(t, "FROM"):
			return true
		case depth == 0 && t.kind == tWord && keywordSet[strings.ToUpper(t.text)]:
			return false
		}
	}
	return false
}

// refs are the tables named after FROM, JOIN, UPDATE and INTO, and after
// each comma of a FROM list, anywhere in the statement: the cursor may be
// in the SELECT list, before the FROM that names them.
func refs(toks []token, n int) []Ref {
	// closes[i] is where the parenthesis opened at token i closes
	closes := map[int]int{}
	var open []int
	for i, t := range toks {
		switch {
		case t.kind == tPunct && t.text == "(":
			open = append(open, i)
		case t.kind == tPunct && t.text == ")" && len(open) > 0:
			closes[open[len(open)-1]] = t.pos
			open = open[:len(open)-1]
		}
	}
	type scope struct {
		lo, hi int
		inFrom bool
		refs   []int // this side's refs, by index in out: a UNION ends a side
	}
	scopes := []scope{{0, n, false, nil}}
	var out []Ref
	add := func(i int) int {
		r, next := readRef(toks, i+1)
		if r.Name == "" {
			return i
		}
		top := &scopes[len(scopes)-1]
		r.lo, r.hi = top.lo, top.hi
		top.refs = append(top.refs, len(out))
		out = append(out, r)
		return next - 1
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		top := &scopes[len(scopes)-1]
		switch {
		case t.kind == tPunct && t.text == "(":
			hi, ok := closes[i]
			if !ok {
				hi = n // still being typed
			}
			scopes = append(scopes, scope{t.pos, hi, false, nil})
		case t.kind == tPunct && t.text == ")" && len(scopes) > 1:
			scopes = scopes[:len(scopes)-1]
		case isKeyword(t, "UNION", "INTERSECT", "EXCEPT"):
			for _, k := range top.refs { // each side of a UNION sees its own tables
				out[k].hi = t.pos
			}
			top.lo, top.refs, top.inFrom = t.end, nil, false
		case isKeyword(t, tableKeywords...):
			top.inFrom = isKeyword(t, "FROM", "JOIN")
			i = add(i)
		case top.inFrom && t.kind == tPunct && t.text == ",":
			i = add(i)
		case t.kind == tWord && keywordSet[strings.ToUpper(t.text)]:
			top.inFrom = false
		}
	}
	return out
}

// visible are the refs a cursor at cur can see, the innermost first: a
// subquery's own tables, then those of the queries around it.
func visible(rs []Ref, cur int) []Ref {
	var out []Ref
	for _, r := range rs {
		if r.lo <= cur && cur <= r.hi {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Ref) int { return (a.hi - a.lo) - (b.hi - b.lo) })
	return out
}

// fold is a name as PostgreSQL keeps it: quoted as written, else lower case.
func fold(t token) string {
	if t.kind == tQuoted {
		return t.text
	}
	return strings.ToLower(t.text)
}

// readRef reads [schema.]name [[AS] alias] at i; next is the token after.
func readRef(toks []token, i int) (Ref, int) {
	if !isName(toks, i) || (toks[i].kind == tWord && keywordSet[strings.ToUpper(toks[i].text)]) {
		return Ref{}, i
	}
	r := Ref{Name: fold(toks[i])}
	i++
	if isDot(toks, i) && isName(toks, i+1) {
		r.Schema, r.Name = r.Name, fold(toks[i+1])
		i += 2
	}
	if i < len(toks) && isKeyword(toks[i], "AS") {
		i++
	}
	if isName(toks, i) && (toks[i].kind == tQuoted || !keywordSet[strings.ToUpper(toks[i].text)]) {
		r.Alias = fold(toks[i])
		i++
	}
	return r, i
}
