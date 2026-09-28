package canvas

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// Client is the read-side Canvas data layer. It knows endpoints and
// pagination; it knows nothing about commands, caching, or output formats.
type Client struct {
	sess *session.Session
	base string // Canvas root URL without a trailing slash
}

// New builds a Client for the given authenticated session and Canvas base
// URL (e.g. config.DefaultCanvasBaseURL).
func New(sess *session.Session, baseURL string) *Client {
	return &Client{sess: sess, base: strings.TrimRight(baseURL, "/")}
}

// perPage is the Canvas page size requested on every list endpoint. The
// server echoes it through the rel="next" links, so it is attached once to
// the first request.
const perPage = 100

// calendarBatchSize bounds how many context codes one calendar_events
// request may carry, matching the blueprint's proven batching.
const calendarBatchSize = 10

// linkNext matches one RFC 5988 Link header entry and captures its URL and
// rel value.
var linkNext = regexp.MustCompile(`<([^>]*)>\s*;\s*rel="([^"]*)"`)

// nextPageURL extracts the rel="next" target from a Link header, or "" when
// the current page is the last.
func nextPageURL(linkHeader string) string {
	for _, m := range linkNext.FindAllStringSubmatch(linkHeader, -1) {
		if m[2] == "next" {
			return m[1]
		}
	}
	return ""
}

// listAll fetches a paginated Canvas list endpoint to exhaustion, following
// Link header rel="next" pointers. The first request carries per_page=100;
// subsequent requests use the server-supplied next URLs verbatim.
func listAll[T any](ctx context.Context, c *Client, url string) ([]T, error) {
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	next := fmt.Sprintf("%s%sper_page=%d", url, sep, perPage)
	var all []T
	for next != "" {
		var page []T
		header, err := c.sess.DoJSONHeader(ctx, "GET", next, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next = nextPageURL(header.Get("Link"))
	}
	return all, nil
}

// ListCourses returns every course the caller is enrolled in, with teachers
// and term embedded. Courses whose access is date-restricted are dropped,
// matching the blueprint: their content endpoints would fail anyway.
func (c *Client) ListCourses(ctx context.Context) ([]Course, error) {
	courses, err := listAll[Course](ctx, c,
		c.base+"/api/v1/courses?include[]=teachers&include[]=term")
	if err != nil {
		return nil, err
	}
	kept := courses[:0]
	for _, course := range courses {
		if !course.AccessRestrictedByDate {
			kept = append(kept, course)
		}
	}
	return kept, nil
}

// ListAssignments returns all assignments of a course, with the caller's own
// submission, due-date overrides and score statistics embedded.
func (c *Client) ListAssignments(ctx context.Context, courseID int64) ([]Assignment, error) {
	url := fmt.Sprintf("%s/api/v1/courses/%d/assignments"+
		"?include[]=submission&include[]=overrides&include[]=all_dates&include[]=score_statistics",
		c.base, courseID)
	return listAll[Assignment](ctx, c, url)
}

// ListFiles returns every file of a course, flat across all folders.
func (c *Client) ListFiles(ctx context.Context, courseID int64) ([]File, error) {
	url := fmt.Sprintf("%s/api/v1/courses/%d/files", c.base, courseID)
	return listAll[File](ctx, c, url)
}

// ListFolders returns every folder of a course, flat across the tree.
func (c *Client) ListFolders(ctx context.Context, courseID int64) ([]Folder, error) {
	url := fmt.Sprintf("%s/api/v1/courses/%d/folders", c.base, courseID)
	return listAll[Folder](ctx, c, url)
}

// ListAnnouncements returns a course's announcements. Canvas serves
// announcements from the discussion-topics endpoint with
// only_announcements=true; there is no separate course-scoped endpoint.
func (c *Client) ListAnnouncements(ctx context.Context, courseID int64) ([]DiscussionTopic, error) {
	url := fmt.Sprintf("%s/api/v1/courses/%d/discussion_topics?only_announcements=true", c.base, courseID)
	return listAll[DiscussionTopic](ctx, c, url)
}

// ListDiscussions returns a course's discussion topics, announcements
// excluded.
func (c *Client) ListDiscussions(ctx context.Context, courseID int64) ([]DiscussionTopic, error) {
	url := fmt.Sprintf("%s/api/v1/courses/%d/discussion_topics", c.base, courseID)
	return listAll[DiscussionTopic](ctx, c, url)
}

// ListCalendarEvents returns assignment-type calendar events for the given
// context codes (e.g. "course_12345") inside [startDate, endDate], dates
// formatted as YYYY-MM-DD. Context codes are batched 10 per request,
// matching the blueprint's proven limit.
func (c *Client) ListCalendarEvents(ctx context.Context, contextCodes []string, startDate, endDate string) ([]CalendarEvent, error) {
	var all []CalendarEvent
	for start := 0; start < len(contextCodes); start += calendarBatchSize {
		end := min(start+calendarBatchSize, len(contextCodes))
		var query strings.Builder
		for _, code := range contextCodes[start:end] {
			fmt.Fprintf(&query, "&context_codes[]=%s", code)
		}
		url := fmt.Sprintf("%s/api/v1/calendar_events?type=assignment%s&start_date=%s&end_date=%s",
			c.base, query.String(), startDate, endDate)
		events, err := listAll[CalendarEvent](ctx, c, url)
		if err != nil {
			return nil, err
		}
		all = append(all, events...)
	}
	return all, nil
}

// GetMySubmission returns the caller's own submission to one assignment,
// with grader comments embedded.
func (c *Client) GetMySubmission(ctx context.Context, courseID, assignmentID int64) (*Submission, error) {
	url := fmt.Sprintf("%s/api/v1/courses/%d/assignments/%d/submissions/self?include[]=submission_comments",
		c.base, courseID, assignmentID)
	var sub Submission
	if err := c.sess.DoJSON(ctx, "GET", url, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// Download opens the file's content stream; the caller must close it. The
// download URL comes from the File entity and already carries its access
// verifier in the query string, so no extra credentials are attached.
func (c *Client) Download(ctx context.Context, file File) (io.ReadCloser, error) {
	resp, err := c.sess.DoStream(ctx, "GET", file.URL)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
