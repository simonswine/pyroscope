package querybackend

import (
	"bytes"
	"context"
	"testing"

	prommodel "github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/block"
	"github.com/grafana/pyroscope/v2/pkg/block/metadata"
	"github.com/grafana/pyroscope/v2/pkg/model"
	"github.com/grafana/pyroscope/v2/pkg/objstore"
	"github.com/grafana/pyroscope/v2/pkg/objstore/providers/memory"
	"github.com/grafana/pyroscope/v2/pkg/phlaredb/tsdb/index"
)

func TestAttributeMetadataParity(t *testing.T) {
	ctx := context.Background()
	writer, err := attributeindex.NewSeriesBuilder(attributeindex.Metadata{Tenant: "tenant", EntityKind: "series", TimeSemantics: attributeindex.TimeLegacyCoarseCoverage}, attributeindex.DefaultBuilderLimits())
	require.NoError(t, err)
	defer writer.Close()
	tsdb := block.NewDatasetIndexWriter()
	defer tsdb.Close()
	for _, series := range []model.Labels{
		model.LabelsFromStrings("service_name", "api", "region", "east", "empty", ""),
		model.LabelsFromStrings("service_name", "api", "region", "west"),
		model.LabelsFromStrings("service_name", "worker", "region", "east"),
		model.LabelsFromStrings("service_name", "worker", "region", "east", "empty", ""),
	} {
		require.NoError(t, writer.AddSeries(0, series))
		tsdb.AddSeries(0, series, prommodel.Fingerprint(series.Hash()))
	}
	var payload, tsdbPayload bytes.Buffer
	_, err = writer.WriteTo(ctx, &payload)
	require.NoError(t, err)
	_, err = tsdb.WriteTo(&tsdbPayload)
	require.NoError(t, err)
	reader, err := index.NewReader(index.RealByteSlice(tsdbPayload.Bytes()))
	require.NoError(t, err)
	defer reader.Close()
	strings := metadata.NewStringTable()
	ds := block.NewAttributeIndexDataset(strings.Put("tenant"), 0, 100, 0, uint64(payload.Len()), strings)
	bucket := objstore.NewBucket(memory.NewInMemBucket())
	require.NoError(t, bucket.Upload(ctx, "block.bin", bytes.NewReader(payload.Bytes())))
	obj := block.NewObject(bucket, &metastorev1.BlockMeta{Datasets: []*metastorev1.Dataset{ds}, StringTable: strings.Strings, Size: uint64(payload.Len())}, block.WithObjectPath("block.bin"))
	dataset := block.NewDataset(ds, obj)
	require.NoError(t, dataset.Open(ctx, block.SectionAttributeMetadata))
	defer dataset.Close()
	attr := dataset.AttributeIndex()

	for _, selector := range []string{"{}", `{service_name="api"}`, `{region=~"e.*"}`, `{empty=""}`, `{empty!=""}`, `{missing!="x"}`, `{missing!~".*"}`, `{region="east",service_name="absent"}`} {
		t.Run(selector, func(t *testing.T) {
			matchers, err := model.ParseMetricSelector(selector)
			require.NoError(t, err)
			names, err := attributeLabelNames(ctx, attr, matchers)
			require.NoError(t, err)
			expectedNames, err := labelNamesForMatchers(reader, matchers)
			if len(matchers) == 0 {
				expectedNames, err = reader.LabelNames()
			}
			require.NoError(t, err)
			require.ElementsMatch(t, expectedNames, names)
			for _, name := range []string{"service_name", "empty", "missing"} {
				values, err := attributeLabelValues(ctx, attr, name, matchers)
				require.NoError(t, err)
				expectedValues, err := labelValuesForMatchers(reader, name, matchers)
				if len(matchers) == 0 {
					expectedValues, err = reader.LabelValues(name)
				}
				require.NoError(t, err)
				require.ElementsMatch(t, expectedValues, values)
			}
			for _, projection := range [][]string{nil, {"service_name"}, {"empty"}, {"missing"}, {"empty", "missing"}, {"region", "region"}} {
				got, err := attributeSeriesLabels(ctx, attr, matchers, projection...)
				require.NoError(t, err)
				want, err := getSeriesLabels(reader, matchers, projection...)
				require.NoError(t, err)
				require.ElementsMatch(t, want, got)
			}
		})
	}
	// Exercise actual handler dispatch and report shape on an opened attribute dataset.
	q := &queryContext{ctx: ctx, ds: dataset, blockContext: &blockContext{req: &request{matchers: []*labels.Matcher{labels.MustNewMatcher(labels.MatchEqual, "service_name", "api")}}}}
	report, err := queryLabelNames(q, &queryv1.Query{LabelNames: &queryv1.LabelNamesQuery{}})
	require.NoError(t, err)
	require.Equal(t, []string{"empty", "region", "service_name"}, report.LabelNames.LabelNames)
	report, err = queryLabelValues(q, &queryv1.Query{LabelValues: &queryv1.LabelValuesQuery{LabelName: "region"}})
	require.NoError(t, err)
	require.Equal(t, []string{"east", "west"}, report.LabelValues.LabelValues)
	report, err = querySeriesLabels(q, &queryv1.Query{SeriesLabels: &queryv1.SeriesLabelsQuery{LabelNames: []string{"service_name"}}})
	require.NoError(t, err)
	require.Len(t, report.SeriesLabels.SeriesLabels, 1)

	// Run the complete block path. This object contains only index bytes: any
	// attempt to read block metadata or profile datasets would fail.
	invocation := &queryv1.InvokeRequest{Options: &queryv1.InvokeOptions{UseAttributeIndex: true}, Query: []*queryv1.Query{
		{QueryType: queryv1.QueryType_QUERY_LABEL_NAMES, LabelNames: &queryv1.LabelNamesQuery{}},
		{QueryType: queryv1.QueryType_QUERY_LABEL_VALUES, LabelValues: &queryv1.LabelValuesQuery{LabelName: "region"}},
		{QueryType: queryv1.QueryType_QUERY_SERIES_LABELS, SeriesLabels: &queryv1.SeriesLabelsQuery{LabelNames: []string{"service_name"}}},
	}}
	b := &blockContext{ctx: ctx, obj: obj, req: &request{src: invocation}, agg: newAggregator(invocation)}
	require.NoError(t, b.execute())
	require.Len(t, b.agg.staged, 3)
}

