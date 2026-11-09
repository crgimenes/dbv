package db

import "github.com/crgimenes/dbv/sqlcomplete"

// leadingWords is the first word of each statement in sqlText, upper case,
// read by the same lexer as completion: quotes, E” escapes, comments
// (nested too) and dollar-quoted bodies do not cut a statement.
func leadingWords(sqlText string) []string { return sqlcomplete.FirstWords(sqlText) }

// txControl names the first statement in sqlText that would end or open a
// transaction of its own, "" when there is none.
func txControl(sqlText string) string {
	for _, w := range leadingWords(sqlText) {
		switch w {
		case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT", "PREPARE TRANSACTION":
			return w
		}
	}
	return ""
}
