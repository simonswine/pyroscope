package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func resolveRevision(ctx context.Context, source, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("Git ref is required")
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	cmd.Dir = source
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve Git ref %q: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Build production code from a detached revision, using the same integration
// harness for both targets. Never check out refs into the developer's worktree.
func buildRevisionCluster(ctx context.Context, source, commit, output, harness string) error {
	dir, err := os.MkdirTemp("", "macro-revision-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tree := filepath.Join(dir, "source")
	if err := command(ctx, source, nil, "git", "worktree", "add", "--detach", tree, commit); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := command(cleanupCtx, source, nil, "git", "worktree", "remove", "--force", tree); err != nil {
			// Keep cleanup best-effort; the original build error is more useful.
			log.Printf("worktree cleanup: %v", err)
		}
	}()
	if err := copyHarness(harness, filepath.Join(tree, "pkg/test/integration/cluster")); err != nil {
		return err
	}
	return command(ctx, tree, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "build", "-o", output, "./pkg/test/integration/cluster/cmd/macro-cluster")
}

func copyHarness(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(dest, relative))
	})
}
