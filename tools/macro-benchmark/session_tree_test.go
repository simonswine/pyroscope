package main

import "testing"

func TestSessionTree(t *testing.T) {
	states := sessionTreeOrder([]sessionState{
		{Config: runConfig{RunID: "child-a"}, Kind: "rerun", ParentRunID: "owner", StorageRunID: "owner"},
		{Config: runConfig{RunID: "child-b"}, Kind: "rerun", ParentRunID: "owner", StorageRunID: "owner"},
		{Config: runConfig{RunID: "owner"}},
	})
	for i, want := range []string{"owner", "├─ child-a", "└─ child-b"} {
		if got := sessionTreeLabel(states, i); got != want {
			t.Fatalf("row %d: got %q, want %q", i, got, want)
		}
	}
}

func TestListInvalidatesChildrenOfDestroyedOwner(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	owner := sessionState{Config: runConfig{RunID: "owner"}, Phase: "destroyed"}
	child := sessionState{Config: runConfig{RunID: "child"}, Kind: "rerun", ParentRunID: "owner", StorageRunID: "owner", Phase: "completed", Error: "old error", Progress: &runProgress{Stage: "old progress"}}
	for _, state := range []*sessionState{&owner, &child} {
		if err := store.save(state); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		states, err := store.list()
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 2 || states[0].Config.RunID != "owner" || states[1].Phase != "destroyed" || states[1].Error != "" || states[1].Progress != nil {
			t.Fatalf("unexpected states: %+v", states)
		}
	}
}
