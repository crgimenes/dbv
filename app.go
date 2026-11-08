package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/crgimenes/filo-term-go/kit"
	"github.com/crgimenes/filo-term-go/screen"

	"github.com/crgimenes/dbv/db"
)

// view is one screen of the program. Every view draws inside the frame and
// says which keys work there; the app owns the last two rows (legend, note
// or command line).
type view interface {
	title() string
	hints() []kit.Hint
	draw(a *app, c *screen.Canvas, top, left, rows, cols int)
	key(a *app, k screen.Key)
}

type app struct {
	t     *screen.Terminal
	c     *screen.Canvas
	pg    *db.Postgres
	views []view // a stack: ESC pops back to the one before
	quit  bool

	note string
	tone kit.Tone

	// the command line, when one is open at the bottom
	prompt string
	input  kit.Input
	onLine func(a *app, line string)
	onEdit func(a *app, line string) // each keystroke, for a live filter

	// what was entered at each prompt, oldest first; yagni: kept for the
	// session only, a file in the state directory when someone misses it
	sqlText string // the last :sql that ran, to edit again

	history map[string][]string
	histAt  int // len(history[prompt]) while typing a new line
}

const historyMax = 100

const (
	minW = 40
	minH = 10
)

// statuser is a view with a line about where the cursor is, shown when
// there is no note.
type statuser interface {
	status() string
}

func (a *app) push(v view) { a.views = append(a.views, v) }

func (a *app) pop() {
	a.views = a.views[:len(a.views)-1]
	if len(a.views) == 0 {
		a.quit = true
	}
}

func (a *app) top() view { return a.views[len(a.views)-1] }

func (a *app) say(tone kit.Tone, format string, args ...any) {
	a.tone = tone
	a.note = fmt.Sprintf(format, args...)
}

func (a *app) fail(err error) {
	if errors.Is(err, context.Canceled) {
		a.say(kit.Warn, "cancelled")
		return
	}
	a.say(kit.Err, "%v", err)
}

// busy runs op off the event loop so the screen stays alive: the spinner
// turns and Esc cancels op's context. op must not touch the views; the
// caller applies what it brought once busy returns.
func (a *app) busy(what string, op func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- op(ctx) }()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	events := a.t.Events()
	shown := false // a step that ends within a tick never shows the spinner
	for {
		select {
		case err := <-done:
			if shown {
				a.note = ""
			}
			return err
		case <-tick.C:
			kit.Spin++
			if !shown {
				a.say(kit.Wait, "%s · esc cancels", what)
				shown = true
			}
		case ev, ok := <-events:
			if !ok {
				events = nil // the terminal is gone; op still has to finish
				cancel()
				continue
			}
			if ev.Key == screen.KeyEsc {
				cancel()
				a.say(kit.Wait, "cancelling %s", what)
				shown = true
			}
		}
		a.paint()
	}
}

// ask opens the command line with prompt and value; done gets the line on
// Enter, nothing on ESC.
func (a *app) ask(prompt, value string, done func(a *app, line string)) {
	a.prompt = prompt
	a.input = kit.Input{Max: 4096}
	a.input.Set(value)
	a.onLine = done
	a.onEdit = nil
	a.note = ""
	a.histAt = len(a.history[a.histKey()])
}

// histKey keeps each screen's lines apart: a table list's :rename is no
// use at a grid's ":".
func (a *app) histKey() string {
	return fmt.Sprintf("%T%s", a.top(), a.prompt)
}

func (a *app) remember(line string) {
	if a.history == nil {
		a.history = map[string][]string{}
	}
	h := a.history[a.histKey()]
	if line == "" || (len(h) > 0 && h[len(h)-1] == line) {
		return
	}
	h = append(h, line)
	if len(h) > historyMax {
		h = h[len(h)-historyMax:]
	}
	a.history[a.histKey()] = h
}

// recall walks the prompt's history; past the newest the line is empty.
func (a *app) recall(d int) {
	h := a.history[a.histKey()]
	a.histAt = max(0, min(len(h), a.histAt+d))
	line := ""
	if a.histAt < len(h) {
		line = h[a.histAt]
	}
	a.input.Set(line)
	if a.onEdit != nil {
		a.onEdit(a, line)
	}
}

