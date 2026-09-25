package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestAgentClientHandshake(t *testing.T) {
	root := t.TempDir()
	agent, err := openAgentServer(root)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	defer client.Close()
	defer remote.Close()
	finished := make(chan error, 1)
	go func() { finished <- agent.serveAgent(context.Background(), remote, remote) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	greeting, err := client.Handshake(ctx, "node-1", true)
	if err != nil || greeting.Node.ID != "node-1" {
		t.Fatalf("greeting=%+v err=%v", greeting, err)
	}
	var runs []agentRunSummary
	if err := client.Request(ctx, "list_runs", "", nil, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatal(runs)
	}
	client.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
