package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// apiData unwraps the {status, message, data} envelope every video API
// response carries. status is always present and must be 200; the "result"
// key was never observed and is not accepted.
func apiData(raw json.RawMessage) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("video API returned non-JSON: %w", err)
	}
	status, ok := valueAsInt64(value["status"])
	if !ok {
		return nil, errors.New("video service envelope is missing status")
	}
	if status != 200 {
		if message := str(value, "message"); message != "" {
			return nil, errors.New(message)
		}
		return nil, fmt.Errorf("video service returned status %d", status)
	}
	data, ok := value["data"].(map[string]any)
	if !ok {
		return nil, errors.New("video service returned no data")
	}
	return data, nil
}

// valueAsInt64 accepts JSON numbers only; the platform never sends numeric
// strings.
func valueAsInt64(v any) (int64, bool) {
	n, ok := v.(float64)
	return int64(n), ok
}

// valueAsString accepts JSON strings only.
func valueAsString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// str returns the record's string value under the one pinned key.
func str(m map[string]any, key string) string {
	s, _ := valueAsString(m[key])
	return s
}

// timeLayout is the one timestamp shape the platform emits.
const timeLayout = "2006-01-02 15:04:05"

// parseTime parses one upstream timestamp; unparseable input is an error.
func parseTime(s string) (time.Time, error) {
	return time.ParseInLocation(timeLayout, s, time.Local)
}

// dailyLessonNumbers assigns each record its 1-based index within its
// calendar day, ordered by begin time. Records are pre-validated to carry
// courBeginTime.
func dailyLessonNumbers(records []map[string]any) map[int]int64 {
	byDay := map[string][]int{}
	for i, record := range records {
		date, _, _ := strings.Cut(str(record, "courBeginTime"), " ")
		byDay[date] = append(byDay[date], i)
	}
	out := map[int]int64{}
	for _, indices := range byDay {
		sort.Slice(indices, func(a, b int) bool {
			return str(records[indices[a]], "courBeginTime") <
				str(records[indices[b]], "courBeginTime")
		})
		for lesson, recordIndex := range indices {
			out[recordIndex] = int64(lesson + 1)
		}
	}
	return out
}

// parseReplays decodes the subject_vod_list_new response. Field names are
// pinned to the sampled reality: a record missing id or courBeginTime is an
// error, never skipped and never aliased.
func parseReplays(raw json.RawMessage) ([]Replay, error) {
	data, err := apiData(raw)
	if err != nil {
		return nil, err
	}
	recordsRaw, ok := data["records"].([]any)
	if !ok {
		return nil, errors.New("video list is missing records")
	}
	records := make([]map[string]any, 0, len(recordsRaw))
	for i, r := range recordsRaw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("record %d is not an object", i)
		}
		if _, ok := valueAsInt64(m["id"]); !ok {
			return nil, fmt.Errorf("record %d is missing id", i)
		}
		if str(m, "courBeginTime") == "" {
			return nil, fmt.Errorf("record %d is missing courBeginTime", i)
		}
		records = append(records, m)
	}
	lessons := dailyLessonNumbers(records)

	replays := make([]Replay, 0, len(records))
	for i, record := range records {
		id, _ := valueAsInt64(record["id"])
		begin, err := parseTime(str(record, "courBeginTime"))
		if err != nil {
			return nil, fmt.Errorf("record %d: bad courBeginTime %q", id, str(record, "courBeginTime"))
		}
		var end time.Time
		if s := str(record, "courEndTime"); s != "" {
			end, err = parseTime(s)
			if err != nil {
				return nil, fmt.Errorf("record %d: bad courEndTime %q", id, s)
			}
		}
		weekNumber, _ := valueAsInt64(record["weekNumber"])
		weekDay, _ := valueAsInt64(record["week"])
		status, _ := valueAsInt64(record["vodStatus"])

		replays = append(replays, Replay{
			ID:          id,
			SubjName:    str(record, "subjName"),
			Teacher:     str(record, "tecName"),
			Classroom:   str(record, "classRoomName"),
			BeginTime:   begin,
			EndTime:     end,
			WeekNumber:  weekNumber,
			WeekDay:     weekDay,
			DailyLesson: lessons[i],
			VodStatus:   status,
			Status:      availability(status),
		})
	}
	return replays, nil
}

// availability maps vodStatus to a stable label: 5 ready, 4 repairing,
// 6 unavailable, anything else unavailable.
func availability(status int64) string {
	switch status {
	case 5:
		return "ready"
	case 4:
		return "repairing"
	default:
		return "unavailable"
	}
}

