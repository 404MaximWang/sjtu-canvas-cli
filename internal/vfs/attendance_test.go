package vfs

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// fakeAttendanceSource serves fixed payloads and counts fetches per node.
type fakeAttendanceSource struct {
	payloads map[string]json.RawMessage
	calls    map[string]int
	err      error
}

// fetch records one access and serves the node's payload.
func (s *fakeAttendanceSource) fetch(name string) (json.RawMessage, error) {
	s.calls[name]++
	if s.err != nil {
		return nil, s.err
	}
	return s.payloads[name], nil
}

// Status implements AttendanceSource.
func (s *fakeAttendanceSource) Status(context.Context, int64) (json.RawMessage, error) {
	return s.fetch("status")
}

// Current implements AttendanceSource.
func (s *fakeAttendanceSource) Current(context.Context, int64) (json.RawMessage, error) {
	return s.fetch("current")
}

// Records implements AttendanceSource.
func (s *fakeAttendanceSource) Records(context.Context, int64) (json.RawMessage, error) {
	return s.fetch("records")
}

// newAttendanceTestFS builds an FS whose course 10001 carries the fake
// attendance source.
func newAttendanceTestFS(src AttendanceSource) *FS {
	return New(context.Background(), &fakeSource{courses: []canvas.Course{{ID: 10001}}}, nil, src)
}

// TestAttendanceNodes lists the attendance directory and reads each
// projection back: content must pass through verbatim.
func TestAttendanceNodes(t *testing.T) {
	src := &fakeAttendanceSource{
		payloads: map[string]json.RawMessage{
			"status":  json.RawMessage(`{"resultCode":"200","body":{}}`),
			"current": json.RawMessage(`{"curricula":[]}`),
			"records": json.RawMessage(`{"resultCode":"200","body":{"records":[]}}`),
		},
		calls: map[string]int{},
	}
	fsys := newAttendanceTestFS(src)

	entries, err := fs.ReadDir(fsys, "courses/10001/attendance")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"current", "records", "status"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}

	for _, name := range want {
		data, err := fs.ReadFile(fsys, "courses/10001/attendance/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != string(src.payloads[name])+"\n" {
			t.Fatalf("%s = %q, want %q", name, data, string(src.payloads[name])+"\n")
		}
	}
}

// TestAttendanceRefetch pins the TTL=0 tier: every open fetches again.
func TestAttendanceRefetch(t *testing.T) {
	src := &fakeAttendanceSource{
		payloads: map[string]json.RawMessage{"status": json.RawMessage(`{}`)},
		calls:    map[string]int{},
	}
	fsys := newAttendanceTestFS(src)
	for range 2 {
		if _, err := fs.ReadFile(fsys, "courses/10001/attendance/status"); err != nil {
			t.Fatal(err)
		}
	}
	if src.calls["status"] != 2 {
		t.Fatalf("status fetched %d times, want 2", src.calls["status"])
	}
}

// TestAttendanceNilSource leaves the directory present but empty.
func TestAttendanceNilSource(t *testing.T) {
	fsys := newAttendanceTestFS(nil)
	entries, err := fs.ReadDir(fsys, "courses/10001/attendance")
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, err = %v", entries, err)
	}
}

// TestAttendanceError propagates a source failure to the reader.
func TestAttendanceError(t *testing.T) {
	src := &fakeAttendanceSource{
		payloads: map[string]json.RawMessage{},
		calls:    map[string]int{},
		err:      errors.New("token expired"),
	}
	fsys := newAttendanceTestFS(src)
	if _, err := fs.ReadFile(fsys, "courses/10001/attendance/status"); err == nil {
		t.Fatal("want error, got nil")
	}
}
