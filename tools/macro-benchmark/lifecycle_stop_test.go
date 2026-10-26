package main

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestReconcileStop(t *testing.T) {
	for _, phase := range []string{"uploading", "ready", "running", "starting", "stopped", "interrupted"} {
		t.Run(phase, func(t *testing.T) {
			local, remote := net.Pipe()
			client := newProtocolClient(local)
			t.Cleanup(func() { client.Close(); remote.Close() })
			done := make(chan error, 1)
			go func() {
				methods := []string{"get_run"}
				if phase != "ready" && phase != "uploading" {
					methods = append(methods, "stop_run")
				}
				for _, method := range methods {
					f, err := readProtocolFrame(remote)
					if err != nil {
						done <- err
						return
					}
					if f.Header.Method != method {
						t.Errorf("method = %q, want %q", f.Header.Method, method)
					}
					result := agentRunSummary{ID: "child", Phase: phase}
					if method == "stop_run" {
						result.Phase = "stopped"
					}
					body, err := json.Marshal(result)
					if err != nil {
						done <- err
						return
					}
					err = writeProtocolFrame(remote, protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "response", RequestID: f.Header.RequestID, Body: body}})
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			run, err := reconcileStop(ctx, client, "child")
			if err != nil {
				t.Fatal(err)
			}
			if run.Phase != "stopped" {
				t.Fatalf("phase = %q", run.Phase)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRerunNodeAvailableRejectsStaleStoppingSession(t *testing.T) {
	states := []sessionState{{Config: runConfig{RunID: "child"}, InstanceID: "i-test", Phase: "stopping"}}
	if err := rerunNodeAvailable(states, "i-test"); err == nil {
		t.Fatal("allowed stopping child")
	}
	states[0].Phase = "stopped"
	if err := rerunNodeAvailable(states, "i-test"); err != nil {
		t.Fatal(err)
	}
}
