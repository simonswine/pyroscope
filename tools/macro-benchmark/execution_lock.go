package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// executionLock is an OS lock kept open for the worker's complete lifetime.
// It survives agent disconnects and is automatically released on worker exit.
type executionLock struct{ file *os.File }

func acquireExecutionLock(root string) (*executionLock, error) {
	if err := os.MkdirAll(filepath.Join(root, "locks"), 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "locks", "execution.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("node_busy: %w", err)
		}
		return nil, err
	}
	return &executionLock{file: file}, nil
}

func (l *executionLock) Close() error { return l.file.Close() }
