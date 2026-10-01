package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ergochat/readline"
	"mvdan.cc/sh/v3/syntax"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/jaccount"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/logging"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/vfs"
)

// Banner lines, Debian login-banner style. Bump the version per phase.
const (
	versionLine  = "CanvasFS 0.3 (built 2026-10-01 UTC)"
	warrantyLine = "CanvasFS comes with ABSOLUTELY NO WARRANTY, to the extent permitted by applicable law."
)

// bannerArt is the startup ASCII art, embedded from banner.txt.
//
//go:embed banner.txt
var bannerArt string

// runTUI starts the interactive line-mode REPL: readline provides line
// editing, history and TAB path completion; mvdan/sh syntax provides command
// parsing. The session keeps an in-process cwd so relative paths work, and
// records itself to the state directory. Command implementations are shared
// with the CLI; only the rendering differs (human-readable here).
func runTUI(ctx context.Context, stderr io.Writer) error {
	stateDir, err := logging.StateDir()
	if err != nil {
		return err
	}
	// Capture before newSessionLog creates today's record file.
	last := lastLogin(stateDir)

	fsys, client, tokenSet, err := openVFS(ctx, true)
	if err != nil {
		return err
	}
	record, err := newSessionLog()
	if err != nil {
		return err
	}
	defer record.Close()

	cwd := "/"
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          promptFor(cwd),
		HistoryFile:     filepath.Join(stateDir, "history"),
		AutoComplete:    &pathCompleter{fsys: fsys, cwd: &cwd},
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return fmt.Errorf("init readline: %w", err)
	}
	defer rl.Close()

	fmt.Print(strings.TrimLeft(bannerArt, "\n"))
	if !strings.HasSuffix(bannerArt, "\n") {
		fmt.Println()
	}
	fmt.Println(versionLine)
	fmt.Println(warrantyLine)
	if !last.IsZero() {
		fmt.Printf("Last login: %s\n", last.Format("Mon Jan _2 15:04:05 2006"))
	}
	selfCheck(ctx, stderr, client, tokenSet)

	for {
		rl.SetPrompt(promptFor(cwd))
		line, err := rl.Readline()
		switch {
		case errors.Is(err, readline.ErrInterrupt):
			continue // Ctrl-C clears the current line
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return fmt.Errorf("read line: %w", err)
		}

		args, err := splitLine(line)
		if err != nil {
			reportHumanError(stderr, err)
			continue
		}
		if len(args) == 0 {
			continue
		}
		record.logCommand(line)

		switch args[0] {
		case "exit", "quit":
			return nil
		case "pwd":
			fmt.Println(cwd)
		case "cd":
			next, err := changeDir(fsys, cwd, args[1:])
			if err != nil {
				reportHumanError(stderr, err)
				continue
			}
			cwd = next
			record.logCwd(cwd)
		case "ls", "stat", "cat":
			target, err := commandTarget(cwd, args)
			if err != nil {
				reportHumanError(stderr, err)
				continue
			}
			if err := runFS(fsys, args[0], target, true, os.Stdout); err != nil {
				reportHumanError(stderr, err)
			}
		case "download":
			if len(args) < 2 || len(args) > 3 {
				reportHumanError(stderr, fail("usage", "download <vfs-path> [local-path]", exitUsage))
				continue
			}
			local := ""
			if len(args) == 3 {
				// local relative paths resolve against the process cwd, not the vfs cwd.
				local = args[2]
			}
			if err := runDownload(ctx, fsys, resolvePath(cwd, args[1]), local, true, os.Stdout, stderr); err != nil {
				reportHumanError(stderr, err)
			}
		default:
			reportHumanError(stderr, fail("usage", "unknown command "+args[0]+"; available: ls stat cat download cd pwd exit", exitUsage))
		}
	}
}

// reportHumanError renders a command failure as one plain line.
// The structured JSON contract applies to CLI mode only; the TUI is for
// humans.
func reportHumanError(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "error: %s\n", classify(err).hint)
}

// warnf prints one red warning line; warnings never block the session.
func warnf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "\033[31mwarning: "+format+"\033[0m\n", args...)
}

// selfCheck verifies both credentials at TUI startup. Success is silent;
// every failure is one red warning line, and the session continues either
// way — commands that need a valid credential fail on their own.
func selfCheck(ctx context.Context, stderr io.Writer, client *canvas.Client, tokenSet bool) {
	if !tokenSet {
		warnf(stderr, "not logged in; run sjtu auth canvas login")
	} else if err := client.Verify(ctx); err != nil {
		warnf(stderr, "%s", classify(err).hint)
	}

	store, err := cred.Open()
	if err != nil {
		warnf(stderr, "cannot check jAccount session: %v", err)
		return
	}
	cookie, err := store.Get(cred.KeyJAccount)
	if errors.Is(err, cred.ErrNotFound) {
		warnf(stderr, "jAccount not logged in; run sjtu auth jaccount login")
		return
	}
	if err != nil {
		warnf(stderr, "cannot check jAccount session: %v", err)
		return
	}
	session.RegisterSecret(cookie)
	if err := jaccount.Verify(ctx, cookie); err != nil {
		warnf(stderr, "jAccount session expired; run sjtu auth jaccount login")
	}
}

