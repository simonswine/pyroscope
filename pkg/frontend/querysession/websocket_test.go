package querysession

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/grafana/dskit/user"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/stretchr/testify/require"
)

func socketServer(t *testing.T, h *Handler) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(user.InjectOrgID(r.Context(), "tenant")))
	}))
	t.Cleanup(s.Close)
	return "ws" + strings.TrimPrefix(s.URL, "http")
}
func readFrame(t *testing.T, c *websocket.Conn) frame {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	var f frame
	require.NoError(t, c.ReadJSON(&f))
	return f
}
func TestSharedDictionaryAndPagination(t *testing.T) {
	h := newTestHandler(t, func(_ context.Context, _ Request, visit func(metadatastream.Element) error) error {
		for _, e := range names("b", "a") {
			if err := visit(e); err != nil {
				return err
			}
		}
		return nil
	})
	url := socketServer(t, h)
	c, _, err := websocket.DefaultDialer.Dial(url+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteJSON(Request{ID: "q1", Op: "query", Type: "LabelNames", PageSize: 1}))
	first := readFrame(t, c)
	require.Equal(t, "page", first.Op)
	require.Equal(t, []string{"b"}, first.Strings.Values)
	require.Equal(t, []int{0}, first.Refs)
	require.NotEmpty(t, first.NextToken)
	require.Equal(t, "complete", readFrame(t, c).Op)
	require.NoError(t, c.WriteJSON(Request{ID: "q2", Op: "query", Type: "LabelNames", PageSize: 1}))
	second := readFrame(t, c)
	require.Empty(t, second.Strings.Values)
	require.Equal(t, []int{0}, second.Refs)
	require.Equal(t, 1, second.Strings.Start)
	readFrame(t, c)
	require.NoError(t, c.WriteJSON(Request{ID: "n1", Op: "next", NextToken: first.NextToken}))
	next := readFrame(t, c)
	require.Equal(t, []string{"a"}, next.Strings.Values)
	require.Equal(t, []int{1}, next.Refs)
	readFrame(t, c)
	// Cached pages are re-encoded against the current shared table, without deltas.
	require.NoError(t, c.WriteJSON(Request{ID: "replay", Op: "next", NextToken: first.NextToken}))
	replay := readFrame(t, c)
	require.Empty(t, replay.Strings.Values)
	require.Equal(t, next.Refs, replay.Refs)
	readFrame(t, c)
	require.NoError(t, c.WriteJSON(Request{ID: "end", Op: "next", NextToken: next.NextToken}))
	require.Equal(t, "page", readFrame(t, c).Op)
	require.True(t, readFrame(t, c).QueryComplete)
}
func TestNegotiationAndOrigin(t *testing.T) {
	h := newTestHandler(t, func(context.Context, Request, func(metadatastream.Element) error) error { return nil })
	url := socketServer(t, h)
	for _, accept := range []string{"", "?accept=application/protobuf"} {
		_, resp, err := websocket.DefaultDialer.Dial(url+accept, nil)
		require.Error(t, err)
		require.Equal(t, http.StatusNotAcceptable, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
	_, resp, err := websocket.DefaultDialer.Dial(url+"?accept=application/json", http.Header{"Origin": []string{"https://other.example"}})
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	dialer := *websocket.DefaultDialer
	dialer.EnableCompression = true
	c, resp, err := dialer.Dial(url+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	require.Empty(t, resp.Header.Get("Sec-WebSocket-Extensions"))
}
func TestDictionaryBudgetIsTransactional(t *testing.T) {
	h := newTestHandler(t, func(_ context.Context, r Request, visit func(metadatastream.Element) error) error {
		values := []string{"a", "b"}
		if r.Name == "single" {
			values = values[:1]
		}
		for _, e := range names(values...) {
			if err := visit(e); err != nil {
				return err
			}
		}
		return nil
	})
	h.config.MaxDictionaryEntries = 1
	c, _, err := websocket.DefaultDialer.Dial(socketServer(t, h)+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteJSON(Request{ID: "too-many", Op: "query", Type: "LabelNames"}))
	require.Equal(t, "resource_exhausted", readFrame(t, c).Code)
	require.NoError(t, c.WriteJSON(Request{ID: "small", Op: "query", Type: "LabelNames", Name: "single"}))
	f := readFrame(t, c)
	require.Equal(t, 0, f.Strings.Start)
	require.Equal(t, []string{"a"}, f.Strings.Values)
	readFrame(t, c)
	h.Close()
	_, _, err = c.ReadMessage()
	require.Error(t, err)
}

func TestClientResetPreservesPaginationAndReplays(t *testing.T) {
	h := newTestHandler(t, func(_ context.Context, _ Request, visit func(metadatastream.Element) error) error {
		for _, e := range names("a", "b") {
			if err := visit(e); err != nil {
				return err
			}
		}
		return nil
	})
	c, _, err := websocket.DefaultDialer.Dial(socketServer(t, h)+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteJSON(Request{ID: "q", Op: "query", Type: "LabelNames", PageSize: 1}))
	first := readFrame(t, c)
	readFrame(t, c)
	require.Equal(t, uint32(0), first.TableEpoch)
	for epoch := uint32(1); epoch <= 2; epoch++ {
		id := fmt.Sprintf("reset%d", epoch)
		require.NoError(t, c.WriteJSON(Request{ID: id, Op: "reset_string_table"}))
		reset := readFrame(t, c)
		require.Equal(t, "table_reset", reset.Op)
		require.Equal(t, id, reset.ID)
		require.Equal(t, epoch, reset.TableEpoch)
		require.Equal(t, "complete", readFrame(t, c).Op)
		require.NoError(t, c.WriteJSON(Request{ID: fmt.Sprintf("next%d", epoch), Op: "next", NextToken: first.NextToken}))
		p := readFrame(t, c)
		require.Equal(t, "page", p.Op)
		require.Equal(t, epoch, p.TableEpoch)
		require.Zero(t, p.Strings.Start)
		require.Equal(t, []string{"b"}, p.Strings.Values)
		require.Equal(t, []int{0}, p.Refs)
		readFrame(t, c)
	}
}

func TestServerResetAtDictionaryLimitAndReplay(t *testing.T) {
	h := newTestHandler(t, func(_ context.Context, _ Request, visit func(metadatastream.Element) error) error {
		for _, e := range names("a", "b", "c") {
			if err := visit(e); err != nil {
				return err
			}
		}
		return nil
	})
	h.config.MaxDictionaryEntries = 1
	c, _, err := websocket.DefaultDialer.Dial(socketServer(t, h)+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteJSON(Request{ID: "q", Op: "query", Type: "LabelNames", PageSize: 1}))
	first := readFrame(t, c)
	readFrame(t, c)
	token := first.NextToken
	for epoch, value := range []string{"b", "c", "b"} {
		require.NoError(t, c.WriteJSON(Request{ID: fmt.Sprintf("n%d", epoch), Op: "next", NextToken: token}))
		reset := readFrame(t, c)
		require.Equal(t, "table_reset", reset.Op)
		require.Empty(t, reset.ID)
		require.Equal(t, uint32(epoch+1), reset.TableEpoch)
		p := readFrame(t, c)
		require.Equal(t, "page", p.Op)
		require.Equal(t, reset.TableEpoch, p.TableEpoch)
		require.Zero(t, p.Strings.Start)
		require.Equal(t, []string{value}, p.Strings.Values)
		require.Equal(t, []int{0}, p.Refs)
		readFrame(t, c)
		token = p.NextToken
		if epoch == 1 {
			token = first.NextToken
		} // Replay a cached page after another reset.
	}
	h.Close()
	require.Eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.bytes == 0 }, time.Second, time.Millisecond)
}

func TestResetsAreOrderedWithMultiplexedPages(t *testing.T) {
	h := newTestHandler(t, func(_ context.Context, r Request, visit func(metadatastream.Element) error) error {
		return visit(names(r.Name)[0])
	})
	c, _, err := websocket.DefaultDialer.Dial(socketServer(t, h)+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	for i := 0; i < 6; i++ {
		require.NoError(t, c.WriteJSON(Request{ID: fmt.Sprintf("q%d", i), Op: "query", Type: "LabelNames", Name: fmt.Sprintf("v%d", i)}))
		require.NoError(t, c.WriteJSON(Request{ID: fmt.Sprintf("r%d", i), Op: "reset_string_table"}))
	}
	var table []string
	var epoch uint32
	completed := 0
	for completed < 12 {
		f := readFrame(t, c)
		switch f.Op {
		case "table_reset":
			require.Equal(t, epoch+1, f.TableEpoch)
			epoch = f.TableEpoch
			table = nil
		case "page":
			require.Equal(t, epoch, f.TableEpoch)
			require.Equal(t, len(table), f.Strings.Start)
			table = append(table, f.Strings.Values...)
			require.Len(t, f.Refs, 1)
			require.GreaterOrEqual(t, f.Refs[0], 0)
			require.Less(t, f.Refs[0], len(table))
			require.Equal(t, "v"+strings.TrimPrefix(f.ID, "q"), table[f.Refs[0]])
		case "complete":
			completed++
		default:
			t.Fatalf("unexpected frame: %+v", f)
		}
	}
	require.Equal(t, uint32(6), epoch)
}

func TestMultiplexAndCancellation(t *testing.T) {
	slowStarted := make(chan struct{})
	h := newTestHandler(t, func(ctx context.Context, r Request, visit func(metadatastream.Element) error) error {
		if r.Name == "slow" {
			close(slowStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		return visit(names("fast")[0])
	})
	c, _, err := websocket.DefaultDialer.Dial(socketServer(t, h)+"?accept=application/json", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteJSON(Request{ID: "slow", Op: "query", Type: "LabelNames", Name: "slow"}))
	<-slowStarted
	require.NoError(t, c.WriteJSON(Request{ID: "fast", Op: "query", Type: "LabelNames"}))
	require.Equal(t, "fast", readFrame(t, c).ID)
	require.Equal(t, "fast", readFrame(t, c).ID)
	require.NoError(t, c.WriteJSON(Request{ID: "cancel", Op: "cancel", TargetID: "slow"}))
	foundError, foundAck := false, false
	for i := 0; i < 2; i++ {
		f := readFrame(t, c)
		if f.ID == "slow" {
			require.Equal(t, "cancelled", f.Code)
			foundError = true
		}
		if f.ID == "cancel" {
			foundAck = true
		}
	}
	require.True(t, foundError)
	require.True(t, foundAck)
}
