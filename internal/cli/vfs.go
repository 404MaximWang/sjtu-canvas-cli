package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/config"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/mlearning"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/vfs"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// newFsCmd builds one of the three filesystem verbs; they differ only in the
// verb passed to runFS.
func newFsCmd(stdout io.Writer, verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <path>",
		Short: short,
		Args:  absolutePathArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			fsys, _, _, err := openVFS(cmd.Context(), false)
			if err != nil {
				return err
			}
			return runFS(fsys, verb, args[0], false, stdout)
		},
	}
}

// newLsCmd builds `sjtu ls <path>`.
func newLsCmd(stdout io.Writer) *cobra.Command {
	return newFsCmd(stdout, "ls", "List a directory in the Canvas file system")
}

// newStatCmd builds `sjtu stat <path>`.
func newStatCmd(stdout io.Writer) *cobra.Command {
	return newFsCmd(stdout, "stat", "Describe one node in the Canvas file system")
}

// newCatCmd builds `sjtu cat <path>`.
func newCatCmd(stdout io.Writer) *cobra.Command {
	return newFsCmd(stdout, "cat", "Stream a file's content to stdout")
}

// absolutePathArg validates the single positional path argument: CLI mode
// has no working directory, so the path must be absolute.
func absolutePathArg(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fail("usage", "exactly one absolute path argument required", exitUsage)
	}
	if !strings.HasPrefix(args[0], "/") {
		return fail("usage", "path must start with / (CLI mode accepts absolute paths only)", exitUsage)
	}
	return nil
}

// openVFS assembles the Canvas-backed file system for one command run:
// config → credential store → authenticated session → canvas client →
// metadata cache → FS. The client rides along for the TUI's startup
// self-check; tokenSet reports whether a Canvas credential exists at all.
//
// With lenient=false a missing credential is a hard error (CLI behavior:
// auth_required). With lenient=true the tree is built over an empty token
// (TUI behavior: the startup warning explains, and every fetch then fails
// with 401 until the user logs in).
func openVFS(ctx context.Context, lenient bool) (*vfs.FS, *canvas.Client, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, false, fmt.Errorf("load config: %w", err)
	}
	store, err := cred.Open()
	if err != nil {
		return nil, nil, false, fmt.Errorf("open credential store: %w", err)
	}
	token, err := store.Get(cred.KeyCanvas)
	if err != nil {
		if !lenient || !errors.Is(err, cred.ErrNotFound) {
			return nil, nil, false, err // cred.ErrNotFound classifies as auth_required
		}
		token = ""
	}
	tokenSet := token != ""
	// Register before any use so the token can never reach a log line.
	session.RegisterSecret(token)
	client := canvas.New(session.NewToken(token), cfg.CanvasBaseURL)
	src, err := vfs.NewCachedSource(vfs.CanvasSource(client))
	if err != nil {
		return nil, nil, false, err
	}
	sess := openJAccountSession(store)
	return vfs.New(ctx, src, openVideoSource(sess), openAttendanceSource(sess)), client, tokenSet, nil
}

// openJAccountSession builds one cookie session carrying the stored
// JAAuthCookie, shared by the jAccount-backed sources (video, attendance).
// It returns nil when no usable credential exists.
func openJAccountSession(store cred.Store) *session.Session {
	cookie, err := getCredential(store, cred.KeyJAccount)
	if err != nil {
		return nil
	}
	sess, err := session.NewCookies()
	if err != nil {
		return nil
	}
	sess.SeedCookie("JAAuthCookie", cookie, "jaccount.sjtu.edu.cn", "my.sjtu.edu.cn")
	return sess
}

// openVideoSource builds the video source over the jAccount session. A nil
// session keeps the source present but answering every call with a
// structured auth error, so replay/ and live/ explain themselves instead
// of vanishing from the tree.
func openVideoSource(sess *session.Session) vfs.VideoSource {
	if sess == nil {
		return videoAuthSource{}
	}
	return video.New(sess)
}

// openAttendanceSource builds the attendance source over the jAccount
// session, with the same missing-credential stub policy as video.
func openAttendanceSource(sess *session.Session) vfs.AttendanceSource {
	if sess == nil {
		return attendanceAuthSource{}
	}
	return mlearning.New(sess)
}

// videoAuthSource is the VideoSource used when no JAAuthCookie is stored.
type videoAuthSource struct{}

// errVideoAuth is the single answer of videoAuthSource; it classifies as
// auth_required with a jaccount-specific hint.
var errVideoAuth = fail("auth_required", "video features need a jAccount session; run sjtu auth jaccount login", exitAuth)

// Replays reports the missing-credential error.
func (videoAuthSource) Replays(context.Context, int64) ([]video.Replay, error) {
	return nil, errVideoAuth
}

// ReplayViews reports the missing-credential error.
func (videoAuthSource) ReplayViews(context.Context, int64, int64) ([]video.View, error) {
	return nil, errVideoAuth
}

// Subtitle reports the missing-credential error.
func (videoAuthSource) Subtitle(context.Context, int64, int64) (json.RawMessage, error) {
	return nil, errVideoAuth
}

// Summary reports the missing-credential error.
func (videoAuthSource) Summary(context.Context, int64, int64) (json.RawMessage, error) {
	return nil, errVideoAuth
}

// LiveSessions reports the missing-credential error.
func (videoAuthSource) LiveSessions(context.Context, int64) ([]video.LiveSession, error) {
	return nil, errVideoAuth
}

