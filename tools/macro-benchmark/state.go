package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const stateVersion = 1

type sessionState struct {
	Version        int
	Kind           string
	ParentRunID    string
	StorageRunID   string
	Config         runConfig
	SourceDir      string
	Plan           *runPlan
	Phase          string
	Error          string
	UpdatedAt      time.Time
	ExpiresAt      time.Time
	KeyName        string
	GroupID        string
	InstanceID     string
	Uploaded       bool
	StartRequested bool
}

type stateStore struct {
	root           string
	lock           *os.File
	transportCtx   context.Context
	agentMu        sync.Mutex
	agents         map[string]*protocolClient
	agentAddresses map[string]string
}

func stateDirectory() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "pyroscope-macro-benchmark"), nil
}

func openStateStore(root string) (*stateStore, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(root, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another controller owns %s: %w", root, err)
	}
	return &stateStore{root: root, lock: lock, transportCtx: context.Background(), agents: make(map[string]*protocolClient), agentAddresses: make(map[string]string)}, nil
}

func (s *stateStore) Close() error {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	for _, agent := range s.agents {
		_ = agent.Close()
	}
	return s.lock.Close()
}

// Rename publishes a complete snapshot; fsync makes both content and the rename
// durable before a caller proceeds to the next external side effect.
func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *stateStore) path(id string) string {
	return filepath.Join(s.root, "sessions", id, "state.json")
}

func (s *stateStore) save(state *sessionState) error {
	if !validSessionID(state.Config.RunID) {
		return errors.New("invalid session ID")
	}
	state.Version = stateVersion
	state.UpdatedAt = time.Now().UTC()
	return atomicJSON(s.path(state.Config.RunID), state)
}

func validSessionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *stateStore) list() ([]sessionState, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "sessions"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var states []sessionState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(s.path(entry.Name()))
		if os.IsNotExist(err) {
			continue
		} // A crash before the first snapshot.
		if err != nil {
			return nil, err
		}
		var state sessionState
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("state %s: %w", entry.Name(), err)
		}
		if state.Version != stateVersion || state.Config.RunID != entry.Name() || !validSessionID(state.Config.RunID) {
			return nil, fmt.Errorf("unsupported or invalid state: %s", entry.Name())
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Config.RunID < states[j].Config.RunID })
	return states, nil
}

// archive hides a terminal resource-free session from the active list while
// retaining its snapshot for audit or manual recovery. It never deletes AWS.
func (s *stateStore) archive(id string) error {
	if !validSessionID(id) {
		return errors.New("invalid session ID")
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return err
	}
	var state sessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != stateVersion || state.Config.RunID != id {
		return errors.New("invalid session snapshot")
	}
	switch state.Phase {
	case "draft":
		if state.InstanceID != "" || state.GroupID != "" || state.KeyName != "" || state.StartRequested {
			return errors.New("draft still references AWS resources")
		}
	case "destroyed":
	default:
		return fmt.Errorf("only draft or destroyed sessions can be archived (phase %q)", state.Phase)
	}
	destDir := filepath.Join(s.root, "archived-sessions")
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return err
	}
	dest := filepath.Join(destDir, id)
	if _, err := os.Lstat(dest); err == nil {
		return errors.New("session already archived")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(filepath.Dir(s.path(id)), dest); err != nil {
		return err
	}
	for _, dir := range []string{s.root, filepath.Join(s.root, "sessions"), destDir} {
		file, err := os.Open(dir)
		if err != nil {
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (s *stateStore) create(dataset string) (*sessionState, error) {
	id, err := observabilityName()
	if err != nil {
		return nil, err
	}
	source, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cfg := defaultRunConfig()
	cfg.RunID, cfg.KeyPath = id, filepath.Join(s.root, "ssh", "id_ed25519")
	cfg.Bundle, cfg.Results = filepath.Join(s.root, "bundles", id), filepath.Join(s.root, "results")
	cfg.Inputs.Dataset = dataset
	cfg.Inputs.TenantID = id
	state := &sessionState{Config: cfg, SourceDir: source, Phase: "draft"}
	return state, s.save(state)
}
