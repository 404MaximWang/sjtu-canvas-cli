package canvas

// This file mirrors the read-side entity definitions of the SJTU Canvas
// Helper project (src-tauri/src/model/mod.rs), which is the blueprint for
// the CLI's JSON output contract. Field names and optionality follow the
// blueprint exactly; `updated_at` is added to entities Canvas stamps it on,
// because the metadata cache's freshness key is the (id, updated_at, size)
// triple.
//
// Canvas timestamps arrive as ISO 8601 strings or null. They are decoded
// into plain strings with omitempty: an absent value and an empty string are
// deliberately not distinguished, matching the blueprint's Option<String>
// defaults.

// Course is one enrollment's course as returned by /api/v1/courses.
type Course struct {
	ID                     int64        `json:"id"`
	UUID                   string       `json:"uuid,omitempty"`
	Name                   string       `json:"name"`
	CourseCode             string       `json:"course_code,omitempty"`
	Enrollments            []Enrollment `json:"enrollments,omitempty"`
	AccessRestrictedByDate bool         `json:"access_restricted_by_date,omitempty"`
	Teachers               []Teacher    `json:"teachers,omitempty"`
	Term                   Term         `json:"term"`
	SyllabusBody           string       `json:"syllabus_body,omitempty"`
}

// Teacher is a course's instructor as embedded by include[]=teachers.
type Teacher struct {
	ID             int64  `json:"id"`
	AnonymousID    string `json:"anonymous_id,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	AvatarImageURL string `json:"avatar_image_url,omitempty"`
	HTMLURL        string `json:"html_url,omitempty"`
}

// Term is the enrollment term a course belongs to, as embedded by
// include[]=term. Name carries the "2025-2026-1" style semester label the
// vfs uses for its term directories.
type Term struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	StartAt       string `json:"start_at,omitempty"`
	EndAt         string `json:"end_at,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	WorkflowState string `json:"workflow_state,omitempty"`
}

// Enrollment is the caller's enrollment record inside a course. Role stays a
// plain string: the blueprint enumerates known values but Canvas can invent
// new ones.
type Enrollment struct {
	Type            string `json:"type"`
	Role            string `json:"role"`
	RoleID          int64  `json:"role_id"`
	UserID          int64  `json:"user_id"`
	EnrollmentState string `json:"enrollment_state"`
}

// File is one file in a course's file tree. URL carries a download verifier
// in its query string; treat it as a credential and never log it raw.
type File struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid,omitempty"`
	FolderID    int64  `json:"folder_id"`
	DisplayName string `json:"display_name"`
	Filename    string `json:"filename,omitempty"`
	URL         string `json:"url,omitempty"`
	Size        uint64 `json:"size"`
	Locked      bool   `json:"locked,omitempty"`
	MimeClass   string `json:"mime_class,omitempty"`
	ContentType string `json:"content-type,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// Folder is one directory in a course's file tree. ParentFolderID is null
// for the course's root folder. FullName is the slash-joined path from the
// root (e.g. "course files/lectures").
type Folder struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	FullName       string `json:"full_name"`
	ParentFolderID *int64 `json:"parent_folder_id,omitempty"`
	Locked         bool   `json:"locked,omitempty"`
	FoldersURL     string `json:"folders_url,omitempty"`
	FilesURL       string `json:"files_url,omitempty"`
	FilesCount     int64  `json:"files_count,omitempty"`
	FoldersCount   int64  `json:"folders_count,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// Assignment is one course assignment. Submission carries the caller's own
// submission when fetched with include[]=submission.
type Assignment struct {
	ID                     int64                `json:"id"`
	Name                   string               `json:"name"`
	CourseID               int64                `json:"course_id"`
	Description            string               `json:"description,omitempty"`
	DueAt                  string               `json:"due_at,omitempty"`
	UnlockAt               string               `json:"unlock_at,omitempty"`
	LockAt                 string               `json:"lock_at,omitempty"`
	PointsPossible         *float64             `json:"points_possible,omitempty"`
	NeedsGradingCount      *int32               `json:"needs_grading_count,omitempty"`
	HTMLURL                string               `json:"html_url,omitempty"`
	SubmissionTypes        []string             `json:"submission_types,omitempty"`
	AllowedExtensions      []string             `json:"allowed_extensions,omitempty"`
	HasSubmittedSubmission bool                 `json:"has_submitted_submissions,omitempty"`
	Published              bool                 `json:"published,omitempty"`
	SubmissionsDownloadURL string               `json:"submissions_download_url,omitempty"`
	Submission             *Submission          `json:"submission,omitempty"`
	Overrides              []AssignmentOverride `json:"overrides,omitempty"`
	AllDates               []AssignmentDate     `json:"all_dates,omitempty"`
	ScoreStatistics        *ScoreStatistics     `json:"score_statistics,omitempty"`
	GradingType            string               `json:"grading_type,omitempty"`
	UpdatedAt              string               `json:"updated_at,omitempty"`
}

// AssignmentDate is one effective due-date row of an assignment (base date
// or one override's resolved date).
type AssignmentDate struct {
	ID       int64  `json:"id,omitempty"`
	Base     bool   `json:"base,omitempty"`
	Title    string `json:"title,omitempty"`
	DueAt    string `json:"due_at,omitempty"`
	UnlockAt string `json:"unlock_at,omitempty"`
	LockAt   string `json:"lock_at,omitempty"`
}

