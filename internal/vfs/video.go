package vfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strconv"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// VideoSource provides the v.sjtu.edu.cn data behind /courses/<id>/replay
// and /courses/<id>/live. *video.Client implements it directly. A nil
// VideoSource leaves both directories empty (tests, token-only setups).
type VideoSource interface {
	Replays(ctx context.Context, courseID int64) ([]video.Replay, error)
	ReplayViews(ctx context.Context, courseID int64, replayID int64) ([]video.View, error)
	Subtitle(ctx context.Context, courseID int64, replayID int64) (json.RawMessage, error)
	PPT(ctx context.Context, courseID int64, replayID int64) (json.RawMessage, error)
	Summary(ctx context.Context, courseID int64, replayID int64) (json.RawMessage, error)
	LiveSessions(ctx context.Context, courseID int64) ([]video.LiveSession, error)
	LiveChannels(ctx context.Context, courseID, sessionID int64) ([]video.LiveChannel, error)
	OpenMedia(ctx context.Context, rawURL string) (io.ReadCloser, error)
	ProbeMedia(ctx context.Context, rawURL string) (size int64, ranges bool, err error)
	FetchRange(ctx context.Context, rawURL string, begin, endInclusive int64) (io.ReadCloser, error)
}

// RangeFetch streams bytes [begin, endInclusive] of a ranged-capable file.
type RangeFetch func(ctx context.Context, begin, endInclusive int64) (io.ReadCloser, error)

// RangedFile resolves name and reports its ranged-download capability: the
// probed total size and a range fetcher. ok is false for ordinary files.
func (f *FS) RangedFile(name string) (size int64, fetch RangeFetch, ok bool, err error) {
	n, err := f.resolve(name, true, 0)
	if err != nil {
		return 0, nil, false, err
	}
	if n.ranged == nil {
		return 0, nil, false, nil
	}
	return n.size, n.ranged, true, nil
}

// errReplayNotPlayable rejects url/video reads of replays whose vodStatus
// is not "ready" (repairing, unavailable).
var errReplayNotPlayable = errors.New("replay not playable yet (see info's vodStatus)")

// listReplay builds the children of /courses/<courseID>/replay: one entity
// directory per recording plus MM-DD-N symlinks derived from courBeginTime.
func (f *FS) listReplay(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		if f.video == nil {
			return nil, nil
		}
		replays, err := f.video.Replays(f.ctx, courseID)
		if err != nil {
			return nil, err
		}
		nodes := make([]node, 0, 2*len(replays))
		for _, r := range replays {
			nodes = append(nodes, f.replayNode(courseID, r))
			if link, ok := replaySymlink(courseID, r); ok {
				nodes = append(nodes, link)
			}
		}
		sortNodes(nodes)
		return nodes, nil
	}
}

// replaySymlink derives the MM-DD-N alias of one replay. Begin times are
// validated when the list is parsed; the zero-time guard only covers
// hand-built fixtures.
func replaySymlink(courseID int64, r video.Replay) (node, bool) {
	if r.BeginTime.IsZero() {
		return node{}, false
	}
	t := r.BeginTime
	name := fmt.Sprintf("%02d-%02d-%d", t.Month(), t.Day(), r.DailyLesson)
	target := "/courses/" + strconv.FormatInt(courseID, 10) + "/replay/" + strconv.FormatInt(r.ID, 10)
	return symlinkNode(name, target), true
}

