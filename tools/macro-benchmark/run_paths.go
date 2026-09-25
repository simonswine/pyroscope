package main

import (
	"errors"
	"path/filepath"
)

// RunPaths is the only constructor for per-run remote paths. The caller must
// pass these into worker components rather than changing process-wide paths.
type RunPaths struct {
	ID      string
	Root    string
	Bundle  string
	Worker  string
	Results string
	Archive string
	Ingest  string
	Minio   string
	Cluster string
	Private string
}

func newRunPaths(base, id string) (RunPaths, error) {
	if !validSessionID(id) || id == "." || id == ".." {
		return RunPaths{}, errors.New("invalid run ID")
	}
	if !filepath.IsAbs(base) {
		return RunPaths{}, errors.New("run base must be absolute")
	}
	root := filepath.Join(base, "runs", id)
	return RunPaths{
		ID: id, Root: root, Bundle: filepath.Join(root, "bundle"),
		Worker: filepath.Join(root, "worker"), Results: filepath.Join(root, "results"),
		Archive: filepath.Join(root, "results.tar.gz"), Ingest: filepath.Join(root, "work", "ingest"),
		Minio: filepath.Join(root, "work", "minio"), Cluster: filepath.Join(root, "work", "cluster"), Private: filepath.Join(root, "private"),
	}, nil
}

func (p RunPaths) unitName() string { return "macro-benchmark-run-" + p.ID + ".service" }
