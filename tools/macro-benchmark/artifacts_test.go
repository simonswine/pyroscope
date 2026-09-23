package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestYAMLConfig(t *testing.T) {
	for _, text := range []string{"unknown: true\n", "region: us-east-1\n---\nregion: us-east-2\n", "inputs:\n  typo: value\n"} {
		path := filepath.Join(t.TempDir(), "run.yaml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		var bad runConfig
		if err := readYAML(path, &bad); err == nil {
			t.Fatalf("accepted invalid YAML configuration: %q", text)
		}
	}
}

func TestBundleIntegrity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.replay")
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	sum, err := checksum(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "checksums.yaml")
	if err := writeYAML(manifest, map[string]string{"fixture.replay": sum}); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundle(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundle(dir); err == nil {
		t.Fatal("accepted tampered fixture")
	}
	if err := writeYAML(manifest, map[string]string{"../escape": sum}); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundle(dir); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestArchive(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "results")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bench.txt"), []byte("benchmark result"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "results.tar.gz")
	if err := archiveDirectory(root, archive); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "bench.txt" {
		t.Fatalf("unexpected artifact: %s", header.Name)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "benchmark result" {
		t.Fatal("artifact changed")
	}
	if err := os.Symlink(filepath.Join(root, "bench.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := archiveDirectory(root, archive); err == nil {
		t.Fatal("accepted symlink artifact")
	}
}
