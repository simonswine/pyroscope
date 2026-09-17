package block

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/block/metadata"
	phlareobj "github.com/grafana/pyroscope/v2/pkg/objstore"
	"github.com/grafana/pyroscope/v2/pkg/objstore/providers/memory"
)

func TestEmbeddedAttributeIndex(t *testing.T) {
	ctx := context.Background()
	payload := newAttributeIndexPayload(t, "tenant-a", []uint32{0})
	object, indexMeta, count := newEmbeddedAttributeIndexObject(t, ctx, "tenant-a", payload, nil)

	dataset := NewDataset(indexMeta, object)
	t.Cleanup(func() { require.NoError(t, dataset.Close()) })
	require.NoError(t, dataset.Open(ctx, SectionAttributeIndex))
	require.NotNil(t, dataset.AttributeIndex())

	ids, err := dataset.AttributeIndex().DatasetIDs(ctx, []attributeindex.Matcher{{
		Key:      attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: "service_name"},
		Operator: attributeindex.MatchEqual,
		Value:    attributeindex.StringValue("api"),
	}})
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, ids)

	// Opening an embedded index fetches its fixed metadata and mapping page, not
	// the containing block or every payload page. The test block includes an
	// unused 2 MiB suffix, which would be read by Object.Open's normal cache.
	require.Less(t, count.Load(), uint64(1<<20))
}

func TestEmbeddedAttributeIndexRejectsInvalidDatasetReferences(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		datasetIDs []uint32
		extra      func(*metadata.StringTable) *metastorev1.Dataset
		want       string
	}{
		{
			name:       "out of range",
			datasetIDs: []uint32{2},
			want:       "outside block metadata",
		},
		{
			name:       "pseudo dataset",
			datasetIDs: []uint32{1},
			extra: func(strings *metadata.StringTable) *metastorev1.Dataset {
				return &metastorev1.Dataset{Format: uint32(DatasetFormat1), Tenant: strings.Put("tenant-a"), Name: 0, TableOfContents: []uint64{0}, Size: 1}
			},
			want: "non-profile dataset",
		},
		{
			name:       "foreign tenant",
			datasetIDs: []uint32{1},
			extra: func(strings *metadata.StringTable) *metastorev1.Dataset {
				return &metastorev1.Dataset{Format: uint32(DatasetFormat0), Tenant: strings.Put("tenant-b"), Name: strings.Put("other"), TableOfContents: []uint64{0, 0, 0}}
			},
			want: "outside tenant",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := newAttributeIndexPayload(t, "tenant-a", test.datasetIDs)
			object, indexMeta, _ := newEmbeddedAttributeIndexObject(t, ctx, "tenant-a", payload, test.extra)
			dataset := NewDataset(indexMeta, object)
			err := dataset.Open(ctx, SectionAttributeIndex)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestAttributeIndexDatasetCannotOpenProfileOrTSDBSections(t *testing.T) {
	strings := metadata.NewStringTable()
	indexMeta := NewAttributeIndexDataset(strings.Put("tenant-a"), 1, 2, 0, 1, strings)
	dataset := NewDataset(indexMeta, NewObject(nil, &metastorev1.BlockMeta{StringTable: strings.Strings}))

	err := dataset.Open(context.Background(), SectionTSDB)
	require.ErrorContains(t, err, "not supported by dataset format")
}

func TestAttributeIndexRangeSourceBoundsRequests(t *testing.T) {
	source := attributeIndexRangeSource{object: &Object{path: "block.bin"}, offset: 10, size: 5}
	for _, test := range []struct {
		off, length int64
	}{
		{-1, 1}, {0, -1}, {6, 0}, {5, 1},
	} {
		_, err := source.GetRange(context.Background(), "block.bin", test.off, test.length)
		require.Error(t, err)
	}
}

func TestWeightOfAttributeIndex(t *testing.T) {
	weight := WeightOf(&metastorev1.Dataset{Format: uint32(DatasetFormat2), TableOfContents: []uint64{10}, Size: 42})
	require.Equal(t, uint64(42), weight.AttributeIndexBytes)
	require.Zero(t, weight.TSDBBytes)
	require.Zero(t, weight.IndexLookupCount)
	require.Equal(t, uint64(42), weight.Total())
}

func newAttributeIndexPayload(t *testing.T, tenant string, datasetIDs []uint32) []byte {
	t.Helper()
	writer, err := attributeindex.NewWriter(attributeindex.Metadata{
		Tenant:        tenant,
		EntityKind:    "series",
		TimeSemantics: attributeindex.TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)
	require.NoError(t, writer.AddEntityWithDatasets(attributeindex.Entity{Attributes: []attributeindex.Attribute{{
		Key:   attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: "service_name"},
		Value: attributeindex.StringValue("api"),
	}}}, datasetIDs))
	payload, err := writer.Bytes()
	require.NoError(t, err)
	return payload
}

func newEmbeddedAttributeIndexObject(t *testing.T, ctx context.Context, tenant string, payload []byte, extra func(*metadata.StringTable) *metastorev1.Dataset) (*Object, *metastorev1.Dataset, *atomic.Uint64) {
	t.Helper()
	strings := metadata.NewStringTable()
	tenantID := strings.Put(tenant)
	real := &metastorev1.Dataset{
		Format:          uint32(DatasetFormat0),
		Tenant:          tenantID,
		Name:            strings.Put("api"),
		TableOfContents: []uint64{0, 0, 0},
	}
	prefix := bytes.Repeat([]byte{0xff}, 64)
	var data bytes.Buffer
	_, err := data.Write(prefix)
	require.NoError(t, err)
	_, err = data.Write(payload)
	require.NoError(t, err)
	// This suffix must not be fetched when opening the page-oriented payload.
	_, err = data.Write(bytes.Repeat([]byte{0xff}, 2<<20))
	require.NoError(t, err)
	indexMeta := NewAttributeIndexDataset(tenantID, 1, 2, uint64(len(prefix)), uint64(len(payload)), strings)
	datasets := []*metastorev1.Dataset{real}
	if extra != nil {
		datasets = append(datasets, extra(strings))
	}
	datasets = append(datasets, indexMeta)
	meta := &metastorev1.BlockMeta{
		Id:             ulid.Make().String(),
		MetadataOffset: uint64(data.Len()),
		Datasets:       datasets,
		StringTable:    strings.Strings,
	}
	require.NoError(t, metadata.Encode(&data, meta))
	meta.Size = uint64(data.Len())

	raw := memory.NewInMemBucket()
	bucket := phlareobj.NewBucket(raw)
	var bytesRead atomic.Uint64
	counting := phlareobj.NewCountingBucket(bucket, &bytesRead)
	const path = "block.bin"
	require.NoError(t, counting.Upload(ctx, path, bytes.NewReader(data.Bytes())))
	return NewObject(counting, meta, WithObjectPath(path)), indexMeta, &bytesRead
}