func (a *app) asking() bool { return a.onLine != nil }

func (a *app) closeLine() {
	a.onLine, a.onEdit, a.prompt = nil, nil, ""
}

func (a *app) run() error {
	a.paint()
	for !a.quit {
		ev, ok := <-a.t.Events()
		if !ok {
			return nil // the terminal went away
		}
		if ev.Resize {
			a.paint()
			continue
		}
		start := debugNow()
		view := ""
		if debugOut != nil {
			view = fmt.Sprintf("%T", a.top()) // before the key: it may close the last view
		}
		a.handle(ev.Key)
		painted := debugNow()
		if !a.quit {
			a.paint()
		}
		if debugOut != nil {
			debugEvent("key", start, "key", fmt.Sprintf("%#x", ev.Key), "view", view,
				"paint_ms", fmt.Sprintf("%.1f", float64(time.Since(painted))/1e6))
		}
	}
	return nil
}

func (a *app) handle(k screen.Key) {
	if !a.asking() {
		a.note = ""
		a.top().key(a, k)
		return
	}
	switch k {
	case screen.KeyEsc:
		done := a.onEdit
		a.closeLine()
		if done != nil {
			done(a, "") // a live filter clears with its line
		}
	case screen.KeyEnter:
		done, line := a.onLine, a.input.String()
		a.remember(line)
		a.closeLine()
		done(a, line)
	case screen.KeyUp:
		a.recall(-1)
	case screen.KeyDown:
		a.recall(1)
	default:
		a.input.Key(k)
		if a.onEdit != nil {
			a.onEdit(a, a.input.String())
		}
	}
}

func (a *app) paint() {
	w, h := a.t.Size()
	a.c.Resize(w, h)
	if w < minW || h < minH {
		msg := fmt.Sprintf("dbv needs %dx%d, this is %dx%d", minW, minH, w, h)
		a.c.Print(h/2, max(0, (w-screen.Width(msg))/2), msg, screen.Pen{FG: kit.CWarn, BG: screen.Default})
		_ = a.t.Draw(a.c)
		return
	}
	if len(a.views) == 0 { // connecting, before the first screen
		kit.Frame(a.c, 0, 0, h-2, w, "dbv "+GitTag)
		kit.Note(a.c, h-1, 1, w-2, a.tone, a.note)
		_ = a.t.Draw(a.c)
		return
	}
	v := a.top()
	kit.Frame(a.c, 0, 0, h-2, w, v.title())
	v.draw(a, a.c, 1, 2, h-4, w-4)
	kit.Legend(a.c, h-2, 1, w-2, v.hints()...)
	if a.asking() {
		n := a.c.Print(h-1, 1, a.prompt, kit.Hot)
		a.input.Draw(a.c, h-1, 1+n, w-2-n, kit.Text, true)
	} else {
		kit.Note(a.c, h-1, 1, w-2, a.tone, a.note)
		st, ok := v.(statuser)
		if a.note == "" && ok {
			kit.Note(a.c, h-1, 1, w-2, kit.Plain, st.status())
		}
	}
	_ = a.t.Draw(a.c)
}

