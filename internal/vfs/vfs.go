// Package vfs exposes Canvas course content as a read-only io/fs file
// system.
//
// Layout:
//
//	/                                       root
//	/courses                                courses flat by numeric ID, plus one
//	                                        directory per term
//	/courses/<courseID>/                    a course (the only real entity)
//	/courses/<courseID>/announcements/<id>/ one directory per announcement,
//	                                        holding the info projection
//	/courses/<courseID>/discussions/<id>/   one directory per discussion,
//	                                        holding the info projection
//	/courses/<courseID>/files/              the course's native file tree
//	/courses/<courseID>/assignments/<id>/   one directory per assignment:
//	                                        info plus submissions/latest
//	/courses/<courseID>/attendance/         placeholder, empty this phase
//	/courses/<courseID>/replay/<videoID>/   one directory per recording: info,
//	                                        url, subtitle, summary projections
//	                                        plus one video-N byte node per view
//	/courses/<courseID>/replay/<MM-DD>-<N>  symlink → replay/<videoID>
//	/courses/<courseID>/live/<sessionID>/   one directory per live session:
//	                                        info plus one .flv URL file per channel
//	/courses/<term>/<courseID>              symlink → /courses/<courseID>
//	/courses/<term>/<courseName>            symlink → /courses/<term>/<courseID>
//	/courses/current                        symlink → the current term directory
//
// Entities are directories; the read-only metadata projection inside is
// always named "info" and carries no extension. Its content is compact JSON.
//
// The current term is the one whose [start_at, end_at] contains today; when
// none does, the term with the latest start_at wins. Terms without a
// parseable start_at are never current.
//
// FS implements fs.FS and fs.ReadLinkFS: Open follows symbolic links, Lstat
// does not, and ReadLink reports the root-anchored target.
package vfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// Source provides the Canvas data backing the file system. Every list is
// complete (pagination already resolved by the canvas layer) and scoped as
// named. OpenFile streams one file's content; the caller closes it.
type Source interface {
	Courses(ctx context.Context) ([]canvas.Course, error)
	Assignments(ctx context.Context, courseID int64) ([]canvas.Assignment, error)
	Announcements(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error)
	Discussions(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error)
	Folders(ctx context.Context, courseID int64) ([]canvas.Folder, error)
	Files(ctx context.Context, courseID int64) ([]canvas.File, error)
	OpenFile(ctx context.Context, file canvas.File) (io.ReadCloser, error)
}

// CanvasSource adapts a *canvas.Client to the Source interface.
func CanvasSource(c *canvas.Client) Source {
	return canvasSource{c}
}

// canvasSource is the method-name adapter between the canvas data layer
// (List* verbs) and the vfs Source interface.
type canvasSource struct {
	client *canvas.Client
}

// Courses implements Source.
func (s canvasSource) Courses(ctx context.Context) ([]canvas.Course, error) {
	return s.client.ListCourses(ctx)
}

// Assignments implements Source.
func (s canvasSource) Assignments(ctx context.Context, courseID int64) ([]canvas.Assignment, error) {
	return s.client.ListAssignments(ctx, courseID)
}

// Announcements implements Source.
func (s canvasSource) Announcements(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	return s.client.ListAnnouncements(ctx, courseID)
}

// Discussions implements Source.
func (s canvasSource) Discussions(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	return s.client.ListDiscussions(ctx, courseID)
}

// Folders implements Source.
func (s canvasSource) Folders(ctx context.Context, courseID int64) ([]canvas.Folder, error) {
	return s.client.ListFolders(ctx, courseID)
}

// Files implements Source.
func (s canvasSource) Files(ctx context.Context, courseID int64) ([]canvas.File, error) {
	return s.client.ListFiles(ctx, courseID)
}

// OpenFile implements Source.
func (s canvasSource) OpenFile(ctx context.Context, file canvas.File) (io.ReadCloser, error) {
	return s.client.Download(ctx, file)
}

// maxSymlinkDepth bounds symlink chains during resolution, mirroring the
// kernel's ELOOP limit.
const maxSymlinkDepth = 40

// Resolution-time errors, wrapped in *fs.PathError at the boundary.
var (
	errIsDirectory  = errors.New("is a directory")
	errNotDirectory = errors.New("not a directory")
	errNotSymlink   = errors.New("not a symbolic link")
	errSymlinkLoop  = errors.New("too many levels of symbolic links")
)

// FS is a read-only Canvas file system. All state is fetched lazily through
// Source on first access; an FS is cheap to build and safe to use from one
// goroutine at a time.
type FS struct {
	ctx   context.Context // bounds every fetch triggered by Open/ReadDir
	src   Source
	video VideoSource      // nil leaves replay/ and live/ empty
	now   func() time.Time // clock for the current-term rule; tests may stub
}

