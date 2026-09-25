package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// protocolClient owns a single transport. The reader only delivers bounded
// responses; it never waits for a caller to consume one. Closing it does not
// imply stopping any worker that may have been started through the transport.
type protocolWrite struct {
	ctx    context.Context
	frame  protocolFrame
	result chan error
}

type protocolClient struct {
	conn    io.ReadWriteCloser
	writes  chan protocolWrite
	mu      sync.Mutex
	pending map[string]chan protocolFrame
	next    uint64
	closed  bool
	readErr error
	done    chan struct{}
}

const maxPendingRequests = 64

func newProtocolClient(conn io.ReadWriteCloser) *protocolClient {
	c := &protocolClient{conn: conn, pending: make(map[string]chan protocolFrame), done: make(chan struct{}), writes: make(chan protocolWrite, maxPendingRequests)}
	go c.readLoop()
	go c.writeLoop()
	return c
}

func (c *protocolClient) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case item := <-c.writes:
			if err := item.ctx.Err(); err != nil {
				item.result <- err
				continue
			}
			err := writeProtocolFrame(c.conn, item.frame)
			item.result <- err
			if err != nil {
				c.fail(err)
				return
			}
		}
	}
}

func (c *protocolClient) readLoop() {
	for {
		f, err := readProtocolFrame(c.conn)
		if err != nil {
			c.fail(err)
			return
		}
		if f.Header.Kind != "response" {
			c.fail(fmt.Errorf("unexpected protocol frame %q", f.Header.Kind))
			return
		}
		c.mu.Lock()
		ch, ok := c.pending[f.Header.RequestID]
		if ok {
			delete(c.pending, f.Header.RequestID)
			ch <- f
		}
		c.mu.Unlock()
		if !ok { // A canceled request may still receive a late response.
			continue
		}
	}
}

func (c *protocolClient) fail(err error) {
	c.mu.Lock()
	if !c.closed {
		c.readErr = err
		c.closed = true
		close(c.done)
		_ = c.conn.Close()
	}
	c.mu.Unlock()
}

func (c *protocolClient) Close() error {
	c.fail(io.ErrClosedPipe)
	return nil
}

// Request IDs are connection-local. A canceled request may have taken effect
// remotely: callers must reconcile authoritative state before retrying it.
func (c *protocolClient) Request(ctx context.Context, method, runID string, input, output any) error {
	if method == "" {
		return errors.New("missing method")
	}
	if runID != "" && !validSessionID(runID) {
		return errors.New("invalid run ID")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return c.exchange(ctx, protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "request", Method: method, RunID: runID, Body: body}}, output)
}

func (c *protocolClient) exchange(ctx context.Context, f protocolFrame, output any) error {
	response, err := c.exchangeFrame(ctx, f)
	if err != nil {
		return err
	}
	if output != nil {
		return json.Unmarshal(response.Header.Body, output)
	}
	return nil
}

func (c *protocolClient) exchangeFrame(ctx context.Context, f protocolFrame) (protocolFrame, error) {
	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		return protocolFrame{}, err
	}
	if len(c.pending) >= maxPendingRequests {
		c.mu.Unlock()
		return protocolFrame{}, errors.New("too many pending protocol requests")
	}
	c.next++
	id := strconv.FormatUint(c.next, 10)
	ch := make(chan protocolFrame, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	f.Header.RequestID = id
	result := make(chan error, 1)
	select {
	case c.writes <- protocolWrite{ctx: ctx, frame: f, result: result}:
	case <-ctx.Done():
		return protocolFrame{}, ctx.Err()
	case <-c.done:
		return protocolFrame{}, c.connectionError()
	}
	select {
	case err := <-result:
		if err != nil {
			return protocolFrame{}, err
		}
	case <-ctx.Done():
		return protocolFrame{}, ctx.Err()
	case <-c.done:
		return protocolFrame{}, c.connectionError()
	}
	select {
	case response := <-ch:
		return checkedResponse(response)
	case <-ctx.Done():
		return protocolFrame{}, ctx.Err()
	case <-c.done:
		// The reader may have dispatched the response just before EOF.
		select {
		case response := <-ch:
			return checkedResponse(response)
		default:
			return protocolFrame{}, c.connectionError()
		}
	}
}

func checkedResponse(response protocolFrame) (protocolFrame, error) {
	if response.Header.Error != nil {
		return protocolFrame{}, fmt.Errorf("%s: %s", response.Header.Error.Code, response.Header.Error.Message)
	}
	return response, nil
}

func (c *protocolClient) connectionError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

// UploadBlob uses a one-chunk acknowledgement window. Disconnects discard the
// private partial on the server; the next attempt restarts from byte zero.
func (c *protocolClient) UploadBlob(ctx context.Context, digest string, file *os.File) error {
	if !validDigest(digest) {
		return errors.New("invalid blob digest")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxBlobSize {
		return errors.New("invalid blob size or type")
	}
	var state blobProgress
	if err := c.Request(ctx, "upload_blob", "", blobRequest{Digest: digest, Size: info.Size()}, &state); err != nil {
		return err
	}
	if state.Complete {
		return nil
	}
	complete := false
	defer func() {
		if !complete {
			cancelCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = c.exchange(cancelCtx, protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "cancel", StreamID: digest}}, nil)
		}
	}()
	if state.Offset != 0 {
		return errors.New("unsupported upload resume offset")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, maxFramePayload)
	for offset := int64(0); offset < info.Size(); {
		if err := ctx.Err(); err != nil {
			return err
		}
		limit := len(buffer)
		if remaining := info.Size() - offset; remaining < int64(limit) {
			limit = int(remaining)
		}
		n, err := io.ReadFull(file, buffer[:limit])
		if err != nil {
			return err
		}
		frame := protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "chunk", StreamID: digest, Offset: offset}, Payload: buffer[:n]}
		if err := c.exchange(ctx, frame, &state); err != nil {
			return err
		}
		offset += int64(n)
		if state.Offset != offset || state.Complete != (offset == info.Size()) {
			return errors.New("upload acknowledgement mismatch")
		}
	}
	complete = true
	return nil
}

// Handshake verifies the identity returned by the remote host. Callers must
// verify AWS ownership before passing initialize=true for a fresh instance.
func (c *protocolClient) Handshake(ctx context.Context, nodeID string, initialize bool) (agentGreeting, error) {
	if !validSessionID(nodeID) {
		return agentGreeting{}, errors.New("invalid expected node identity")
	}
	var greeting agentGreeting
	err := c.Request(ctx, "hello", "", agentHello{Versions: []int{protocolVersion}, NodeID: nodeID, Initialize: initialize}, &greeting)
	if err != nil {
		return agentGreeting{}, err
	}
	if greeting.Version != protocolVersion || greeting.Node.ID != nodeID || greeting.Node.Version != 1 || greeting.MaxHeader <= 0 || greeting.MaxHeader > maxFrameHeader || greeting.MaxPayload <= 0 || greeting.MaxPayload > maxFramePayload {
		_ = c.Close()
		return agentGreeting{}, errors.New("agent handshake identity, version or limits mismatch")
	}
	return greeting, nil
}
