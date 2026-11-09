package db

import (
	"slices"
	"testing"
)

func TestLeadingWords(t *testing.T) {
	cases := map[string][]string{
		"select 1; update t set a = 1":                       {"SELECT", "UPDATE"},
		"insert into t values ('a; commit; b'); select 2":    {"INSERT", "SELECT"},
		`select "x;commit" from t`:                           {"SELECT"},
		"-- commit;\nselect 1":                               {"SELECT"},
		"/* begin; */ delete from t":                         {"DELETE"},
		"do $$ begin perform 1; end $$; commit":              {"DO", "COMMIT"},
		"create function f() returns int as $f$ begin; $f$;": {"CREATE"},
		"  begin;update t set a=1;COMMIT":                    {"BEGIN", "UPDATE", "COMMIT"},
		"select 'it''s; fine'; end":                          {"SELECT", "END"},
		"select case when true then 1 end; rollback":         {"SELECT", "ROLLBACK"},
		"select E'it\\'s'; commit":                           {"SELECT", "COMMIT"}, // the escape must not hide the COMMIT
		"select e'a\\\\'; commit":                            {"SELECT", "COMMIT"},
		"/* a /* b */ ; commit; */ select 1":                 {"SELECT"},
	}
	for in, want := range cases {
		got := leadingWords(in)
		got = got[:len(got)/4] // the first reading; the others assume other server settings
		if !slices.Equal(got, want) {
			t.Errorf("leadingWords(%q) = %v, want %v", in, got, want)
		}
	}
	if txControl("select 1; update t set a = 1") != "" || txControl("update t set a=1; commit") != "COMMIT" {
		t.Fatal("txControl")
	}
	if txControl("prepare transaction as select 1; execute transaction") != "" {
		t.Fatal("a statement named transaction is no transaction control")
	}
	for _, hidden := range []string{
		"SELECT 1;\fCOMMIT;",            // form feed
		"SELECT 1;\vCOMMIT;",            // vertical tab
		"SELECT $é$'$é$; COMMIT;",       // a non-ASCII dollar tag
		"SELECT E'a'\n'b\\'c'; COMMIT;", // an E string continued on the next line
		"SELECT 'a\\'b'; COMMIT;",       // standard_conforming_strings=off
		"SELECT E'it\\'s'; COMMIT;",
		"update t set a = 1; (commit)", // punctuation before the word
		"SELECT 1; -- x\rCOMMIT;",      // a comment ends at CR too
		"update t set a = 1; prepare transaction 'x'; select 1/0",
		"SELECT E'a' -- gap\n'b\\'c', '\\'; COMMIT; --'", // continued across a comment
		"SELECT E'a'\r'b\\'c', '\\'; COMMIT; --'",        // and across a CR
	} {
		if txControl(hidden) == "" {
			t.Errorf("missed the COMMIT in %q", hidden)
		}
	}
}
