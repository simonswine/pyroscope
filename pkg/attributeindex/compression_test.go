package attributeindex

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributeIndexV1_CompressesEveryDataPage(t *testing.T) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntityWithDatasets(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
		{Key: Key{Scope: ScopeLegacy, Name: "environment"}, Value: StringValue("production")},
	}}, []uint32{2, 7}))
	object, err := writer.Bytes()
	require.NoError(t, err)

	features := binary.LittleEndian.Uint16(object[10:12])
	require.Equal(t, featurePageCompression|featureDatasetMappings, features)
	_, _, pages := compressionDirectory(t, object)
	require.Len(t, pages, 6, "entity, dictionary, postings, two columns, and mappings")
	for _, page := range pages {
		require.Equal(t, pageCodecZstd, page.codec)
		require.Greater(t, page.length, uint32(0))
		require.Greater(t, page.decodedLength, uint32(0))
		// All frames are independent and begin at the page's own offset.
		require.Equal(t, []byte{0x28, 0xb5, 0x2f, 0xfd}, object[page.offset:page.offset+4])
	}

	reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	entities, err := reader.Entities(context.Background())
	require.NoError(t, err)
	require.Len(t, entities, 1)
	dictionaries, err := reader.Dictionaries(context.Background())
	require.NoError(t, err)
	require.Len(t, dictionaries, 2)
	_, err = reader.Postings(context.Background())
	require.NoError(t, err)
	_, err = reader.ForwardColumns(context.Background())
	require.NoError(t, err)
	ids, err := reader.DatasetIDs(context.Background(), []Matcher{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Operator: MatchEqual, Value: StringValue("api")}})
	require.NoError(t, err)
	require.Equal(t, []uint32{2, 7}, ids)
}

func TestAttributeIndexV1_RejectsCorruptCompressedPageBeforeDecompression(t *testing.T) {
	object, pages := compressedTestObject(t)
	object[pages[0].offset+4] ^= 0xff

	reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	_, err = reader.Entities(context.Background())
	require.ErrorContains(t, err, "checksum mismatch")
}

func TestAttributeIndexV1_RejectsTruncatedCompressedFrameAndWrongDecodedLength(t *testing.T) {
	t.Run("truncated frame", func(t *testing.T) {
		object, _ := compressedTestObject(t)
		updatePageDescriptor(t, object, 0, func(page *pageDescriptor) {
			page.length--
			page.crc32 = checksum(object[page.offset : page.offset+int64(page.length)])
		})

		reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		_, err = reader.Entities(context.Background())
		require.Error(t, err)
		require.NotContains(t, err.Error(), "checksum mismatch")
	})
	t.Run("wrong decoded length", func(t *testing.T) {
		object, _ := compressedTestObject(t)
		updatePageDescriptor(t, object, 0, func(page *pageDescriptor) { page.decodedLength++ })

		reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		_, err = reader.Entities(context.Background())
		require.Error(t, err)
	})
}

func TestAttributeIndexV1_ReusesCachedDecodedForwardColumn(t *testing.T) {
	object, pages := compressedTestObject(t)
	reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	_, err = reader.ForwardColumns(context.Background())
	require.NoError(t, err)

	for _, page := range pages {
		if page.kind == pageForwardColumn {
			// A second read would fail its stored-frame checksum. The selective
			// API must instead reuse the decoded page already in the cache.
			object[page.offset+4] ^= 0xff
			break
		}
	}
	columns, err := reader.ForwardColumnsFor(context.Background(), []Key{{Scope: ScopeLegacy, Name: "service"}})
	require.NoError(t, err)
	require.Len(t, columns, 1)
	require.NotNil(t, columns[0])
}

