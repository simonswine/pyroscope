package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type manifestFile struct {
	Path       string `json:"path"`
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

type runManifest struct {
	RunID    string         `json:"run_id"`
	Deadline time.Time      `json:"deadline"`
	Files    []manifestFile `json:"files"`
}

type prepareResult struct {
	Digest  string   `json:"digest"`
	Missing []string `json:"missing"`
}

func validateManifest(m runManifest) error {
	if !validSessionID(m.RunID) || m.Deadline.IsZero() || len(m.Files) == 0 || len(m.Files) > 10000 {
		return errors.New("invalid run manifest")
	}
	seen := map[string]bool{}
	blobSizes := map[string]int64{}
	for _, f := range m.Files {
		if !validDigest(f.Digest) || f.Size < 0 || f.Size > maxBlobSize {
			return errors.New("invalid manifest blob")
		}
		if previous, ok := blobSizes[f.Digest]; ok && previous != f.Size {
			return errors.New("conflicting blob size")
		}
		blobSizes[f.Digest] = f.Size
		if f.Path == "" || len(f.Path) > 512 || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.Contains(f.Path, "\\") || strings.ContainsAny(f.Path, "\x00\n") || f.Path == "." || f.Path == ".." || strings.HasPrefix(f.Path, "../") || strings.Contains(f.Path, "/../") {
			return errors.New("invalid manifest path")
		}
		for prefix := f.Path; prefix != "."; prefix = path.Dir(prefix) {
			if seen[prefix] {
				return errors.New("duplicate or conflicting manifest path")
			}
		}
		seen[f.Path] = true
	}
	for _, f := range m.Files {
		for parent := path.Dir(f.Path); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return errors.New("file conflicts with directory")
			}
		}
	}
	return nil
}

func manifestDigest(m runManifest) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *agentServer) prepareRun(m runManifest) (prepareResult, error) {
	if err := validateManifest(m); err != nil {
		return prepareResult{}, err
	}
	paths, err := newRunPaths(s.root, m.RunID)
	if err != nil {
		return prepareResult{}, err
	}
	digest, err := manifestDigest(m)
	if err != nil {
		return prepareResult{}, err
	}
	manifestPath := filepath.Join(paths.Root, "manifest.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var saved runManifest
		if err := json.Unmarshal(data, &saved); err != nil {
			return prepareResult{}, err
		}
		previous, err := manifestDigest(saved)
		if err != nil {
			return prepareResult{}, err
		}
		if previous != digest {
			return prepareResult{}, errors.New("manifest_conflict")
		}
	} else if os.IsNotExist(err) {
		if err := atomicJSON(manifestPath, m); err != nil {
			return prepareResult{}, err
		}
	} else {
		return prepareResult{}, err
	}
	missing := make([]string, 0)
	seen := map[string]bool{}
	for _, f := range m.Files {
		if seen[f.Digest] {
			continue
		}
		seen[f.Digest] = true
		info, err := os.Lstat(s.blobPath(f.Digest))
		if os.IsNotExist(err) {
			missing = append(missing, f.Digest)
			continue
		}
		if err != nil {
			return prepareResult{}, err
		}
		if !info.Mode().IsRegular() || info.Size() != f.Size {
			return prepareResult{}, errors.New("blob size or type mismatch")
		}
	}
	sort.Strings(missing)
	return prepareResult{Digest: digest, Missing: missing}, nil
}

func (s *agentServer) finalizeRun(id, digest string) error {
	if !validDigest(digest) {
		return errors.New("invalid manifest digest")
	}
	paths, err := newRunPaths(s.root, id)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(paths.Root, "manifest.json"))
	if err != nil {
		return err
	}
	var m runManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if err := validateManifest(m); err != nil {
		return err
	}
	if m.RunID != id {
		return errors.New("manifest run identity mismatch")
	}
	saved, err := manifestDigest(m)
	if err != nil {
		return err
	}
	if digest != saved {
		return errors.New("manifest_conflict")
	}
	if info, err := os.Lstat(paths.Bundle); err == nil {
		if !info.IsDir() {
			return errors.New("bundle path is not a directory")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(paths.Root, ".bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, f := range m.Files {
		blob := s.blobPath(f.Digest)
		info, err := os.Lstat(blob)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != f.Size {
			return errors.New("manifest blob size or type mismatch")
		}
		dest := filepath.Join(stage, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		src, err := os.Open(blob)
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if f.Executable {
			mode = 0700
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = src.Close()
			return err
		}
		h := sha256.New()
		copied, copyErr := io.Copy(io.MultiWriter(out, h), src)
		err = errors.Join(copyErr, out.Sync(), out.Close(), src.Close())
		if err != nil {
			return err
		}
		if copied != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Digest {
			return fmt.Errorf("checksum_mismatch: %s", f.Path)
		}
	}
	// fsync the tree before making the bundle visible as a unit.
	if err := syncTree(stage); err != nil {
		return err
	}
	if err := os.Rename(stage, paths.Bundle); err != nil {
		return err
	}
	dir, err := os.Open(paths.Root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func syncTree(root string) error {
	return filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		dir, err := os.Open(name)
		if err != nil {
			return err
		}
		return errors.Join(dir.Sync(), dir.Close())
	})
}
