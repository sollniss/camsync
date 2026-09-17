package app

import (
	"context"
	"errors"
	"time"

	"github.com/sollniss/camsync"
)

// SuggestQuery asks how far each camera's clock is off, measured against the
// cameras whose clocks can be trusted.
type SuggestQuery struct {
	Scope
	// References are the trusted cameras, as printed by list: exact name, row
	// number or unique substring. When empty, phones and cameras whose
	// timestamps agree with their GPS times are used.
	References []string
	// MaxOffset is the largest offset searched; zero selects
	// camsync.DefaultMaxOffset.
	MaxOffset time.Duration
}

// Validate reports whether the query can be executed. A failure is a
// *ValidationError.
func (q SuggestQuery) Validate() error { return invalid(q.check()) }

func (q SuggestQuery) check() error {
	if err := q.validate(); err != nil {
		return err
	}
	if q.MaxOffset < 0 {
		return errors.New("maximum offset must not be negative")
	}
	return nil
}

// Suggestion is the clock estimate for one camera.
type Suggestion struct {
	Index int // 1-based position, as in ListResult
	Name  string
	Files int
	camsync.ClockEstimate
}

// SuggestResult is the answer to a SuggestQuery.
type SuggestResult struct {
	Cameras   []Suggestion
	MaxOffset time.Duration // the search range used
}

// SuggestHandler executes SuggestQuery.
type SuggestHandler struct{}

// Handle scans the scope and estimates the clock offset of every camera.
func (SuggestHandler) Handle(ctx context.Context, q SuggestQuery) (*SuggestResult, error) {
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
	groups := camsync.GroupPhotos(res.Photos)
	opts := camsync.ClockOptions{MaxOffset: q.MaxOffset}
	for _, r := range q.References {
		g, err := camsync.SelectGroup(groups, r)
		if err != nil {
			return nil, invalid(err)
		}
		opts.References = append(opts.References, g)
	}
	if opts.MaxOffset == 0 {
		opts.MaxOffset = camsync.DefaultMaxOffset
	}
	out := &SuggestResult{MaxOffset: opts.MaxOffset}
	for i, e := range camsync.EstimateClocks(groups, opts) {
		out.Cameras = append(out.Cameras, Suggestion{Index: i + 1, Name: e.Group.Camera, Files: len(e.Group.Photos), ClockEstimate: e})
	}
	return out, nil
}
