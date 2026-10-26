package main

import (
	"path/filepath"
	"testing"
)

func TestRunPaths(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", "../x", "a/b", ".", "..", "a b"} {
		if _, err := newRunPaths(root, id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	if _, err := newRunPaths("relative", "run-1"); err == nil {
		t.Fatal("accepted relative base")
	}
	a, err := newRunPaths(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := newRunPaths(root, "run-2")
	if err != nil {
		t.Fatal(err)
	}
	if a.Root == b.Root || a.Archive == b.Archive || a.Ingest == b.Ingest {
		t.Fatal("run paths overlap")
	}
	if a.Bundle != filepath.Join(root, "runs", "run-1", "bundle") || a.unitName() != "macro-benchmark-run-run-1.service" {
		t.Fatalf("paths: %+v", a)
	}
}
