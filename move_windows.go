//go:build windows

package camsync

import (
	"errors"
	"syscall"
)

// ERROR_NOT_SAME_DEVICE is what MoveFileEx reports across volumes.
func isCrossDevice(err error) bool { return errors.Is(err, syscall.Errno(17)) }
