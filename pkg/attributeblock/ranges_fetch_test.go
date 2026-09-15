package attributeblock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchRanges(t *testing.T) {
	source := &memoryRanges{data: []byte("abcdefghijkl")}
	buffers, err := FetchRanges(context.Background(), source, ObjectName, []Range{
		{Offset: 0, Length: 3},
		{Offset: 8, Length: 4},
	}, FetchOptions{MaxConcurrent: 2, MaxBytesInFlight: 8})
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte("abc"), []byte("ijkl")}, buffers)
}

func TestFetchRangesRejectsRangeAboveByteBudget(t *testing.T) {
	_, err := FetchRanges(context.Background(), &memoryRanges{}, ObjectName, []Range{{Length: 9}}, FetchOptions{MaxConcurrent: 1, MaxBytesInFlight: 8})
	require.ErrorContains(t, err, "exceeds budget")
}
