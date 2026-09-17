package block_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/block"
	"github.com/grafana/pyroscope/v2/pkg/metrics"
	phlaremodel "github.com/grafana/pyroscope/v2/pkg/model"
	"github.com/grafana/pyroscope/v2/pkg/objstore"
	"github.com/grafana/pyroscope/v2/pkg/objstore/testutil"
	"github.com/grafana/pyroscope/v2/pkg/phlaredb/tsdb/index"
	"github.com/grafana/pyroscope/v2/pkg/test/mocks/mockmetrics"
)

func Test_CompactBlocks(t *testing.T) {
	ctx := context.Background()
	bucket, _ := testutil.NewFilesystemBucket(t, ctx, "testdata")

	var resp metastorev1.GetBlockMetadataResponse
	raw, err := os.ReadFile("testdata/block-metas.json")
	require.NoError(t, err)
	err = protojson.Unmarshal(raw, &resp)
	require.NoError(t, err)

	dst, tempdir := testutil.NewFilesystemBucket(t, ctx, t.TempDir())
	compactedBlocks, err := block.Compact(ctx, resp.Blocks, bucket,
		block.WithCompactionDestination(dst),
		block.WithCompactionTempDir(tempdir),
		block.WithCompactionObjectOptions(
			block.WithObjectDownload(filepath.Join(tempdir, "source")),
			block.WithObjectMaxSizeLoadInMemory(0)), // Force download.
	)

	require.NoError(t, err)
	require.Len(t, compactedBlocks, 1)
	require.NotZero(t, compactedBlocks[0].Size)
	require.Len(t, compactedBlocks[0].Datasets, 5)

	compactedJson, err := json.MarshalIndent(compactedBlocks, "", "  ")
	require.NoError(t, err)
	expectedJson, err := os.ReadFile("testdata/compacted.golden")
	require.NoError(t, err)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile("testdata/compacted.golden", compactedJson, 0644))
	} else {
		assert.Equal(t, string(expectedJson), string(compactedJson))
	}

	assertCompactedAttributeIndex(t, ctx, dst, compactedBlocks[0])

	t.Run("Compact compacted blocks", func(t *testing.T) {
		compactedBlocks, err = block.Compact(ctx, compactedBlocks, dst,
			block.WithCompactionDestination(dst),
			block.WithCompactionTempDir(tempdir),
			block.WithCompactionObjectOptions(
				block.WithObjectDownload(filepath.Join(tempdir, "source")),
				block.WithObjectMaxSizeLoadInMemory(0)), // Force download.
		)

		require.NoError(t, err)
		require.Len(t, compactedBlocks, 1)
		require.NotZero(t, compactedBlocks[0].Size)
		require.Len(t, compactedBlocks[0].Datasets, 5)
		assertCompactedAttributeIndex(t, ctx, dst, compactedBlocks[0])
	})

	t.Run("mixed indexed and legacy metadata with reordered dataset positions", func(t *testing.T) {
		legacy := proto.Clone(compactedBlocks[0]).(*metastorev1.BlockMeta)
		legacy.Datasets = slices.DeleteFunc(legacy.Datasets, func(ds *metastorev1.Dataset) bool {
			return block.DatasetFormat(ds.Format) == block.DatasetFormat2
		})
		slices.Reverse(legacy.Datasets)
		mixed, err := block.Compact(ctx, []*metastorev1.BlockMeta{legacy, compactedBlocks[0]}, dst,
			block.WithCompactionTempDir(tempdir))
		require.NoError(t, err)
		require.Len(t, mixed, 1)
		assertCompactedAttributeIndex(t, ctx, dst, mixed[0])
	})
}

