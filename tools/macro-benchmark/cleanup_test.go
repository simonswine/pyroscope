package main

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestCleanupArchive(t *testing.T) {
	data, err := cleanupArchive()
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "cleanup_lambda.py" {
		t.Fatal("incorrect Lambda archive")
	}
	file, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	source, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(source, cleanupSource) {
		t.Fatalf("Lambda source mismatch: %v", err)
	}
}

func TestCleanupIdentity(t *testing.T) {
	name := cleanupName("run-1")
	if len(name) > 64 || name != cleanupName("run-1") || name == cleanupName("run-2") {
		t.Fatal("cleanup names must be stable, unique and valid AWS names")
	}
	if instanceLifetime != 32*time.Hour {
		t.Fatal("unexpected instance lifetime")
	}
}