// New builds an FS over src, with video backing the replay/live subtrees
// (nil disables them). ctx bounds every fetch the file system triggers, so
// Ctrl-C cancellation reaches in-flight directory listings.
func New(ctx context.Context, src Source, video VideoSource) *FS {
	return &FS{ctx: ctx, src: src, video: video, now: time.Now}
}

// node is one resolved path: a directory, a JSON entity file, a remote
// Canvas file, or a symbolic link. Exactly one of the payload fields is
// active, selected by mode's type bits.
type node struct {
	name    string                        // base name as shown in directory listings
	mode    fs.FileMode                   // type bits plus permissions
	size    int64                         // regular-file content length; symlink target length
	modTime time.Time                     // parsed from the entity's updated_at, else zero
	target  string                        // symlink destination, anchored at the fs root
	data    []byte                        // JSON entity file content
	file    *canvas.File                  // remote Canvas file
	open    func() (io.ReadCloser, error) // dynamic content file (video projections, media)
	ranged  RangeFetch                    // ranged-download capability of a media node
	list    func() ([]node, error)        // directory child loader
	entries []node                        // loaded children, sorted by name
	loaded  bool                          // whether list has run
}

// children returns the directory's entries, loading them on first use.
func (n *node) children() ([]node, error) {
	if !n.loaded {
		entries, err := n.list()
		if err != nil {
			return nil, err
		}
		n.entries = entries
		n.loaded = true
	}
	return n.entries, nil
}

// info returns the node's metadata as an fs.FileInfo.
func (n *node) info() fs.FileInfo { return fileInfo{n} }

// fileInfo adapts a node to fs.FileInfo. Sys is nil: there is no lower
// layer to expose.
type fileInfo struct {
	n *node
}

// Name returns the node's base name.
func (fi fileInfo) Name() string { return fi.n.name }

// Size returns the content length in bytes.
func (fi fileInfo) Size() int64 { return fi.n.size }

// Mode returns the type bits plus read-only permissions.
func (fi fileInfo) Mode() fs.FileMode { return fi.n.mode }

// ModTime returns the entity's updated_at, or the zero time when Canvas did
// not supply one.
func (fi fileInfo) ModTime() time.Time { return fi.n.modTime }

// IsDir reports whether the node is a directory.
func (fi fileInfo) IsDir() bool { return fi.n.mode.IsDir() }

// Sys always returns nil.
func (fi fileInfo) Sys() any { return nil }

// dirNode builds a directory node whose children come from list.
func dirNode(name string, list func() ([]node, error)) node {
	return node{name: name, mode: fs.ModeDir | 0o555, list: list}
}

// symlinkNode builds a symbolic link node pointing at target, which is
// anchored at the file system root (e.g. "/courses/12345").
func symlinkNode(name, target string) node {
	return node{name: name, mode: fs.ModeSymlink | 0o777, target: target, size: int64(len(target))}
}

// jsonFileNode builds a read-only projection file: v marshaled as compact
// JSON with a trailing newline. Projection files never carry an extension.
func jsonFileNode(name string, v any, updatedAt string) (node, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return node{}, fmt.Errorf("marshal projection %s: %w", name, err)
	}
	data = append(data, '\n')
	return node{
		name:    name,
		mode:    0o444,
		size:    int64(len(data)),
		modTime: parseTime(updatedAt),
		data:    data,
	}, nil
}

// entityDirNode builds an entity directory named by its numeric ID. The
// directory carries the entity's updated_at as its mod time and holds the
// given children (the info projection plus any sub-resources).
func entityDirNode(id int64, updatedAt string, children []node) node {
	sortNodes(children)
	entries := children
	return node{
		name:    strconv.FormatInt(id, 10),
		mode:    fs.ModeDir | 0o555,
		modTime: parseTime(updatedAt),
		list:    func() ([]node, error) { return entries, nil },
	}
}

// remoteFileNode builds a regular file node backed by a Canvas file's
// content stream.
func remoteFileNode(name string, f canvas.File) node {
	file := f
	return node{
		name:    name,
		mode:    0o444,
		size:    int64(f.Size),
		modTime: parseTime(f.UpdatedAt),
		file:    &file,
	}
}

// parseTime converts a Canvas ISO 8601 timestamp; unparseable or empty input
// yields the zero time.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// sanitizeName turns an arbitrary Canvas display string into one valid path
// element: path separators become full-width lookalikes and control
// characters are dropped. It returns "" when nothing usable remains;
// callers substitute a fallback name.
func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/':
			return '／'
		case '\\':
			return '＼'
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

