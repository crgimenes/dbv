package main

import (
	"slices"
	"strings"
	"unicode"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

// whereWords are what joins and compares column names in a :where.
var whereWords = []string{
	"AND", "OR", "NOT", "LIKE", "ILIKE", "BETWEEN", "IN", "IS", "NULL",
	"TRUE", "FALSE", "DISTINCT", "FROM", "SIMILAR", "TO", "ANY",
	"LOWER", "UPPER", "COALESCE", "NOW", "CURRENT_DATE",
}

// reserved names a bare column cannot take; the common ones, not the list.
var reserved = []string{
	"all", "and", "any", "asc", "check", "column", "default", "desc", "end",
	"false", "from", "group", "in", "is", "limit", "not", "null", "offset",
	"or", "order", "select", "table", "to", "true", "user", "where",
}

// sqlName is name as a WHERE can say it: bare when it can be, quoted when
// case, a space or a keyword would change what it means.
func sqlName(name string) string {
	plain := name != "" && !unicode.IsDigit(rune(name[0])) && !slices.Contains(reserved, name)
	for _, r := range name {
		plain = plain && (r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}
	if plain {
		return name
	}
	return db.QuoteName(name)
}

// completion is what Tab may put in a line: names as they are written
// there (quoted or not, the caller's call), then words (keywords, written
// in the case the user types them); sep follows a whole match.
type completion struct {
	names, words []string
	sep          string
}

// complete finishes the word at the end of line from c. One match is
// taken whole, with c.sep after; several are extended to what they share and
// returned with head, the line before the word, for the menu to walk.
// yagni: the word is the line's last, not the one under the caret, since
// kit.Input does not say where the caret is.
func complete(line string, c completion) (string, string, []string) {
	start := len(line)
	for start > 0 {
		r := rune(line[start-1])
		if r != '_' && r != '"' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && r < 0x80 {
			break
		}
		start--
	}
	head, word := line[:start], strings.TrimPrefix(line[start:], `"`)
	lower := strings.ToLower(word) == word

	var found []string
	for _, n := range c.names {
		if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(n, `"`)), strings.ToLower(word)) {
			found = append(found, n)
		}
	}
	for _, w := range c.words {
		if !strings.HasPrefix(strings.ToUpper(w), strings.ToUpper(word)) {
			continue
		}
		if lower {
			w = strings.ToLower(w)
		}
		found = append(found, w)
	}
	switch len(found) {
	case 0:
		return line, head, nil
	case 1:
		return head + found[0] + c.sep, head, nil
	}
	common := found[0]
	for _, f := range found[1:] {
		for !strings.HasPrefix(strings.ToLower(f), strings.ToLower(common)) {
			common = string([]rune(common)[:len([]rune(common))-1])
		}
	}
	if len(common) > len(word) {
		return head + common, head, found
	}
	return line, head, found
}

// tabMenu is the lightbar Tab opens when several match: walking it puts
// each in the line after head; at -1 is none chosen yet, typed is the line
// ESC goes back to.
type tabMenu struct {
	head, typed, sep string
	items            []string
	at               int
}

func (m *tabMenu) move(d int) string {
	if m.at < 0 && d < 0 {
		m.at = 0 // back from none chosen is the last one
	}
	n := len(m.items)
	m.at = (m.at + d + n) % n
	return m.head + m.items[m.at]
}

// draw paints the items in a row, the chosen one lit, scrolled so it shows.
func (m *tabMenu) draw(c *screen.Canvas, row, col, width int) {
	c.Fill(row, col, 1, width, ' ', kit.Text)
	cells := func(i int) int { return screen.Width(m.items[i]) + 2 }
	first, used := 0, 0
	for i := 0; i <= m.at; i++ {
		used += cells(i)
	}
	for first < m.at && used > width {
		used -= cells(first)
		first++
	}
	at := col
	for i := first; i < len(m.items) && at < col+width; i++ {
		pen := kit.Soft
		if i == m.at {
			pen = kit.Focus
		}
		at += c.Print(row, at, kit.Cut(" "+m.items[i]+" ", col+width-at), pen)
	}
}

// commandCompletion offers a screen's commands for the line's first word.
func commandCompletion(line string, commands []string) completion {
	if strings.ContainsRune(strings.TrimLeft(line, " "), ' ') {
		return completion{}
	}
	return completion{words: commands, sep: " "}
}
