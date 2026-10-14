//go:build integration

package block

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/require"
	thanosobjstore "github.com/thanos-io/objstore"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/option"

	"github.com/grafana/pyroscope/v2/pkg/objstore"
	"github.com/grafana/pyroscope/v2/pkg/phlaredb/query"
)

// TestProfileBatchRemote is opt-in, read-only, and restricted to one GCS object.
// It uses gcloud's active identity without printing or persisting its token.
// Cold means a fresh Parquet footer/index view, not a flushed GCS/server cache.
// Warm reuses footer/index metadata, not payload pages. The object generation is
// pinned, and all HTTP requests/response-body bytes (including SDK retries) count.
//
//	PROFILE_BATCH_GCS_URI=gs://bucket/path/block.bin go test -tags=integration \
//	  ./pkg/block -run '^TestProfileBatchRemote$' -v -count=1 -timeout=15m
func TestProfileBatchRemote(t *testing.T) {
	uri := os.Getenv("PROFILE_BATCH_GCS_URI")
	if uri == "" {
		t.Skip("set PROFILE_BATCH_GCS_URI to opt in to remote reads")
	}
	u, err := url.Parse(uri)
	require.NoError(t, err)
	require.Equal(t, "gs", u.Scheme)
	require.NotEmpty(t, u.Host)
	name := strings.TrimPrefix(u.Path, "/")
	require.NotEmpty(t, name)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	token, err := exec.CommandContext(ctx, "gcloud", "auth", "print-access-token").Output()
	require.NoError(t, err, "gcloud authentication is required")
	authClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: strings.TrimSpace(string(token))}))
	transport := &remoteScanTransport{base: authClient.Transport}
	authClient.Transport = transport
	client, err := storage.NewClient(ctx, option.WithHTTPClient(authClient))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()); authClient.CloseIdleConnections() })
	object := client.Bucket(u.Host).Object(name)
	attrs, err := object.Attrs(ctx)
	require.NoError(t, err)
	object = object.Generation(attrs.Generation)
	bucket := objstore.NewBucket(&remoteScanBucket{object: object, name: name, size: attrs.Size})
	objectView, err := NewObjectFromPath(ctx, bucket, name, WithObjectMaxSizeLoadInMemory(0))
	require.NoError(t, err)
	md := objectView.Metadata()
	// Exercise the largest profile section rather than a possibly tiny first dataset.
	selected := -1
	var off, size int64
	for i, ds := range md.Datasets {
		if ds.Format != uint32(DatasetFormat0) || len(ds.TableOfContents) < 3 {
			continue
		}
		start, end := ds.TableOfContents[0], ds.TableOfContents[1]
		require.LessOrEqual(t, start, end)
		require.LessOrEqual(t, end, uint64(attrs.Size))
		if int64(end-start) > size {
			selected = i
			off = int64(start)
			size = int64(end - start)
		}
	}
	require.NotEqual(t, -1, selected, "no format-0 profile section in this object")
	t.Logf("object_bytes=%d generation=%d datasets=%d selected_dataset=%d profile_bytes=%d", attrs.Size, attrs.Generation, len(md.Datasets), selected, size)
	open := func() (*ParquetFile, error) {
		return openParquetFile(bucket, name, off, size, estimateFooterSize(size), parquet.SkipBloomFilters(true), parquet.FileReadMode(parquet.ReadModeAsync), parquet.ReadBufferSize(estimateReadBufferSize(size)))
	}
	pf, err := open()
	require.NoError(t, err)
	series := map[uint32]struct{}{}
	minTime, maxTime := int64(math.MaxInt64), int64(math.MinInt64)
	discovery := query.NewBinaryJoinIterator(0, pf.Column(ctx, "SeriesIndex", nil), pf.Column(ctx, "TimeNanos", nil))
	var columns [][]parquet.Value
	for discovery.Next() {
		columns = discovery.At().Columns(columns, "SeriesIndex", "TimeNanos")
		series[columns[0][0].Uint32()] = struct{}{}
		timestamp := columns[1][0].Int64()
		if timestamp < minTime {
			minTime = timestamp
		}
		if timestamp > maxTime {
			maxTime = timestamp
		}
	}
	require.NoError(t, discovery.Err())
	require.NoError(t, discovery.Close())
	require.NoError(t, pf.Close())
	require.NotEmpty(t, series)
	ids := make([]uint32, 0, len(series))
	for id := range series {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sparse := map[uint32]struct{}{ids[len(ids)/2]: {}}
	midpoint := minTime + (maxTime-minTime)/2
	width := (maxTime - minTime) / 100
	t.Logf("rows=%d row_groups=%d series=%d time_span_ns=%d read_buffer_bytes=%d", pf.NumRows(), len(pf.RowGroups()), len(series), maxTime-minTime, estimateReadBufferSize(size))
	cases := []remoteScanCase{
		{"dense", series, minTime, maxTime},
		{"sparse", sparse, minTime, maxTime},
		{"narrow", series, midpoint, midpoint + width},
	}
	repetitions := 3
	if v := os.Getenv("PROFILE_BATCH_REMOTE_REPEATS"); v != "" {
		repetitions, err = strconv.Atoi(v)
		require.NoError(t, err)
		require.Greater(t, repetitions, 0)
		require.LessOrEqual(t, repetitions, 10)
	}
	for _, tc := range cases {
		var expected remoteScanResult
		for rep := 0; rep < repetitions; rep++ {
			engines := []string{"scalar", "batch"}
			if rep%2 == 1 {
				engines = []string{"batch", "scalar"}
			}
			for _, engine := range engines {
				beforeRequests, beforeBytes := transport.requests.Load(), transport.bytes.Load()
				start := time.Now()
				pf, err := open()
				require.NoError(t, err)
				result, err := runRemoteProfileScan(ctx, pf, tc, engine)
				require.NoError(t, err)
				elapsed := time.Since(start)
				if rep == 0 && engine == "scalar" {
					expected = result
				}
				require.Equal(t, expected, result, "case=%s engine=%s cold", tc.name, engine)
				t.Logf("case=%s rep=%d cache=cold engine=%s elapsed_ms=%.3f requests=%d bytes=%d rows=%d", tc.name, rep, engine, float64(elapsed.Microseconds())/1000, transport.requests.Load()-beforeRequests, transport.bytes.Load()-beforeBytes, result.rows)
				beforeRequests, beforeBytes = transport.requests.Load(), transport.bytes.Load()
				start = time.Now()
				result, err = runRemoteProfileScan(ctx, pf, tc, engine)
				require.NoError(t, err)
				elapsed = time.Since(start)
				require.Equal(t, expected, result, "case=%s engine=%s warm", tc.name, engine)
				t.Logf("case=%s rep=%d cache=warm engine=%s elapsed_ms=%.3f requests=%d bytes=%d rows=%d", tc.name, rep, engine, float64(elapsed.Microseconds())/1000, transport.requests.Load()-beforeRequests, transport.bytes.Load()-beforeBytes, result.rows)
				require.NoError(t, pf.Close())
			}
		}
		// A bounded four-query workload, each with its own readers and cold metadata.
		for _, engine := range []string{"scalar", "batch"} {
			beforeRequests, beforeBytes := transport.requests.Load(), transport.bytes.Load()
			start := time.Now()
			var group errgroup.Group
			for worker := 0; worker < 4; worker++ {
				group.Go(func() error {
					pf, err := open()
					if err != nil {
						return err
					}
					defer pf.Close()
					result, err := runRemoteProfileScan(ctx, pf, tc, engine)
					if err != nil {
						return err
					}
					if result != expected {
						return fmt.Errorf("concurrent %s result mismatch", engine)
					}
					return nil
				})
			}
			require.NoError(t, group.Wait())
			t.Logf("case=%s concurrency=4 cache=cold engine=%s elapsed_ms=%.3f requests=%d bytes=%d", tc.name, engine, float64(time.Since(start).Microseconds())/1000, transport.requests.Load()-beforeRequests, transport.bytes.Load()-beforeBytes)
		}
	}
}

