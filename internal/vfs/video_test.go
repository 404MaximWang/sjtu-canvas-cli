package vfs

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// fakeVideoSource serves canned video data.
type fakeVideoSource struct {
	replays  []video.Replay
	views    map[int64][]video.View
	sessions []video.LiveSession
	channels map[int64][]video.LiveChannel
	media    map[string]string // url → content
	sizes    map[string]int64
}

// Replays implements VideoSource.
func (f *fakeVideoSource) Replays(context.Context, int64) ([]video.Replay, error) {
	return f.replays, nil
}

// ReplayViews implements VideoSource.
func (f *fakeVideoSource) ReplayViews(_ context.Context, _ int64, replayID int64) ([]video.View, error) {
	return f.views[replayID], nil
}

// Subtitle implements VideoSource.
func (f *fakeVideoSource) Subtitle(context.Context, int64, int64) (json.RawMessage, error) {
	return json.RawMessage(`[{"bg":0,"ed":1500,"res":"大家好"}]`), nil
}

// PPT implements VideoSource.
func (f *fakeVideoSource) PPT(context.Context, int64, int64) (json.RawMessage, error) {
	return json.RawMessage(`[{"imageSeekTime":0,"imageUrl":"https://live.example/slide-1.png"}]`), nil
}

// Summary implements VideoSource.
func (f *fakeVideoSource) Summary(context.Context, int64, int64) (json.RawMessage, error) {
	return json.RawMessage(`{"summary":"概要","mindmap":{}}`), nil
}

// LiveSessions implements VideoSource.
func (f *fakeVideoSource) LiveSessions(context.Context, int64) ([]video.LiveSession, error) {
	return f.sessions, nil
}

// LiveChannels implements VideoSource.
func (f *fakeVideoSource) LiveChannels(_ context.Context, _ int64, sessionID int64) ([]video.LiveChannel, error) {
	return f.channels[sessionID], nil
}

// OpenMedia implements VideoSource.
func (f *fakeVideoSource) OpenMedia(_ context.Context, rawURL string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.media[rawURL])), nil
}

// ProbeMedia implements VideoSource.
func (f *fakeVideoSource) ProbeMedia(_ context.Context, rawURL string) (int64, bool, error) {
	return f.sizes[rawURL], true, nil
}

// FetchRange implements VideoSource.
func (f *fakeVideoSource) FetchRange(_ context.Context, rawURL string, begin, end int64) (io.ReadCloser, error) {
	content := f.media[rawURL]
	return io.NopCloser(strings.NewReader(content[begin : end+1])), nil
}

// newVideoTestFS builds an FS over one course with two replays (one ready,
// one repairing) and one live session with two identically-named channels.
func newVideoTestFS() *FS {
	src := &fakeSource{
		courses: []canvas.Course{{ID: 10001, Name: "数学分析"}},
	}
	fsys := New(context.Background(), src, &fakeVideoSource{
		replays: []video.Replay{
			{ID: 111, SubjName: "数学", BeginTime: time.Date(2026, 9, 28, 8, 0, 0, 0, time.Local),
				EndTime:    time.Date(2026, 9, 28, 9, 40, 0, 0, time.Local),
				WeekNumber: 3, WeekDay: 1, DailyLesson: 1, VodStatus: 5, Status: "ready"},
			{ID: 222, SubjName: "数学", BeginTime: time.Date(2026, 9, 28, 10, 0, 0, 0, time.Local),
				EndTime:    time.Date(2026, 9, 28, 11, 40, 0, 0, time.Local),
				WeekNumber: 3, WeekDay: 1, DailyLesson: 2, VodStatus: 4, Status: "repairing"},
		},
		views: map[int64][]video.View{
			111: {
				{ViewNum: 1, URL: "https://videos.example/teacher?key=k1"},
				{ViewNum: 5, URL: "https://videos.example/slides?key=k2"},
			},
		},
		media: map[string]string{
			"https://videos.example/teacher?key=k1": "TEACHER-BYTES",
			"https://videos.example/slides?key=k2":  "SLIDES",
		},
		sizes: map[string]int64{
			"https://videos.example/teacher?key=k1": int64(len("TEACHER-BYTES")),
			"https://videos.example/slides?key=k2":  int64(len("SLIDES")),
		},
		sessions: []video.LiveSession{
			{ID: 88, CourseName: "数学", Classroom: "东上院312", LiveEndTime: 1759200000},
		},
		channels: map[int64][]video.LiveChannel{
			88: {
				{Name: "东上院312老师/课件", URL: "https://live.example/a.flv?auth_key=k&account_token=t"},
				{Name: "东上院312老师/课件", URL: "https://live.example/b.flv?auth_key=k2"},
			},
		},
	}, nil)
	return fsys
}

