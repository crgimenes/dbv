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
	}
	for in, want := range cases {
		got := leadingWords(in)
		if !slices.Equal(got, want) {
			t.Errorf("leadingWords(%q) = %v, want %v", in, got, want)
		}
	}
	if txControl("select 1; update t set a = 1") != "" || txControl("update t set a=1; commit") != "COMMIT" {
		t.Fatal("txControl")
	}
}
