package vfs

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// listCourses builds the children of /courses: one directory per course
// (named by numeric ID), one directory per term, and the current-term
// symlink.
func (f *FS) listCourses() ([]node, error) {
	courses, err := f.src.Courses(f.ctx)
	if err != nil {
		return nil, err
	}

	// Sort by ID so name-collision suffixes are stable across fetches.
	sort.Slice(courses, func(i, j int) bool { return courses[i].ID < courses[j].ID })

	var nodes []node
	termEntries := map[string][]node{}       // term directory name → symlink entries
	termSeen := map[string]map[string]bool{} // per-term name uniqueness
	termByName := map[string]canvas.Term{}   // term directory name → term record
	for _, course := range courses {
		id := strconv.FormatInt(course.ID, 10)
		courseID := course.ID
		nodes = append(nodes, dirNode(id, func() ([]node, error) {
			return f.listCourseDir(courseID)
		}))

		termName := termLabel(course.Term)
		if termName == "" {
			// A course without a usable term label stays reachable by ID
			// only.
			continue
		}
		if _, ok := termByName[termName]; !ok {
			termByName[termName] = course.Term
			termSeen[termName] = map[string]bool{}
		}
		seen := termSeen[termName]
		// The numeric ID symlink claims its name first, so a course
		// literally named like an ID cannot shadow the canonical link.
		idLink := symlinkNode(id, "/courses/"+id)
		seen[id] = true
		nameLink := symlinkNode(uniqueName(seen, sanitizeName(course.Name), course.ID),
			"/courses/"+termName+"/"+id)
		termEntries[termName] = append(termEntries[termName], idLink, nameLink)
	}

	for termName, entries := range termEntries {
		entries := entries
		nodes = append(nodes, dirNode(termName, func() ([]node, error) {
			sortNodes(entries)
			return entries, nil
		}))
	}

	if current := currentTermName(termByName, f.now()); current != "" {
		nodes = append(nodes, symlinkNode("current", "/courses/"+current))
	}

	sortNodes(nodes)
	return nodes, nil
}

// canonicalTermName matches SJTU Canvas's term naming: "2026-2027 Fall".
var canonicalTermName = regexp.MustCompile(`^(\d{4}-\d{4}) (Fall|Spring|Summer)$`)

// termSeason maps the season word to SJTU's academic semester number.
var termSeason = map[string]int{"Fall": 1, "Spring": 2, "Summer": 3}

// termLabel derives the term's 202X-202Y-Z directory name from its Canvas
// name: "2026-2027 Fall" → "2026-2027-1". Any name outside the canonical
// pattern yields "": the course gets no term directory and stays reachable
// by ID only. No label is ever guessed.
func termLabel(term canvas.Term) string {
	if m := canonicalTermName.FindStringSubmatch(term.Name); m != nil {
		return fmt.Sprintf("%s-%d", m[1], termSeason[m[2]])
	}
	return ""
}

// listCourseDir builds the children of /courses/<courseID>.
func (f *FS) listCourseDir(courseID int64) ([]node, error) {
	return []node{
		dirNode("announcements", f.listAnnouncements(courseID)),
		dirNode("assignments", f.listAssignments(courseID)),
		dirNode("attendance", f.listAttendance(courseID)),
		dirNode("discussions", f.listDiscussions(courseID)),
		dirNode("files", f.listFilesRoot(courseID)),
		dirNode("live", f.listLive(courseID)),
		dirNode("replay", f.listReplay(courseID)),
	}, nil
}

// listAnnouncements builds one entity directory per announcement.
func (f *FS) listAnnouncements(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		topics, err := f.src.Announcements(f.ctx, courseID)
		if err != nil {
			return nil, err
		}
		return topicNodes(topics)
	}
}

// listDiscussions builds one entity directory per discussion topic.
func (f *FS) listDiscussions(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		topics, err := f.src.Discussions(f.ctx, courseID)
		if err != nil {
			return nil, err
		}
		return topicNodes(topics)
	}
}

// topicNodes converts discussion topics to sorted entity directories, each
// holding an info projection.
func topicNodes(topics []canvas.DiscussionTopic) ([]node, error) {
	nodes := make([]node, 0, len(topics))
	for _, topic := range topics {
		info, err := jsonFileNode("info", topic, topic.UpdatedAt)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, entityDirNode(topic.ID, topic.UpdatedAt, []node{info}))
	}
	sortNodes(nodes)
	return nodes, nil
}

// listAssignments builds one entity directory per assignment.
func (f *FS) listAssignments(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		assignments, err := f.src.Assignments(f.ctx, courseID)
		if err != nil {
			return nil, err
		}
		nodes := make([]node, 0, len(assignments))
		for _, a := range assignments {
			n, err := assignmentNode(a)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, n)
		}
		sortNodes(nodes)
		return nodes, nil
	}
}

