package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentIdentityAndLease(t *testing.T) {
	root := t.TempDir()
	a, err := openAgentServer(root)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := openAgentServer(root); err == nil {
		b.Close()
		t.Fatal("second controller acquired lease")
	}
	if _, err := a.hello(agentHello{Versions: []int{1}, NodeID: "node-1"}); err == nil {
		t.Fatal("initialized implicitly")
	}
	if _, err := a.hello(agentHello{Versions: []int{2}, NodeID: "node-1", Initialize: true}); err == nil {
		t.Fatal("accepted unknown protocol")
	}
	greeting, err := a.hello(agentHello{Versions: []int{1}, NodeID: "node-1", Initialize: true})
	if err != nil || greeting.Node.ID != "node-1" {
		t.Fatalf("greeting=%+v err=%v", greeting, err)
	}
	if _, err := a.hello(agentHello{Versions: []int{1}, NodeID: "node-2", Initialize: true}); err == nil {
		t.Fatal("accepted identity mismatch")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = openAgentServer(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.hello(agentHello{Versions: []int{1}, NodeID: "node-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentStdioReadOnly(t *testing.T) {
	root := t.TempDir()
	a, err := openAgentServer(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	request := func(id, method, runID string, body any) protocolFrame {
		b, _ := json.Marshal(body)
		return protocolFrame{Header: frameHeader{Version: 1, Kind: "request", RequestID: id, Method: method, RunID: runID, Body: b}}
	}
	var input, output bytes.Buffer
	for _, f := range []protocolFrame{request("1", "hello", "", agentHello{Versions: []int{1}, NodeID: "node-1", Initialize: true}), request("2", "list_runs", "", nil), request("3", "start_run", "run-1", nil)} {
		if err := writeProtocolFrame(&input, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.serveAgent(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f, err := readProtocolFrame(&output)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && f.Header.Error != nil {
			t.Fatal(f.Header.Error)
		}
		if i == 2 && f.Header.Error == nil {
			t.Fatal("start_run must not be acknowledged")
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "runs", "run-1", "worker"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs", "run-1", "worker", "started"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	run, err := a.runSummary("run-1")
	if err != nil || run.Phase != "interrupted" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}