// AssignmentOverride is a per-section/per-student due-date override.
type AssignmentOverride struct {
	ID              int64   `json:"id"`
	AssignmentID    int64   `json:"assignment_id"`
	QuizID          int64   `json:"quiz_id,omitempty"`
	ContextModuleID int64   `json:"context_module_id,omitempty"`
	StudentIDs      []int64 `json:"student_ids,omitempty"`
	GroupID         int64   `json:"group_id,omitempty"`
	CourseSectionID int64   `json:"course_section_id,omitempty"`
	Title           string  `json:"title,omitempty"`
	DueAt           string  `json:"due_at,omitempty"`
	AllDay          bool    `json:"all_day,omitempty"`
	AllDayDate      string  `json:"all_day_date,omitempty"`
	UnlockAt        string  `json:"unlock_at,omitempty"`
	LockAt          string  `json:"lock_at,omitempty"`
}

// ScoreStatistics is the grade distribution of an assignment, present when
// the instructor enabled it.
type ScoreStatistics struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

// CalendarEvent is one entry from /api/v1/calendar_events. Canvas returns
// the id as a string on this endpoint, matching the blueprint.
type CalendarEvent struct {
	Title         string     `json:"title"`
	WorkflowState string     `json:"workflow_state,omitempty"`
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Assignment    Assignment `json:"assignment"`
	HTMLURL       string     `json:"html_url,omitempty"`
	ContextCode   string     `json:"context_code,omitempty"`
	ContextName   string     `json:"context_name,omitempty"`
	EndAt         string     `json:"end_at,omitempty"`
	StartAt       string     `json:"start_at,omitempty"`
	URL           string     `json:"url,omitempty"`
	ImportantDate bool       `json:"important_dates,omitempty"`
}

// Submission is one user's submission to one assignment.
type Submission struct {
	ID                 int64               `json:"id"`
	SubmittedAt        string              `json:"submitted_at,omitempty"`
	Grade              string              `json:"grade,omitempty"`
	AssignmentID       int64               `json:"assignment_id"`
	UserID             int64               `json:"user_id"`
	Late               bool                `json:"late,omitempty"`
	Attachments        []Attachment        `json:"attachments,omitempty"`
	SubmissionComments []SubmissionComment `json:"submission_comments,omitempty"`
	WorkflowState      string              `json:"workflow_state"`
}

// Attachment is a file attached to a submission or discussion topic.
type Attachment struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid,omitempty"`
	FolderID    *int64 `json:"folder_id,omitempty"`
	DisplayName string `json:"display_name"`
	Filename    string `json:"filename,omitempty"`
	URL         string `json:"url,omitempty"`
	Size        int64  `json:"size"`
	Locked      bool   `json:"locked,omitempty"`
	MimeClass   string `json:"mime_class,omitempty"`
	ContentType string `json:"content-type,omitempty"`
}

// SubmissionComment is one grader/peer comment on a submission.
type SubmissionComment struct {
	ID           int64         `json:"id,omitempty"`
	Comment      string        `json:"comment,omitempty"`
	AuthorID     int64         `json:"author_id,omitempty"`
	AuthorName   string        `json:"author_name,omitempty"`
	CreatedAt    string        `json:"created_at,omitempty"`
	AvatarPath   string        `json:"avatar_path,omitempty"`
	MediaComment *MediaComment `json:"media_comment,omitempty"`
	Attachments  []Attachment  `json:"attachments,omitempty"`
}

// MediaComment is a voice/video note attached to a submission comment.
type MediaComment struct {
	MediaType string `json:"media_type,omitempty"`
	URL       string `json:"url,omitempty"`
}

// DiscussionTopic is one entry under a course's discussions; with
// only_announcements=true the same endpoint serves announcements.
type DiscussionTopic struct {
	ID                      int64        `json:"id"`
	Title                   string       `json:"title"`
	Message                 string       `json:"message,omitempty"`
	UserName                string       `json:"user_name,omitempty"`
	PostedAt                string       `json:"posted_at,omitempty"`
	CreatedAt               string       `json:"created_at,omitempty"`
	UpdatedAt               string       `json:"updated_at,omitempty"`
	LastReplyAt             string       `json:"last_reply_at,omitempty"`
	DelayedPostAt           string       `json:"delayed_post_at,omitempty"`
	LockAt                  string       `json:"lock_at,omitempty"`
	AssignmentID            *int64       `json:"assignment_id,omitempty"`
	DiscussionType          string       `json:"discussion_type,omitempty"`
	DiscussionSubentryCount int64        `json:"discussion_subentry_count"`
	ReadState               string       `json:"read_state,omitempty"`
	UnreadCount             int64        `json:"unread_count"`
	Published               bool         `json:"published,omitempty"`
	Locked                  bool         `json:"locked,omitempty"`
	Pinned                  bool         `json:"pinned,omitempty"`
	LockedForUser           bool         `json:"locked_for_user,omitempty"`
	LockExplanation         string       `json:"lock_explanation,omitempty"`
	RequireInitialPost      bool         `json:"require_initial_post,omitempty"`
	UserCanSeePosts         bool         `json:"user_can_see_posts,omitempty"`
	CommentsDisabled        bool         `json:"comments_disabled,omitempty"`
	Subscribed              bool         `json:"subscribed,omitempty"`
	Permissions             *Permissions `json:"permissions,omitempty"`
	Attachments             []Attachment `json:"attachments,omitempty"`
	HTMLURL                 string       `json:"html_url,omitempty"`
	URL                     string       `json:"url,omitempty"`
	Assignment              *Assignment  `json:"assignment,omitempty"`
}

// Permissions records what the caller may do with a discussion topic.
type Permissions struct {
	Attach bool `json:"attach,omitempty"`
	Update bool `json:"update,omitempty"`
	Reply  bool `json:"reply,omitempty"`
	Delete bool `json:"delete,omitempty"`
}
