package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

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
}

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
	a.say(kit.Err, "%v", err)
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
}

func (a *app) asking() bool { return a.onLine != nil }

func (a *app) closeLine() {
	a.onLine, a.onEdit, a.prompt = nil, nil, ""
}

func (a *app) run() error {
	for !a.quit {
		a.paint()
		ev, ok := <-a.t.Events()
		if !ok {
			return nil // the terminal went away
		}
		if ev.Resize {
			continue
		}
		a.handle(ev.Key)
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
		a.closeLine()
		done(a, line)
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
	pg, err := db.New(cfg.URL)
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
