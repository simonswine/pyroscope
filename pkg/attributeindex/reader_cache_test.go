package attributeindex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReaderCachesPagesAndDoesNotExposeCachedData(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}))
	data, err := writer.Bytes()
	require.NoError(t, err)
	source := &memoryRanges{data: data}
	reader, err := Open(context.Background(), source, PayloadName, int64(len(data)))
	require.NoError(t, err)

	dictionaries, err := reader.Dictionaries(context.Background())
	require.NoError(t, err)
	dictionaries[0][0].Data[0] = 'X'
	_, err = reader.Dictionaries(context.Background())
	require.NoError(t, err)
	columns, err := reader.ForwardColumns(context.Background())
	require.NoError(t, err)
	columns[0][0] = 0
	columnsAgain, err := reader.ForwardColumns(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint32(1), columnsAgain[0][0])

	// Header, footer, directory, dictionary, and forward-column page.
	require.Equal(t, 5, source.calls)
	values, err := reader.Values(context.Background(), Key{Scope: ScopeLegacy, Name: "service"}, nil)
	require.NoError(t, err)
	require.Equal(t, []Value{StringValue("api")}, values)
}
