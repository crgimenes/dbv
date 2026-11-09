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
	"strconv"
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
	onTab  func(line string) completion
	menu   *tabMenu          // open while Tab walks several matches
	onPick func(item string) // the menu's choice as it moves; "" is ESC

	// what was entered at each prompt, oldest first; yagni: kept for the
	// session only, a file in the state directory when someone misses it
	sqlText string // the last :sql that ran, to edit again

	history   map[string][]string
	histFile  string // "" keeps the history in memory only (tests)
	histAt    int    // len(history[prompt]) while typing a new line
	histDraft string // the line being typed, while ↑ shows older ones
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
	a.onEdit, a.onTab, a.onPick, a.menu = nil, nil, nil, nil
	a.note = ""
	a.histAt = len(a.history[a.histKey()])
}

// histKey keeps each screen's lines apart: a table list's :rename is no
// use at a grid's ":".
func (a *app) histKey() string {
	return fmt.Sprintf("%T%s", a.top(), a.prompt)
}

func (a *app) remember(line string) { a.rememberAt(a.histKey(), line) }

// rememberAt keeps line for this session and, with a history file, the
// next ones; a file that cannot be written costs the next run its lines,
// not this one its work.
func (a *app) rememberAt(key, line string) {
	if a.history == nil {
		a.history = map[string][]string{}
	}
	addLine(a.history, key, line)
	if a.histFile == "" || line == "" {
		return
	}
	err := saveLine(a.histFile, key, line)
	if err != nil {
		debugEvent("history", debugNow(), "err", err)
	}
}

// last is the newest line kept at key, "" when there is none.
func (a *app) last(key string) string {
	h := a.history[key]
	if len(h) == 0 {
		return ""
	}
	return h[len(h)-1]
}

// recall walks the prompt's history the way a shell does: nothing to walk
// leaves the line alone, and past the newest comes back what was typed.
func (a *app) recall(d int) {
	h := a.history[a.histKey()]
	at := max(0, min(len(h), a.histAt+d))
	if at == a.histAt {
		return
	}
	if a.histAt == len(h) {
		a.histDraft = a.input.String()
	}
	a.histAt = at
	line := a.histDraft
	if a.histAt < len(h) {
		line = h[a.histAt]
	}
	a.input.Set(line)
	if a.onEdit != nil {
		a.onEdit(a, line)
	}
}

func (a *app) tab() {
	if a.onTab == nil {
		return
	}
	line := a.input.String()
	c := a.onTab(line)
	line, head, found := complete(line, c)
	a.setLine(line)
	if len(found) > 1 {
		a.menu = &tabMenu{head: head, typed: line, sep: c.sep, items: found, at: -1}
	}
}

// menuKey is a key while the Tab menu is open; false hands it on to the
// line, the menu closed and the chosen word kept.
func (a *app) menuKey(k screen.Key) bool {
	m := a.menu
	pick := func(item string) {
		if a.onPick != nil {
			a.onPick(item)
		}
	}
	switch k {
	case screen.KeyTab, screen.KeyRight:
		a.setLine(m.move(1))
		pick(m.items[m.at])
		return true
	case screen.KeyBackTab, screen.KeyLeft:
		a.setLine(m.move(-1))
		pick(m.items[m.at])
		return true
	case screen.KeyEsc:
		a.setLine(m.typed)
		a.menu = nil
		pick("")
		return true
	case screen.KeyEnter:
		a.menu = nil
		if m.at < 0 {
			return false // nothing chosen: Enter runs the line
		}
		a.setLine(m.head + m.items[m.at] + m.sep)
		return true
	}
	a.menu = nil
	return false
}

// setLine replaces the line as typing would, a live filter following it.
func (a *app) setLine(line string) {
	a.input.Set(line)
	if a.onEdit != nil {
		a.onEdit(a, line)
	}
}

func (a *app) asking() bool { return a.onLine != nil }

func (a *app) closeLine() {
	a.onLine, a.onEdit, a.onTab, a.onPick, a.menu, a.prompt = nil, nil, nil, nil, nil, ""
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
	if a.menu != nil && a.menuKey(k) {
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
	case screen.KeyTab:
		a.tab()
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
	if a.menu != nil {
		a.menu.draw(a.c, h-2, 1, w-2)
	}
	if a.asking() && a.menu == nil && a.note != "" {
		a.c.Fill(h-2, 1, 1, w-2, ' ', kit.Text)
		kit.Note(a.c, h-2, 1, w-2, a.tone, a.note) // the line's own row is taken
	}
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

// absList joins paths, made absolute, as a PATH-like list.
func absList(paths []string) string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err == nil {
			p = abs
		}
		out = append(out, p)
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// external runs program (an editor, a pager) on text in a temporary file
// and returns what the file holds afterwards, as it is.
func (a *app) external(program, text string) (string, error) {
	out, _, err := a.externalSaved(program, text)
	return out, err
}

// externalSaved is external, saying also whether the file was written:
// :wq on text left as it was is a save, :q! is not, and only the file's
// time tells them apart.
func (a *app) externalSaved(program, text string) (string, bool, error) {
	f, err := os.CreateTemp("", "dbv-*.sql")
	if err != nil {
		return "", false, err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	_, err = f.WriteString(text)
	if err != nil {
		_ = f.Close()
		return "", false, err
	}
	err = f.Close()
	if err != nil {
		return "", false, err
	}
	before, err := os.Stat(name)
	if err != nil {
		return "", false, err
	}

	err = a.t.Suspend()
	if err != nil {
		return "", false, err
	}
	args := strings.Fields(program)                         // EDITOR="code -w" is a command with flags
	cmd := exec.Command(args[0], append(args[1:], name)...) // #nosec G204 G702 -- the user's own $EDITOR/$PAGER
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if a.pg != nil {
		cmd.Env = append(os.Environ(), "DBV_DB="+strconv.Itoa(dbNumber), "DBV_CONFIG="+absList(configFiles))
	}
	runErr := cmd.Run()
	err = a.t.Resume()
	if runErr != nil {
		return "", false, fmt.Errorf("%s: %w", args[0], runErr)
	}
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		return "", false, err
	}
	after, err := os.Stat(name)
	saved := err == nil && (!os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size())
	return string(b), saved, nil
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
// connect opens cfg, the n-th connection (from 1) of the configs read; the
// number, not the title, tells the editor's dbv lsp which one it is.
func (a *app) connect(cfg db.DBConfig, n int) error {
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
	DBTitle, dbNumber = cfg.Title, n
	userViewsDir = viewsDirFor(cfg)
	a.note = ""
	return nil
}

func runTUI(configs []db.DBConfig, first func(a *app) view) error {
	if len(configs) == 0 {
		return fmt.Errorf("no connection configured in %s", configPath())
	}
	return withApp(func(a *app) error {
		if len(configs) > 1 {
			a.push(&dbMenu{configs: configs, next: first})
			return nil
		}
		err := a.connect(configs[0], 1)
		if err != nil {
			return err
		}
		a.push(first(a))
		return nil
	})
}

// withApp opens the terminal, lets start push the first screen, and runs
// the screens until the last one closes.
func withApp(start func(a *app) error) error {
	t, err := screen.Open()
	if err != nil {
		return err
	}
	a := &app{t: t, c: screen.New(t.Size())}
	a.histFile, err = historyFile()
	if err == nil {
		a.history, err = readHistory(a.histFile)
	}
	if err != nil {
		debugEvent("history", debugNow(), "err", err) // the lines typed last time are lost, nothing else
	}
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

	err = start(a)
	if err != nil {
		return err
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
	err := a.connect(m.configs[i], i+1)
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
