package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
)

// agentForSession performs the AWS ownership check on each attach. The SSH
// transport is shared by TUI operations and owned by the controller, not by
// an individual cancelable action.
func (s *stateStore) agentForSession(ctx context.Context, state *sessionState) (*protocolClient, error) {
	host, err := sessionHost(ctx, state)
	if err != nil {
		return nil, err
	}
	id := state.Config.RunID
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	// One agent holds the node's control lease. A child shares its owner's
	// instance; close the previous connection before bootstrapping current code.
	for other, address := range s.agentAddresses {
		if other != id && address == host.address {
			_ = s.agents[other].Close()
			delete(s.agents, other)
			delete(s.agentAddresses, other)
		}
	}
	if cached := s.agents[id]; cached != nil {
		cached.mu.Lock()
		closed := cached.closed
		cached.mu.Unlock()
		if !closed && s.agentAddresses[id] == host.address {
			return cached, nil
		}
		_ = cached.Close()
		delete(s.agents, id)
		delete(s.agentAddresses, id)
	}
	binary := filepath.Join(state.Config.Bundle, "macro-benchmark")
	// EC2 may report running before sshd accepts connections. Retry bootstrap
	// on this same managed transport path, never through a separate SSH poll.
	attachCtx, stop := context.WithTimeout(ctx, 10*time.Minute)
	defer stop()
	var client *protocolClient
	backoff := 2 * time.Second
	for {
		if err := attachCtx.Err(); err != nil {
			return nil, err
		}
		client, err = host.connectAgent(s.transportCtx, binary)
		if err == nil {
			break
		}
		if !retryBootstrapConnection(err) {
			return nil, fmt.Errorf("connect agent: %w", err)
		}
		log.Printf("agent SSH not ready; retrying: %v", err)
		if waitErr := waitPoll(attachCtx, backoff); waitErr != nil {
			return nil, fmt.Errorf("connect agent: %w (last error: %v)", waitErr, err)
		}
		backoff = min(backoff*2, 10*time.Second)
	}
	nodeID := id
	if state.Kind == "rerun" {
		nodeID = state.StorageRunID
	}
	if _, err := client.Handshake(attachCtx, nodeID, state.Kind != "rerun"); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("agent handshake: %w", err)
	}
	s.agents[id] = client
	s.agentAddresses[id] = host.address
	return client, nil
}

func retryBootstrapConnection(err error) bool {
	message := err.Error()
	return strings.Contains(message, "ssh: connect to host") ||
		strings.Contains(message, "Connection refused") ||
		strings.Contains(message, "Connection timed out") ||
		strings.Contains(message, "Connection closed")
}
