package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveDirectoryAtomic(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(t.TempDir(), "results.tar.gz")
	if err := os.WriteFile(dest, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	if err := archiveDirectoryAtomic(root, dest); err == nil {
		t.Fatal("accepted symlink")
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "previous" {
		t.Fatalf("replaced complete archive after error: %q %v", data, err)
	}
	if err := os.Remove(filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "status.yaml"), []byte("success: true"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := archiveDirectoryAtomic(root, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Size() <= int64(len("previous")) {
		t.Fatalf("archive: %v %v", info, err)
	}
}
