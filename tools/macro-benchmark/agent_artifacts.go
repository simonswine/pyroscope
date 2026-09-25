package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type artifactInfo struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type artifactRequest struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Offset int64  `json:"offset"`
}

type artifactChunk struct {
	Artifact artifactInfo `json:"artifact"`
	Offset   int64        `json:"offset"`
	End      bool         `json:"end"`
}

func (s *agentServer) artifact(id string) (artifactInfo, error) {
	paths, err := newRunPaths(s.root, id)
	if err != nil {
		return artifactInfo{}, err
	}
	data, err := os.ReadFile(filepath.Join(paths.Root, "results-manifest.json"))
	if err != nil {
		return artifactInfo{}, err
	}
	var artifact artifactInfo
	if err := json.Unmarshal(data, &artifact); err != nil {
		return artifactInfo{}, err
	}
	if artifact.Name != "results.tar.gz" || !validDigest(artifact.Digest) || artifact.Size < 0 {
		return artifactInfo{}, errors.New("invalid artifact manifest")
	}
	info, err := os.Lstat(paths.Archive)
	if err != nil {
		return artifactInfo{}, err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return artifactInfo{}, errors.New("artifact size or type mismatch")
	}
	return artifact, nil
}

func (s *agentServer) downloadArtifact(id string, req artifactRequest) (artifactChunk, []byte, error) {
	if req.Name != "results.tar.gz" || !validDigest(req.Digest) || req.Offset < 0 {
		return artifactChunk{}, nil, errors.New("invalid artifact request")
	}
	artifact, err := s.artifact(id)
	if err != nil {
		return artifactChunk{}, nil, err
	}
	if req.Digest != artifact.Digest || req.Offset > artifact.Size {
		return artifactChunk{}, nil, errors.New("artifact identity mismatch")
	}
	paths, _ := newRunPaths(s.root, id)
	file, err := os.Open(paths.Archive)
	if err != nil {
		return artifactChunk{}, nil, err
	}
	defer file.Close()
	size := min(int64(maxFramePayload), artifact.Size-req.Offset)
	payload := make([]byte, size)
	if size > 0 {
		n, err := file.ReadAt(payload, req.Offset)
		if err != nil {
			return artifactChunk{}, nil, fmt.Errorf("read artifact: %w", err)
		}
		if int64(n) != size {
			return artifactChunk{}, nil, io.ErrUnexpectedEOF
		}
	}
	return artifactChunk{Artifact: artifact, Offset: req.Offset, End: req.Offset+size == artifact.Size}, payload, nil
}
