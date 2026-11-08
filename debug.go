package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// debugOut receives -debug's lines; nil is off. One line per event,
// "dbv event: OP ms=N key=value ...", so an agent greps it instead of
// reading the screen.
var debugOut io.Writer

// debugNow is the start of a timed step, or zero when -debug is off, so the
// caller pays a nil check and no clock read.
func debugNow() time.Time {
	if debugOut == nil {
		return time.Time{}
	}
	return time.Now()
}

// debugEvent writes op, its duration since start and the key=value pairs in
// kv; a nil value (an error that did not happen) is left out.
func debugEvent(op string, start time.Time, kv ...any) {
	if debugOut == nil || start.IsZero() {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "dbv event: %s ms=%.1f", op, float64(time.Since(start))/1e6)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil || kv[i+1] == "" {
			continue
		}
		fmt.Fprintf(&b, " %v=%s", kv[i], debugValue(kv[i+1]))
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(debugOut, b.String())
}

func debugValue(v any) string {
	if err, ok := v.(error); ok {
		v = err.Error()
	}
	s := fmt.Sprint(v)
	if s == "" || strings.ContainsAny(s, " \t\n\"=") {
		return strconv.Quote(s)
	}
	return s
}

// stderrIsTerminal says the interface and -debug would write on the same
// screen.
func stderrIsTerminal() bool {
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
