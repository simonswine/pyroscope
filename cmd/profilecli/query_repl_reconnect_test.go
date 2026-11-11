package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/grafana/pyroscope/v2/pkg/frontend/querysession"
	"github.com/stretchr/testify/require"
)

type replConcurrentOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (o *replConcurrentOutput) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Write(b)
}
func (o *replConcurrentOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}
func waitREPLText(t *testing.T, out *replConcurrentOutput, text string) {
	t.Helper()
	require.Eventually(t, func() bool { return strings.Contains(out.text(), text) }, 3*time.Second, time.Millisecond, "output: %s", out.text())
}
func sendREPLLine(t *testing.T, w io.Writer, line string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := fmt.Fprintln(w, line); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("REPL stopped reading commands")
	}
}
func waitREPLRequest(t *testing.T, requests <-chan querysession.Request) querysession.Request {
	t.Helper()
	select {
	case r := <-requests:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no WebSocket request received")
		return querysession.Request{}
	}
}
func waitREPLConnection(t *testing.T, connections <-chan *websocket.Conn) *websocket.Conn {
	t.Helper()
	select {
	case c := <-connections:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("no WebSocket connection received")
		return nil
	}
}

func TestREPLReconnectPreservesDefaultsAndInvalidatesOldSession(t *testing.T) {
	connections := make(chan *websocket.Conn, 4)
	requests := make(chan querysession.Request, 8)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		number := count.Add(1)
		connections <- conn
		for {
			var req querysession.Request
			if conn.ReadJSON(&req) != nil {
				return
			}
			requests <- req
			value, token := "new", ""
			if number == 1 {
				value, token = "old", "old-cursor"
			}
			if conn.WriteJSON(querysession.Frame{ID: req.ID, Op: "page", Type: req.Type, Strings: &querysession.StringDelta{Values: []string{value}}, Refs: []int{0}, NextToken: token}) != nil {
				return
			}
			if conn.WriteJSON(querysession.Frame{ID: req.ID, Op: "complete", QueryComplete: token == ""}) != nil {
				return
			}
		}
	}))
	defer server.Close()
	input, write := io.Pipe()
	defer input.Close()
	defer write.Close()
	var out replConcurrentOutput
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- runQueryREPL(ctx, replTestParams(server.URL), input, &out, false) }()
	first := waitREPLConnection(t, connections)
	sendREPLLine(t, write, "from now-2h")
	sendREPLLine(t, write, `selector {service_name="abcd"}`)
	sendREPLLine(t, write, "page_size 2")
	sendREPLLine(t, write, "label_names")
	original := waitREPLRequest(t, requests)
	waitREPLText(t, &out, "page complete; next "+original.ID)
	require.NoError(t, first.Close()) // Same unexpected EOF as an idle server closing the socket.
	waitREPLConnection(t, connections)
	waitREPLText(t, &out, "Reconnected.")
	sendREPLLine(t, write, "next "+original.ID)
	waitREPLText(t, &out, "disconnected session; run it again")
	sendREPLLine(t, write, "show")
	waitREPLText(t, &out, "disconnected=true")
	sendREPLLine(t, write, "label_values job")
	next := waitREPLRequest(t, requests)
	require.Equal(t, "query", next.Op)
	require.Equal(t, "LabelValues", next.Type)
	require.Equal(t, "job", next.Name)
	require.Equal(t, 2, next.PageSize)
	require.Equal(t, []string{`{service_name="abcd"}`}, next.Matchers)
	require.InDelta(t, 2*time.Hour.Milliseconds(), next.End-next.Start, 100)
	require.NotEqual(t, original.ID, next.ID)
	waitREPLText(t, &out, "["+next.ID+"] query_complete")
	require.Contains(t, out.text(), `"new"`) // Server restarted IDs at zero; stale table would reject the page.
	require.Contains(t, out.text(), "from now-2h")
	require.Contains(t, out.text(), "old queries and cursors are invalid")
	select {
	case r := <-requests:
		t.Fatalf("unexpected replay: %+v", r)
	default:
	}
	sendREPLLine(t, write, "quit")
	require.NoError(t, <-result)
}