// uniqueName appends "-<id>" on collision so every directory entry is
// unambiguous and stable across identical fetches.
func uniqueName(seen map[string]bool, base string, id int64) string {
	if base == "" {
		base = strconv.FormatInt(id, 10)
	}
	name := base
	for suffix := 0; seen[name]; suffix++ {
		name = fmt.Sprintf("%s-%d-%d", base, id, suffix)
	}
	seen[name] = true
	return name
}

// sortNodes orders entries by name, as fs.ReadDir requires.
func sortNodes(nodes []node) {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].name < nodes[j].name })
}

// rootNode builds the "/" directory.
func (f *FS) rootNode() *node {
	return &node{name: ".", mode: fs.ModeDir | 0o555, list: func() ([]node, error) {
		return []node{dirNode("courses", f.listCourses)}, nil
	}}
}

// Open opens name for reading, following symbolic links.
func (f *FS) Open(name string) (fs.File, error) {
	n, err := f.resolve(name, true, 0)
	if err != nil {
		return nil, err
	}
	switch {
	case n.mode.IsDir():
		return &openFile{node: n, path: name}, nil
	case n.file != nil:
		rc, err := f.src.OpenFile(f.ctx, *n.file)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &openFile{node: n, path: name, remote: rc}, nil
	case n.open != nil:
		rc, err := n.open()
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &openFile{node: n, path: name, remote: rc}, nil
	default:
		return &openFile{node: n, path: name, reader: bytes.NewReader(n.data)}, nil
	}
}

// Lstat describes name without following a final symbolic link.
func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	n, err := f.resolve(name, false, 0)
	if err != nil {
		return nil, err
	}
	return n.info(), nil
}

// ReadLink returns the destination of the symbolic link at name, anchored at
// the file system root (e.g. "/courses/12345").
func (f *FS) ReadLink(name string) (string, error) {
	n, err := f.resolve(name, false, 0)
	if err != nil {
		return "", err
	}
	if n.mode&fs.ModeSymlink == 0 {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: errNotSymlink}
	}
	return n.target, nil
}

// resolve walks name from the root. A symlink in a non-final position, or in
// the final position when followFinal is set, is replaced by its target and
// the walk restarts there. depth guards against symlink cycles.
func (f *FS) resolve(name string, followFinal bool, depth int) (*node, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if depth > maxSymlinkDepth {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errSymlinkLoop}
	}
	cur := f.rootNode()
	if name == "." {
		return cur, nil
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if !cur.mode.IsDir() {
			return nil, &fs.PathError{Op: "open", Path: name, Err: errNotDirectory}
		}
		entries, err := cur.children()
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		var next *node
		for j := range entries {
			if entries[j].name == part {
				next = &entries[j]
				break
			}
		}
		if next == nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		if next.mode&fs.ModeSymlink != 0 && (i+1 < len(parts) || followFinal) {
			rest := strings.TrimPrefix(next.target, "/")
			if i+1 < len(parts) {
				rest += "/" + strings.Join(parts[i+1:], "/")
			}
			return f.resolve(rest, followFinal, depth+1)
		}
		cur = next
	}
	return cur, nil
}

// openFile is one open handle. Directories additionally implement
// fs.ReadDirFile.
type openFile struct {
	node   *node
	path   string // full fs path, for error reporting
	dirPos int    // ReadDir cursor

	reader *bytes.Reader // JSON entity content
	remote io.ReadCloser // Canvas file stream
}

// Stat returns the open node's metadata.
func (f *openFile) Stat() (fs.FileInfo, error) { return f.node.info(), nil }

// Read streams file content. Reading a directory is an error.
func (f *openFile) Read(p []byte) (int, error) {
	switch {
	case f.node.mode.IsDir():
		return 0, &fs.PathError{Op: "read", Path: f.path, Err: errIsDirectory}
	case f.remote != nil:
		return f.remote.Read(p)
	default:
		return f.reader.Read(p)
	}
}

// Close releases the remote stream, if any.
func (f *openFile) Close() error {
	if f.remote != nil {
		return f.remote.Close()
	}
	return nil
}

// ReadDir returns up to count entries, or all remaining entries when
// count <= 0, following fs.ReadDirFile semantics: a positive count at end of
// directory yields io.EOF.
func (f *openFile) ReadDir(count int) ([]fs.DirEntry, error) {
	if !f.node.mode.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: f.path, Err: errNotDirectory}
	}
	entries, err := f.node.children()
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: f.path, Err: err}
	}
	n := len(entries) - f.dirPos
	if n == 0 && count > 0 {
		return nil, io.EOF
	}
	if count > 0 && n > count {
		n = count
	}
	out := make([]fs.DirEntry, n)
	for i := range out {
		out[i] = fs.FileInfoToDirEntry(entries[f.dirPos+i].info())
	}
	f.dirPos += n
	return out, nil
}
