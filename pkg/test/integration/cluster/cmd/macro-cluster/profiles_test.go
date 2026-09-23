package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/pprof/profile"
)

func TestClusterProfiles(t *testing.T) {
	root := t.TempDir()
	cpu, heap := filepath.Join(root, "cpu.pprof"), filepath.Join(root, "heap.pprof")
	stop, err := startProfiles(cpu, heap)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cpu, heap} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := profile.ParseData(data)
		if err != nil {
			t.Fatalf("invalid profile %s: %v", path, err)
		}
		if err := p.CheckValid(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := startProfiles(cpu, heap); err == nil {
		t.Fatal("overwrote existing profiles")
	}
}

func TestProfilesDisabled(t *testing.T) {
	stop, err := startProfiles("", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestProfilesInvalidPath(t *testing.T) {
	if _, err := startProfiles(filepath.Join(t.TempDir(), "missing", "cpu.pprof"), ""); err == nil {
		t.Fatal("accepted missing profile directory")
	}
}
