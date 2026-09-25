package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func followManaged(ctx context.Context, store *stateStore, state *sessionState) error {
	client, err := store.agentForSession(ctx, state)
	if err != nil {
		return err
	}
	if !state.Uploaded {
		if time.Now().After(state.ExpiresAt) {
			return errors.New("deadline_expired: upload requires a new run")
		}
		state.Phase = "uploading"
		if err := store.save(state); err != nil {
			return err
		}
		if _, err := uploadRunBundle(ctx, client, state); err != nil {
			return err
		}
		state.Uploaded = true
		state.Phase = "ready"
		if err := store.save(state); err != nil {
			return err
		}
	}
	var run agentRunSummary
	if err := client.Request(ctx, "get_run", state.Config.RunID, nil, &run); err != nil {
		return err
	}
	if run.Phase == "ready" {
		if time.Now().After(state.ExpiresAt) {
			return errors.New("deadline_expired: cannot start run")
		}
		m, _, err := sessionRunManifest(state)
		if err != nil {
			return err
		}
		digest, err := manifestDigest(m)
		if err != nil {
			return err
		}
		state.StartRequested = true
		state.Phase = "starting"
		if err := store.save(state); err != nil {
			return err
		}
		if err := client.Request(ctx, "start_run", state.Config.RunID, struct {
			Digest string `json:"digest"`
		}{digest}, &run); err != nil {
			return fmt.Errorf("start run (resume to reconcile): %w", err)
		}
	}
	for {
		state.Phase = run.Phase
		state.Error = run.Error
		if err := store.save(state); err != nil {
			return err
		}
		switch run.Phase {
		case "starting", "running":
		case "completed", "failed", "stopped", "interrupted":
			if err := collectManaged(ctx, store, state); err != nil {
				return err
			}
			if run.Phase == "completed" {
				return nil
			}
			return fmt.Errorf("run %s: %s", run.Phase, run.Error)
		default:
			return fmt.Errorf("unexpected run phase %q", run.Phase)
		}
		if err := waitPoll(ctx, 3*time.Second); err != nil {
			return err
		}
		if err := client.Request(ctx, "get_run", state.Config.RunID, nil, &run); err != nil {
			return err
		}
	}
}

func collectManaged(ctx context.Context, store *stateStore, state *sessionState) error {
	client, err := store.agentForSession(ctx, state)
	if err != nil {
		return err
	}
	return client.DownloadArtifact(ctx, state.Config.RunID, state.Config.Results)
}