// TestReplayTree walks one course's replay tree: symlinks, projections,
// per-view media nodes, and the non-playable replay's reduced shape.
func TestReplayTree(t *testing.T) {
	fsys := newVideoTestFS()

	// top level of replay/: two recording directories plus two MM-DD-N symlinks
	entries, err := fs.ReadDir(fsys, "courses/10001/replay")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"09-28-1", "09-28-2", "111", "222"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("replay listing %v, want %v", names, want)
	}

	// symlink target
	target, err := fsys.ReadLink("courses/10001/replay/09-28-1")
	if err != nil || target != "/courses/10001/replay/111" {
		t.Fatalf("symlink target %q, %v", target, err)
	}

	// info projection
	data, err := fs.ReadFile(fsys, "courses/10001/replay/111/info")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info["vodStatus"].(float64) != 5 || info["status"] != "ready" {
		t.Fatalf("info %v", info)
	}
	if info["durationSeconds"].(float64) != 6000 {
		t.Fatalf("duration %v", info["durationSeconds"])
	}

	// url projection: viewNum values preserved verbatim
	data, err = fs.ReadFile(fsys, "courses/10001/replay/111/url")
	if err != nil {
		t.Fatal(err)
	}
	var urlDoc struct {
		Views []struct {
			ViewNum int64  `json:"viewNum"`
			URL     string `json:"url"`
		} `json:"views"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(data, &urlDoc); err != nil {
		t.Fatal(err)
	}
	if len(urlDoc.Views) != 2 || urlDoc.Views[1].ViewNum != 5 {
		t.Fatalf("views %+v", urlDoc.Views)
	}
	if urlDoc.Headers["Referer"] == "" {
		t.Fatal("headers missing Referer")
	}

	// ppt projection: docList entries are exposed verbatim
	data, err = fs.ReadFile(fsys, "courses/10001/replay/111/ppt")
	if err != nil {
		t.Fatal(err)
	}
	var ppt []struct {
		ImageSeekTime int64  `json:"imageSeekTime"`
		ImageURL      string `json:"imageUrl"`
	}
	if err := json.Unmarshal(data, &ppt); err != nil {
		t.Fatal(err)
	}
	if len(ppt) != 1 || ppt[0].ImageSeekTime != 0 || ppt[0].ImageURL != "https://live.example/slide-1.png" {
		t.Fatalf("ppt %+v", ppt)
	}

	// video-N nodes are named by the real viewNum values, sizes probed
	for _, name := range []string{"video-1", "video-5"} {
		fi, err := fs.Stat(fsys, "courses/10001/replay/111/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() == 0 {
			t.Fatalf("%s size not probed", name)
		}
	}

	// cat video-N yields the media bytes
	data, err = fs.ReadFile(fsys, "courses/10001/replay/111/video-1")
	if err != nil || string(data) != "TEACHER-BYTES" {
		t.Fatalf("video-1 content %q, %v", data, err)
	}

	// RangedFile capability
	size, fetch, ok, err := fsys.RangedFile("courses/10001/replay/111/video-5")
	if err != nil || !ok || size != int64(len("SLIDES")) {
		t.Fatalf("RangedFile: size %d ok %v err %v", size, ok, err)
	}
	rc, err := fetch(context.Background(), 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	chunk, _ := io.ReadAll(rc)
	rc.Close()
	if string(chunk) != "LID" {
		t.Fatalf("range fetch %q", chunk)
	}

	// non-playable replay: url read fails with a structured error, subtitle still works
	if _, err := fs.ReadFile(fsys, "courses/10001/replay/222/url"); err == nil {
		t.Fatal("want error reading url of repairing replay")
	}
	data, err = fs.ReadFile(fsys, "courses/10001/replay/222/subtitle")
	if err != nil || !strings.Contains(string(data), "大家好") {
		t.Fatalf("subtitle %q, %v", data, err)
	}

	// a non-playable replay exposes no video-N nodes
	entries, err = fs.ReadDir(fsys, "courses/10001/replay/222")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "video-") {
			t.Fatalf("non-playable replay must not expose video nodes: %v", e.Name())
		}
	}
}

// TestLiveTree walks one course's live tree: info projection and per-channel
// .flv URL nodes with sanitized, deduplicated names.
func TestLiveTree(t *testing.T) {
	fsys := newVideoTestFS()

	entries, err := fs.ReadDir(fsys, "courses/10001/live")
	if err != nil || len(entries) != 1 || entries[0].Name() != "88" {
		t.Fatalf("live listing %v, %v", entries, err)
	}

	data, err := fs.ReadFile(fsys, "courses/10001/live/88/info")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info["classroom"] != "东上院312" || info["liveEndTime"].(float64) != 1759200000 {
		t.Fatalf("live info %v", info)
	}

	// channel names have / sanitized to full-width; duplicates are deduplicated
	entries, err = fs.ReadDir(fsys, "courses/10001/live/88")
	if err != nil {
		t.Fatal(err)
	}
	var channelFiles []string
	for _, e := range entries {
		if e.Name() != "info" {
			channelFiles = append(channelFiles, e.Name())
		}
	}
	if len(channelFiles) != 2 || !strings.Contains(channelFiles[0], "／") {
		t.Fatalf("channel names %v", channelFiles)
	}
	if channelFiles[0] == channelFiles[1] {
		t.Fatalf("channel names not deduplicated: %v", channelFiles)
	}

	data, err = fs.ReadFile(fsys, "courses/10001/live/88/"+channelFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.URL, ".flv") || !strings.Contains(doc.URL, "auth_key") {
		t.Fatalf("channel url %q", doc.URL)
	}
}

// TestVideoNilSource pins that video stays absent when the FS has no
// VideoSource, with zero behavior change for the rest of the tree.
func TestVideoNilSource(t *testing.T) {
	fsys := New(context.Background(), &fakeSource{courses: []canvas.Course{{ID: 10001}}}, nil, nil)
	for _, dir := range []string{"courses/10001/replay", "courses/10001/live"} {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s: entries %v err %v", dir, entries, err)
		}
	}
}

// TestVideoFSCompliance runs fstest.TestFS over a video-backed FS with
// only playable fixtures.
func TestVideoFSCompliance(t *testing.T) {
	// fstest.TestFS walks the whole tree and opens every file, which is
	// incompatible with the "repairing replay's url read fails" contract, so
	// this fixture is all-playable.
	src := &fakeVideoSource{
		replays: []video.Replay{
			{ID: 111, SubjName: "数学", BeginTime: time.Date(2026, 9, 28, 8, 0, 0, 0, time.Local),
				EndTime:    time.Date(2026, 9, 28, 9, 40, 0, 0, time.Local),
				WeekNumber: 3, WeekDay: 1, DailyLesson: 1, VodStatus: 5, Status: "ready"},
		},
		views: map[int64][]video.View{
			111: {{ViewNum: 1, URL: "https://videos.example/teacher?key=k1"}},
		},
		media: map[string]string{"https://videos.example/teacher?key=k1": "TEACHER-BYTES"},
		sizes: map[string]int64{"https://videos.example/teacher?key=k1": int64(len("TEACHER-BYTES"))},
		sessions: []video.LiveSession{
			{ID: 88, CourseName: "数学", Classroom: "东上院312", LiveEndTime: 1759200000},
		},
		channels: map[int64][]video.LiveChannel{
			88: {{Name: "东上院312老师", URL: "https://live.example/a.flv?auth_key=k"}},
		},
	}
	playable := New(context.Background(), &fakeSource{courses: []canvas.Course{{ID: 10001}}}, src, nil)
	err := fstest.TestFS(playable,
		"courses/10001/replay",
		"courses/10001/replay/111",
		"courses/10001/replay/111/info",
		"courses/10001/replay/111/url",
		"courses/10001/replay/111/video-1",
		"courses/10001/replay/09-28-1",
		"courses/10001/live",
		"courses/10001/live/88",
		"courses/10001/live/88/info",
	)
	if err != nil {
		t.Fatal(err)
	}
}
