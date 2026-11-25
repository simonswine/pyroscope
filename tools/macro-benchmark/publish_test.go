package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishGitRepository(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		if err := publishGit(ctx, dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	remote := filepath.Join(root, "remote.git")
	git(root, "init", "--bare", remote)
	seed := filepath.Join(root, "seed")
	git(root, "init", "--initial-branch=main", seed)
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(seed, "index.html"), "<h1>Existing</h1><ul><li>old report</li></ul>")
	write(filepath.Join(seed, "README.md"), "# Existing reports\n")
	write(filepath.Join(seed, "unrelated"), "preserve me")
	git(seed, "add", ".")
	git(seed, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "Initialize")
	git(seed, "remote", "add", "origin", remote)
	git(seed, "push", "origin", "main")
	stage := filepath.Join(root, "stage")
	write(filepath.Join(stage, "index.html"), "report")
	write(filepath.Join(stage, "index.assets", "profile.pprof"), "profile")
	if err := publishGitRepository(ctx, remote, "main", stage, "test-run", "https://example.com/test-run/"); err != nil {
		t.Fatal(err)
	}
	check := filepath.Join(root, "check")
	git(root, "clone", "--branch", "main", remote, check)
	for _, file := range []string{"test-run/index.html", "test-run/index.assets/profile.pprof", "unrelated", ".nojekyll"} {
		if _, err := os.Stat(filepath.Join(check, file)); err != nil {
			t.Fatal(err)
		}
	}
	index, err := os.ReadFile(filepath.Join(check, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "old report") || !strings.Contains(string(index), `href="test-run/"`) {
		t.Fatalf("unexpected index: %s", index)
	}
	readme, err := os.ReadFile(filepath.Join(check, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "# Existing reports") || !strings.Contains(string(readme), "https://example.com/test-run/") {
		t.Fatalf("unexpected README: %s", readme)
	}
	if err := publishGitRepository(ctx, remote, "main", stage, "test-run", "https://example.com/test-run/"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate accepted: %v", err)
	}
}

func TestPublishIndexSafety(t *testing.T) {
	for _, kind := range []string{"unrecognized", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "index.html")
			if kind == "symlink" {
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("<ul></ul>"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("custom index"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := updatePublishIndexes(root, "test", "https://example.com/test/"); err == nil {
				t.Fatal("unsafe index accepted")
			}
		})
	}
}

func TestPublishCommandValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--repo", "../oops", "results.tar.gz"}, {"--name", "../oops", t.TempDir()}} {
		if err := publishCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := publishCommand(context.Background(), []string{"--help"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
