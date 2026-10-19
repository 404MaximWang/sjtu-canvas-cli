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
	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// Cache TTL tiers. Course membership changes rarely; assignments,
// announcements and discussions change daily; file trees sit in between; the
// replay index changes when a lecture is published or its status is
// repaired. Attendance projections are TTL=0 realtime: they bypass this
// cache entirely and fetch on every open.
const (
	// TTLCourses caches the course list.
	TTLCourses = 24 * time.Hour
	// TTLFiles caches folder and file trees.
	TTLFiles = time.Hour
	// TTLEntities caches assignments, announcements and discussions.
	TTLEntities = 5 * time.Minute
	// TTLVideoLists caches the replay index of one course, so a new lecture
	// or a status change surfaces within five minutes.
	TTLVideoLists = 5 * time.Minute
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

// cacheStore is the on-disk location and clock shared by the Canvas metadata
// cache and the video list cache.
type cacheStore struct {
	dir string
	now func() time.Time
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
	cacheStore
}

// NewCachedSource wraps inner with the standard metadata cache under
// MetaCacheDir, creating the directory owner-only: cached data includes
// grades.
func NewCachedSource(inner Source) (*CachedSource, error) {
	store, err := newCacheStore()
	if err != nil {
		return nil, err
	}
	return &CachedSource{inner: inner, cacheStore: store}, nil
}

// newCacheStore returns the standard cache location, creating it owner-only:
// cached data includes grades.
func newCacheStore() (cacheStore, error) {
	dir, err := MetaCacheDir()
	if err != nil {
		return cacheStore{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cacheStore{}, fmt.Errorf("create meta cache dir: %w", err)
	}
	return cacheStore{dir: dir, now: time.Now}, nil
}

// newCachedSourceAt builds a cache in dir with a stubbed clock, for tests.
func newCachedSourceAt(inner Source, dir string, now func() time.Time) *CachedSource {
	return &CachedSource{inner: inner, cacheStore: cacheStore{dir: dir, now: now}}
}

// serve returns the cached value of key while its envelope is younger than
// ttl, and otherwise fetches the value, rewrites the envelope, and returns
// the fetched value. A cache-write failure is logged and tolerated: the
// cache is an accelerator, never a gate. A failed fetch is never cached.
func serve[T any](ctx context.Context, cs cacheStore, key string, ttl time.Duration, fetch func(context.Context) (T, error)) (T, error) {
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
	return serve(ctx, cs.cacheStore, "courses.json", TTLCourses, cs.inner.Courses)
}

// Assignments implements Source with the short TTL tier.
func (cs *CachedSource) Assignments(ctx context.Context, courseID int64) ([]canvas.Assignment, error) {
	key := fmt.Sprintf("course-%d-assignments.json", courseID)
	return serve(ctx, cs.cacheStore, key, TTLEntities, func(ctx context.Context) ([]canvas.Assignment, error) {
		return cs.inner.Assignments(ctx, courseID)
	})
}

// Announcements implements Source with the short TTL tier.
func (cs *CachedSource) Announcements(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	key := fmt.Sprintf("course-%d-announcements.json", courseID)
	return serve(ctx, cs.cacheStore, key, TTLEntities, func(ctx context.Context) ([]canvas.DiscussionTopic, error) {
		return cs.inner.Announcements(ctx, courseID)
	})
}

// Discussions implements Source with the short TTL tier.
func (cs *CachedSource) Discussions(ctx context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	key := fmt.Sprintf("course-%d-discussions.json", courseID)
	return serve(ctx, cs.cacheStore, key, TTLEntities, func(ctx context.Context) ([]canvas.DiscussionTopic, error) {
		return cs.inner.Discussions(ctx, courseID)
	})
}

// Folders implements Source with the medium TTL tier.
func (cs *CachedSource) Folders(ctx context.Context, courseID int64) ([]canvas.Folder, error) {
	key := fmt.Sprintf("course-%d-folders.json", courseID)
	return serve(ctx, cs.cacheStore, key, TTLFiles, func(ctx context.Context) ([]canvas.Folder, error) {
		return cs.inner.Folders(ctx, courseID)
	})
}

// Files implements Source with the medium TTL tier.
func (cs *CachedSource) Files(ctx context.Context, courseID int64) ([]canvas.File, error) {
	key := fmt.Sprintf("course-%d-files.json", courseID)
	return serve(ctx, cs.cacheStore, key, TTLFiles, func(ctx context.Context) ([]canvas.File, error) {
		return cs.inner.Files(ctx, courseID)
	})
}

// OpenFile implements Source by passing through: file content is never
// cached this phase.
func (cs *CachedSource) OpenFile(ctx context.Context, file canvas.File) (io.ReadCloser, error) {
	return cs.inner.OpenFile(ctx, file)
}

// CachedVideoSource caches the video list endpoints behind the metadata
// cache. Only the replay index is cached: it carries no media URL and no
// signature, so nothing secret reaches the disk, and its staleness window is
// TTLVideoLists. The media URL, media byte, live and AI projection endpoints
// pass through untouched.
type CachedVideoSource struct {
	inner VideoSource
	cacheStore
}

// NewCachedVideoSource wraps inner with the standard metadata cache under
// MetaCacheDir, creating the directory owner-only.
func NewCachedVideoSource(inner VideoSource) (*CachedVideoSource, error) {
	store, err := newCacheStore()
	if err != nil {
		return nil, err
	}
	return &CachedVideoSource{inner: inner, cacheStore: store}, nil
}

// Replays implements VideoSource with the replay index TTL tier.
func (cs *CachedVideoSource) Replays(ctx context.Context, courseID int64) ([]video.Replay, error) {
	key := fmt.Sprintf("course-%d-replays.json", courseID)
	return serve(ctx, cs.cacheStore, key, TTLVideoLists, func(ctx context.Context) ([]video.Replay, error) {
		return cs.inner.Replays(ctx, courseID)
	})
}

// ReplayViews implements VideoSource by passing through: its entries carry
// signed media URLs, which are never written to disk.
func (cs *CachedVideoSource) ReplayViews(ctx context.Context, courseID, replayID int64) ([]video.View, error) {
	return cs.inner.ReplayViews(ctx, courseID, replayID)
}

// Subtitle implements VideoSource by passing through.
func (cs *CachedVideoSource) Subtitle(ctx context.Context, courseID, replayID int64) (json.RawMessage, error) {
	return cs.inner.Subtitle(ctx, courseID, replayID)
}

// PPT implements VideoSource by passing through.
func (cs *CachedVideoSource) PPT(ctx context.Context, courseID, replayID int64) (json.RawMessage, error) {
	return cs.inner.PPT(ctx, courseID, replayID)
}

// Summary implements VideoSource by passing through.
func (cs *CachedVideoSource) Summary(ctx context.Context, courseID, replayID int64) (json.RawMessage, error) {
	return cs.inner.Summary(ctx, courseID, replayID)
}

// LiveSessions implements VideoSource by passing through: its semantic is
// "broadcasting now", so it is never cached.
func (cs *CachedVideoSource) LiveSessions(ctx context.Context, courseID int64) ([]video.LiveSession, error) {
	return cs.inner.LiveSessions(ctx, courseID)
}

// LiveChannels implements VideoSource by passing through.
func (cs *CachedVideoSource) LiveChannels(ctx context.Context, courseID, sessionID int64) ([]video.LiveChannel, error) {
	return cs.inner.LiveChannels(ctx, courseID, sessionID)
}

// OpenMedia implements VideoSource by passing through.
func (cs *CachedVideoSource) OpenMedia(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	return cs.inner.OpenMedia(ctx, rawURL)
}

// ProbeMedia implements VideoSource by passing through.
func (cs *CachedVideoSource) ProbeMedia(ctx context.Context, rawURL string) (int64, bool, error) {
	return cs.inner.ProbeMedia(ctx, rawURL)
}

// FetchRange implements VideoSource by passing through.
func (cs *CachedVideoSource) FetchRange(ctx context.Context, rawURL string, begin, endInclusive int64) (io.ReadCloser, error) {
	return cs.inner.FetchRange(ctx, rawURL, begin, endInclusive)
}
