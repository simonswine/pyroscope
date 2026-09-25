package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestProtocolCancelDuringBlockedWrite(t *testing.T) {
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	defer client.Close()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Request(ctx, "upload_blob", "", blobRequest{Digest: "abc", Size: 1}, nil) }()
	// net.Pipe has no reader yet. The local operation must still detach without
	// closing the shared transport or waiting for a blocked write.
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked on transport")
	}
}