func Test_CompactBlocks_recordingRules(t *testing.T) {
	ctx := context.Background()
	bucket, _ := testutil.NewFilesystemBucket(t, ctx, "testdata")

	var resp metastorev1.GetBlockMetadataResponse
	raw, err := os.ReadFile("testdata/block-metas.json")
	require.NoError(t, err)
	err = protojson.Unmarshal(raw, &resp)
	require.NoError(t, err)

	exporter := &stringExporter{}
	ruler := new(mockmetrics.MockRuler)
	ruler.On("RecordingRules", mock.Anything).Return([]*phlaremodel.RecordingRule{
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "goroutine:goroutine:count:goroutine:count"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_goroutines_total_count"}),
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_objects:count:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_total_count"}),
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_space:bytes:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_total_bytes"}),
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_objects:count:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_total_count"}),
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_space:bytes:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_total_bytes"}),
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "process_cpu:cpu:nanoseconds:cpu:nanoseconds"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_cpu_usage_total_nanoseconds"}),
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "process_cpu:samples:count:cpu:nanoseconds"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_cpu_usage_total_samples"}),
		},
		// functions
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "goroutine:goroutine:count:goroutine:count"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_goroutines_function_total_servehttp_count"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_objects:count:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_function_total_servehttp_count"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_space:bytes:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_function_total_servehttp_bytes"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_objects:count:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_function_total_servehttp_count"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_space:bytes:space:bytes"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_function_total_servehttp_bytes"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "process_cpu:cpu:nanoseconds:cpu:nanoseconds"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_cpu_usage_function_total_servehttp_nanoseconds"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "process_cpu:samples:count:cpu:nanoseconds"),
			},
			GroupBy:        []string{"service_name"},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_cpu_usage_function_total_servehttp_samples"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
	})
	sampleObserver := metrics.NewSampleObserver(0, exporter, ruler, labels.EmptyLabels())

	compactedBlocks, err := block.Compact(ctx, resp.Blocks, bucket,
		block.WithSampleObserver(sampleObserver),
	)
	// Close observer to flush export
	sampleObserver.Close()

	require.NoError(t, err)
	require.Len(t, compactedBlocks, 1)
	require.NotZero(t, compactedBlocks[0].Size)
	require.Len(t, compactedBlocks[0].Datasets, 5)

	expectedMetrics, err := os.ReadFile("testdata/profiles_recorded.txt")
	require.NoError(t, err)
	expectedMetricsArray := strings.Split(string(expectedMetrics), "\n")
	sort.Strings(expectedMetricsArray)
	actualMetricsArray := strings.Split(exporter.String(), "\n")
	sort.Strings(actualMetricsArray)
	assert.Equal(t, expectedMetricsArray, actualMetricsArray)

	compactedJson, err := json.MarshalIndent(compactedBlocks, "", "  ")
	require.NoError(t, err)
	expectedJson, err := os.ReadFile("testdata/compacted.golden")
	require.NoError(t, err)
	assert.Equal(t, string(expectedJson), string(compactedJson))
}

