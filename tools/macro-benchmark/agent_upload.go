package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Upload v1 restarts interrupted transfers at offset zero. At most one upload
// is in flight on each agent connection; chunks are acknowledged individually
// and thus require at most one frame of credit.
const maxBlobSize = 1 << 40

type blobRequest struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type blobProgress struct {
	Offset   int64 `json:"offset"`
	Complete bool  `json:"complete"`
}

type blobUpload struct {
	request blobRequest
	file    *os.File
	hasher  hash.Hash
	offset  int64
}

func validDigest(d string) bool {
	if len(d) != 64 || strings.ToLower(d) != d {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil
}

func (s *agentServer) blobPath(digest string) string {
	return filepath.Join(s.root, "blobs", "sha256", digest)
}

func (s *agentServer) startBlobUpload(req blobRequest) (blobProgress, error) {
	if !validDigest(req.Digest) || req.Size < 0 || req.Size > maxBlobSize {
		return blobProgress{}, errors.New("invalid blob identity or size")
	}
	if s.upload != nil {
		return blobProgress{}, errors.New("transfer already active")
	}
	path := s.blobPath(req.Digest)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() != req.Size {
			return blobProgress{}, errors.New("blob identity conflict")
		}
		// Recheck disk contents rather than treating a filename as proof of integrity.
		digest, err := hashFile(path)
		if err != nil {
			return blobProgress{}, err
		}
		if digest != req.Digest {
			return blobProgress{}, errors.New("cached blob checksum mismatch")
		}
		return blobProgress{Offset: req.Size, Complete: true}, nil
	} else if !os.IsNotExist(err) {
		return blobProgress{}, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return blobProgress{}, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return blobProgress{}, err
	}
	if uint64(req.Size) > stat.Bavail*uint64(stat.Bsize) {
		return blobProgress{}, errors.New("insufficient_space")
	}
	file, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return blobProgress{}, err
	}
	if req.Size == 0 {
		if req.Digest != hex.EncodeToString(sha256.New().Sum(nil)) {
			_ = file.Close()
			_ = os.Remove(file.Name())
			return blobProgress{}, errors.New("checksum_mismatch")
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			_ = os.Remove(file.Name())
			return blobProgress{}, err
		}
		if err := os.Rename(file.Name(), path); err != nil {
			_ = os.Remove(file.Name())
			return blobProgress{}, err
		}
		d, err := os.Open(dir)
		if err != nil {
			return blobProgress{}, err
		}
		defer d.Close()
		if err := d.Sync(); err != nil {
			return blobProgress{}, err
		}
		return blobProgress{Complete: true}, nil
	}
	s.upload = &blobUpload{request: req, file: file, hasher: sha256.New()}
	return blobProgress{Offset: 0}, nil
}

func (s *agentServer) abortUpload() {
	if s.upload == nil {
		return
	}
	_ = s.upload.file.Close()
	_ = os.Remove(s.upload.file.Name())
	s.upload = nil
}

func (s *agentServer) uploadChunk(digest string, offset int64, bytes []byte) (blobProgress, error) {
	u := s.upload
	if u == nil || u.request.Digest != digest {
		return blobProgress{}, errors.New("no matching transfer")
	}
	if len(bytes) == 0 || offset != u.offset || int64(len(bytes)) > u.request.Size-u.offset {
		return blobProgress{}, errors.New("invalid chunk offset or size")
	}
	n, err := u.file.Write(bytes)
	if err != nil || n != len(bytes) {
		s.abortUpload()
		if err == nil {
			err = io.ErrShortWrite
		}
		return blobProgress{}, fmt.Errorf("write upload: %w", err)
	}
	_, _ = u.hasher.Write(bytes)
	u.offset += int64(len(bytes))
	if u.offset < u.request.Size {
		return blobProgress{Offset: u.offset}, nil
	}
	if hex.EncodeToString(u.hasher.Sum(nil)) != digest {
		s.abortUpload()
		return blobProgress{}, errors.New("checksum_mismatch")
	}
	if err := u.file.Sync(); err != nil {
		s.abortUpload()
		return blobProgress{}, err
	}
	if err := u.file.Close(); err != nil {
		s.abortUpload()
		return blobProgress{}, err
	}
	path := s.blobPath(digest)
	if err := os.Rename(u.file.Name(), path); err != nil {
		s.abortUpload()
		return blobProgress{}, err
	}
	s.upload = nil
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return blobProgress{}, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return blobProgress{}, err
	}
	return blobProgress{Offset: u.offset, Complete: true}, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
