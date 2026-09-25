package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type storageCredentials struct {
	Version   int    `json:"version"`
	OwnerID   string `json:"owner_id"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// Credentials belong to the storage owner, never to a child bundle or result.
// The file is created once, before MinIO starts; an existing file is never
// replaced, including after a failed or interrupted ingestion.
func createStorageCredentials(paths RunPaths) (storageCredentials, error) {
	if paths.Rerun || !validSessionID(paths.ID) {
		return storageCredentials{}, errors.New("only a fresh storage owner can create credentials")
	}
	if err := os.MkdirAll(paths.Private, 0700); err != nil {
		return storageCredentials{}, err
	}
	info, err := os.Lstat(paths.Private)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return storageCredentials{}, errors.New("unsafe private storage directory")
	}
	// Never create new credentials against a populated MinIO directory.
	if entries, err := os.ReadDir(paths.Minio); err == nil && len(entries) != 0 {
		return storageCredentials{}, errors.New("refusing new credentials for existing MinIO storage")
	} else if err != nil && !os.IsNotExist(err) {
		return storageCredentials{}, err
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return storageCredentials{}, err
	}
	c := storageCredentials{Version: 1, OwnerID: paths.ID, AccessKey: "benchmark", SecretKey: hex.EncodeToString(secret)}
	file, err := os.OpenFile(filepath.Join(paths.Private, "storage-credentials.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return storageCredentials{}, fmt.Errorf("storage credentials already exist or cannot be created: %w", err)
	}
	data, err := json.Marshal(c)
	if err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return storageCredentials{}, err
	}
	dir, err := os.Open(paths.Private)
	if err != nil {
		return storageCredentials{}, err
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return storageCredentials{}, err
	}
	return c, nil
}

func readStorageCredentials(owner RunPaths) (storageCredentials, error) {
	private, err := os.Lstat(owner.Private)
	if err != nil {
		return storageCredentials{}, err
	}
	if !private.IsDir() || private.Mode().Perm() != 0700 {
		return storageCredentials{}, errors.New("unsafe private storage directory")
	}
	path := filepath.Join(owner.Private, "storage-credentials.json")
	info, err := os.Lstat(path)
	if err != nil {
		return storageCredentials{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return storageCredentials{}, errors.New("unsafe storage credentials file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return storageCredentials{}, err
	}
	var c storageCredentials
	if err := json.Unmarshal(data, &c); err != nil {
		return storageCredentials{}, err
	}
	if c.Version != 1 || c.OwnerID != owner.ID || c.AccessKey != "benchmark" || len(c.SecretKey) != 48 {
		return storageCredentials{}, errors.New("invalid storage credential binding")
	}
	if _, err := hex.DecodeString(c.SecretKey); err != nil {
		return storageCredentials{}, err
	}
	return c, nil
}
