package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (c *protocolClient) DownloadArtifact(ctx context.Context, runID, root string) error {
	if !validSessionID(runID) {
		return errors.New("invalid run ID")
	}
	var artifacts []artifactInfo
	if err := c.Request(ctx, "get_artifacts", runID, nil, &artifacts); err != nil {
		return err
	}
	if len(artifacts) != 1 || artifacts[0].Name != "results.tar.gz" || !validDigest(artifacts[0].Digest) || artifacts[0].Size < 0 {
		return errors.New("artifact not ready")
	}
	artifact := artifacts[0]
	directory := filepath.Join(root, runID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(directory, artifact.Name)
	partial := path + ".partial"
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	h := sha256.New()
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, _ := json.Marshal(artifactRequest{Name: artifact.Name, Digest: artifact.Digest, Offset: offset})
		response, err := c.exchangeFrame(ctx, protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "request", Method: "download_artifact", RunID: runID, Body: body}})
		if err != nil {
			return err
		}
		var chunk artifactChunk
		if err := json.Unmarshal(response.Header.Body, &chunk); err != nil {
			return err
		}
		if chunk.Artifact != artifact || chunk.Offset != offset || len(response.Payload) > maxFramePayload || (len(response.Payload) == 0 && !chunk.End) {
			return errors.New("artifact chunk identity or offset mismatch")
		}
		if offset+int64(len(response.Payload)) > artifact.Size || chunk.End != (offset+int64(len(response.Payload)) == artifact.Size) {
			return errors.New("invalid artifact length")
		}
		if _, err := io.Copy(io.MultiWriter(file, h), bytes.NewReader(response.Payload)); err != nil {
			return err
		}
		offset += int64(len(response.Payload))
		if chunk.End {
			break
		}
	}
	if offset != artifact.Size || hex.EncodeToString(h.Sum(nil)) != artifact.Digest {
		return fmt.Errorf("checksum_mismatch: artifact %s", artifact.Name)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
