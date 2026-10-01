package vfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// Cache TTL tiers. Course membership changes rarely; assignments,
// announcements and discussions change daily; file trees sit in between.
// Attendance projections are TTL=0 realtime: they bypass this cache
// entirely and fetch on every open.
const (
	// TTLCourses caches the course list.
	TTLCourses = 24 * time.Hour
	// TTLFiles caches folder and file trees.
	TTLFiles = time.Hour
	// TTLEntities caches assignments, announcements and discussions.
	TTLEntities = 5 * time.Minute
)

// MetaCacheDir returns the metadata cache directory,
// ~/.cache/sjtu/meta, honoring XDG_CACHE_HOME.
func MetaCacheDir() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "sjtu", "meta"), nil
}

// envelope wraps one cached list with its fetch time and TTL.
type envelope struct {
	FetchedAt time.Time       `json:"fetched_at"`
	TTL       time.Duration   `json:"ttl"`
	Data      json.RawMessage `json:"data"`
}

// CachedSource is a Source that caches metadata lists as one JSON envelope
// per list. A hit younger than its TTL is served without touching the
// network; a stale or unreadable envelope is refetched and rewritten.
//
// Cached records keep their (id, updated_at, size) fields so a later sync
// phase can do freshness comparisons; TTL expiry is the only invalidation
// this phase performs. File content is never cached: OpenFile passes
// through.
type CachedSource struct {
	inner Source
	dir   string
	now   func() time.Time
}

// NewCachedSource wraps inner with the standard metadata cache under
// MetaCacheDir, creating the directory owner-only: cached data includes
// grades.
func NewCachedSource(inner Source) (*CachedSource, error) {
	dir, err := MetaCacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create meta cache dir: %w", err)
	}
	return &CachedSource{inner: inner, dir: dir, now: time.Now}, nil
}

// newCachedSourceAt builds a cache in dir with a stubbed clock, for tests.
func newCachedSourceAt(inner Source, dir string, now func() time.Time) *CachedSource {
	return &CachedSource{inner: inner, dir: dir, now: now}
}

// cached serves one list from the cache when fresh, otherwise fetches it and
// rewrites the envelope. A cache-write failure is logged and tolerated: the
// cache is an accelerator, never a gate.
func cached[T any](ctx context.Context, cs *CachedSource, key string, ttl time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	path := filepath.Join(cs.dir, key)
	if blob, err := os.ReadFile(path); err == nil {
		var env envelope
		if json.Unmarshal(blob, &env) == nil && cs.now().Before(env.FetchedAt.Add(env.TTL)) {
			var result T
			if json.Unmarshal(env.Data, &result) == nil {
				return result, nil
			}
		}
	}
	result, err := fetch(ctx)
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return result, fmt.Errorf("marshal %s for cache: %w", key, err)
	}
	blob, err := json.Marshal(envelope{FetchedAt: cs.now(), TTL: ttl, Data: raw})
	if err != nil {
		return result, fmt.Errorf("marshal envelope for %s: %w", key, err)
	}
	if err := writeCacheFile(path, blob); err != nil {
		slog.Warn("cache write failed", "key", key, "error", err)
	}
	return result, nil
}

// writeCacheFile atomically replaces the cache envelope via temp file plus
// rename: concurrent writers (goroutines in one daemon, or a CLI racing the
// daemon) must never leave a torn half-JSON file behind. Last writer wins;
// the loser's temp file is removed by the deferred cleanup.
func writeCacheFile(path string, blob []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Courses implements Source with the long TTL tier.
func (cs *CachedSource) Courses(ctx context.Context) ([]canvas.Course, error) {
	return cached(ctx, cs, "courses.json", TTLCourses, cs.inner.Courses)
}

// Assignments implements Source with the short TTL tier.
func (cs *CachedSource) Assignments(ctx context.Context, courseID int64) ([]canvas.Assignment, error) {
	key := fmt.Sprintf("course-%d-assignments.json", courseID)
	return cached(ctx, cs, key, TTLEntities, func(ctx context.Context) ([]canvas.Assignment, error) {
		return cs.inner.Assignments(ctx, courseID)
	})
}

// Announcements implements Source with the short TTL tier.
func (cs *CachedSource) Announcements(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	key := fmt.Sprintf("course-%d-announcements.json", courseID)
	return cached(ctx, cs, key, TTLEntities, func(ctx context.Context) ([]canvas.DiscussionTopic, error) {
		return cs.inner.Announcements(ctx, courseID)
	})
}

// Discussions implements Source with the short TTL tier.
func (cs *CachedSource) Discussions(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	key := fmt.Sprintf("course-%d-discussions.json", courseID)
	return cached(ctx, cs, key, TTLEntities, func(ctx context.Context) ([]canvas.DiscussionTopic, error) {
		return cs.inner.Discussions(ctx, courseID)
	})
}

// Folders implements Source with the medium TTL tier.
func (cs *CachedSource) Folders(ctx context.Context, courseID int64) ([]canvas.Folder, error) {
	key := fmt.Sprintf("course-%d-folders.json", courseID)
	return cached(ctx, cs, key, TTLFiles, func(ctx context.Context) ([]canvas.Folder, error) {
		return cs.inner.Folders(ctx, courseID)
	})
}

// Files implements Source with the medium TTL tier.
func (cs *CachedSource) Files(ctx context.Context, courseID int64) ([]canvas.File, error) {
	key := fmt.Sprintf("course-%d-files.json", courseID)
	return cached(ctx, cs, key, TTLFiles, func(ctx context.Context) ([]canvas.File, error) {
		return cs.inner.Files(ctx, courseID)
	})
}

// OpenFile implements Source by passing through: file content is never
// cached this phase.
func (cs *CachedSource) OpenFile(ctx context.Context, file canvas.File) (io.ReadCloser, error) {
	return cs.inner.OpenFile(ctx, file)
}
