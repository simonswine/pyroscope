package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestProtocolClientConcurrentRequests(t *testing.T) {
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	defer client.Close()
	defer remote.Close()
	const count = 32
	serverDone := make(chan error, 1)
	go func() {
		requests := make([]protocolFrame, 0, count)
		for i := 0; i < count; i++ {
			f, err := readProtocolFrame(remote)
			if err != nil {
				serverDone <- err
				return
			}
			requests = append(requests, f)
		}
		for i := len(requests) - 1; i >= 0; i-- {
			f := requests[i]
			err := writeProtocolFrame(remote, protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "response", RequestID: f.Header.RequestID, Body: f.Header.Body}})
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var response int
			if err := client.Request(ctx, "echo", "", i, &response); err != nil {
				t.Error(err)
				return
			}
			if response != i {
				t.Errorf("response %d for request %d", response, i)
			}
		}(i)
	}
	wg.Wait()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolClientCancelDoesNotCloseConnection(t *testing.T) {
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	defer client.Close()
	defer remote.Close()
	serverDone := make(chan error, 1)
	received := make(chan struct{})
	go func() {
		first, err := readProtocolFrame(remote)
		if err != nil {
			serverDone <- err
			return
		}
		close(received)
		second, err := readProtocolFrame(remote)
		if err != nil {
			serverDone <- err
			return
		}
		for _, f := range []protocolFrame{second, first} {
			body, _ := json.Marshal("ok")
			if err := writeProtocolFrame(remote, protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "response", RequestID: f.Header.RequestID, Body: body}}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Request(ctx, "start_run", "run-1", nil, nil) }()
	<-received
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	var response string
	ctx2, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := client.Request(ctx2, "get_run", "run-1", nil, &response); err != nil {
		t.Fatal(err)
	}
	if response != "ok" {
		t.Fatalf("response: %q", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