func Test_CompactBlocks_recordingRules_shadowedSymbols(t *testing.T) {
	ctx := context.Background()
	bucket, _ := testutil.NewFilesystemBucket(t, ctx, "testdata")

	var resp metastorev1.GetBlockMetadataResponse
	raw, err := os.ReadFile("testdata/block-metas.json")
	require.NoError(t, err)
	err = protojson.Unmarshal(raw, &resp)
	require.NoError(t, err)

	exporter := &stringExporter{}
	ruler := new(mockmetrics.MockRuler)
	ruler.On("RecordingRules", mock.Anything).Return([]*phlaremodel.RecordingRule{
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/ingester"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_space:bytes:space:bytes"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_total_pyroscope_ingester_Push_bytes"}),
			FunctionName:   "github.com/grafana/pyroscope/pkg/ingester.(*Ingester).Push",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/ingester"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_space:bytes:space:bytes"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_total_pyroscope_ingester_Push_bytes"}),
			FunctionName:   "github.com/grafana/pyroscope/pkg/ingester.(*Ingester).Push",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/ingester"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "process_cpu:samples:count:cpu:nanoseconds"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_cpu_usage_total_pyroscope_ingester_Push_samples"}),
			FunctionName:   "github.com/grafana/pyroscope/pkg/ingester.(*Ingester).Push",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/ingester"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_space:bytes:space:bytes"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_total_pyroscope_ingester_Serve_bytes"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/ingester"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_space:bytes:space:bytes"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_total_pyroscope_ingester_Serve_bytes"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/ingester"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "process_cpu:samples:count:cpu:nanoseconds"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_cpu_usage_total_pyroscope_ingester_Serve_samples"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/query-frontend"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:inuse_space:bytes:space:bytes"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_inuse_total_query_Serve_bytes"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
		{
			Matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "service_name", "pyroscope-test/query-frontend"),
				labels.MustNewMatcher(labels.MatchEqual, "__profile_type__", "memory:alloc_space:bytes:space:bytes"),
			},
			ExternalLabels: labels.New(labels.Label{Name: "__name__", Value: "profiles_recorded_mem_alloc_total_query_Serve_bytes"}),
			FunctionName:   "net/http.HandlerFunc.ServeHTTP",
		},
	})
	sampleObserver := metrics.NewSampleObserver(0, exporter, ruler, labels.EmptyLabels())

	compactedBlocks, err := block.Compact(ctx, resp.Blocks, bucket,
		block.WithSampleObserver(sampleObserver),
	)
	// Close observer to flush export
	sampleObserver.Close()

	require.NoError(t, err)
	require.Len(t, compactedBlocks, 1)
	require.NotZero(t, compactedBlocks[0].Size)
	require.Len(t, compactedBlocks[0].Datasets, 5)

	expectedMetrics, err := os.ReadFile("testdata/profiles_recorded_shadowed.txt")
	require.NoError(t, err)
	expectedMetricsArray := strings.Split(string(expectedMetrics), "\n")
	sort.Strings(expectedMetricsArray)
	actualMetricsArray := strings.Split(exporter.String(), "\n")
	sort.Strings(actualMetricsArray)
	assert.Equal(t, expectedMetricsArray, actualMetricsArray)

	compactedJson, err := json.MarshalIndent(compactedBlocks, "", "  ")
	require.NoError(t, err)
	expectedJson, err := os.ReadFile("testdata/compacted.golden")
	require.NoError(t, err)
	assert.Equal(t, string(expectedJson), string(compactedJson))
}

type stringExporter struct {
	output string
}

func (e *stringExporter) Send(tenant string, series []prompb.TimeSeries) error {
	for _, s := range series {
		e.output += fmt.Sprintf("%s: %s\n", tenant, s.String())
	}
	return nil
}

func (*stringExporter) Flush() {}

func (e *stringExporter) String() string {
	return e.output
}

// collectSeriesLabels reads the TSDB index of every named dataset in the
// given blocks and returns the labels of each series, keyed by
// (tenant, dataset, fingerprint). Anonymous datasets (the per-tenant
// dataset-index pseudo-dataset) are skipped: they aggregate series of the
// tenant's real datasets and would double-count them.
func collectSeriesLabels(ctx context.Context, t *testing.T, storage objstore.Bucket, metas []*metastorev1.BlockMeta) map[string]string {
	out := make(map[string]string)
	for _, md := range metas {
		obj := block.NewObject(storage, md)
		require.NoError(t, obj.Open(ctx))
		for _, dsMeta := range md.Datasets {
			ds := block.NewDataset(dsMeta, obj)
			if ds.Name() == "" {
				continue
			}
			require.NoError(t, ds.Open(ctx, block.SectionTSDB))
			k, v := index.AllPostingsKey()
			postings, err := ds.Index().Postings(k, nil, v)
			require.NoError(t, err)
			var lbls phlaremodel.Labels
			var chks []index.ChunkMeta
			for postings.Next() {
				fp, err := ds.Index().Series(postings.At(), &lbls, &chks)
				require.NoError(t, err)
				key := fmt.Sprintf("%s/%s/%016x", ds.TenantID(), ds.Name(), fp)
				val := phlaremodel.LabelPairsString(lbls)
				if prev, ok := out[key]; ok {
					require.Equal(t, prev, val, "same series with diverging labels: %s", key)
				}
				out[key] = val
			}
			require.NoError(t, postings.Err())
		}
		require.NoError(t, obj.Close())
	}
	return out
}