// parseViews decodes the course_vod_urls_new response into per-view URLs.
// Fields are pinned to the sampled reality (each entry carries url and
// viewNum); a view missing either is an error, never skipped and never
// aliased.
func parseViews(raw json.RawMessage) ([]View, error) {
	data, err := apiData(raw)
	if err != nil {
		return nil, err
	}
	viewsRaw, _ := data["courseVodViewList"].([]any)
	views := make([]View, 0, len(viewsRaw))
	for _, v := range viewsRaw {
		view, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("view entry is not an object")
		}
		u := str(view, "url")
		if u == "" {
			return nil, errors.New("view is missing url")
		}
		viewNum, ok := valueAsInt64(view["viewNum"])
		if !ok {
			return nil, errors.New("view is missing viewNum")
		}
		views = append(views, View{ViewNum: viewNum, URL: u})
	}
	if len(views) == 0 {
		return nil, errors.New("no playable video source was returned")
	}
	return views, nil
}

// parseLiveSessions decodes the vod_live response and keeps only sessions
// broadcasting now. The records key is always present (empty array when
// nothing is live); malformed live records are errors, never skipped.
func parseLiveSessions(raw json.RawMessage) ([]LiveSession, error) {
	data, err := apiData(raw)
	if err != nil {
		return nil, err
	}
	recordsRaw, ok := data["records"].([]any)
	if !ok {
		return nil, errors.New("live list is missing records")
	}
	var sessions []LiveSession
	for i, r := range recordsRaw {
		record, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("record %d is not an object", i)
		}
		open, _ := valueAsInt64(record["courLiveOpen"])
		enable, _ := valueAsInt64(record["liveEnable"])
		if str(record, "liveDesc") != "直播中" && open != 1 && enable != 1 {
			continue
		}
		id, ok := valueAsInt64(record["id"])
		if !ok {
			return nil, fmt.Errorf("record %d is missing id", i)
		}
		endTime, ok := valueAsInt64(record["liveEndTime"])
		if !ok {
			return nil, fmt.Errorf("record %d is missing liveEndTime", i)
		}
		sessions = append(sessions, LiveSession{
			ID:          id,
			CourseName:  str(record, "subjName"),
			Classroom:   str(record, "clroName"),
			LiveEndTime: endTime,
		})
	}
	return sessions, nil
}

// parseLiveChannels decodes the course_vod_videoinfos response, appending
// account_token to each channel URL. The endpoint only answers while a
// session is live, so a missing list or a channel lacking any of its three
// pinned fields is an error, never skipped.
func parseLiveChannels(raw json.RawMessage) ([]LiveChannel, error) {
	data, err := apiData(raw)
	if err != nil {
		return nil, err
	}
	listRaw, ok := data["courseDeviceViewDtoList"].([]any)
	if !ok {
		return nil, errors.New("video infos are missing courseDeviceViewDtoList")
	}
	channels := make([]LiveChannel, 0, len(listRaw))
	for i, c := range listRaw {
		channel, ok := c.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("channel %d is not an object", i)
		}
		playURL := str(channel, "chanNameMainPlayUrl")
		if playURL == "" {
			return nil, fmt.Errorf("channel %d is missing chanNameMainPlayUrl", i)
		}
		token := str(channel, "mainTokenStr")
		if token == "" {
			return nil, fmt.Errorf("channel %d is missing mainTokenStr", i)
		}
		name := str(channel, "chanNameMain")
		if name == "" {
			return nil, fmt.Errorf("channel %d is missing chanNameMain", i)
		}
		channels = append(channels, LiveChannel{
			Name: name,
			URL:  playURL + "&account_token=" + token,
		})
	}
	return channels, nil
}

// jwtFromLocation extracts the jwt_token carried by the launch redirect,
// from either the fragment ("...#jwt_token=...") or the query. The value is
// URL-decoded.
func jwtFromLocation(location string) (string, error) {
	rest := location
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		rest = rest[i+1:]
	}
	for _, pair := range strings.Split(rest, "&") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key != "jwt_token" {
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			return "", fmt.Errorf("jwt_token undecodable: %w", err)
		}
		return decoded, nil
	}
	return "", errors.New("jwt_token not found in launch redirect")
}

// parseLaunchForm extracts the action and input fields of the first form in
// an auto-submitting launch page.
func parseLaunchForm(body []byte) (string, url.Values, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", nil, fmt.Errorf("parse launch page: %w", err)
	}
	form := findElement(doc, "form")
	if form == nil {
		return "", nil, errors.New("no launch form found")
	}
	action := attr(form, "action")
	if action == "" {
		return "", nil, errors.New("launch form has no action")
	}
	fields := url.Values{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "input" {
				if name := attr(c, "name"); name != "" {
					fields.Set(name, attr(c, "value"))
				}
			}
			walk(c)
		}
	}
	walk(form)
	return action, fields, nil
}

// findElement returns the first element with the given tag name.
func findElement(root *html.Node, tag string) *html.Node {
	if root.Type == html.ElementNode && root.Data == tag {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// attr reads one attribute, "" when absent.
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