type remoteScanCase struct {
	name       string
	series     map[uint32]struct{}
	start, end int64
}
type remoteScanResult struct {
	rows   int64
	digest [32]byte
}

func runRemoteProfileScan(ctx context.Context, pf *ParquetFile, tc remoteScanCase, engine string) (result remoteScanResult, err error) {
	h := sha256.New()
	appendRow := func(row int64, series uint32, timestamp int64, partition uint64) {
		hashRemoteProfile(h, row, series, timestamp, partition)
		result.rows++
	}
	pi, _ := query.GetColumnIndexByPath(pf.Root(), "StacktracePartition")
	if engine == "batch" {
		si, _ := query.GetColumnIndexByPath(pf.Root(), "SeriesIndex")
		ti, _ := query.GetColumnIndexByPath(pf.Root(), "TimeNanos")
		var partitions []int
		if pi >= 0 {
			partitions = []int{pi}
		}
		scanner, e := query.NewProfileBatchScanner(ctx, pf.RowGroups(), si, ti, 256, tc.series, tc.start, tc.end, partitions...)
		if e != nil {
			return result, e
		}
		defer scanner.Close()
		for scanner.Next() {
			batch := scanner.At()
			for i, row := range batch.RowNumbers {
				var partition uint64
				if batch.Partitions != nil {
					partition = batch.Partitions[i]
				}
				appendRow(row, batch.SeriesIndexes[i], batch.TimeNanos[i], partition)
			}
		}
		if err = scanner.Err(); err != nil {
			return result, err
		}
		if err = scanner.Close(); err != nil {
			return result, err
		}
	} else {
		scanner := query.NewBinaryJoinIterator(0, pf.Column(ctx, "SeriesIndex", query.NewMapPredicate(tc.series)), pf.Column(ctx, "TimeNanos", query.NewIntBetweenPredicate(tc.start, tc.end)))
		names := []string{"SeriesIndex", "TimeNanos"}
		if pi >= 0 {
			scanner = query.NewBinaryJoinIterator(0, scanner, pf.Column(ctx, "StacktracePartition", nil))
			names = append(names, "StacktracePartition")
		}
		defer scanner.Close()
		var columns [][]parquet.Value
		for scanner.Next() {
			row := scanner.At()
			columns = row.Columns(columns, names...)
			var partition uint64
			if pi >= 0 {
				partition = columns[2][0].Uint64()
			}
			appendRow(row.RowNumber[0], columns[0][0].Uint32(), columns[1][0].Int64(), partition)
		}
		if err = scanner.Err(); err != nil {
			return result, err
		}
		if err = scanner.Close(); err != nil {
			return result, err
		}
	}
	copy(result.digest[:], h.Sum(nil))
	return result, nil
}

