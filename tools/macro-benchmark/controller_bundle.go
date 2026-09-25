package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func localRunManifest(root, id string, deadline time.Time) (runManifest, map[string]string, error) {
	m := runManifest{RunID: id, Deadline: deadline}
	sources := make(map[string]string)
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle contains nonregular file %s", name)
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		digest, err := hashFile(name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		sources[digest] = name
		m.Files = append(m.Files, manifestFile{Path: relative, Digest: digest, Size: info.Size(), Executable: info.Mode()&0111 != 0})
		return nil
	})
	if err != nil {
		return runManifest{}, nil, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if err := validateManifest(m); err != nil {
		return runManifest{}, nil, err
	}
	return m, sources, nil
}

func sessionRunManifest(state *sessionState) (runManifest, map[string]string, error) {
	m, sources, err := localRunManifest(state.Config.Bundle, state.Config.RunID, state.ExpiresAt)
	if err != nil {
		return m, sources, err
	}
	if state.Kind == "rerun" {
		m.Mode, m.StorageOwner = "rerun", state.StorageRunID
	}
	return m, sources, validateManifest(m)
}

func uploadRunBundle(ctx context.Context, client *protocolClient, state *sessionState) (string, error) {
	m, sources, err := sessionRunManifest(state)
	if err != nil {
		return "", err
	}
	var prepared prepareResult
	if err := client.Request(ctx, "prepare_run", m.RunID, m, &prepared); err != nil {
		return "", err
	}
	expected, err := manifestDigest(m)
	if err != nil {
		return "", err
	}
	if prepared.Digest != expected {
		return "", fmt.Errorf("agent manifest digest mismatch")
	}
	for _, digest := range prepared.Missing {
		path, ok := sources[digest]
		if !ok {
			return "", fmt.Errorf("agent requested undeclared blob %s", digest)
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		transferErr := client.UploadBlob(ctx, digest, file)
		if err := errors.Join(transferErr, file.Close()); err != nil {
			return "", err
		}
	}
	if err := client.Request(ctx, "finalize_run", m.RunID, struct {
		Digest string `json:"digest"`
	}{expected}, nil); err != nil {
		return "", err
	}
	return expected, nil
}
