package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageCredentialsImmutable(t *testing.T) {
	paths, err := newRunPaths(t.TempDir(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Minio, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := createStorageCredentials(paths)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := readStorageCredentials(paths)
	if err != nil || loaded != c {
		t.Fatalf("credentials did not round trip: %v", err)
	}
	if _, err := createStorageCredentials(paths); err == nil {
		t.Fatal("credentials were replaced")
	}
	loaded, err = readStorageCredentials(paths)
	if err != nil || loaded != c {
		t.Fatal("credentials changed")
	}
	child, err := newRerunPaths(filepath.Dir(filepath.Dir(paths.Root)), "child", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createStorageCredentials(child); err == nil {
		t.Fatal("child created owner credentials")
	}
}

func TestStorageCredentialsRejectPopulatedStore(t *testing.T) {
	paths, err := newRunPaths(t.TempDir(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Minio, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Minio, "existing"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := createStorageCredentials(paths); err == nil {
		t.Fatal("created credentials over existing store")
	}
}