func hashRemoteProfile(h hash.Hash, row int64, series uint32, timestamp int64, partition uint64) {
	var buf [28]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(row))
	binary.LittleEndian.PutUint32(buf[8:12], series)
	binary.LittleEndian.PutUint64(buf[12:20], uint64(timestamp))
	binary.LittleEndian.PutUint64(buf[20:28], partition)
	_, _ = h.Write(buf[:])
}

// Only the methods used by NewObjectFromPath/openParquetFile are exposed by this
// read-only adapter. All actual ranges use the same GCS SDK as the bucket client.
type remoteScanBucket struct {
	thanosobjstore.Bucket
	object *storage.ObjectHandle
	name   string
	size   int64
}

func (b *remoteScanBucket) Attributes(_ context.Context, name string) (thanosobjstore.ObjectAttributes, error) {
	if name != b.name {
		return thanosobjstore.ObjectAttributes{}, fmt.Errorf("unexpected object")
	}
	return thanosobjstore.ObjectAttributes{Size: b.size}, nil
}
func (b *remoteScanBucket) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	if name != b.name {
		return nil, fmt.Errorf("unexpected object")
	}
	return b.object.NewRangeReader(ctx, off, length)
}

type remoteScanTransport struct {
	base            http.RoundTripper
	requests, bytes atomic.Int64
}

func (t *remoteScanTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	response, err := t.base.RoundTrip(req)
	if response != nil && response.Body != nil {
		response.Body = &remoteScanBody{ReadCloser: response.Body, bytes: &t.bytes}
	}
	return response, err
}

type remoteScanBody struct {
	io.ReadCloser
	bytes *atomic.Int64
}

func (b *remoteScanBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes.Add(int64(n))
	return n, err
}
