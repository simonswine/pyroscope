package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionLock(t *testing.T) {
	root := t.TempDir()
	first, err := acquireExecutionLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireExecutionLock(root); err == nil {
		other.Close()
		t.Fatal("lock acquired twice")
	} else if !strings.Contains(err.Error(), "node_busy") {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireExecutionLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	info, err := os.Stat(filepath.Join(root, "locks", "execution.lock"))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("lock permissions: %v %v", info, err)
	}
}
