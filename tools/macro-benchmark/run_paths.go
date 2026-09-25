package main

import (
	"errors"
	"path/filepath"
)

// RunPaths is the only constructor for per-run remote paths. The caller must
// pass these into worker components rather than changing process-wide paths.
type RunPaths struct {
	ID      string
	OwnerID string
	Root    string
	Bundle  string
	Worker  string
	Results string
	Archive string
	Ingest  string
	Minio   string
	Cluster string
	Private string
	Rerun   bool
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

func newRerunPaths(base, id, ownerID string) (RunPaths, error) {
	if !validSessionID(ownerID) || ownerID == id {
		return RunPaths{}, errors.New("invalid rerun storage owner")
	}
	paths, err := newRunPaths(base, id)
	if err != nil {
		return RunPaths{}, err
	}
	owner, err := newRunPaths(base, ownerID)
	if err != nil {
		return RunPaths{}, err
	}
	paths.OwnerID, paths.Rerun = ownerID, true
	paths.Ingest, paths.Minio = owner.Ingest, owner.Minio
	return paths, nil
}

func (p RunPaths) unitName() string { return "macro-benchmark-run-" + p.ID + ".service" }
