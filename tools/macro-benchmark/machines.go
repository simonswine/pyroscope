package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// machineState owns infrastructure independently of benchmark sessions. Snapshot
// uses the existing lifecycle representation, never a benchmark plan or progress.
type machineState struct {
	Version  int
	ID       string
	Snapshot sessionState
}

func (s *stateStore) machinePath(id string) string {
	return filepath.Join(s.root, "machines", id, "state.json")
}
func (s *stateStore) saveMachineSnapshot(state *sessionState) error {
	if !validSessionID(state.Config.RunID) {
		return errors.New("invalid machine ID")
	}
	state.Kind = "machine"
	state.UpdatedAt = time.Now().UTC()
	return atomicJSON(s.machinePath(state.Config.RunID), machineState{Version: 1, ID: state.Config.RunID, Snapshot: *state})
}
func (s *stateStore) loadMachine(id string) (sessionState, error) {
	if !validSessionID(id) {
		return sessionState{}, errors.New("invalid machine ID")
	}
	data, err := os.ReadFile(s.machinePath(id))
	if err != nil {
		return sessionState{}, err
	}
	var m machineState
	if err := json.Unmarshal(data, &m); err != nil {
		return sessionState{}, err
	}
	if m.Version != 1 || m.ID != id || m.Snapshot.Config.RunID != id || m.Snapshot.Kind != "machine" {
		return sessionState{}, errors.New("invalid machine snapshot")
	}
	return m.Snapshot, nil
}
func (s *stateStore) migrateMachines(states []sessionState) error {
	for i := range states {
		st := &states[i]
		if st.MachineID != "" || st.Kind == "rerun" || (st.InstanceID == "" && st.GroupID == "" && st.KeyName == "" && st.Phase != "destroyed") {
			continue
		}
		id := st.Config.RunID
		if _, err := s.loadMachine(id); os.IsNotExist(err) {
			m := *st
			m.Kind = "machine"
			m.Plan = nil
			m.Progress = nil
			m.Uploaded = false
			m.StartRequested = false
			if m.Phase != "destroyed" && m.Phase != "destroying" {
				m.Phase = "ready"
				if m.InstanceID == "" {
					m.Phase = "provisioning"
				}
			}
			if err := s.saveMachineSnapshot(&m); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		st.MachineID = id
		if err := s.save(st); err != nil {
			return err
		}
	}
	owners := map[string]string{}
	for _, st := range states {
		owners[st.Config.RunID] = st.MachineID
	}
	for i := range states {
		st := &states[i]
		if st.MachineID == "" && st.Kind == "rerun" && owners[st.StorageRunID] != "" {
			st.MachineID = owners[st.StorageRunID]
			if err := s.save(st); err != nil {
				return err
			}
		}
	}
	for i := range states {
		st := &states[i]
		if st.MachineID == "" || st.Phase == "destroyed" {
			continue
		}
		m, err := s.loadMachine(st.MachineID)
		if err != nil {
			return err
		}
		if m.Phase == "destroyed" {
			st.Phase = "destroyed"
			st.Progress = nil
			if err := s.save(st); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *stateStore) tree() ([]sessionState, error) {
	sessions, err := s.list()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "machines"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var rows []sessionState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := s.loadMachine(entry.Name())
		if err != nil {
			return nil, err
		}
		rows = append(rows, m)
		for _, st := range sessions {
			if st.MachineID == m.Config.RunID {
				st.InstanceID, st.GroupID, st.KeyName = m.InstanceID, m.GroupID, m.KeyName
				st.Config.Region, st.Config.KeyPath, st.Config.PrivateIP = m.Config.Region, m.Config.KeyPath, m.Config.PrivateIP
				rows = append(rows, st)
			}
		}
	}
	for _, st := range sessions {
		if st.MachineID == "" {
			rows = append(rows, st)
		}
	}
	return rows, nil
}
func (s *stateStore) createMachine() (*sessionState, error) {
	id, err := observabilityName()
	if err != nil {
		return nil, err
	}
	cfg := defaultRunConfig()
	cfg.RunID = id
	cfg.KeyPath = filepath.Join(s.root, "ssh", "id_ed25519")
	cfg.Results = filepath.Join(s.root, "results")
	m := &sessionState{Kind: "machine", Config: cfg, Phase: "draft"}
	return m, s.saveMachineSnapshot(m)
}
func (s *stateStore) requireReadyMachine(id string) error {
	if id == "" {
		return errors.New("select a ready machine first")
	}
	m, err := s.loadMachine(id)
	if err != nil {
		return fmt.Errorf("load machine: %w", err)
	}
	if m.Phase != "ready" || m.InstanceID == "" {
		return fmt.Errorf("machine %s is not ready (phase %s); provision it first", id, m.Phase)
	}
	return nil
}

func (s *stateStore) machineBusy(id string) error {
	states, err := s.list()
	if err != nil {
		return err
	}
	for _, st := range states {
		if st.MachineID == id && (activePhase(st.Phase) || st.Phase == "ready") {
			return fmt.Errorf("machine has active session %s; stop it first", st.Config.RunID)
		}
	}
	return nil
}
func provisionMachine(ctx context.Context, s *stateStore, m *sessionState) error {
	if m.Kind != "machine" {
		return errors.New("select a machine")
	}
	if m.Phase == "destroyed" || m.Phase == "destroying" {
		return errors.New("machine is destroyed")
	}
	if err := completeConfig(ctx, &m.Config); err != nil {
		return err
	}
	if err := s.save(m); err != nil {
		return err
	}
	if err := provisionAWS(ctx, s, m); err != nil {
		m.Error = err.Error()
		_ = s.save(m)
		return err
	}
	m.Phase, m.Error = "ready", ""
	return s.save(m)
}
func destroyMachine(ctx context.Context, s *stateStore, m *sessionState) error {
	if m.Kind != "machine" {
		return errors.New("select a machine")
	}
	// Destruction is the recovery path even when a worker cannot be stopped.
	if err := destroySession(ctx, s, m); err != nil {
		return err
	}
	states, err := s.list()
	if err != nil {
		return err
	}
	for i := range states {
		if states[i].MachineID == m.Config.RunID {
			states[i].Phase = "destroyed"
			if err := s.save(&states[i]); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *stateStore) archiveMachine(id string) error {
	m, err := s.loadMachine(id)
	if err != nil {
		return err
	}
	if m.Phase != "destroyed" && !(m.Phase == "draft" && m.InstanceID == "" && m.GroupID == "" && m.KeyName == "") {
		return errors.New("destroy machine before archiving")
	}
	sessions, err := s.list()
	if err != nil {
		return err
	}
	pending := make(map[string]sessionState)
	for _, st := range sessions {
		if st.MachineID != id {
			continue
		}
		switch st.Phase {
		case "draft":
			if st.InstanceID != "" || st.GroupID != "" || st.KeyName != "" || st.StartRequested {
				return errors.New("draft still references AWS resources")
			}
		case "completed", "failed", "stopped", "interrupted", "prepared", "destroyed":
		default:
			return fmt.Errorf("session %s must be stopped before archiving machine", st.Config.RunID)
		}
		pending[st.Config.RunID] = st
	}
	dest := filepath.Join(s.root, "archived-machines")
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dest, id)); !os.IsNotExist(err) {
		return errors.New("machine archive already exists")
	}
	// Archive leaves first so reruns never outlive their storage source.
	for len(pending) > 0 {
		progress := false
		for sessionID := range pending {
			dependent := false
			for _, st := range sessions {
				if st.StorageRunID == sessionID || st.ParentRunID == sessionID {
					dependent = true
					break
				}
			}
			if dependent {
				continue
			}
			if err := s.archive(sessionID); err != nil {
				return fmt.Errorf("archive machine session %s: %w", sessionID, err)
			}
			delete(pending, sessionID)
			for i, st := range sessions {
				if st.Config.RunID == sessionID {
					sessions = append(sessions[:i], sessions[i+1:]...)
					break
				}
			}
			progress = true
		}
		if !progress {
			return errors.New("machine sessions have dependent sessions outside the machine or cyclic dependencies")
		}
	}
	return os.Rename(filepath.Dir(s.machinePath(id)), filepath.Join(dest, id))
}