func TestREPLReconnectDoesNotReplayUnfinishedPage(t *testing.T) {
	connections := make(chan *websocket.Conn, 4)
	requests := make(chan querysession.Request, 4)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		number := count.Add(1)
		connections <- conn
		for {
			var req querysession.Request
			if conn.ReadJSON(&req) != nil {
				return
			}
			requests <- req
			if number == 1 {
				_ = conn.WriteJSON(querysession.Frame{ID: req.ID, Op: "page", Type: req.Type, Strings: &querysession.StringDelta{Values: []string{"partial"}}, Refs: []int{0}, NextToken: "not-committed"})
				return // Drop after data, before request completion.
			}
		}
	}))
	defer server.Close()
	input, write := io.Pipe()
	defer input.Close()
	defer write.Close()
	var out replConcurrentOutput
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- runQueryREPL(ctx, replTestParams(server.URL), input, &out, false) }()
	waitREPLConnection(t, connections)
	sendREPLLine(t, write, "label_names")
	original := waitREPLRequest(t, requests)
	waitREPLConnection(t, connections)
	waitREPLText(t, &out, "Reconnected.")
	require.Contains(t, out.text(), "Unfinished requests will not be replayed")
	require.NotContains(t, out.text(), "query_complete")
	sendREPLLine(t, write, "next "+original.ID)
	waitREPLText(t, &out, "disconnected session; run it again")
	select {
	case r := <-requests:
		t.Fatalf("unfinished request was replayed: %+v", r)
	default:
	}
	sendREPLLine(t, write, "quit")
	require.NoError(t, <-result)
}

func TestREPLReconnectBackoffKeepsLocalCommandsAndQuitResponsive(t *testing.T) {
	connections := make(chan *websocket.Conn, 1)
	var refuse atomic.Bool
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if refuse.Load() {
			http.Error(w, "temporary outage", http.StatusServiceUnavailable)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections <- conn
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	input, write := io.Pipe()
	defer input.Close()
	defer write.Close()
	var out replConcurrentOutput
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- runQueryREPL(ctx, replTestParams(server.URL), input, &out, false) }()
	first := waitREPLConnection(t, connections)
	refuse.Store(true)
	require.NoError(t, first.Close())
	waitREPLText(t, &out, "Reconnect failed:")
	sendREPLLine(t, write, "from now-3h")
	sendREPLLine(t, write, "show")
	waitREPLText(t, &out, "from now-3h")
	sendREPLLine(t, write, "label_names")
	waitREPLText(t, &out, "retry the command after reconnection")
	sendREPLLine(t, write, "quit")
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("quit blocked behind reconnect backoff")
	}
	after := attempts.Load()
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, after, attempts.Load(), "retry goroutine continued after quit")
}

func TestREPLDisconnectAtEOFReportsUnfinishedRequests(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request querysession.Request
		_ = conn.ReadJSON(&request)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := runQueryREPL(ctx, replTestParams(server.URL), strings.NewReader("label_names\n"), io.Discard, false)
	require.ErrorContains(t, err, "unfinished requests (not replayed)")
	require.Equal(t, int32(1), attempts.Load())
}

func TestREPLDoesNotReconnectOnMalformedProtocol(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request querysession.Request
		if conn.ReadJSON(&request) != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"unknown_payload":{}}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := runQueryREPL(ctx, replTestParams(server.URL), strings.NewReader("label_names\n"), io.Discard, false)
	require.ErrorContains(t, err, "unknown field")
	require.Equal(t, int32(1), attempts.Load())
}

func TestREPLConnectionLostClearsConnectionStateOnly(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	q, err := s.command("label_names")
	require.NoError(t, err)
	s.table = []string{"old"}
	s.tableBytes = 19
	s.tableEpoch = 2
	s.queries[q.request.ID].next = "old-token"
	s.connectionLost()
	require.Empty(t, s.pending)
	require.Empty(t, s.table)
	require.Zero(t, s.tableBytes)
	require.Zero(t, s.tableEpoch)
	require.True(t, s.queries[q.request.ID].disconnected)
	require.False(t, s.queries[q.request.ID].busy)
	require.Empty(t, s.queries[q.request.ID].next)
	require.Equal(t, "now-1h", s.from)
	require.Equal(t, "{}", s.selector)
	_, err = s.command("cancel " + q.request.ID)
	require.ErrorContains(t, err, "disconnected session")
	next, err := s.command("series")
	require.NoError(t, err)
	require.NotEqual(t, q.request.ID, next.request.ID)
}
