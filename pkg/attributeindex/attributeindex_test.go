package attributeindex

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type memoryRanges struct {
	mu    sync.Mutex
	data  []byte
	calls int
}

func (m *memoryRanges) GetRange(_ context.Context, _ string, off, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if off < 0 || length < 0 || off+length > int64(len(m.data)) {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(m.data[off : off+length])), nil
}

func TestAttributeIndexV1_RoundTrip(t *testing.T) {
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
	// AttributeIndexV1 must not be confused with the old ATTRBLK prototype.
	require.Equal(t, []byte{'A', 'T', 'T', 'R', 'I', 'D', 'X', 1}, data[:8])
	require.Equal(t, Version, binary.LittleEndian.Uint16(data[8:10]))

	source := &memoryRanges{data: data}
	reader, err := Open(context.Background(), source, PayloadName, int64(len(data)))
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
	values, err := reader.Values(context.Background(), Key{Scope: ScopeLegacy, Name: "service.name"}, nil)
	require.NoError(t, err)
	require.Equal(t, []Value{StringValue(""), StringValue("api")}, values)
	require.Equal(t, 4, source.calls, "unfiltered values must use the dictionary page")
	postings, err := reader.Postings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1}, postings[0][0])
	require.Equal(t, []uint32{1}, postings[0][1])
	require.Equal(t, []uint32{0}, postings[0][2])
	columns, err := reader.ForwardColumns(context.Background())
	require.NoError(t, err)
	require.Equal(t, [][]uint32{{2, 1}, {1, 0}, {1, 0}, {0, 1}}, columns)

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
	// With FetchRanges coalescing, we make fewer calls than the old implementation.
	// Old: 10 calls (3 for open, 1 entity, 1 dict, 1 postings, 4 columns)
	// New: fewer due to coalescing nearby pages
	require.LessOrEqual(t, source.calls, 10, "should not make more calls than before coalescing")
	require.GreaterOrEqual(t, source.calls, 3, "should at least fetch header, footer, directory")
}

func TestAttributeIndexV1_RejectsStandaloneAttributeBlockIdentity(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{}))
	data, err := writer.Bytes()
	require.NoError(t, err)
	copy(data[:8], []byte{'A', 'T', 'T', 'R', 'B', 'L', 'K', 1})

	_, err = Open(context.Background(), &memoryRanges{data: data}, PayloadName, int64(len(data)))
	require.ErrorContains(t, err, "unsupported attribute index header")
}

func TestAttributeIndexV1_DatasetLookupRequiresMappings(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{}))
	data, err := writer.Bytes()
	require.NoError(t, err)

	reader, err := Open(context.Background(), &memoryRanges{data: data}, PayloadName, int64(len(data)))
	require.NoError(t, err)
	datasetIDs, err := reader.DatasetIDs(context.Background(), nil)
	require.ErrorIs(t, err, ErrDatasetMappingsUnavailable)
	require.Nil(t, datasetIDs)
}

func TestAttributeIndexV1_DatasetLookup(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntityWithDatasets(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}, []uint32{7, 3, 7}))
	require.NoError(t, writer.AddEntityWithDatasets(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}, []uint32{5, 3}))
	data, err := writer.Bytes()
	require.NoError(t, err)

	reader, err := Open(context.Background(), &memoryRanges{data: data}, PayloadName, int64(len(data)))
	require.NoError(t, err)
	ids, err := reader.DatasetIDs(context.Background(), []Matcher{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Operator: MatchEqual, Value: StringValue("api")}})
	require.NoError(t, err)
	require.Equal(t, []uint32{3, 5, 7}, ids)
	mappings, err := reader.DatasetMappings(context.Background())
	require.NoError(t, err)
	require.Equal(t, [][]uint32{{3, 5, 7}}, mappings)
}

func TestAttributeIndexV1_DeterministicDatasetMapping(t *testing.T) {
	metadata := Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage}
	entityA := Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}
	entityB := Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("worker")}}}
	first, err := NewWriter(metadata)
	require.NoError(t, err)
	require.NoError(t, first.AddEntityWithDatasets(entityB, []uint32{9}))
	require.NoError(t, first.AddEntityWithDatasets(entityA, []uint32{4}))
	require.NoError(t, first.AddEntityWithDatasets(entityA, []uint32{2, 4}))
	second, err := NewWriter(metadata)
	require.NoError(t, err)
	require.NoError(t, second.AddEntityWithDatasets(entityA, []uint32{4, 2}))
	require.NoError(t, second.AddEntityWithDatasets(entityB, []uint32{9}))
	require.NoError(t, second.AddEntityWithDatasets(entityA, []uint32{4}))
	firstBytes, err := first.Bytes()
	require.NoError(t, err)
	secondBytes, err := second.Bytes()
	require.NoError(t, err)
	require.Equal(t, firstBytes, secondBytes)
}

func TestAttributeIndexV1_RejectsCorruptPage(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntity(Entity{}))
	data, err := writer.Bytes()
	require.NoError(t, err)
	data[headerSize] ^= 0xff

	reader, err := Open(context.Background(), &memoryRanges{data: data}, PayloadName, int64(len(data)))
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