// weekdayAbbr renders the ASCII weekday abbreviation; day 1 is Monday.
var weekdayAbbr = []string{"", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// replayName composes one replay's display name for the info projection:
// "<subjName>-W<week>-<Day>-L<lesson>". Without week info the schedule part
// is the begin timestamp.
func replayName(r video.Replay) string {
	var schedule string
	if r.WeekNumber > 0 && r.WeekDay >= 1 && r.WeekDay <= 7 {
		schedule = fmt.Sprintf("W%d-%s-L%d", r.WeekNumber, weekdayAbbr[r.WeekDay], r.DailyLesson)
	} else {
		schedule = r.BeginTime.Format("2006-01-02T15:04:05")
	}
	if r.SubjName != "" {
		return r.SubjName + "-" + schedule
	}
	return schedule
}

// replayNode builds one recording's entity directory, named by its video id.
func (f *FS) replayNode(courseID int64, r video.Replay) node {
	return node{
		name:    strconv.FormatInt(r.ID, 10),
		mode:    fs.ModeDir | 0o555,
		modTime: r.BeginTime,
		list:    f.replayChildren(courseID, r),
	}
}

// replayInfo is the info projection of one replay.
func replayInfo(r video.Replay) map[string]any {
	info := map[string]any{
		"id":          r.ID,
		"name":        replayName(r),
		"subject":     r.SubjName,
		"teacher":     r.Teacher,
		"classroom":   r.Classroom,
		"weekNumber":  r.WeekNumber,
		"weekDay":     r.WeekDay,
		"dailyLesson": r.DailyLesson,
		"beginTime":   r.BeginTime.Format("2006-01-02 15:04:05"),
		"vodStatus":   r.VodStatus,
		"status":      r.Status,
	}
	if !r.EndTime.IsZero() {
		info["endTime"] = r.EndTime.Format("2006-01-02 15:04:05")
		info["durationSeconds"] = int64(r.EndTime.Sub(r.BeginTime).Seconds())
	}
	return info
}

// replayChildren loads one replay directory: the info projection eagerly,
// url/subtitle/ppt/summary lazily, and one media node per view. Entering
// the directory of a playable replay costs one ReplayViews call plus one
// size probe per view.
func (f *FS) replayChildren(courseID int64, r video.Replay) func() ([]node, error) {
	return func() ([]node, error) {
		info, err := jsonFileNode("info", replayInfo(r), "")
		if err != nil {
			return nil, err
		}
		children := []node{info}

		if !r.Playable() {
			// url/video reads of non-ready replays fail with a structured
			// error; subtitle/ppt/summary stay live and let the API decide.
			children = append(children, node{
				name: "url",
				mode: 0o444,
				open: func() (io.ReadCloser, error) { return nil, errReplayNotPlayable },
			})
		} else {
			views, err := f.video.ReplayViews(f.ctx, courseID, r.ID)
			if err != nil {
				return nil, err
			}
			urlNode, err := jsonFileNode("url", urlProjection(views), "")
			if err != nil {
				return nil, err
			}
			children = append(children, urlNode)
			for _, v := range views {
				children = append(children, f.mediaNode(v))
			}
		}

		children = append(children,
			f.projectionNode("subtitle", func() (json.RawMessage, error) { return f.video.Subtitle(f.ctx, courseID, r.ID) }),
			f.projectionNode("ppt", func() (json.RawMessage, error) { return f.video.PPT(f.ctx, courseID, r.ID) }),
			f.projectionNode("summary", func() (json.RawMessage, error) { return f.video.Summary(f.ctx, courseID, r.ID) }),
		)
		sortNodes(children)
		return children, nil
	}
}

// urlProjection is the content of a replay's url file.
func urlProjection(views []video.View) map[string]any {
	list := make([]map[string]any, 0, len(views))
	for _, v := range views {
		list = append(list, map[string]any{"viewNum": v.ViewNum, "url": v.URL})
	}
	return map[string]any{
		"views":   list,
		"headers": map[string]string{"Referer": video.Referer()},
	}
}

// projectionNode builds a file whose JSON content is fetched on open. Size
// is reported as 0 until read: the content length is unknowable without the
// fetch, and fs.FileInfo has no error channel.
func (f *FS) projectionNode(name string, fetch func() (json.RawMessage, error)) node {
	return node{
		name: name,
		mode: 0o444,
		open: func() (io.ReadCloser, error) {
			data, err := fetch()
			if err != nil {
				return nil, err
			}
			data = append(data, '\n')
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}
}

// mediaNode builds one video-N byte node. The size probe runs here — once
// per entered replay directory — so Stat and ranged downloads know the
// total without a second request.
func (f *FS) mediaNode(v video.View) node {
	n := node{
		name: fmt.Sprintf("video-%d", v.ViewNum),
		mode: 0o444,
		open: func() (io.ReadCloser, error) { return f.video.OpenMedia(f.ctx, v.URL) },
		ranged: func(ctx context.Context, begin, end int64) (io.ReadCloser, error) {
			return f.video.FetchRange(ctx, v.URL, begin, end)
		},
	}
	size, ranges, err := f.video.ProbeMedia(f.ctx, v.URL)
	if err != nil {
		// The node stays readable via OpenMedia; only ranged downloads lose.
		slog.Warn("media size probe failed", "view", v.ViewNum, "error", err)
		return n
	}
	n.size = size
	if !ranges {
		n.ranged = nil
	}
	return n
}

// listLive builds the children of /courses/<courseID>/live: one directory
// per session broadcasting now; empty when nothing is live.
func (f *FS) listLive(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		if f.video == nil {
			return nil, nil
		}
		sessions, err := f.video.LiveSessions(f.ctx, courseID)
		if err != nil {
			return nil, err
		}
		nodes := make([]node, 0, len(sessions))
		for _, s := range sessions {
			nodes = append(nodes, node{
				name: strconv.FormatInt(s.ID, 10),
				mode: fs.ModeDir | 0o555,
				list: f.liveChildren(courseID, s),
			})
		}
		sortNodes(nodes)
		return nodes, nil
	}
}

// liveChildren loads one live session directory: the info projection plus
// one JSON file per channel carrying its .flv URL.
func (f *FS) liveChildren(courseID int64, s video.LiveSession) func() ([]node, error) {
	return func() ([]node, error) {
		channels, err := f.video.LiveChannels(f.ctx, courseID, s.ID)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(channels))
		for _, c := range channels {
			names = append(names, c.Name)
		}
		info, err := jsonFileNode("info", map[string]any{
			"sessionId":   s.ID,
			"course":      s.CourseName,
			"classroom":   s.Classroom,
			"liveEndTime": s.LiveEndTime,
			"channels":    names,
		}, "")
		if err != nil {
			return nil, err
		}
		children := []node{info}
		seen := map[string]bool{}
		for i, c := range channels {
			name := sanitizeName(c.Name)
			if name == "" {
				name = "channel"
			}
			name = uniqueName(seen, name, int64(i))
			channelNode, err := jsonFileNode(name, map[string]any{
				"url":     c.URL,
				"headers": map[string]string{"Referer": video.Referer()},
			}, "")
			if err != nil {
				return nil, err
			}
			children = append(children, channelNode)
		}
		sortNodes(children)
		return children, nil
	}
}
