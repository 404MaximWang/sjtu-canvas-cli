package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/vfs"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// newDownloadCmd builds `sjtu download <vfs-path> [local-path]`.
func newDownloadCmd(rt *Runtime, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "download <vfs-path> [local-path]",
		Short: "Download a file from the virtual file system to local disk",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return fail("usage", "download <vfs-path> [local-path]", exitUsage)
			}
			if !strings.HasPrefix(args[0], "/") {
				return fail("usage", "vfs path must start with / (CLI mode accepts absolute paths only)", exitUsage)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			fsys, _, _, err := rt.openVFS(cmd.Context(), false)
			if err != nil {
				return err
			}
			local := ""
			if len(args) == 2 {
				local = args[1]
			}
			return runDownload(cmd.Context(), fsys, args[0], local, false, stdout, stderr)
		},
	}
}

// runDownload implements the download verb: the exact bytes cat would print,
// written to disk. No per-type branching: a ranged media node downloads in
// parallel through its capability, everything else streams through Open.
func runDownload(ctx context.Context, fsys *vfs.FS, displayPath, localPath string, human bool, stdout, stderr io.Writer) error {
	name := fsName(displayPath)
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return err // fs.ErrNotExist classifies as not_found
	}
	if info.IsDir() {
		return fail("is_a_directory", displayPath+" is a directory; download takes files only", exitError)
	}

	local := localPath
	if local == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate home directory: %w", err)
		}
		local = filepath.Join(home, "Downloads", path.Base(displayPath))
	}
	if st, err := os.Stat(filepath.Dir(local)); err != nil || !st.IsDir() {
		return fail("target_parent_not_found", filepath.Dir(local)+" does not exist", exitError)
	}

	dst, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return fail("target_exists", local+" already exists; refusing to overwrite", exitError)
	}
	if err != nil {
		return err
	}

	written, err := downloadInto(ctx, fsys, name, info.Size(), dst, stderr)
	if err != nil {
		dst.Close()
		os.Remove(local) // the partial file is ours; leave no debris
		return err
	}
	if err := dst.Close(); err != nil {
		os.Remove(local)
		return err
	}

	if human {
		fmt.Fprintf(stdout, "downloaded %s (%d bytes)\n", local, written)
		return nil
	}
	return writeJSON(stdout, map[string]any{"path": local, "size": written})
}

// downloadInto writes the source's bytes to dst, reporting progress to
// stderr when the total is known.
func downloadInto(ctx context.Context, fsys *vfs.FS, name string, total int64, dst *os.File, stderr io.Writer) (int64, error) {
	var prog *progress
	if total > 0 {
		prog = &progress{w: stderr, total: total}
	}
	size, fetch, ok, err := fsys.RangedFile(name)
	if err != nil {
		return 0, err
	}
	if ok {
		var cb func(done, total int64)
		if prog != nil {
			prog.total = size
			cb = func(done, _ int64) { prog.set(done) }
		}
		return video.DownloadRanged(ctx, size, video.RangeFetcher(fetch), dst, cb)
	}

	src, err := fsys.Open(name)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	return io.Copy(progressWriter{dst: dst, progress: prog}, src)
}

// progressWriter counts streamed bytes through a Writer.
type progressWriter struct {
	dst      io.Writer
	progress *progress
}

// Write streams through to dst while accumulating progress.
func (w progressWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if w.progress != nil {
		w.progress.add(int64(n))
	}
	return n, err
}

// progress prints throttled percentage lines to stderr.
type progress struct {
	w     io.Writer
	total int64
	done  int64
	last  time.Time
}

// set records absolute progress (ranged path reports cumulative offsets).
func (p *progress) set(done int64) {
	p.done = done
	p.print(false)
}

// add accumulates relative progress (sequential copy path).
func (p *progress) add(n int64) {
	p.done += n
	p.print(false)
}

// print renders at most one line per 200ms, plus the final 100%.
func (p *progress) print(force bool) {
	now := time.Now()
	if !force && p.done < p.total && now.Sub(p.last) < 200*time.Millisecond {
		return
	}
	p.last = now
	fmt.Fprintf(p.w, "\rdownloading: %d/%d bytes (%.0f%%)", p.done, p.total, 100*float64(p.done)/float64(p.total))
	if p.done >= p.total {
		fmt.Fprintln(p.w)
	}
}
