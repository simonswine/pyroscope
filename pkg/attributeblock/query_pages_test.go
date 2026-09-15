package attributeblock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReaderSeriesUsesForwardColumnsInsteadOfEntityPage(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
		{Key: Key{Scope: ScopeLegacy, Name: "version"}, Value: StringValue("v1")},
	}}))
	data, err := writer.Bytes()
	require.NoError(t, err)
	source := &memoryRanges{data: data}
	reader, err := Open(context.Background(), source, ObjectName, int64(len(data)))
	require.NoError(t, err)

	series, err := reader.Series(context.Background(), []Matcher{{
		Key: Key{Scope: ScopeLegacy, Name: "service"}, Operator: MatchEqual, Value: StringValue("api"),
	}}, []Key{{Scope: ScopeLegacy, Name: "version"}})
	require.NoError(t, err)
	require.Equal(t, []Entity{{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "version"}, Value: StringValue("v1")}}}}, series)
	// Open reads header/footer/directory. The query reads dictionary, forward
	// columns, and postings, but never the entity page; the dictionary is reused
	// from the reader's query-lifetime cache.
	require.Equal(t, 6, source.calls)
}
