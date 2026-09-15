package attributeblock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReaderCloseReleasesCacheAndPreventsFurtherPageReads(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}))
	data, err := writer.Bytes()
	require.NoError(t, err)
	source := &memoryRanges{data: data}
	reader, err := Open(context.Background(), source, ObjectName, int64(len(data)))
	require.NoError(t, err)
	_, err = reader.Dictionaries(context.Background())
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, reader.Close())

	_, err = reader.Dictionaries(context.Background())
	require.ErrorContains(t, err, "closed")
	require.Equal(t, 4, source.calls)
}