// Test_Compact_PreservesSeriesLabels verifies, end to end, that compaction
// writes every input series' labels to the output block unmodified: the
// (tenant, dataset, fingerprint) -> labels mapping of the compacted block
// must equal the union of the source blocks'. This exercises the expectation
// that ProfileEntry.Labels are not mutated.
func Test_Compact_PreservesSeriesLabels(t *testing.T) {
	ctx := context.Background()
	bucket, _ := testutil.NewFilesystemBucket(t, ctx, "testdata")

	var resp metastorev1.GetBlockMetadataResponse
	raw, err := os.ReadFile("testdata/block-metas.json")
	require.NoError(t, err)
	require.NoError(t, protojson.Unmarshal(raw, &resp))

	expected := collectSeriesLabels(ctx, t, bucket, resp.Blocks)
	require.NotEmpty(t, expected)

	dst, tempdir := testutil.NewFilesystemBucket(t, ctx, t.TempDir())
	compacted, err := block.Compact(ctx, resp.Blocks, bucket,
		block.WithCompactionDestination(dst),
		block.WithCompactionTempDir(tempdir),
		block.WithCompactionObjectOptions(
			block.WithObjectDownload(filepath.Join(tempdir, "source")),
			block.WithObjectMaxSizeLoadInMemory(0)),
	)
	require.NoError(t, err)

	actual := collectSeriesLabels(ctx, t, dst, compacted)
	require.Equal(t, expected, actual)
}

// Rebuild an oracle from persisted output TSDB series, independently of the
// compaction row stream. Deterministic bytes cover all entities and references.
func assertCompactedAttributeIndex(t *testing.T, ctx context.Context, bucket objstore.Bucket, md *metastorev1.BlockMeta) {
	t.Helper()
	builder, err := attributeindex.NewSeriesBuilder(attributeindex.Metadata{
		Tenant: md.StringTable[md.Tenant], EntityKind: "series", TimeSemantics: attributeindex.TimeLegacyCoarseCoverage,
	}, attributeindex.DefaultBuilderLimits())
	require.NoError(t, err)
	defer builder.Close()
	obj := block.NewObject(bucket, md)
	require.NoError(t, obj.Open(ctx))
	defer obj.Close()
	var attr *metastorev1.Dataset
	for id, meta := range md.Datasets {
		if block.DatasetFormat(meta.Format) == block.DatasetFormat2 {
			attr = meta
			continue
		}
		if meta.Name == 0 {
			continue
		}
		ds := block.NewDataset(meta, obj)
		require.NoError(t, ds.Open(ctx, block.SectionTSDB))
		k, v := index.AllPostingsKey()
		postings, err := ds.Index().Postings(k, nil, v)
		require.NoError(t, err)
		var lbls phlaremodel.Labels
		var chunks []index.ChunkMeta
		for postings.Next() {
			_, err := ds.Index().Series(postings.At(), &lbls, &chunks)
			require.NoError(t, err)
			require.NoError(t, builder.AddSeries(uint32(id), lbls))
		}
		require.NoError(t, postings.Err())
		require.NoError(t, ds.Close())
	}
	require.NotNil(t, attr)
	require.Equal(t, md.MinTime, attr.MinTime)
	require.Equal(t, md.MaxTime, attr.MaxTime)
	expected, err := builder.Bytes(ctx)
	require.NoError(t, err)
	r, err := bucket.GetRange(ctx, block.ObjectPath(md), int64(attr.TableOfContents[0]), int64(attr.Size))
	require.NoError(t, err)
	actual, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.Equal(t, expected, actual)
	ds := block.NewDataset(attr, obj)
	require.NoError(t, ds.Open(ctx, block.SectionAttributeIndex))
	require.NoError(t, ds.Close())
}
