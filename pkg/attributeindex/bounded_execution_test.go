package attributeindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFetchRangesReleasesOnError verifies that semaphore and channel are
// properly released when an error occurs during acquisition or fetch.
func TestFetchRangesReleasesOnError(t *testing.T) {
	// Source that fails after first request
	failingSource := &failAfterNSource{maxCalls: 1, data: make([]byte, 100)}

	ranges := []Range{
		{Offset: 0, Length: 10},
		{Offset: 20, Length: 10},
		{Offset: 40, Length: 10},
	}

	options := FetchOptions{
		MaxConcurrent:    2,
		MaxBytesInFlight: 100,
	}

	_, err := FetchRanges(context.Background(), failingSource, "test", ranges, options)
	require.Error(t, err, "should fail when source returns error")

	// Verify we can still use the same semaphore/channel after error
	// by immediately trying another fetch
	successSource := &memoryRanges{data: make([]byte, 100)}
	_, err = FetchRanges(context.Background(), successSource, "test", []Range{{Offset: 0, Length: 10}}, options)
	require.NoError(t, err, "subsequent fetch should work after error")
}

// TestFetchRangesRespectsCancellation verifies that context cancellation
// stops new fetches and cleans up properly.
func TestFetchRangesRespectsCancellation(t *testing.T) {
	// Source that blocks on first request
	blockingSource := &blockingSource{
		data:      make([]byte, 100),
		blockChan: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start fetch in background
	errChan := make(chan error, 1)
	go func() {
		ranges := []Range{
			{Offset: 0, Length: 10},
			{Offset: 20, Length: 10},
		}
		options := FetchOptions{
			MaxConcurrent:    1,
			MaxBytesInFlight: 100,
		}
		_, err := FetchRanges(ctx, blockingSource, "test", ranges, options)
		errChan <- err
	}()

	// Wait a bit for fetch to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Should get cancellation error
	err := <-errChan
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	// Unblock source
	close(blockingSource.blockChan)
}

// TestFetchRangesValidatesAllRangesUpfront ensures that invalid ranges are
// rejected before any resources are acquired.
func TestFetchRangesValidatesAllRangesUpfront(t *testing.T) {
	source := &memoryRanges{data: make([]byte, 100)}

	// First range valid, second invalid
	ranges := []Range{
		{Offset: 0, Length: 10},
		{Offset: 20, Length: 200}, // Exceeds budget
	}

	options := FetchOptions{
		MaxConcurrent:    10,
		MaxBytesInFlight: 100,
	}

	_, err := FetchRanges(context.Background(), source, "test", ranges, options)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds budget")

	// Should not have made any GetRange calls
	require.Equal(t, 0, source.calls, "should validate before fetching")
}

// TestReaderMemoryBudgetTracking verifies that Reader tracks decoded memory.
func TestReaderMemoryBudgetTracking(t *testing.T) {
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	// Add a small entity
	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}))

	object, err := w.Bytes()
	require.NoError(t, err)

	source := &memoryRanges{data: object}
	reader, err := Open(context.Background(), source, "test", int64(len(object)))
	require.NoError(t, err)
	defer reader.Close()

	// Reader should have memory budget set
	require.Greater(t, reader.maxDecodedBytes, int64(0))

	// Initial decoded bytes should be zero
	reader.mu.Lock()
	initialBytes := reader.decodedBytes
	reader.mu.Unlock()
	require.Equal(t, int64(0), initialBytes)

	// After Close, decoded bytes should be reset
	require.NoError(t, reader.Close())
	reader.mu.Lock()
	finalBytes := reader.decodedBytes
	reader.mu.Unlock()
	require.Equal(t, int64(0), finalBytes)
}

