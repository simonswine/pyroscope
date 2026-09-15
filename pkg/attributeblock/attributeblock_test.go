package attributeblock

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type memoryRanges struct {
	data  []byte
	calls int
}

func (m *memoryRanges) GetRange(_ context.Context, _ string, off, length int64) (io.ReadCloser, error) {
	m.calls++
	if off < 0 || length < 0 || off+length > int64(len(m.data)) {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(m.data[off : off+length])), nil
}

func TestAttributeBlockV1_RoundTrip(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service.name"}, Value: StringValue("api")},
		{Key: Key{Scope: ScopeResource, Name: "enabled"}, Value: Value{Type: ValueBool, Data: []byte{1}}},
		{Key: Key{Scope: ScopeResource, Name: "build"}, Value: Value{Type: ValueInt64, Data: []byte{42, 0, 0, 0, 0, 0, 0, 0}}},
	}}))
	require.NoError(t, writer.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service.name"}, Value: StringValue("")},
		{Key: Key{Scope: ScopeResource, Name: "payload"}, Value: BytesValue([]byte{0, 1, 2})},
	}}))
	data, err := writer.Bytes()
	require.NoError(t, err)

	source := &memoryRanges{data: data}
	reader, err := Open(context.Background(), source, ObjectName, int64(len(data)))
	require.NoError(t, err)
	require.Equal(t, Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage}, reader.Metadata())
	require.Equal(t, 3, source.calls, "open must not fetch the entity page")
	keys, err := reader.Names(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, []Key{
		{Scope: ScopeLegacy, Name: "service.name"},
		{Scope: ScopeResource, Name: "build"},
		{Scope: ScopeResource, Name: "enabled"},
		{Scope: ScopeResource, Name: "payload"},
	}, keys)
	require.Equal(t, 3, source.calls, "unfiltered names must use the scoped directory")

	entities, err := reader.Entities(context.Background())
	require.NoError(t, err)
	require.Equal(t, []Entity{
		{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service.name"}, Value: StringValue("api")},
			{Key: Key{Scope: ScopeResource, Name: "build"}, Value: Value{Type: ValueInt64, Data: []byte{42, 0, 0, 0, 0, 0, 0, 0}}},
			{Key: Key{Scope: ScopeResource, Name: "enabled"}, Value: Value{Type: ValueBool, Data: []byte{1}}},
		}},
		{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service.name"}, Value: StringValue("")},
			{Key: Key{Scope: ScopeResource, Name: "payload"}, Value: BytesValue([]byte{0, 1, 2})},
		}},
	}, entities)
	require.Equal(t, 4, source.calls)
}

func TestAttributeBlockV1_RejectsCorruptPage(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{}))
	data, err := writer.Bytes()
	require.NoError(t, err)
	data[headerSize] ^= 0xff

	reader, err := Open(context.Background(), &memoryRanges{data: data}, ObjectName, int64(len(data)))
	require.NoError(t, err)
	_, err = reader.Entities(context.Background())
	require.ErrorContains(t, err, "checksum")
}

func TestWriterRejectsAmbiguousOrUnsupportedValues(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.ErrorContains(t, writer.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "a"}, Value: StringValue("one")},
		{Key: Key{Scope: ScopeLegacy, Name: "a"}, Value: StringValue("two")},
	}}), "duplicate")
	require.ErrorContains(t, writer.AddEntity(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "a"}, Value: Value{Type: ValueMap}}}}), "not supported")
}