func TestAttributeIndexV1_CompressesIncompressibleAndEmptyPages(t *testing.T) {
	incompressible := make([]byte, 128<<10)
	var state uint32 = 1
	for i := range incompressible {
		// A deterministic xorshift stream avoids a test dependency on a random
		// source while remaining incompressible for this purpose.
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		incompressible[i] = byte(state)
	}
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntityWithDatasets(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeResource, Name: "payload"}, Value: BytesValue(incompressible)}}}, []uint32{1}))
	object, err := writer.Bytes()
	require.NoError(t, err)
	_, _, pages := compressionDirectory(t, object)
	require.Greater(t, pages[0].length, pages[0].decodedLength, "Zstd frames are retained even when a page expands")

	reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	entities, err := reader.Entities(context.Background())
	require.NoError(t, err)
	require.Equal(t, incompressible, entities[0].Attributes[0].Value.Data)

	// No current logical page encoder produces an empty payload (each has at
	// least a count varint), but the page format permits one and the decoder
	// must handle it without an allocation or a special raw-page path.
	emptyFrame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0x00, 0x01, 0x00, 0x00}
	empty, err := reader.decodePage(context.Background(), pageDescriptor{codec: pageCodecZstd, decodedLength: 0}, emptyFrame)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestAttributeIndexV1_RejectsOversizedWindowAndCanceledReads(t *testing.T) {
	t.Run("window", func(t *testing.T) {
		object, pages := compressedTestObject(t)
		// A Zstd frame with a 128 MiB window (window descriptor 17 << 3),
		// followed by an empty final raw block. The reader allows 64 MiB only.
		oversizedWindowFrame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x88, 0x01, 0x00, 0x00}
		copy(object[pages[0].offset:], oversizedWindowFrame)
		updatePageDescriptor(t, object, 0, func(page *pageDescriptor) {
			page.length = uint32(len(oversizedWindowFrame))
			page.decodedLength = 0
			page.crc32 = checksum(oversizedWindowFrame)
		})
		reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		_, err = reader.Entities(context.Background())
		require.Error(t, err)
	})
	t.Run("canceled read", func(t *testing.T) {
		object, _ := compressedTestObject(t)
		source := &memorySource{object: "index", data: object}
		reader, err := Open(context.Background(), source, "index", int64(len(object)))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = reader.Entities(ctx)
		require.ErrorIs(t, err, context.Canceled)
		reader.mu.Lock()
		defer reader.mu.Unlock()
		require.Equal(t, int64(maxPageLen), reader.decodedBytes, "canceled work must release its page reservations")
	})
}

func TestAttributeIndexV1_RejectsUnsupportedPageCodecAndMemoryExhaustion(t *testing.T) {
	t.Run("legacy raw-page feature set", func(t *testing.T) {
		object, _ := compressedTestObject(t)
		binary.LittleEndian.PutUint16(object[10:12], featureDatasetMappings)
		binary.LittleEndian.PutUint16(object[len(object)-footerSize+10:len(object)-footerSize+12], featureDatasetMappings)
		_, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
		require.ErrorContains(t, err, "unsupported attribute index required features")
	})
	t.Run("codec", func(t *testing.T) {
		object, _ := compressedTestObject(t)
		updatePageDescriptor(t, object, 0, func(page *pageDescriptor) { page.codec = pageCodec(99) })
		_, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
		require.ErrorContains(t, err, "invalid page descriptor")
	})
	t.Run("memory", func(t *testing.T) {
		object, _ := compressedTestObject(t)
		reader, err := Open(context.Background(), &memorySource{object: "index", data: object}, "index", int64(len(object)))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		reader.maxDecodedBytes = 1
		_, err = reader.Entities(context.Background())
		require.ErrorContains(t, err, "memory budget")
	})
}

func compressedTestObject(t *testing.T) ([]byte, []pageDescriptor) {
	t.Helper()
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntityWithDatasets(Entity{Attributes: []Attribute{{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")}}}, []uint32{1}))
	object, err := writer.Bytes()
	require.NoError(t, err)
	_, _, pages := compressionDirectory(t, object)
	return object, pages
}

func updatePageDescriptor(t *testing.T, object []byte, pageIndex int, mutate func(*pageDescriptor)) {
	t.Helper()
	metadata, keys, pages := compressionDirectory(t, object)
	mutate(&pages[pageIndex])
	directory := encodeDirectory(metadata, keys, pages)
	footerOffset := len(object) - footerSize
	directoryOffset := binary.LittleEndian.Uint64(object[footerOffset+12 : footerOffset+20])
	require.Equal(t, len(directory), int(binary.LittleEndian.Uint32(object[footerOffset+20:footerOffset+24])))
	copy(object[directoryOffset:footerOffset], directory)
	binary.LittleEndian.PutUint32(object[footerOffset+24:footerOffset+28], checksum(directory))
}

func compressionDirectory(t *testing.T, object []byte) (Metadata, []Key, []pageDescriptor) {
	t.Helper()
	footerOffset := len(object) - footerSize
	offset := binary.LittleEndian.Uint64(object[footerOffset+12 : footerOffset+20])
	length := binary.LittleEndian.Uint32(object[footerOffset+20 : footerOffset+24])
	metadata, keys, pages, err := decodeDirectory(object[offset : offset+uint64(length)])
	require.NoError(t, err)
	return metadata, keys, pages
}
