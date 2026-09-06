package app

import (
	"context"

	"github.com/sollniss/camsync"
)

// ListQuery asks which cameras took the photos in a directory and what shift
// each currently has.
type ListQuery struct {
	Scope
}

// Validate reports whether the query can be executed. A failure is a
// *ValidationError.
func (q ListQuery) Validate() error { return invalid(q.validate()) }

// Camera is one camera's group with its summary and photos.
type Camera struct {
	Index  int // 1-based position, accepted by the commands' Camera field
	Name   string
	Photos []*camsync.Photo
	camsync.Summary
}

// ListResult is the answer to a ListQuery.
type ListResult struct {
	Cameras []Camera
	Photos  int // photos found in total
	Ignored int // regular files that are neither photos nor sidecars
}

// ListHandler executes ListQuery.
type ListHandler struct{}

// Handle scans the scope and groups the photos by camera.
func (ListHandler) Handle(ctx context.Context, q ListQuery) (*ListResult, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := q.scan(ctx)
	if err != nil {
		return nil, err
	}
	out := &ListResult{Photos: len(res.Photos), Ignored: res.Ignored}
	for i, g := range camsync.GroupPhotos(res.Photos) {
		out.Cameras = append(out.Cameras, Camera{Index: i + 1, Name: g.Camera, Photos: g.Photos, Summary: g.Summary()})
	}
	return out, nil
}