// assignmentNode builds one assignment's entity directory: the info
// projection plus submissions/latest when Canvas embeds my submission.
func assignmentNode(a canvas.Assignment) (node, error) {
	info, err := jsonFileNode("info", a, a.UpdatedAt)
	if err != nil {
		return node{}, err
	}
	children := []node{info}
	if a.Submission != nil {
		latest, err := jsonFileNode("latest", *a.Submission, a.Submission.SubmittedAt)
		if err != nil {
			return node{}, err
		}
		children = append(children, dirNode("submissions", func() ([]node, error) {
			return []node{latest}, nil
		}))
	}
	return entityDirNode(a.ID, a.UpdatedAt, children), nil
}

// listFilesRoot builds the children of /courses/<courseID>/files: the
// contents of the course's root folder, or — when Canvas reports several
// roots — one directory per root folder.
func (f *FS) listFilesRoot(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		tree, err := f.fileTree(courseID)
		if err != nil {
			return nil, err
		}
		roots := tree.roots
		if len(roots) == 1 {
			return tree.children(roots[0].ID), nil
		}
		nodes := make([]node, 0, len(roots))
		seen := map[string]bool{}
		for _, root := range roots {
			root := root
			nodes = append(nodes, dirNode(uniqueName(seen, sanitizeName(root.Name), root.ID),
				func() ([]node, error) { return tree.children(root.ID), nil }))
		}
		sortNodes(nodes)
		return nodes, nil
	}
}

// fileTree indexes one course's folders and files by folder for path
// resolution.
type fileTree struct {
	subdirs map[int64][]canvas.Folder // parent folder ID → child folders
	files   map[int64][]canvas.File   // folder ID → contained files
	roots   []canvas.Folder           // folders without a parent
}

// fileTree fetches and indexes the course's whole file tree.
func (f *FS) fileTree(courseID int64) (*fileTree, error) {
	folders, err := f.src.Folders(f.ctx, courseID)
	if err != nil {
		return nil, err
	}
	files, err := f.src.Files(f.ctx, courseID)
	if err != nil {
		return nil, err
	}
	tree := &fileTree{
		subdirs: map[int64][]canvas.Folder{},
		files:   map[int64][]canvas.File{},
	}
	known := map[int64]bool{}
	for _, folder := range folders {
		known[folder.ID] = true
	}
	for _, folder := range folders {
		if folder.ParentFolderID == nil || !known[*folder.ParentFolderID] {
			// A folder whose parent is missing (e.g. hidden folders Canvas
			// omits from the listing) is treated as a root so its subtree
			// stays reachable.
			tree.roots = append(tree.roots, folder)
			continue
		}
		tree.subdirs[*folder.ParentFolderID] = append(tree.subdirs[*folder.ParentFolderID], folder)
	}
	for _, file := range files {
		tree.files[file.FolderID] = append(tree.files[file.FolderID], file)
	}
	return tree, nil
}

// children builds the entries of one folder: subfolders as directories,
// files as remote file nodes, deduplicated and sorted.
func (t *fileTree) children(folderID int64) []node {
	var nodes []node
	seen := map[string]bool{}
	for _, sub := range t.subdirs[folderID] {
		sub := sub
		nodes = append(nodes, dirNode(uniqueName(seen, sanitizeName(sub.Name), sub.ID),
			func() ([]node, error) { return t.children(sub.ID), nil }))
	}
	for _, file := range t.files[folderID] {
		nodes = append(nodes, remoteFileNode(uniqueName(seen, sanitizeName(file.DisplayName), file.ID), file))
	}
	sortNodes(nodes)
	return nodes
}

// currentTermName picks the term directory the /courses/current symlink
// points at: the term whose [start_at, end_at] contains now, or — when none
// does — the term with the latest start_at. It returns "" when no term has
// a parseable start_at at all.
func currentTermName(terms map[string]canvas.Term, now time.Time) string {
	type candidate struct {
		name  string
		start time.Time
		end   time.Time
	}
	var candidates []candidate
	for name, term := range terms {
		start, err := time.Parse(time.RFC3339, term.StartAt)
		if err != nil {
			continue
		}
		end, _ := time.Parse(time.RFC3339, term.EndAt)
		candidates = append(candidates, candidate{name, start, end})
	}
	if len(candidates) == 0 {
		return ""
	}
	// Sort by start time, latest first.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].start.After(candidates[j].start) })
	for _, c := range candidates {
		if now.Before(c.start) {
			continue
		}
		if c.end.IsZero() || !now.After(c.end) {
			return c.name
		}
	}
	return candidates[0].name
}
