package video

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestJWTFromLocation covers the two jwt_token forms in a 302 Location
// (fragment and query) plus the no-token error.
func TestJWTFromLocation(t *testing.T) {
	cases := []struct {
		name     string
		location string
		want     string
		wantErr  bool
	}{
		{"fragment", "https://v.sjtu.edu.cn/ui/#jwt_token=abc.def.ghi", "abc.def.ghi", false},
		{"query", "https://v.sjtu.edu.cn/ui/?jwt_token=abc", "abc", false},
		{"fragment with inner query", "https://x/#/launch?jwt_token=tok&other=1", "tok", false},
		{"url encoded", "https://x/#jwt_token=a%2Bb%3D", "a+b=", false},
		{"other params first", "https://x/?a=1&jwt_token=t&b=2", "t", false},
		{"missing", "https://x/?a=1", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jwtFromLocation(tc.location)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestParseLaunchForm covers hidden-input extraction, jsessionid quoting,
// and the action-missing error.
func TestParseLaunchForm(t *testing.T) {
	page := `<html><body><form action="/lti/launch" method="post">
		<input type="hidden" name="oauth_nonce" value="n1"/>
		<input type="hidden" name="oauth_signature" value="s1"/>
		<input name="no_value"/>
		<div><input type="hidden" name="deep" value="d"/></div>
	</form></body></html>`
	action, fields, err := parseLaunchForm([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if action != "/lti/launch" {
		t.Fatalf("action %q", action)
	}
	if fields.Get("oauth_nonce") != "n1" || fields.Get("oauth_signature") != "s1" || fields.Get("deep") != "d" {
		t.Fatalf("fields %v", fields)
	}
	if _, ok := fields["no_value"]; !ok || fields.Get("no_value") != "" {
		t.Fatalf("valueless input should map to empty string: %v", fields)
	}

	if _, _, err := parseLaunchForm([]byte(`<html>no form</html>`)); err == nil {
		t.Fatal("want error for missing form")
	}
	if _, _, err := parseLaunchForm([]byte(`<form><input name="a" value="1"/></form>`)); err == nil {
		t.Fatal("want error for missing action")
	}
}

// TestAPIData checks the strict envelope: status must be present and 200,
// data must be an object; the result alias and missing status are errors.
func TestAPIData(t *testing.T) {
	if _, err := apiData(json.RawMessage(`{"status":500,"message":"boom"}`)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want message error, got %v", err)
	}
	data, err := apiData(json.RawMessage(`{"status":200,"data":{"a":1}}`))
	if err != nil || data["a"].(float64) != 1 {
		t.Fatalf("data unwrap: %v %v", data, err)
	}
	if _, err := apiData(json.RawMessage(`{"result":{"b":2}}`)); err == nil {
		t.Fatal("want error for missing status")
	}
	if _, err := apiData(json.RawMessage(`{"status":200}`)); err == nil {
		t.Fatal("want error for missing data")
	}
	if _, err := apiData(json.RawMessage(`{"status":200,"result":{"b":2}}`)); err == nil {
		t.Fatal("result alias is dead: want error for missing data")
	}
}

// TestParseReplays checks the pinned-field decoding: real key names only,
// daily lesson numbering, and hard errors on missing id/courBeginTime.
func TestParseReplays(t *testing.T) {
	raw := `{"status":200,"data":{"records":[
		{"id":111,"subjName":"数学","weekNumber":3,"week":2,"vodStatus":5,
		 "courBeginTime":"2026-09-28 08:00:00","courEndTime":"2026-09-28 09:40:00","tecName":"张"},
		{"id":222,"subjName":"数学","weekNumber":3,"week":2,"vodStatus":4,
		 "courBeginTime":"2026-09-28 10:00:00","courEndTime":"2026-09-28 11:40:00"},
		{"id":333,"vodStatus":6,"courBeginTime":"2026-09-29 08:00:00"}
	]}}`
	replays, err := parseReplays(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(replays) != 3 {
		t.Fatalf("got %d replays", len(replays))
	}
	r := replays[0]
	if r.ID != 111 || r.SubjName != "数学" || !r.Playable() || r.Status != "ready" {
		t.Fatalf("replay 0: %+v", r)
	}
	if got := r.BeginTime.Format("2006-01-02 15:04:05"); got != "2026-09-28 08:00:00" {
		t.Fatalf("beginTime %q", got)
	}
	if replays[1].DailyLesson != 2 || replays[1].Status != "repairing" || replays[1].Playable() {
		t.Fatalf("replay 1: %+v", replays[1])
	}
	if replays[2].DailyLesson != 1 || replays[2].Status != "unavailable" {
		t.Fatalf("replay 2: %+v", replays[2])
	}

	// a timestamp outside the pinned layout is a hard error
	if _, err := parseReplays(json.RawMessage(
		`{"status":200,"data":{"records":[{"id":9,"courBeginTime":"2026-09-28T08:00:00Z"}]}}`)); err == nil {
		t.Fatal("want error for unpinned timestamp shape")
	}

	// alias keys are dead: a record carrying only courseId has no id
	if _, err := parseReplays(json.RawMessage(
		`{"status":200,"data":{"records":[{"courseId":"9","courBeginTime":"2026-09-28 08:00:00"}]}}`)); err == nil {
		t.Fatal("want error for missing id")
	}
	if _, err := parseReplays(json.RawMessage(
		`{"status":200,"data":{"records":[{"id":9,"beginTime":"2026-09-28 08:00:00"}]}}`)); err == nil {
		t.Fatal("want error for missing courBeginTime")
	}
	// a string id is dead: ids are always JSON numbers
	if _, err := parseReplays(json.RawMessage(
		`{"status":200,"data":{"records":[{"id":"9","courBeginTime":"2026-09-28 08:00:00"}]}}`)); err == nil {
		t.Fatal("want error for string id")
	}
}

// TestParseViews checks the pinned-field decoding: url and viewNum only;
// missing fields are hard errors, alias keys are dead.
func TestParseViews(t *testing.T) {
	raw := `{"status":200,"data":{"id":111,"courseVodViewList":[
		{"viewNum":1,"url":"https://videos.example/a?key=1"},
		{"viewNum":5,"url":"https://videos.example/b?key=2"}
	]}}`
	views, err := parseViews(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[1].ViewNum != 5 {
		t.Fatalf("views %+v", views)
	}
	// playUrl is dead: url missing is an error
	if _, err := parseViews(json.RawMessage(
		`{"status":200,"data":{"id":1,"courseVodViewList":[{"viewNum":1,"playUrl":"https://x/y"}]}}`)); err == nil {
		t.Fatal("want error for missing url")
	}
	if _, err := parseViews(json.RawMessage(
		`{"status":200,"data":{"id":1,"courseVodViewList":[{"url":"https://x/y"}]}}`)); err == nil {
		t.Fatal("want error for missing viewNum")
	}
	if _, err := parseViews(json.RawMessage(
		`{"status":200,"data":{"id":1,"courseVodViewList":[]}}`)); err == nil {
		t.Fatal("want error for empty views")
	}
}

// TestParseLiveSessions checks the live-now filter over the three signals,
// with every record carrying the five pinned fields, and the hard errors on
// malformed records.
func TestParseLiveSessions(t *testing.T) {
	raw := `{"status":200,"data":{"records":[
		{"id":10,"liveDesc":"直播中","courLiveOpen":1,"liveEnable":1,"liveEndTime":1789695900000,"subjName":"数学","clroName":"东上院312"},
		{"id":11,"liveDesc":"未开始","courLiveOpen":0,"liveEnable":0,"liveEndTime":1789695901000},
		{"id":12,"liveDesc":"未开始","courLiveOpen":1,"liveEnable":0,"liveEndTime":1789695902000},
		{"id":13,"liveDesc":"未开始","courLiveOpen":0,"liveEnable":1,"liveEndTime":1789695903000}
	]}}`
	sessions, err := parseLiveSessions(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != 10 || sessions[0].Classroom != "东上院312" || sessions[0].LiveEndTime != 1789695900000 {
		t.Fatalf("session 0: %+v", sessions[0])
	}

	// no live session: records is an empty array, the key is always present
	sessions, err = parseLiveSessions(json.RawMessage(`{"status":200,"data":{"records":[]}}`))
	if err != nil || len(sessions) != 0 {
		t.Fatalf("empty live list: %v %v", sessions, err)
	}
	if _, err := parseLiveSessions(json.RawMessage(`{"status":200,"data":{}}`)); err == nil {
		t.Fatal("want error for missing records key")
	}
	if _, err := parseLiveSessions(json.RawMessage(
		`{"status":200,"data":{"records":[{"id":10,"liveDesc":"直播中","courLiveOpen":1,"liveEnable":1}]}}`)); err == nil {
		t.Fatal("want error for missing liveEndTime")
	}
	if _, err := parseLiveSessions(json.RawMessage(
		`{"status":200,"data":{"records":[{"liveDesc":"直播中","courLiveOpen":1,"liveEnable":1,"liveEndTime":1}]}}`)); err == nil {
		t.Fatal("want error for missing id")
	}
}

// TestParseLiveChannels checks that all three pinned fields are mandatory
// and account_token is always appended.
func TestParseLiveChannels(t *testing.T) {
	raw := `{"status":200,"data":{"courseDeviceViewDtoList":[
		{"chanNameMain":"东上院312老师","chanNameMainPlayUrl":"https://live.example/a.flv?auth_key=k1","mainTokenStr":"tok1"},
		{"chanNameMain":"东上院312课件","chanNameMainPlayUrl":"https://live.example/b.flv?auth_key=k2","mainTokenStr":"tok2"}
	]}}`
	channels, err := parseLiveChannels(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 {
		t.Fatalf("got %d channels: %+v", len(channels), channels)
	}
	if channels[0].URL != "https://live.example/a.flv?auth_key=k1&account_token=tok1" {
		t.Fatalf("account_token not appended: %s", channels[0].URL)
	}
	if channels[1].URL != "https://live.example/b.flv?auth_key=k2&account_token=tok2" {
		t.Fatalf("token must always be appended: %s", channels[1].URL)
	}

	if _, err := parseLiveChannels(json.RawMessage(`{"status":200,"data":{}}`)); err == nil {
		t.Fatal("want error for missing courseDeviceViewDtoList")
	}
	if _, err := parseLiveChannels(json.RawMessage(
		`{"status":200,"data":{"courseDeviceViewDtoList":[{"chanNameMain":"x","chanNameMainPlayUrl":"https://x/y"}]}}`)); err == nil {
		t.Fatal("want error for missing mainTokenStr")
	}
	if _, err := parseLiveChannels(json.RawMessage(
		`{"status":200,"data":{"courseDeviceViewDtoList":[{"chanNameMain":"x","mainTokenStr":"t"}]}}`)); err == nil {
		t.Fatal("want error for missing chanNameMainPlayUrl")
	}
	if _, err := parseLiveChannels(json.RawMessage(
		`{"status":200,"data":{"courseDeviceViewDtoList":[{"chanNameMainPlayUrl":"https://x/y","mainTokenStr":"t"}]}}`)); err == nil {
		t.Fatal("want error for missing chanNameMain")
	}
}

// rangeServer serves content with honest range support and records the
// largest requested right endpoint.
type rangeServer struct {
	t       *testing.T
	content []byte
	mu      sync.Mutex
	maxEnd  int64
}

// handler serves exact inclusive byte ranges.
func (s *rangeServer) handler(w http.ResponseWriter, r *http.Request) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(s.content)))
		w.Write(s.content)
		return
	}
	var begin, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &begin, &end); err != nil {
		s.t.Errorf("bad range header %q", rangeHeader)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if end > s.maxEnd {
		s.maxEnd = end
	}
	s.mu.Unlock()
	if end >= int64(len(s.content)) {
		s.t.Errorf("requested right endpoint %d exceeds content size %d (clamp missing)", end, len(s.content))
		end = int64(len(s.content)) - 1
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", begin, end, len(s.content)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(s.content[begin : end+1])
}

// TestProbeMedia checks size and range-support detection over httptest.
func TestProbeMedia(t *testing.T) {
	content := make([]byte, 12345)
	srv := &rangeServer{t: t, content: content}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	c := New(nil)
	size, ranges, err := c.ProbeMedia(context.Background(), ts.URL+"/media?key=secret")
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(content)) || !ranges {
		t.Fatalf("size %d ranges %v", size, ranges)
	}
}

// TestProbeMediaNoRange pins that a full-body 200 means range unsupported.
func TestProbeMediaNoRange(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "777")
		w.Write([]byte("x"))
	}))
	defer ts.Close()
	c := New(nil)
	size, ranges, err := c.ProbeMedia(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ranges {
		t.Fatal("plain 200 without range headers must not report range support")
	}
	if size != 777 {
		t.Fatalf("size from Content-Length: %d", size)
	}
}

// TestDownloadRanged verifies a multi-worker ranged download reassembles
// the source bytes exactly.
func TestDownloadRanged(t *testing.T) {
	// two full chunks plus a tail: every worker gets several chunks and the
	// last range is short
	content := make([]byte, 2*chunkSize+1000)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	srv := &rangeServer{t: t, content: content}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	c := New(nil)
	dst := make([]byte, len(content))
	var mu sync.Mutex
	var calls int
	done, err := c.Download(context.Background(), ts.URL+"/m?key=s", &sliceWriterAt{dst}, func(d, total int64) {
		mu.Lock()
		calls++
		mu.Unlock()
		if total != int64(len(content)) {
			t.Errorf("progress total %d, want %d", total, len(content))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if done != int64(len(content)) {
		t.Fatalf("done %d, want %d", done, len(content))
	}
	if !bytes.Equal(dst, content) {
		t.Fatal("downloaded bytes differ from source")
	}
	if calls == 0 {
		t.Fatal("progress never invoked")
	}
}

// TestDownloadSequentialFallback pins the plain GET path when the server
// ignores Range.
func TestDownloadSequentialFallback(t *testing.T) {
	content := []byte(strings.Repeat("abc", 5000))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ignores Range, always serves the whole body with 200
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	}))
	defer ts.Close()

	c := New(nil)
	dst := make([]byte, len(content))
	done, err := c.Download(context.Background(), ts.URL, &sliceWriterAt{dst}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done != int64(len(content)) || !bytes.Equal(dst, content) {
		t.Fatalf("sequential fallback: done %d", done)
	}
}

// TestDownloadUnknownSize pins the plain GET path when the server sends no
// Content-Length.
func TestDownloadUnknownSize(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // no Content-Length
	}))
	defer ts.Close()
	c := New(nil)
	if _, err := c.Download(context.Background(), ts.URL, &sliceWriterAt{nil}, nil); err == nil {
		t.Fatal("want error for unknown size")
	}
}

// sliceWriterAt adapts a byte slice to io.WriterAt.
type sliceWriterAt struct{ buf []byte }

// WriteAt writes into the fixed window beginning at off.
func (w *sliceWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > int64(len(w.buf)) {
		return 0, errors.New("overflow")
	}
	return copy(w.buf[off:], p), nil
}
