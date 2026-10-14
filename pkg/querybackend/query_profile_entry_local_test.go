package querybackend

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/pyroscope/v2/pkg/block"
	"github.com/grafana/pyroscope/v2/pkg/iter"
	"github.com/grafana/pyroscope/v2/pkg/objstore/providers/filesystem"
)

// BenchmarkV2ProfileEntryLocal uses a locally downloaded block, selected via
// PROFILE_BATCH_LOCAL_BLOCK. It preserves the remote test's Parquet reader
// configuration (async pages, estimated buffers), replacing only GCS with local
// ReaderAt calls. Metadata and OS caches are warm. Neither block opening nor
// correctness hashing is timed, but TSDB series selection and entry projection
// are. This is not a complete query/aggregation benchmark.
func BenchmarkV2ProfileEntryLocal(b *testing.B) {
	path := os.Getenv("PROFILE_BATCH_LOCAL_BLOCK")
	if path == "" {
		b.Skip("set PROFILE_BATCH_LOCAL_BLOCK to a downloaded block.bin")
	}
	path, err := filepath.Abs(path)
	require.NoError(b, err)
	bucket, err := filesystem.NewBucket(filepath.Dir(path))
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, bucket.Close()) })
	ctx := context.Background()
	obj, err := block.NewObjectFromPath(ctx, bucket, filepath.Base(path), block.WithObjectMaxSizeLoadInMemory(0))
	require.NoError(b, err)
	selected := -1
	var size uint64
	for i, ds := range obj.Metadata().Datasets {
		if block.DatasetFormat(ds.Format) != block.DatasetFormat0 || len(ds.TableOfContents) < 3 {
			continue
		}
		start, end := ds.TableOfContents[0], ds.TableOfContents[1]
		require.LessOrEqual(b, start, end)
		require.LessOrEqual(b, end, obj.Metadata().Size)
		if end-start > size {
			selected = i
			size = end - start
		}
	}
	require.NotEqual(b, -1, selected)
	require.Greater(b, size, uint64(1<<20), "use a section larger than the in-memory dataset threshold")
	ds := block.NewDataset(obj.Metadata().Datasets[selected], obj)
	require.NoError(b, ds.Open(ctx, block.SectionTSDB, block.SectionProfiles))
	b.Cleanup(func() { require.NoError(b, ds.Close()) })
	q := &queryContext{ctx: ctx, ds: ds, blockContext: &blockContext{req: &request{startTime: 0, endTime: math.MaxInt64}}}
	b.Logf("dataset=%d profile_bytes=%d rows=%d row_groups=%d", selected, size, ds.Profiles().NumRows(), len(ds.Profiles().RowGroups()))
	for _, withIDs := range []bool{false, true} {
		projection := "without_ids"
		if withIDs {
			projection = "with_ids"
		}
		opts := []profileIteratorOption{withAllLabels(), withFetchProfileIDs(withIDs)}
		create := func(tb testing.TB, engine string) iter.Iterator[ProfileEntry] {
			tb.Helper()
			if engine == "scalar" {
				selected, err := getSeries(ds.Index(), q.req.matchers, opts...)
				require.NoError(tb, err)
				return scalarProfileEntryIterator(q, selected, iteratorOptsFromOptions(opts))
			}
			entries, err := profileEntryIterator(q, opts...)
			require.NoError(tb, err)
			require.IsType(tb, &iter.AsyncBatchIterator[ProfileEntry, ProfileEntry]{}, entries)
			return entries
		}
		// Verify all fields once, outside timing, and warm data/metadata caches for
		// both engines. No labels, IDs or other profile contents are logged.
		var reference [32]byte
		var count int64
		for _, engine := range []string{"scalar", "batch"} {
			entries := create(b, engine)
			h := sha256.New()
			encoder := json.NewEncoder(h)
			var n int64
			for entries.Next() {
				require.NoError(b, encoder.Encode(entries.At()))
				n++
			}
			require.NoError(b, entries.Err())
			require.NoError(b, entries.Close())
			var digest [32]byte
			copy(digest[:], h.Sum(nil))
			if engine == "scalar" {
				reference = digest
				count = n
			} else {
				require.Equal(b, count, n)
				require.Equal(b, reference, digest)
			}
		}
		for _, engine := range []string{"scalar", "batch"} {
			b.Run(projection+"/"+engine, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					entries := create(b, engine)
					var n int64
					for entries.Next() {
						_ = entries.At()
						n++
					}
					require.NoError(b, entries.Err())
					require.NoError(b, entries.Close())
					require.Equal(b, count, n)
				}
				b.StopTimer()
				b.ReportMetric(float64(count), "profiles/op")
			})
		}
	}
}