// LiveChannels reports the missing-credential error.
func (videoAuthSource) LiveChannels(context.Context, int64, int64) ([]video.LiveChannel, error) {
	return nil, errVideoAuth
}

// OpenMedia reports the missing-credential error.
func (videoAuthSource) OpenMedia(context.Context, string) (io.ReadCloser, error) {
	return nil, errVideoAuth
}

// ProbeMedia reports the missing-credential error.
func (videoAuthSource) ProbeMedia(context.Context, string) (int64, bool, error) {
	return 0, false, errVideoAuth
}

// FetchRange reports the missing-credential error.
func (videoAuthSource) FetchRange(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errVideoAuth
}

// attendanceAuthSource is the AttendanceSource used when no JAAuthCookie
// is stored; it mirrors videoAuthSource.
type attendanceAuthSource struct{}

// errAttendanceAuth is the single answer of attendanceAuthSource; it
// classifies as auth_required with a jaccount-specific hint.
var errAttendanceAuth = fail("auth_required", "attendance features need a jAccount session; run sjtu auth jaccount login", exitAuth)

// Status reports the missing-credential error.
func (attendanceAuthSource) Status(context.Context, int64) (json.RawMessage, error) {
	return nil, errAttendanceAuth
}

// Current reports the missing-credential error.
func (attendanceAuthSource) Current(context.Context, int64) (json.RawMessage, error) {
	return nil, errAttendanceAuth
}

// Records reports the missing-credential error.
func (attendanceAuthSource) Records(context.Context, int64) (json.RawMessage, error) {
	return nil, errAttendanceAuth
}

// fsName converts an absolute display path ("/courses/12345") to an io/fs
// path ("courses/12345"); "/" maps to the root ".".
func fsName(displayPath string) string {
	name := strings.TrimPrefix(displayPath, "/")
	if name == "" {
		return "."
	}
	return name
}

// runFS executes one vfs command against fsys. displayPath is absolute.
// human selects the TUI rendering (plain lines) over the CLI rendering
// (compact JSON).
func runFS(fsys *vfs.FS, verb, displayPath string, human bool, stdout io.Writer) error {
	name := fsName(displayPath)
	switch verb {
	case "ls":
		entries, err := fs.ReadDir(fsys, name)
		if err != nil {
			return err
		}
		return renderLs(fsys, displayPath, entries, human, stdout)
	case "stat":
		info, err := fs.Stat(fsys, name)
		if err != nil {
			return err
		}
		return renderStat(info, human, stdout)
	case "cat":
		f, err := fsys.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		if info, err := f.Stat(); err == nil && info.IsDir() {
			return fail("is_a_directory", displayPath+" is a directory; use ls", exitError)
		}
		if human && isProjection(displayPath) {
			data, err := io.ReadAll(f)
			if err != nil {
				return err
			}
			return renderCat(displayPath, data, stdout)
		}
		_, err = io.Copy(stdout, f)
		return err
	default:
		return fail("usage", "unknown command "+verb, exitUsage)
	}
}

// lsEntry is one directory entry in CLI JSON output.
type lsEntry struct {
	Name   string `json:"name"`
	Type   string `json:"type"`             // dir | file | link
	Size   int64  `json:"size"`             // bytes; link targets report their path length
	Target string `json:"target,omitempty"` // symlink destination
}

// renderLs writes a directory listing. JSON mode emits one compact array;
// human mode prints one name per line: directories carry a "/" suffix,
// symlinks show "name -> target".
func renderLs(fsys *vfs.FS, displayPath string, entries []fs.DirEntry, human bool, stdout io.Writer) error {
	out := make([]lsEntry, 0, len(entries))
	for _, e := range entries {
		entry := lsEntry{Name: e.Name(), Type: entryType(e)}
		if info, err := e.Info(); err == nil {
			entry.Size = info.Size()
		}
		if e.Type()&fs.ModeSymlink != 0 {
			target, err := fsys.ReadLink(fsName(path.Join(displayPath, e.Name())))
			if err != nil {
				return err
			}
			entry.Target = target
		}
		out = append(out, entry)
		if human {
			line := entry.Name
			switch entry.Type {
			case "dir":
				line += "/"
			case "link":
				line += " -> " + entry.Target
			}
			fmt.Fprintln(stdout, line)
		}
	}
	if human {
		return nil
	}
	return writeJSON(stdout, out)
}

// statOut is the CLI JSON shape of one node's metadata.
type statOut struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
}

// renderStat writes one node's metadata. JSON mode emits one compact object;
// human mode prints key: value lines.
func renderStat(info fs.FileInfo, human bool, stdout io.Writer) error {
	typ := "file"
	if info.IsDir() {
		typ = "dir"
	}
	modTime := ""
	if t := info.ModTime(); !t.IsZero() {
		modTime = t.Format(time.RFC3339)
	}
	if human {
		fmt.Fprintf(stdout, "name: %s\ntype: %s\nsize: %d\n", info.Name(), typ, info.Size())
		if modTime != "" {
			fmt.Fprintf(stdout, "modified: %s\n", modTime)
		}
		return nil
	}
	return writeJSON(stdout, statOut{Name: info.Name(), Type: typ, Size: info.Size(), ModTime: modTime})
}

// entryType classifies a directory entry for output.
func entryType(e fs.DirEntry) string {
	switch {
	case e.IsDir():
		return "dir"
	case e.Type()&fs.ModeSymlink != 0:
		return "link"
	default:
		return "file"
	}
}

// writeJSON compact-marshals v to w with a trailing newline.
func writeJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