// external runs program (an editor, a pager) on text in a temporary file
// and returns what the file holds afterwards, as it is.
func (a *app) external(program, text string) (string, error) {
	f, err := os.CreateTemp("", "dbv-*.sql")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	_, err = f.WriteString(text)
	if err != nil {
		_ = f.Close()
		return "", err
	}
	err = f.Close()
	if err != nil {
		return "", err
	}

	err = a.t.Suspend()
	if err != nil {
		return "", err
	}
	args := strings.Fields(program)                         // EDITOR="code -w" is a command with flags
	cmd := exec.Command(args[0], append(args[1:], name)...) // #nosec G204 G702 -- the user's own $EDITOR/$PAGER
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	err = a.t.Resume()
	if runErr != nil {
		return "", fmt.Errorf("%s: %w", args[0], runErr)
	}
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func envOr(name, fallback string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v
}

func (a *app) edit(text string) (string, error) {
	return a.external(envOr("EDITOR", "vi"), text)
}

func (a *app) page(text string) error {
	_, err := a.external(envOr("PAGER", "less"), text)
	return err
}

// connect opens cfg with the note saying so, since an ssh tunnel or a far
// server takes long enough to look like a hang.
func (a *app) connect(cfg db.DBConfig) error {
	a.say(kit.Wait, "connecting to %s", cfg.Title)
	a.paint()
	start := debugNow()
	pg, err := db.New(cfg.URL)
	debugEvent("connect", start, "title", cfg.Title, "err", err)
	if err != nil {
		return fmt.Errorf("connect %q: %w", cfg.Title, err)
	}
	if a.pg != nil {
		a.pg.Close()
	}
	a.pg = pg
	DBTitle = cfg.Title
	userViewsDir = cfg.ViewsPath
	a.note = ""
	return nil
}

func runTUI(configs []db.DBConfig, first func(a *app) view) error {
	if len(configs) == 0 {
		return fmt.Errorf("no connection configured in %s", configPath())
	}
	t, err := screen.Open()
	if err != nil {
		return err
	}
	a := &app{t: t, c: screen.New(t.Size())}
	defer func() {
		_ = t.Close()
		if a.pg != nil {
			a.pg.Close()
		}
	}()

	// kill or a closed window would otherwise leave the shell in raw mode
	// on the alternate screen.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		s, ok := <-sig
		if !ok {
			return
		}
		_ = t.Close()
		fmt.Fprintln(os.Stderr, "dbv:", s)
		os.Exit(1)
	}()

	switch len(configs) {
	case 1:
		err = a.connect(configs[0])
		if err != nil {
			return err
		}
		a.push(first(a))
	default:
		a.push(&dbMenu{configs: configs, next: first})
	}
	return a.run()
}

// dbMenu picks the connection when the config has several.
type dbMenu struct {
	configs []db.DBConfig
	next    func(a *app) view
	menu    kit.Menu
}

func (m *dbMenu) items() []kit.Item {
	items := make([]kit.Item, len(m.configs))
	for i, c := range m.configs {
		key := "-"
		if i < 9 {
			key = fmt.Sprint(i + 1)
		}
		items[i] = kit.Item{Key: key, Label: c.Title}
	}
	return items
}

func (m *dbMenu) title() string { return "dbv " + GitTag + " · Select a database" }

func (m *dbMenu) hints() []kit.Hint {
	return []kit.Hint{{Key: "↑↓", What: "move"}, {Key: "1-9", What: "open"}, {Key: "enter", What: "open"}, {Key: "q", What: "quit"}}
}

func (m *dbMenu) draw(_ *app, c *screen.Canvas, top, left, _, cols int) {
	m.menu.Draw(c, top+1, left+2, m.items(), 1, cols-4)
}

func (m *dbMenu) key(a *app, k screen.Key) {
	if k == 'q' || k == screen.KeyEsc || k == screen.Ctrl('c') {
		a.quit = true
		return
	}
	i := m.menu.Key(k, m.items(), 1)
	if i < 0 {
		return
	}
	err := a.connect(m.configs[i])
	if err != nil {
		a.fail(err)
		return
	}
	a.push(m.next(a))
}

// clipMax is about what xterm and most emulators take in one OSC 52.
const clipMax = 74 * 1024

// copyText puts text on the clipboard of the terminal itself (OSC 52), so it
// works over ssh with no clipboard program. tmux passes it on with
// "set -g set-clipboard on".
//
// OSC 52: https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h3-Operating-System-Commands
func (a *app) copyText(what, text string) {
	if len(text) > clipMax {
		a.say(kit.Warn, "%s is %d bytes, more than a terminal takes; not copied", what, len(text))
		return
	}
	_, err := fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	if err != nil {
		a.fail(err)
		return
	}
	a.say(kit.OK, "%s copied", what)
}
