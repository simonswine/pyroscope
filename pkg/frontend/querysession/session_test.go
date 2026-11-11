package querysession

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/stretchr/testify/require"
)

func names(values ...string) []metadatastream.Element {
	out := make([]metadatastream.Element, 0, len(values))
	for _, v := range values {
		out = append(out, metadatastream.Element{&typesv1.LabelPair{Name: v}})
	}
	return out
}
func newTestHandler(t *testing.T, stream Stream) *Handler {
	t.Helper()
	h, err := New(DefaultConfig(), stream)
	require.NoError(t, err)
	t.Cleanup(func() {
		h.mu.Lock()
		ss := make([]*session, 0, len(h.sessions))
		for s := range h.sessions {
			ss = append(ss, s)
		}
		h.mu.Unlock()
		for _, s := range ss {
			h.expire(s)
		}
	})
	return h
}
func TestIncrementalPagesDeduplicateAndReplay(t *testing.T) {
	var complete atomic.Bool
	unblock := make(chan struct{})
	h := newTestHandler(t, func(ctx context.Context, r Request, visit func(metadatastream.Element) error) error {
		for _, e := range names("z", "b", "z", "a") {
			if err := visit(e); err != nil {
				return err
			}
		}
		select {
		case <-unblock:
		case <-ctx.Done():
			return ctx.Err()
		}
		for _, e := range names("b", "c", "a") {
			if err := visit(e); err != nil {
				return err
			}
		}
		complete.Store(true)
		return nil
	})
	c, err := h.start(context.Background(), "tenant", Request{Type: "LabelNames", PageSize: 3})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := c.get(ctx, 0)
	require.NoError(t, err)
	require.False(t, complete.Load())
	require.Equal(t, names("a", "b", "z"), first.elements)
	require.NotEmpty(t, first.next)
	replay, err := c.get(ctx, 3)
	require.NoError(t, err)
	require.Same(t, first, replay)
	_, err = c.get(ctx, 2)
	require.Error(t, err)
	_, err = h.lookup("other-tenant", first.next)
	require.ErrorIs(t, err, ErrCursor)
	close(unblock)
	next, err := h.lookup("tenant", first.next)
	require.NoError(t, err)
	second, err := next.get(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, names("c"), second.elements)
	require.True(t, second.exhausted)
	require.Empty(t, second.next)
	h.expire(c.session)
	_, err = h.lookup("tenant", first.next)
	require.ErrorIs(t, err, ErrCursor)
	require.Zero(t, h.bytes)
}
func TestEmptyAndLateFailure(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "late_error"}[late], func(t *testing.T) {
			boom := errors.New("backend failed")
			h := newTestHandler(t, func(_ context.Context, _ Request, visit func(metadatastream.Element) error) error {
				if late {
					if err := visit(names("a")[0]); err != nil {
						return err
					}
					return boom
				}
				return nil
			})
			c, err := h.start(context.Background(), "t", Request{Type: "LabelNames", PageSize: 1})
			require.NoError(t, err)
			p, err := c.get(context.Background(), 0)
			require.NoError(t, err)
			if !late {
				require.True(t, p.exhausted)
				require.Empty(t, p.elements)
				return
			}
			require.False(t, p.exhausted)
			next, err := h.lookup("t", p.next)
			require.NoError(t, err)
			_, err = next.get(context.Background(), 0)
			require.ErrorIs(t, err, boom)
		})
	}
}
func TestCancellationAndMemoryLimit(t *testing.T) {
	exited := make(chan struct{})
	h := newTestHandler(t, func(ctx context.Context, _ Request, _ func(metadatastream.Element) error) error {
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	})
	c, err := h.start(context.Background(), "t", Request{Type: "LabelNames"})
	require.NoError(t, err)
	c.session.cancel()
	_, err = c.get(context.Background(), 0)
	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("stream did not cancel")
	}
	require.ErrorIs(t, h.charge("t", h.config.MaxTenantBytes), ErrLimit)
}
func TestExpiryAndFederatedBudgets(t *testing.T) {
	exited := make(chan struct{})
	h := newTestHandler(t, func(ctx context.Context, _ Request, _ func(metadatastream.Element) error) error {
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	})
	h.config.TTL = 20 * time.Millisecond
	c, err := h.start(context.Background(), "a|b", Request{Type: "LabelNames"})
	require.NoError(t, err)
	h.mu.Lock()
	require.Positive(t, h.tenantBytes["a"])
	require.Equal(t, h.tenantBytes["a"], h.tenantBytes["b"])
	h.mu.Unlock()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("expired stream did not stop")
	}
	require.Eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return c.session.expired && h.bytes == 0 }, time.Second, time.Millisecond)
	h.mu.Lock()
	require.Empty(t, h.tenantBytes)
	require.Empty(t, h.tenantSessions)
	h.mu.Unlock()
}

func TestByteCapAndOversizedElement(t *testing.T) {
	h := newTestHandler(t, func(_ context.Context, _ Request, visit func(metadatastream.Element) error) error {
		for i := 0; i < 3; i++ {
			v := make([]byte, 1500)
			for j := range v {
				v[j] = 'x'
			}
			v[0] = byte('a' + i)
			if err := visit(names(string(v))[0]); err != nil {
				return err
			}
		}
		return nil
	})
	h.config.MaxFrameBytes = 4096
	c, err := h.start(context.Background(), "t", Request{Type: "LabelNames"})
	require.NoError(t, err)
	p, err := c.get(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, p.elements, 1)
	require.NotEmpty(t, p.next)
}
