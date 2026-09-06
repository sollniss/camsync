// Package app is the application layer of camsync: one message type per
// user-facing action and a handler that executes it.
//
// Following the command/query split, ListQuery only reads while ShiftCommand
// and ResetCommand write sidecars. Handlers return structured results and
// report progress through callbacks; they never print. The same handlers
// therefore drive the terminal output, the JSON output and the tests.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/sollniss/camsync"
)

// ValidationError reports that a message could not be executed because of
// what it asked for: a missing or malformed value, a directory that does not
// exist, a camera that matches nothing. Callers can tell it apart from
// failures that happen while doing the work, which is what a command-line
// front end needs to choose an exit code.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// invalid wraps err as a ValidationError; nil stays nil.
func invalid(err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{Err: err}
}

// Scope selects the photos a message applies to.
type Scope struct {
	Dir          string
	Recursive    bool
	SidecarStyle camsync.SidecarStyle
}

func (s Scope) validate() error {
	if s.Dir == "" {
		return errors.New("directory is required")
	}
	st, err := os.Stat(s.Dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", s.Dir)
	}
	return nil
}

func (s Scope) scan(ctx context.Context) (*camsync.ScanResult, error) {
	return camsync.Scan(ctx, s.Dir, camsync.ScanOptions{Recursive: s.Recursive, SidecarStyle: s.SidecarStyle})
}
