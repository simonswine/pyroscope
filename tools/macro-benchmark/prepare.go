package main

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func command(ctx context.Context, dir string, env []string, name string, args ...string) error {
	log.Printf("%s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(source, dest string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func linuxAMD64(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("%s must be a Linux amd64 executable: %w", path, err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 {
		return fmt.Errorf("%s must target amd64 Linux", path)
	}
	return nil
}

func prepare(ctx context.Context, cfg runConfig, sourceDir string, plan *runPlan) error {
	inputs := cfg.Inputs
	if plan == nil {
		return fmt.Errorf("benchmark plan is required")
	}
	if err := plan.validate(inputs); err != nil {
		return err
	}
	if err := inputs.validate(); err != nil {
		return err
	}
	fixturePath := inputs.Fixture
	if fixturePath != "" && !filepath.IsAbs(fixturePath) {
		fixturePath = filepath.Join(sourceDir, fixturePath)
	}
	if inputs.Fixture != "" {
		sum, err := checksum(fixturePath)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, inputs.FixtureSHA256) {
			return fmt.Errorf("fixture checksum mismatch: got %s", sum)
		}
	}
	target, err := filepath.Abs(cfg.Bundle)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	// Only replace a bundle owned by this harness, or an empty directory.
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		if _, err := os.Stat(filepath.Join(target, ".macro-benchmark-bundle")); err != nil {
			return fmt.Errorf("refusing to replace unowned nonempty bundle directory %s", target)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(target), ".macro-bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err := os.Chmod(temp, 0755); err != nil {
		return err
	}
	files := map[string]string{}
	if inputs.Fixture != "" {
		files["fixture.replay"] = fixturePath
	}
	for dest, source := range files {
		if err := copyFile(source, filepath.Join(temp, dest)); err != nil {
			return err
		}
	}
	for name := range files {
		if err := os.Chmod(filepath.Join(temp, name), 0644); err != nil {
			return err
		}
	}
	if err := downloadMinio(ctx, inputs.MinioURL, inputs.MinioSHA256, filepath.Join(temp, "minio")); err != nil {
		return err
	}
	linuxEnv := []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}
	for _, build := range []struct {
		dir  string
		args []string
	}{
		{"../..", []string{"build", "-o", filepath.Join(temp, "profilecli"), "./cmd/profilecli"}},
		{".", []string{"build", "-o", filepath.Join(temp, "macro-benchmark"), "."}},
	} {
		if err := command(ctx, filepath.Join(sourceDir, build.dir), linuxEnv, "go", build.args...); err != nil {
			return err
		}
	}
	// Preserve the exact orchestration source used for all product revisions.
	harness := filepath.Join(temp, "harness")
	if err := copyHarness(filepath.Join(sourceDir, "../../pkg/test/integration/cluster"), harness); err != nil {
		return err
	}
	built := map[string]string{}
	for _, target := range []struct {
		name     string
		revision targetRevision
	}{{"ingest", plan.Ingest}, {"baseline", plan.Baseline}, {"comparison", plan.Comparison}} {
		output := filepath.Join(temp, "cluster-"+target.name)
		if existing, ok := built[target.revision.Commit]; ok {
			if err := copyFile(existing, output); err != nil {
				return err
			}
		} else {
			if err := buildRevisionCluster(ctx, sourceDir, target.revision.Commit, output, harness); err != nil {
				return err
			}
			built[target.revision.Commit] = output
		}
	}
	for _, b := range plan.Benchmarks {
		name := b.Name
		if err := command(ctx, sourceDir, linuxEnv, "go", "test", "-c", "-o", filepath.Join(temp, name+".test"), "./benchmark/"+name); err != nil {
			return err
		}
	}
	if err := writeYAML(filepath.Join(temp, "plan.yaml"), plan); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(temp, "settings.yaml"), inputs); err != nil {
		return err
	}
	// Capture reproducibility inputs, not the local SSH key or AWS credentials.
	for file, args := range map[string][]string{"commit.txt": {"rev-parse", "HEAD"}, "source.patch": {"diff", "--binary", "HEAD"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = sourceDir
		output, err := cmd.Output()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(temp, file), output, 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(temp, ".macro-benchmark-bundle"), []byte("v1\n"), 0600); err != nil {
		return err
	}
	hashes := map[string]string{}
	if err := filepath.WalkDir(temp, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(temp, path)
		if err != nil {
			return err
		}
		sum, err := checksum(path)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(relative)] = sum
		return nil
	}); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(temp, "checksums.yaml"), hashes); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.Rename(temp, target); err != nil {
		return err
	}
	log.Printf("bundle prepared: %s", target)
	return nil
}

func verifyBundle(root string) error {
	var hashes map[string]string
	if err := readYAML(filepath.Join(root, "checksums.yaml"), &hashes); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return fmt.Errorf("empty bundle checksum manifest")
	}
	for relative, expected := range hashes {
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("invalid bundle path %q", relative)
		}
		got, err := checksum(filepath.Join(root, relative))
		if err != nil {
			return err
		}
		if got != expected {
			return fmt.Errorf("bundle checksum mismatch: %s", relative)
		}
	}
	return nil
}
