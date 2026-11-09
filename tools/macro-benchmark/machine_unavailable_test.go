package main

import (
	"context"
	"errors"
	"testing"
)

func TestUnavailableMachineStopsUnfinishedJobs(t *testing.T) {
	s, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.createMachine()
	if err != nil {
		t.Fatal(err)
	}
	m.Phase, m.InstanceID = "ready", "i-gone"
	if err := s.save(m); err != nil {
		t.Fatal(err)
	}
	var jobs []*sessionState
	for _, phase := range []string{"running", "ready", "completed", "failed"} {
		st, err := s.create("checkoutservice")
		if err != nil {
			t.Fatal(err)
		}
		st.MachineID, st.Phase, st.InstanceID = m.Config.RunID, phase, m.InstanceID
		if err := s.save(st); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, st)
	}
	err = sessionAction(context.Background(), s, jobs[0], func(context.Context, *stateStore, *sessionState) error { return errInstanceUnavailable })
	if !errors.Is(err, errInstanceUnavailable) {
		t.Fatal(err)
	}
	for i, want := range []string{"stopped", "stopped", "completed", "failed"} {
		states, err := s.list()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, st := range states {
			if st.Config.RunID != jobs[i].Config.RunID {
				continue
			}
			found = true
			if st.Phase != want {
				t.Fatalf("job %d: got %s, want %s", i, st.Phase, want)
			}
		}
		if !found {
			t.Fatal("missing job")
		}
	}
	mstate, err := s.loadMachine(m.Config.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if mstate.Phase != "interrupted" {
		t.Fatalf("machine: %s", mstate.Phase)
	}
}
