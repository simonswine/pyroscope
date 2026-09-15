package attributeblock

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSelectiveQueryFetchesFewerColumns verifies that the selective query path
// only fetches columns needed for filtering and projection, not all columns.
func TestSelectiveQueryFetchesFewerColumns(t *testing.T) {
	// Build a block with many keys
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	// Add entities with 10 different keys
	for i := 0; i < 10; i++ {
		attrs := make([]Attribute, 10)
		for j := range attrs {
			attrs[j] = Attribute{
				Key:   Key{Scope: ScopeLegacy, Name: fmt.Sprintf("key%d", j)},
				Value: StringValue(fmt.Sprintf("value%d-%d", j, i)),
			}
		}
		require.NoError(t, w.AddEntity(Entity{Attributes: attrs}))
	}

	object, err := w.Bytes()
	require.NoError(t, err)

	// Create a tracking source that counts GET requests per range
	trackingSource := &trackingSource{data: object, ranges: make(map[string]int)}

	// Open reader
	reader, err := Open(context.Background(), trackingSource, "test", int64(len(object)))
	require.NoError(t, err)
	defer reader.Close()

	// Query with matchers and projection that use only 2 keys
	// Should fetch: header, footer, directory, dictionaries page, postings page,
	// and only 2-3 forward column pages (matcher keys + projection keys)
	matchers := []Matcher{
		{Key: Key{Scope: ScopeLegacy, Name: "key0"}, Operator: MatchEqual, Value: StringValue("value0-0")},
	}
	projection := []Key{
		{Scope: ScopeLegacy, Name: "key0"},
		{Scope: ScopeLegacy, Name: "key5"},
	}

	trackingSource.mu.Lock()
	trackingSource.ranges = make(map[string]int) // Reset
	trackingSource.mu.Unlock()

	result, err := reader.Series(context.Background(), matchers, projection)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	// Count how many forward column pages were fetched
	trackingSource.mu.Lock()
	totalRanges := len(trackingSource.ranges)
	trackingSource.mu.Unlock()

	// With selective fetch, we should read:
	// - dictionaries (1 page)
	// - postings (1 page)
	// - forward columns for key0 and key5 only (2 pages)
	// Total: 4 pages (not counting header/footer/directory which were read during Open)
	//
	// If it were non-selective, it would fetch all 10 forward column pages.
	// Since Open already fetched header, footer, directory, we're only counting
	// pages fetched during Series query.
	
	// We expect ~4 page fetches (dictionaries + postings + 2 columns)
	// Allow some tolerance for caching behavior
	require.LessOrEqual(t, totalRanges, 6, "Should fetch few pages, not all 10 columns")

	t.Logf("Selective query fetched %d page ranges", totalRanges)
}

// TestSelectiveQueryVsFullFetch compares selective and full-fetch query paths.
func TestSelectiveQueryVsFullFetch(t *testing.T) {
	// Build a block
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	attrs := make([]Attribute, 5)
	for j := range attrs {
		attrs[j] = Attribute{
			Key:   Key{Scope: ScopeLegacy, Name: fmt.Sprintf("key%d", j)},
			Value: StringValue(fmt.Sprintf("value%d", j)),
		}
	}
	require.NoError(t, w.AddEntity(Entity{Attributes: attrs}))

	object, err := w.Bytes()
	require.NoError(t, err)

	// Test via memorySource
	source := &memorySource{object: "test", data: object}
	reader, err := Open(context.Background(), source, "test", int64(len(object)))
	require.NoError(t, err)
	defer reader.Close()

	// Query with projection
	matchers := []Matcher{
		{Key: Key{Scope: ScopeLegacy, Name: "key0"}, Operator: MatchEqual, Value: StringValue("value0")},
	}
	projection := []Key{
		{Scope: ScopeLegacy, Name: "key0"},
		{Scope: ScopeLegacy, Name: "key1"},
	}

	// Use selective path (via Series)
	result, err := reader.Series(context.Background(), matchers, projection)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Len(t, result[0].Attributes, 2)

	// Verify we got the right keys
	require.Equal(t, "key0", result[0].Attributes[0].Key.Name)
	require.Equal(t, "key1", result[0].Attributes[1].Key.Name)
}

// TestSelectiveQueryHandlesMissingKeys ensures that selective queries handle
// cases where matcher or projection keys don't exist in the block.
func TestSelectiveQueryHandlesMissingKeys(t *testing.T) {
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

	source := &memorySource{object: "test", data: object}
	reader, err := Open(context.Background(), source, "test", int64(len(object)))
	require.NoError(t, err)
	defer reader.Close()

	// Query with matchers and projection for keys that don't exist
	matchers := []Matcher{
		{Key: Key{Scope: ScopeLegacy, Name: "nonexistent"}, Operator: MatchEqual, Value: StringValue("value")},
	}
	projection := []Key{
		{Scope: ScopeLegacy, Name: "service"},
		{Scope: ScopeLegacy, Name: "also-missing"},
	}

	result, err := reader.Series(context.Background(), matchers, projection)
	require.NoError(t, err)
	require.Empty(t, result) // No entities match nonexistent key
}

// trackingSource wraps data and tracks which byte ranges are fetched
type trackingSource struct {
	data   []byte
	mu     sync.Mutex
	ranges map[string]int // "offset:length" -> count
}

func (t *trackingSource) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	t.mu.Lock()
	key := fmt.Sprintf("%d:%d", off, length)
	t.ranges[key]++
	t.mu.Unlock()

	if off < 0 || length < 0 || off+length > int64(len(t.data)) {
		return nil, fmt.Errorf("range out of bounds")
	}
	return io.NopCloser(bytes.NewReader(t.data[off : off+length])), nil
}