func TestAttributeMetadataRouting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		tsdbA := &metastorev1.Dataset{Format: uint32(block.DatasetFormat1), Tenant: 1}
		attrA := &metastorev1.Dataset{Format: uint32(block.DatasetFormat2), Tenant: 1}
		tsdbB := &metastorev1.Dataset{Format: uint32(block.DatasetFormat1), Tenant: 2}
		b := &blockContext{ctx: context.Background(), obj: block.NewObject(nil, &metastorev1.BlockMeta{Datasets: []*metastorev1.Dataset{tsdbA, attrA, tsdbB}}), req: &request{src: &queryv1.InvokeRequest{Options: &queryv1.InvokeOptions{UseAttributeIndex: enabled}, Query: []*queryv1.Query{
			{QueryType: queryv1.QueryType_QUERY_LABEL_NAMES},
			{QueryType: queryv1.QueryType_QUERY_LABEL_VALUES},
			{QueryType: queryv1.QueryType_QUERY_SERIES_LABELS},
		}}}}
		require.Empty(t, b.datasetIndices(), "direct execution must not resolve datasets")
		want := tsdbA
		if enabled {
			want = attrA
		}
		require.ElementsMatch(t, []*metastorev1.Dataset{want, tsdbB}, b.obj.Metadata().Datasets)
		b.obj.SetMetadata(&metastorev1.BlockMeta{Datasets: []*metastorev1.Dataset{tsdbA, attrA, tsdbB}})
		b.req.src.Query = append(b.req.src.Query, &queryv1.Query{QueryType: queryv1.QueryType_QUERY_TREE})
		require.ElementsMatch(t, []*metastorev1.Dataset{want, tsdbB}, b.datasetIndices(), "mixed requests must resolve profile datasets")
		real := &metastorev1.Dataset{Format: uint32(block.DatasetFormat0), Tenant: 1}
		b.obj.SetMetadata(&metastorev1.BlockMeta{Datasets: []*metastorev1.Dataset{real}})
		require.Empty(t, b.datasetIndices())
		require.Equal(t, []*metastorev1.Dataset{real}, b.obj.Metadata().Datasets)
	}
}
