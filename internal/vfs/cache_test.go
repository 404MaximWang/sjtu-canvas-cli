package vfs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// countingSource wraps fakeSource and counts Courses fetches.
type countingSource struct {
	fakeSource
	courseCalls int
}

// Courses implements Source, counting invocations.
func (s *countingSource) Courses(ctx context.Context) ([]canvas.Course, error) {
	s.courseCalls++
	return s.fakeSource.Courses(ctx)
}

// TestCacheTTL pins the cache contract: a fresh envelope is served without a
// fetch, and expiry triggers exactly one refetch.
func TestCacheTTL(t *testing.T) {
	src := &countingSource{fakeSource: fakeSource{courses: []canvas.Course{{ID: 1, Name: "课"}}}}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cs := newCachedSourceAt(src, t.TempDir(), clock)
	ctx := context.Background()

	if _, err := cs.Courses(ctx); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if src.courseCalls != 1 {
		t.Fatalf("first call fetched %d times, want 1", src.courseCalls)
	}
	if _, err := cs.Courses(ctx); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if src.courseCalls != 1 {
		t.Errorf("fresh cache refetched; calls = %d, want 1", src.courseCalls)
	}

	now = now.Add(TTLCourses + time.Minute)
	if _, err := cs.Courses(ctx); err != nil {
		t.Fatalf("post-expiry fetch: %v", err)
	}
	if src.courseCalls != 2 {
		t.Errorf("expired cache not refetched; calls = %d, want 2", src.courseCalls)
	}
}

// TestCacheEnvelope pins the on-disk envelope shape: fetched_at and ttl ride
// alongside the payload.
func TestCacheEnvelope(t *testing.T) {
	src := &countingSource{fakeSource: fakeSource{courses: []canvas.Course{{ID: 1, Name: "课"}}}}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	cs := newCachedSourceAt(src, dir, func() time.Time { return now })
	if _, err := cs.Courses(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	blob, err := os.ReadFile(filepath.Join(dir, "courses.json"))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if !env.FetchedAt.Equal(now) {
		t.Errorf("fetched_at = %v, want %v", env.FetchedAt, now)
	}
	if env.TTL != TTLCourses {
		t.Errorf("ttl = %v, want %v", env.TTL, TTLCourses)
	}
	var courses []canvas.Course
	if err := json.Unmarshal(env.Data, &courses); err != nil || len(courses) != 1 {
		t.Errorf("payload = %s, decode err %v", env.Data, err)
	}
}

// TestCacheCorruptRefetch pins that an unreadable envelope is refetched and
// overwritten rather than surfaced as an error.
func TestCacheCorruptRefetch(t *testing.T) {
	src := &countingSource{fakeSource: fakeSource{courses: []canvas.Course{{ID: 1, Name: "课"}}}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "courses.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := newCachedSourceAt(src, dir, time.Now)
	courses, err := cs.Courses(context.Background())
	if err != nil {
		t.Fatalf("corrupt cache should refetch, got error: %v", err)
	}
	if len(courses) != 1 || src.courseCalls != 1 {
		t.Errorf("courses = %v, calls = %d; want 1 course from 1 fetch", courses, src.courseCalls)
	}
}

// TestWriteCacheFileConcurrent pins the atomicity contract: concurrent
// writers to the same key may disagree on content, but the file must always
// hold exactly one complete document — never a torn interleave — and no
// temp file may leak.
func TestWriteCacheFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.json")

	blob := func(g int) []byte {
		return []byte(`{"writer":` + strconv.Itoa(g) + `,"pad":"` + strings.Repeat("x", 1<<16) + `"}`)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for range 30 {
				if err := writeCacheFile(path, blob(g)); err != nil {
					t.Errorf("writeCacheFile: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var doc struct {
		Writer int    `json:"writer"`
		Pad    string `json:"pad"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("torn cache file: %v", err)
	}
	if doc.Writer < 0 || doc.Writer > 7 || len(doc.Pad) != 1<<16 {
		t.Fatalf("content is no single writer's blob: writer=%d pad=%d", doc.Writer, len(doc.Pad))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "k.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp files leaked: %v", names)
	}
}
