package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// isProjection reports whether displayPath names a metadata projection file:
// <collection>/<numericID>/info or assignments/<id>/submissions/latest.
// Paths may lead through term directories or symlinks, so the match looks at
// the trailing segments only.
func isProjection(displayPath string) bool {
	segs := strings.Split(strings.Trim(displayPath, "/"), "/")
	n := len(segs)
	if n >= 3 && segs[n-1] == "info" && isNumeric(segs[n-2]) {
		switch segs[n-3] {
		case "announcements", "discussions", "assignments":
			return true
		}
	}
	return n >= 4 && segs[n-1] == "latest" && segs[n-2] == "submissions" &&
		isNumeric(segs[n-3]) && segs[n-4] == "assignments"
}

// isAssignmentInfo reports whether displayPath is an assignment's info
// projection.
func isAssignmentInfo(displayPath string) bool {
	segs := strings.Split(strings.Trim(displayPath, "/"), "/")
	n := len(segs)
	return n >= 3 && segs[n-1] == "info" && isNumeric(segs[n-2]) && segs[n-3] == "assignments"
}

// isNumeric reports whether s is all digits (an entity ID path segment).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// renderCat renders one projection file's JSON content for the TUI. Known
// entity shapes get curated adapters; everything else gets the generic
// key-value renderer.
func renderCat(displayPath string, data []byte, stdout io.Writer) error {
	if isAssignmentInfo(displayPath) {
		return renderAssignment(data, stdout)
	}
	return renderKV(data, stdout)
}

// renderAssignment prints an assignment's info projection as a curated
// summary. The description is HTML from Canvas and is flattened to text.
func renderAssignment(data []byte, w io.Writer) error {
	var a canvas.Assignment
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("parse assignment projection: %w", err)
	}
	fmt.Fprintf(w, "name: %s\n", a.Name)
	if a.DueAt != "" {
		fmt.Fprintf(w, "due: %s\n", a.DueAt)
	}
	if a.UnlockAt != "" {
		fmt.Fprintf(w, "unlock: %s\n", a.UnlockAt)
	}
	if a.LockAt != "" {
		fmt.Fprintf(w, "lock: %s\n", a.LockAt)
	}
	if a.PointsPossible != nil {
		fmt.Fprintf(w, "points: %v\n", *a.PointsPossible)
	}
	if len(a.SubmissionTypes) > 0 {
		fmt.Fprintf(w, "submission types: %s\n", strings.Join(a.SubmissionTypes, ", "))
	}
	if len(a.AllowedExtensions) > 0 {
		fmt.Fprintf(w, "allowed extensions: %s\n", strings.Join(a.AllowedExtensions, ", "))
	}
	if a.ScoreStatistics != nil {
		s := a.ScoreStatistics
		fmt.Fprintf(w, "score statistics: min %v, max %v, mean %v\n", s.Min, s.Max, s.Mean)
	}
	if a.HTMLURL != "" {
		fmt.Fprintf(w, "url: %s\n", a.HTMLURL)
	}
	if text := htmlToText(a.Description); text != "" {
		fmt.Fprintf(w, "description:\n%s\n", indentLines(text, "  "))
	}
	return nil
}

// renderKV renders arbitrary projection JSON as indented key-value lines.
// Keys are sorted for stable output; null and empty values are omitted.
func renderKV(data []byte, w io.Writer) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("parse projection: %w", err)
	}
	writeKV(w, v, 0)
	return nil
}

// writeKV prints one decoded JSON value at the given indent depth.
func writeKV(w io.Writer, v any, depth int) {
	pad := strings.Repeat("  ", depth)
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := t[k]
			if isEmptyJSON(val) {
				continue
			}
			switch val.(type) {
			case map[string]any, []any:
				fmt.Fprintf(w, "%s%s:\n", pad, k)
				writeKV(w, val, depth+1)
			default:
				fmt.Fprintf(w, "%s%s: %v\n", pad, k, val)
			}
		}
	case []any:
		for _, item := range t {
			if isEmptyJSON(item) {
				continue
			}
			switch item.(type) {
			case map[string]any, []any:
				fmt.Fprintf(w, "%s-\n", pad)
				writeKV(w, item, depth+1)
			default:
				fmt.Fprintf(w, "%s- %v\n", pad, item)
			}
		}
	default:
		fmt.Fprintf(w, "%s%v\n", pad, t)
	}
}

// isEmptyJSON reports whether a decoded JSON value carries no information.
func isEmptyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

// indentLines prefixes every line of s with pad.
func indentLines(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

// blockElements are the HTML tags that imply a line break when flattening a
// description to plain text.
var blockElements = map[string]bool{
	"p": true, "div": true, "li": true, "tr": true, "table": true,
	"ul": true, "ol": true, "blockquote": true, "pre": true, "hr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// htmlToText flattens a Canvas HTML fragment to plain text: text content
// with newlines for block elements, blank lines and markup removed.
// Unparseable input comes back unchanged; the result is display text only.
func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type != html.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.Data {
		case "script", "style":
			return
		case "br":
			b.WriteByte('\n')
			return
		}
		block := blockElements[n.Data]
		if block {
			b.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			b.WriteByte('\n')
		}
	}
	walk(doc)

	lines := strings.Split(b.String(), "\n")
	out := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
