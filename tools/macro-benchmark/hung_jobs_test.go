package main

import (
	"context"
	"testing"
)

func TestStopBeforeSubmission(t *testing.T) {
	for _, phase := range []string{"draft", "preparing", "prepared", "uploading", "ready"} {
		t.Run(phase, func(t *testing.T) {
			s, err := openStateStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			st := sessionState{Config: runConfig{RunID: "job"}, Phase: phase}
			if err := stopSession(context.Background(), s, &st); err != nil {
				t.Fatal(err)
			}
			if st.Phase != "stopped" {
				t.Fatalf("phase: %s", st.Phase)
			}
		})
	}
}

func TestDestroyMachineWithActiveJob(t *testing.T) {
	s, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := sessionState{Kind: "machine", Config: runConfig{RunID: "host"}, Phase: "draft"}
	job := sessionState{MachineID: "host", Config: runConfig{RunID: "job"}, Phase: "uploading"}
	for _, st := range []*sessionState{&m, &job} {
		if err := s.save(st); err != nil {
			t.Fatal(err)
		}
	}
	if err := destroyMachine(context.Background(), s, &m); err != nil {
		t.Fatal(err)
	}
	if m.Phase != "destroyed" {
		t.Fatal(m.Phase)
	}
	states, err := s.list()
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.MachineID == "host" && st.Phase != "destroyed" {
			t.Fatal(st.Phase)
		}
	}
}
