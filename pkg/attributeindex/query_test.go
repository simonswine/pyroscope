package attributeindex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReaderQueriesApplyMatchersBeforeProjection(t *testing.T) {
	reader := testReader(t, []Entity{
		{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
			{Key: Key{Scope: ScopeLegacy, Name: "version"}, Value: StringValue("v1")},
			{Key: Key{Scope: ScopeResource, Name: "region"}, Value: StringValue("us-east")},
		}},
		{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
			{Key: Key{Scope: ScopeLegacy, Name: "version"}, Value: StringValue("v2")},
		}},
		{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("worker")},
			{Key: Key{Scope: ScopeLegacy, Name: "version"}, Value: StringValue("v1")},
		}},
	})
	matchers := []Matcher{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Operator: MatchEqual, Value: StringValue("api")},
		{Key: Key{Scope: ScopeLegacy, Name: "version"}, Operator: MatchRegexp, Regexp: "v[12]"},
	}

	names, err := reader.Names(context.Background(), matchers)
	require.NoError(t, err)
	require.Equal(t, []Key{
		{Scope: ScopeLegacy, Name: "service"},
		{Scope: ScopeLegacy, Name: "version"},
		{Scope: ScopeResource, Name: "region"},
	}, names)

	values, err := reader.Values(context.Background(), Key{Scope: ScopeLegacy, Name: "version"}, matchers)
	require.NoError(t, err)
	require.Equal(t, []Value{StringValue("v1"), StringValue("v2")}, values)

	series, err := reader.Series(context.Background(), matchers, []Key{{Scope: ScopeLegacy, Name: "service"}})
	require.NoError(t, err)
	require.Equal(t, []Entity{{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}}, series)
}

func TestReaderQueriesMissingLabelsAsEmptyStrings(t *testing.T) {
	reader := testReader(t, []Entity{
		{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}},
		{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "zone"}, Value: StringValue("")}}},
	})
	key := Key{Scope: ScopeLegacy, Name: "zone"}

	series, err := reader.Series(context.Background(), []Matcher{{Key: key, Operator: MatchEqual, Value: StringValue("")}}, nil)
	require.NoError(t, err)
	require.Len(t, series, 2)
	series, err = reader.Series(context.Background(), []Matcher{{Key: key, Operator: MatchNotEqual, Value: StringValue("")}}, nil)
	require.NoError(t, err)
	require.Empty(t, series)
	series, err = reader.Series(context.Background(), []Matcher{{Key: key, Operator: MatchRegexp, Regexp: ".*"}}, nil)
	require.NoError(t, err)
	require.Len(t, series, 2)
}

func testReader(t *testing.T, entities []Entity) *Reader {
	t.Helper()
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	for _, entity := range entities {
		require.NoError(t, writer.AddEntity(entity))
	}
	data, err := writer.Bytes()
	require.NoError(t, err)
	reader, err := Open(context.Background(), &memoryRanges{data: data}, PayloadName, int64(len(data)))
	require.NoError(t, err)
	return reader
}
