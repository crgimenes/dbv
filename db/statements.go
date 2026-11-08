package db

import (
	"strings"
	"unicode"
)

// leadingWords is the first word of each statement in sqlText, upper case.
// Statements end at semicolons outside quotes, quoted identifiers,
// comments and dollar-quoted bodies, so a ';' inside a function body or a
// string does not cut a statement.
func leadingWords(sqlText string) []string {
	var words []string
	var word strings.Builder
	inWord, wantWord := false, true
	endWord := func() {
		if inWord {
			words = append(words, strings.ToUpper(word.String()))
			word.Reset()
			inWord, wantWord = false, false
		}
	}
	s := sqlText
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			endWord()
			wantWord = false
			i = skipQuoted(s, i, c)
		case strings.HasPrefix(s[i:], "--"):
			endWord()
			i = skipTo(s, i, "\n")
		case strings.HasPrefix(s[i:], "/*"):
			endWord()
			i = skipTo(s, i+2, "*/")
		case c == '$':
			endWord()
			tag, ok := dollarTag(s[i:])
			if !ok {
				i++
				continue
			}
			wantWord = false
			i = skipTo(s, i+len(tag), tag)
		case c == ';':
			endWord()
			wantWord = true
			i++
		case wantWord && (unicode.IsLetter(rune(c)) || c == '_'):
			inWord = true
			word.WriteByte(c)
			i++
		default:
			endWord()
			if !unicode.IsSpace(rune(c)) {
				wantWord = false
			}
			i++
		}
	}
	endWord()
	return words
}

// skipQuoted returns the index after the quote that closes the one at i; a
// doubled quote is part of the text.
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

func skipTo(s string, i int, end string) int {
	k := strings.Index(s[i:], end)
	if k < 0 {
		return len(s)
	}
	return i + k + len(end)
}

// dollarTag is the $tag$ (or $$) that opens a dollar-quoted body at s.
func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		c := s[j]
		if c == '$' {
			return s[:j+1], true
		}
		tagChar := unicode.IsLetter(rune(c)) || c == '_' || (j > 1 && unicode.IsDigit(rune(c)))
		if !tagChar {
			return "", false
		}
	}
	return "", false
}

// txControl names the first statement in sqlText that would end or open a
// transaction of its own, "" when there is none.
func txControl(sqlText string) string {
	for _, w := range leadingWords(sqlText) {
		switch w {
		case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT":
			return w
		}
	}
	return ""
}
