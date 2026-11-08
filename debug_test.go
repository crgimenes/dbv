package main

import (
	"bytes"
	"errors"
	"regexp"
	"testing"
)

func TestDebugEvent(t *testing.T) {
	var out bytes.Buffer
	debugOut = nil
	debugEvent("load", debugNow(), "table", "users")
	if !debugNow().IsZero() {
		t.Fatal("off must not read the clock")
	}

	debugOut = &out
	defer func() { debugOut = nil }()
	var none error
	debugEvent("load", debugNow(), "table", "Order Items", "rows", 3, "where", "", "err", none, "e2", errors.New("boom"))
	line := out.String()
	re := regexp.MustCompile(`^dbv event: load ms=\d+\.\d table="Order Items" rows=3 e2=boom\n$`)
	if !re.MatchString(line) {
		t.Fatalf("%q", line)
	}
}
