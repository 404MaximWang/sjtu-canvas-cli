package vfs

import (
	"context"
	"encoding/json"
)

// AttendanceSource provides the mlearning.sjtu.edu.cn data behind
// /courses/<id>/attendance. *mlearning.Client implements it directly. A nil
// AttendanceSource leaves the directory empty.
type AttendanceSource interface {
	Status(ctx context.Context, courseID int64) (json.RawMessage, error)
	Current(ctx context.Context, courseID int64) (json.RawMessage, error)
	Records(ctx context.Context, courseID int64) (json.RawMessage, error)
}

// listAttendance builds the children of /courses/<courseID>/attendance:
// three projection files fetched on every open. A roll call lives for
// minutes, so the tier is TTL=0 and nothing here ever caches.
func (f *FS) listAttendance(courseID int64) func() ([]node, error) {
	return func() ([]node, error) {
		if f.attendance == nil {
			return nil, nil
		}
		return []node{
			f.projectionNode("current", func() (json.RawMessage, error) { return f.attendance.Current(f.ctx, courseID) }),
			f.projectionNode("records", func() (json.RawMessage, error) { return f.attendance.Records(f.ctx, courseID) }),
			f.projectionNode("status", func() (json.RawMessage, error) { return f.attendance.Status(f.ctx, courseID) }),
		}, nil
	}
}