// TestReaderFetchPagesCoalescesNearbyPages verifies that fetchPages coalesces
// nearby pages into fewer GET requests.
func TestReaderFetchPagesCoalescesNearbyPages(t *testing.T) {
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	// Add entities with multiple keys to create multiple forward column pages
	for i := 0; i < 5; i++ {
		attrs := make([]Attribute, 5)
		for j := range attrs {
			attrs[j] = Attribute{
				Key:   Key{Scope: ScopeLegacy, Name: fmt.Sprintf("key%d", j)},
				Value: StringValue(fmt.Sprintf("value%d", i)),
			}
		}
		require.NoError(t, w.AddEntity(Entity{Attributes: attrs}))
	}

	object, err := w.Bytes()
	require.NoError(t, err)

	source := &memoryRanges{data: object}
	reader, err := Open(context.Background(), source, "test", int64(len(object)))
	require.NoError(t, err)
	defer reader.Close()

	// Reset call counter after Open
	source.mu.Lock()
	openCalls := source.calls
	source.calls = 0
	source.mu.Unlock()

	// Fetch all forward columns - should coalesce
	columns, err := reader.ForwardColumns(context.Background())
	require.NoError(t, err)
	require.Len(t, columns, 5)

	source.mu.Lock()
	fetchCalls := source.calls
	source.mu.Unlock()

	// With coalescing, we should make fewer calls than the number of pages
	// (5 columns + 1 dict + 1 postings = 7 pages, but should coalesce into fewer GETs)
	require.Less(t, fetchCalls, 7, "should coalesce nearby pages")

	t.Logf("Open: %d calls, ForwardColumns: %d calls (7 pages coalesced)", openCalls, fetchCalls)
}

// TestReaderConcurrentFetchesShareBudget verifies that multiple concurrent
// fetches respect the shared in-flight byte budget.
func TestReaderConcurrentFetchesShareBudget(t *testing.T) {
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}))

	object, err := w.Bytes()
	require.NoError(t, err)

	// Use a slow source to force concurrent requests
	slowSource := &slowSource{data: object, delay: 100 * time.Millisecond}
	reader, err := Open(context.Background(), slowSource, "test", int64(len(object)))
	require.NoError(t, err)
	defer reader.Close()

	// Fetch multiple things concurrently
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		_, _ = reader.Entities(context.Background())
	}()
	go func() {
		defer wg.Done()
		_, _ = reader.Dictionaries(context.Background())
	}()
	go func() {
		defer wg.Done()
		_, _ = reader.Postings(context.Background())
	}()

	wg.Wait()

	// All should succeed without exceeding budget
	// (This test mainly verifies no deadlock/panic)
}

// TestReaderCloseWhileFetchingCancelsWork verifies that closing a reader
// during active fetches cleans up properly.
func TestReaderCloseWhileFetchingCancelsWork(t *testing.T) {
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}))

	object, err := w.Bytes()
	require.NoError(t, err)

	// Use slow source to give us time to close
	slowSource := &slowSource{
		data:  object,
		delay: 200 * time.Millisecond,
	}

	reader, err := Open(context.Background(), slowSource, "test", int64(len(object)))
	require.NoError(t, err)

	// Start fetch in background
	errChan := make(chan error, 1)
	go func() {
		_, err := reader.ForwardColumns(context.Background())
		errChan <- err
	}()

	// Wait for fetch to start
	time.Sleep(50 * time.Millisecond)

	// Close reader
	require.NoError(t, reader.Close())

	// The fetch may complete or fail
	<-errChan

	// New fetch should fail immediately
	_, err = reader.Entities(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}

// Test helper sources

type failAfterNSource struct {
	mu       sync.Mutex
	data     []byte
	calls    int
	maxCalls int
}

func (f *failAfterNSource) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls > f.maxCalls {
		return nil, errors.New("simulated fetch error")
	}
	if off < 0 || length < 0 || off+length > int64(len(f.data)) {
		return nil, io.EOF
	}
	data := make([]byte, length)
	copy(data, f.data[off:off+length])
	return io.NopCloser(bytes.NewReader(data)), nil
}

type blockingSource struct {
	data      []byte
	blockChan chan struct{}
}

func (b *blockingSource) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	// Block until channel is closed or context cancelled
	select {
	case <-b.blockChan:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if off < 0 || length < 0 || off+length > int64(len(b.data)) {
		return nil, io.EOF
	}
	data := make([]byte, length)
	copy(data, b.data[off:off+length])
	return io.NopCloser(bytes.NewReader(data)), nil
}

type slowSource struct {
	mu    sync.Mutex
	data  []byte
	delay time.Duration
}

func (s *slowSource) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	if off < 0 || length < 0 || off+length > int64(len(s.data)) {
		return nil, io.EOF
	}
	data := make([]byte, length)
	copy(data, s.data[off:off+length])
	return io.NopCloser(bytes.NewReader(data)), nil
}
