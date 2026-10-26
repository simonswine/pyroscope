package main

import (
	"context"
	"strings"
	"testing"
)

func TestMachineMigrationAndTree(t *testing.T) {
	s, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := sessionState{Config: runConfig{RunID: "owner"}, Phase: "completed", InstanceID: "i-existing", KeyName: "key"}
	child := sessionState{Config: runConfig{RunID: "child"}, Kind: "rerun", ParentRunID: "owner", StorageRunID: "owner", Phase: "completed"}
	for _, st := range []*sessionState{&owner, &child} {
		if err := s.save(st); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Kind != "machine" || rows[1].MachineID != "owner" || rows[2].MachineID != "owner" {
		t.Fatalf("unexpected tree: %+v", rows)
	}
	m, err := s.loadMachine("owner")
	if err != nil {
		t.Fatal(err)
	}
	if m.InstanceID != "i-existing" || m.Plan != nil || m.Phase != "ready" {
		t.Fatalf("bad migration: %+v", m)
	}
	if _, err := s.tree(); err != nil {
		t.Fatal(err)
	}
	view := tuiDashboard(160, 40, s.root, rows, 0, "", false, nil, "", 0)
	for _, text := range []string{"SELECTED MACHINE", "▣", "●", "provision", "update IP"} {
		if !strings.Contains(view, text) {
			t.Errorf("missing %q", text)
		}
	}
	view = tuiDashboard(160, 40, s.root, rows, 1, "", false, nil, "", 0)
	if !strings.Contains(view, "SELECTED SESSION") || strings.Contains(view, "i update IP") {
		t.Fatal("session has machine controls")
	}
}

func TestRequireReadyMachine(t *testing.T) {
	s, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.createMachine()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"draft", "provisioning", "destroying", "destroyed", "error", "ready"} {
		m.Phase = phase
		m.InstanceID = ""
		if err := s.save(m); err != nil {
			t.Fatal(err)
		}
		if err := s.requireReadyMachine(m.Config.RunID); err == nil {
			t.Fatalf("accepted %s without instance", phase)
		}
		m.InstanceID = "i-test"
		if err := s.save(m); err != nil {
			t.Fatal(err)
		}
		err := s.requireReadyMachine(m.Config.RunID)
		if (err == nil) != (phase == "ready") {
			t.Fatalf("phase %s: %v", phase, err)
		}
		if phase != "ready" {
			st := sessionState{MachineID: m.Config.RunID}
			if err := followManaged(context.Background(), s, &st); err == nil || !strings.Contains(err.Error(), "not ready") {
				t.Fatalf("deployment not rejected: %v", err)
			}
		}
	}
}

func TestValidateMachineForm(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "abc", "2147483648"} {
		if err := validateMachineForm(&runConfig{}, value); err == nil {
			t.Errorf("accepted volume %q", value)
		}
	}
	cfg := runConfig{}
	if err := validateMachineForm(&cfg, "500"); err != nil || cfg.VolumeSizeGiB != 500 {
		t.Fatalf("valid volume rejected: %v", err)
	}
}

func TestMachineBusyAndArchive(t *testing.T) {
	s, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.createMachine()
	if err != nil {
		t.Fatal(err)
	}
	st := sessionState{Config: runConfig{RunID: "run"}, MachineID: m.Config.RunID, Phase: "running"}
	if err := s.save(&st); err != nil {
		t.Fatal(err)
	}
	if err := s.machineBusy(m.Config.RunID); err == nil {
		t.Fatal("active machine accepted")
	}
	if err := s.archiveMachine(m.Config.RunID); err == nil {
		t.Fatal("archived machine with sessions")
	}
	st.Phase = "completed"
	if err := s.save(&st); err != nil {
		t.Fatal(err)
	}
	child := sessionState{Config: runConfig{RunID: "child"}, MachineID: m.Config.RunID, Phase: "completed", StorageRunID: "run", ParentRunID: "run"}
	if err := s.save(&child); err != nil {
		t.Fatal(err)
	}
	if err := s.archiveMachine(m.Config.RunID); err != nil {
		t.Fatal(err)
	}
	states, err := s.list()
	if err != nil || len(states) != 0 {
		t.Fatalf("sessions remain after archiving machine: %v %v", states, err)
	}
}
