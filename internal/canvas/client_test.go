package canvas

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// TestListAllPagination pins the Link-header pagination contract: the first
// request carries per_page=100, rel="next" pointers are followed verbatim,
// and the walk stops when the header offers no next link.
func TestListAllPagination(t *testing.T) {
	var firstQuery string
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "", "1":
			firstQuery = r.URL.RawQuery
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v1/widgets?page=2&per_page=100>; rel="next"`, serverURL(r)))
			fmt.Fprint(w, `[{"id":1},{"id":2}]`)
		case "2":
			// No Link header: this is the last page.
			fmt.Fprint(w, `[{"id":3}]`)
		}
	}))
	defer server.Close()

	client := New(session.NewToken("tok"), server.URL)
	items, err := listAll[struct {
		ID int `json:"id"`
	}](context.Background(), client, server.URL+"/api/v1/widgets")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 across 2 pages", len(items))
	}
	if !strings.Contains(firstQuery, "per_page=100") {
		t.Errorf("first request query = %q, want per_page=100", firstQuery)
	}
	if len(pages) != 2 {
		t.Fatalf("requests = %v, want exactly 2", pages)
	}
}

// serverURL rebuilds the request's origin for Link header construction.
func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

// TestListCoursesFilter pins that date-restricted courses are dropped.
func TestListCoursesFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "include[]=teachers") {
			t.Errorf("query %q missing include[]=teachers", r.URL.RawQuery)
		}
		fmt.Fprint(w, `[
			{"id":1,"name":"开放课程","term":{"id":1,"name":"2025-2026-1"}},
			{"id":2,"name":"封存课程","access_restricted_by_date":true,"term":{"id":1,"name":"2025-2026-1"}}
		]`)
	}))
	defer server.Close()

	courses, err := New(session.NewToken("tok"), server.URL).ListCourses(context.Background())
	if err != nil {
		t.Fatalf("ListCourses: %v", err)
	}
	if len(courses) != 1 || courses[0].Name != "开放课程" {
		t.Errorf("courses = %+v, want only the unrestricted one", courses)
	}
}
