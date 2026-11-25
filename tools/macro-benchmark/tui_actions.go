package main

import "fmt"

// Reload only after the previous operation has stopped writing checkpoints.
func reloadActionState(store *stateStore, id string) (*sessionState, error) {
	states, err := store.tree()
	if err != nil {
		return nil, err
	}
	for i := range states {
		if states[i].Config.RunID == id {
			return &states[i], nil
		}
	}
	return nil, fmt.Errorf("session %s no longer exists", id)
}