// lastLogin returns the start time of the most recent previous TUI session,
// parsed from session record file names in the state directory. It returns
// the zero time when no prior session exists.
func lastLogin(stateDir string) time.Time {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, e := range entries {
		stamp, ok := strings.CutPrefix(e.Name(), "sjtu-session-")
		if !ok {
			continue
		}
		stamp, ok = strings.CutSuffix(stamp, ".jsonl")
		if !ok {
			continue
		}
		if t, err := time.ParseInLocation("20060102-150405", stamp, time.Local); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// commandTarget resolves the path argument of ls/stat/cat against cwd.
// ls and stat default to the working directory, following shell convention;
// cat always requires an explicit path.
func commandTarget(cwd string, args []string) (string, error) {
	if len(args) > 2 {
		return "", fail("usage", args[0]+" accepts at most one path argument", exitUsage)
	}
	if len(args) == 2 {
		return resolvePath(cwd, args[1]), nil
	}
	if args[0] == "cat" {
		return "", fail("usage", "cat requires a path argument", exitUsage)
	}
	return cwd, nil
}

// promptFor renders the shell prompt with the session cwd.
func promptFor(cwd string) string {
	return "sjtu:" + cwd + "> "
}

// resolvePath joins arg onto cwd when relative and cleans the result,
// yielding an absolute display path.
func resolvePath(cwd, arg string) string {
	p := arg
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}
	return path.Clean(p)
}

// changeDir resolves the cd arguments against cwd and verifies the target is
// a directory. Bare cd returns to the root.
func changeDir(fsys *vfs.FS, cwd string, args []string) (string, error) {
	target := "/"
	if len(args) > 1 {
		return "", fail("usage", "cd accepts at most one argument", exitUsage)
	}
	if len(args) == 1 {
		target = resolvePath(cwd, args[0])
	}
	info, err := fs.Stat(fsys, fsName(target))
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fail("not_a_directory", target+" is not a directory", exitError)
	}
	return target, nil
}

// splitLine parses one command line into words using shell quoting rules.
// It is a splitter, not a shell: expansions, pipes and redirections are
// rejected so nothing is ever executed by accident.
func splitLine(line string) ([]string, error) {
	prog, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return nil, err
	}
	if len(prog.Stmts) == 0 {
		return nil, nil // whitespace-only line
	}
	if len(prog.Stmts) != 1 || len(prog.Stmts[0].Redirs) > 0 {
		return nil, errors.New("one simple command per line (no pipes or redirection)")
	}
	call, ok := prog.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		return nil, errors.New("one simple command per line (no pipes or redirection)")
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		s, err := literalWord(word)
		if err != nil {
			return nil, err
		}
		args = append(args, s)
	}
	return args, nil
}

// literalWord renders one shell word, honoring single quotes, double quotes
// and backslash escapes, but rejecting expansions.
func literalWord(word *syntax.Word) (string, error) {
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescapeLit(p.Value))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", errors.New("no $ expansion inside double quotes")
				}
				b.WriteString(unescapeLit(lit.Value))
			}
		default:
			return "", errors.New("no $ expansion or backquotes")
		}
	}
	return b.String(), nil
}

// unescapeLit resolves backslash escapes in an unquoted literal: the parser
// keeps them raw so the syntax tree can round-trip.
func unescapeLit(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteByte('\\')
	}
	return b.String()
}

// pathCompleter completes the line's final word as a vfs path, relative to
// the session cwd. Directories gain a trailing "/" so completion can
// descend.
type pathCompleter struct {
	fsys *vfs.FS
	cwd  *string
}

// Do implements readline.AutoCompleter. readline inserts candidates at the
// cursor WITHOUT deleting the typed prefix, so each candidate is only the
// suffix beyond what the user already typed; the returned offset is the
// prefix length in runes.
func (c *pathCompleter) Do(line []rune, pos int) ([][]rune, int) {
	prefix := string(line[:pos])
	start := strings.LastIndexAny(prefix, " \t") + 1
	partial := prefix[start:]

	dirPart, base := path.Split(partial)
	displayDir := resolvePath(*c.cwd, dirPart)
	entries, err := fs.ReadDir(c.fsys, fsName(displayDir))
	if err != nil {
		return nil, 0
	}
	baseRunes := []rune(base)
	var candidates [][]rune
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), base) {
			continue
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		candidates = append(candidates, []rune(name)[len(baseRunes):])
	}
	return candidates, len(baseRunes)
}

// sessionEvent is one line in a TUI session record file.
type sessionEvent struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // start | command | cwd | end
	Line string    `json:"line,omitempty"`
	Cwd  string    `json:"cwd,omitempty"`
}

// sessionLog records one TUI session as JSON lines in the state directory.
type sessionLog struct {
	f *os.File
}

// newSessionLog opens today's session record file, named
// sjtu-session-<yyyymmdd-hhmmss>.jsonl, and writes the start event.
func newSessionLog() (*sessionLog, error) {
	dir, err := logging.StateDir()
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("sjtu-session-%s.jsonl", time.Now().Format("20060102-150405"))
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session log: %w", err)
	}
	log := &sessionLog{f: f}
	log.write(sessionEvent{Kind: "start"})
	return log, nil
}

// logCommand records one executed command line.
func (l *sessionLog) logCommand(line string) {
	l.write(sessionEvent{Kind: "command", Line: line})
}

// logCwd records a working-directory change.
func (l *sessionLog) logCwd(cwd string) {
	l.write(sessionEvent{Kind: "cwd", Cwd: cwd})
}

// write appends one event; failures are tolerated because the record is an
// audit trail, not a gate.
func (l *sessionLog) write(ev sessionEvent) {
	ev.Time = time.Now()
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(l.f, "%s\n", data)
}

// Close writes the end event and closes the record file.
func (l *sessionLog) Close() error {
	l.write(sessionEvent{Kind: "end"})
	return l.f.Close()
}
