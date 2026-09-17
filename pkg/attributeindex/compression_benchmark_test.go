package attributeindex

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func BenchmarkAttributeIndexV1_PageCompression(b *testing.B) {
	page := benchmarkEntityPage(b, 1_000)
	encoder, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithEncoderConcurrency(1),
	)
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, encoder.Close()) })
	compressed := encoder.EncodeAll(page, nil)
	compressionRatio := float64(len(compressed)) / float64(len(page))

	b.Run("uncompressed_baseline", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for b.Loop() {
			_ = append([]byte(nil), page...)
		}
		b.ReportMetric(1, "compression_ratio")
	})
	b.Run("zstd_fastest", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for b.Loop() {
			_ = encoder.EncodeAll(page, nil)
		}
		b.ReportMetric(compressionRatio, "compression_ratio")
	})
}

func BenchmarkAttributeIndexV1_PageDecompression(b *testing.B) {
	page := benchmarkEntityPage(b, 1_000)
	encoder, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithEncoderConcurrency(1),
	)
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, encoder.Close()) })
	compressed := encoder.EncodeAll(page, nil)
	decoder, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(maxPageLen),
		zstd.WithDecoderMaxWindow(maxPageLen),
		zstd.WithDecodeAllCapLimit(true),
	)
	require.NoError(b, err)
	b.Cleanup(func() { decoder.Close() })
	b.SetBytes(int64(len(page)))
	b.ReportAllocs()
	for b.Loop() {
		decoded, err := decoder.DecodeAll(compressed, make([]byte, 0, len(page)))
		if err != nil || len(decoded) != len(page) {
			b.Fatalf("decoding benchmark page: %v", err)
		}
	}
}

func BenchmarkAttributeIndexV1_RangedColumnRead(b *testing.B) {
	writer, err := NewWriter(Metadata{Tenant: "tenant-a", EntityKind: "series", TimeSemantics: TimeLegacyCoarseCoverage})
	require.NoError(b, err)
	for i := range 1_000 {
		require.NoError(b, writer.AddEntityWithDatasets(Entity{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
			{Key: Key{Scope: ScopeLegacy, Name: "instance"}, Value: StringValue("instance-" + strconv.Itoa(i))},
		}}, []uint32{uint32(i)}))
	}
	object, err := writer.Bytes()
	require.NoError(b, err)
	key := Key{Scope: ScopeLegacy, Name: "service"}

	b.ReportAllocs()
	totalRangeBytes := int64(0)
	for b.Loop() {
		source := &benchmarkRangeSource{object: "index", data: object}
		reader, err := Open(context.Background(), source, "index", int64(len(object)))
		if err != nil {
			b.Fatal(err)
		}
		_, err = reader.ForwardColumnsFor(context.Background(), []Key{key})
		if err != nil {
			b.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			b.Fatal(err)
		}
		totalRangeBytes += source.rangeBytes
	}
	b.ReportMetric(float64(totalRangeBytes)/float64(b.N), "ranged_read_bytes/op")
}

type benchmarkRangeSource struct {
	object     string
	data       []byte
	rangeBytes int64
}

func (s *benchmarkRangeSource) GetRange(_ context.Context, name string, off, length int64) (io.ReadCloser, error) {
	if name != s.object || off < 0 || length < 0 || off+length > int64(len(s.data)) {
		return nil, io.EOF
	}
	s.rangeBytes += length
	return io.NopCloser(bytes.NewReader(s.data[off : off+length])), nil
}

func benchmarkEntityPage(b testing.TB, entities int) []byte {
	b.Helper()
	values := make([]Entity, entities)
	for i := range values {
		values[i] = Entity{Attributes: []Attribute{
			{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
			{Key: Key{Scope: ScopeLegacy, Name: "environment"}, Value: StringValue("production")},
		}}
	}
	page, err := encodeEntities(values)
	require.NoError(b, err)
	return page
}
