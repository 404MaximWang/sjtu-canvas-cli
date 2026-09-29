package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestIsProjection pins the projection path shapes the TUI renders: entity
// info files and assignment submission files, reachable through any prefix
// (term directory, symlink chain).
func TestIsProjection(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/courses/93929/assignments/419965/info", true},
		{"/courses/current/93929/assignments/419965/info", true},
		{"/courses/93929/assignments/419965/submissions/latest", true},
		{"/courses/93929/announcements/81001/info", true},
		{"/courses/93929/discussions/81002/info", true},
		{"/courses/93929/files/lectures/info", false}, // course file named info
		{"/courses/93929/assignments/419965", false},  // the entity directory itself
		{"/courses/93929/files/data.json", false},     // real uploaded JSON
		{"/courses/93929/assignments/419965/extra", false},
	}
	for _, c := range cases {
		if got := isProjection(c.path); got != c.want {
			t.Errorf("isProjection(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestRenderKV checks the generic projection renderer: sorted keys, indented
// nesting, and omission of null and empty values.
func TestRenderKV(t *testing.T) {
	data := []byte(`{"grade": "95", "empty": "", "nothing": null,
		"comments": [{"comment": "good", "media_comment": null}],
		"workflow_state": "graded"}`)
	var out bytes.Buffer
	if err := renderKV(data, &out); err != nil {
		t.Fatalf("renderKV: %v", err)
	}
	want := "comments:\n  -\n    comment: good\ngrade: 95\nworkflow_state: graded\n"
	if out.String() != want {
		t.Errorf("renderKV output:\n%s\nwant:\n%s", out.String(), want)
	}
}

// TestRenderAssignment checks the curated adapter: key fields present, HTML
// description flattened to text.
func TestRenderAssignment(t *testing.T) {
	data := []byte(`{"id":419965,"name":"作业 1","course_id":93929,
		"description":"<p>请设计一个 <b>API</b></p><ul><li>要求一</li><li>要求二</li></ul>",
		"due_at":"2026-09-30T15:59:00Z","points_possible":10,
		"submission_types":["online_upload"],"html_url":"https://oc.sjtu.edu.cn/x"}`)
	var out bytes.Buffer
	if err := renderAssignment(data, &out); err != nil {
		t.Fatalf("renderAssignment: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"name: 作业 1\n", "due: 2026-09-30T15:59:00Z\n", "points: 10\n",
		"submission types: online_upload\n",
		"description:\n  请设计一个 API\n  要求一\n  要求二\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderAssignment missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<p>") || strings.Contains(got, "<li>") {
		t.Errorf("renderAssignment leaked HTML markup:\n%s", got)
	}
}

// TestHTMLToText checks block flattening, entity decoding, and script
// removal on messy fragments.
func TestHTMLToText(t *testing.T) {
	in := `<h2>标题</h2><p>第一行<br>第二行 &amp; 更多</p><script>alert(1)</script>`
	want := "标题\n第一行\n第二行 & 更多"
	if got := htmlToText(in); got != want {
		t.Errorf("htmlToText = %q, want %q", got, want)
	}
}
