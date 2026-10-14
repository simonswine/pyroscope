//go:build integration

package querybackend

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	thanosobjstore "github.com/thanos-io/objstore"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"

	"github.com/grafana/pyroscope/v2/pkg/block"
	"github.com/grafana/pyroscope/v2/pkg/iter"
	"github.com/grafana/pyroscope/v2/pkg/objstore"
)

// TestV2ProfileEntryRemote exercises the production V2 entry factory, including
// TSDB series/labels and optional profile IDs. This is a warm-metadata smoke test,
// not an end-to-end Invoke benchmark. It never logs profile contents or tokens.
func TestV2ProfileEntryRemote(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	token, err := exec.CommandContext(ctx, "gcloud", "auth", "print-access-token").Output()
	require.NoError(t, err)
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: strings.TrimSpace(string(token))}))
	transport := &v2RemoteTransport{base: httpClient.Transport}
	httpClient.Transport = transport
	client, err := storage.NewClient(ctx, option.WithHTTPClient(httpClient))
	require.NoError(t, err)
	defer client.Close()
	object := client.Bucket(u.Host).Object(name)
	attrs, err := object.Attrs(ctx)
	require.NoError(t, err)
	bucket := objstore.NewBucket(&v2RemoteBucket{object: object.Generation(attrs.Generation), name: name, size: attrs.Size})
	obj, err := block.NewObjectFromPath(ctx, bucket, name, block.WithObjectMaxSizeLoadInMemory(0))
	require.NoError(t, err)
	selected := -1
	var size uint64
	for i, ds := range obj.Metadata().Datasets {
		if block.DatasetFormat(ds.Format) != block.DatasetFormat0 || len(ds.TableOfContents) < 3 {
			continue
		}
		start, end := ds.TableOfContents[0], ds.TableOfContents[1]
		require.LessOrEqual(t, start, end)
		require.LessOrEqual(t, end, uint64(attrs.Size))
		if end-start > size {
			selected = i
			size = end - start
		}
	}
	require.NotEqual(t, -1, selected)
	// This fixture must remain larger than the dataset in-memory threshold so
	// the test measures remote pages rather than a downloaded dataset buffer.
	require.Greater(t, size, uint64(1<<20))
	ds := block.NewDataset(obj.Metadata().Datasets[selected], obj)
	require.NoError(t, ds.Open(ctx, block.SectionTSDB, block.SectionProfiles))
	defer ds.Close()
	q := &queryContext{ctx: ctx, ds: ds, blockContext: &blockContext{req: &request{startTime: 0, endTime: math.MaxInt64}}}
	t.Logf("generation=%d dataset=%d profile_bytes=%d rows=%d", attrs.Generation, selected, size, ds.Profiles().NumRows())
	for _, withIDs := range []bool{false, true} {
		options := []profileIteratorOption{withAllLabels(), withFetchProfileIDs(withIDs)}
		var reference [32]byte
		var referenceRows int
		var referenceRequests, referenceBytes int64
		for _, engine := range []string{"scalar", "batch"} {
			beforeRequests, beforeBytes := transport.requests.Load(), transport.bytes.Load()
			start := time.Now()
			var entries iter.Iterator[ProfileEntry]
			if engine == "scalar" {
				selected, err := getSeries(ds.Index(), q.req.matchers, options...)
				require.NoError(t, err)
				entries = scalarProfileEntryIterator(q, selected, iteratorOptsFromOptions(options))
			} else {
				entries, err = profileEntryIterator(q, options...)
				require.NoError(t, err)
				require.IsType(t, &iter.AsyncBatchIterator[ProfileEntry, ProfileEntry]{}, entries, "must exercise the production V2 batch branch")
			}
			h := sha256.New()
			encoder := json.NewEncoder(h)
			count := 0
			for entries.Next() {
				require.NoError(t, encoder.Encode(entries.At()))
				count++
			}
			require.NoError(t, entries.Err())
			require.NoError(t, entries.Close())
			var digest [32]byte
			copy(digest[:], h.Sum(nil))
			requests, bytes := transport.requests.Load()-beforeRequests, transport.bytes.Load()-beforeBytes
			t.Logf("engine=%s fetch_ids=%t rows=%d elapsed_ms=%.3f requests=%d bytes=%d", engine, withIDs, count, float64(time.Since(start).Microseconds())/1000, requests, bytes)
			if engine == "scalar" {
				reference = digest
				referenceRows = count
				referenceRequests = requests
				referenceBytes = bytes
			} else {
				require.Equal(t, referenceRows, count)
				require.Equal(t, reference, digest, "ordered ProfileEntries must match, including labels/fingerprints/IDs")
				require.Equal(t, referenceRequests, requests, "remote request parity on this fixture")
				require.Equal(t, referenceBytes, bytes, "response-body byte parity on this fixture")
			}
		}
	}
}

// Read-only adapter using the same GCS range-reader SDK as the production bucket.
type v2RemoteBucket struct {
	thanosobjstore.Bucket
	object *storage.ObjectHandle
	name   string
	size   int64
}

func (b *v2RemoteBucket) Attributes(_ context.Context, name string) (thanosobjstore.ObjectAttributes, error) {
	if name != b.name {
		return thanosobjstore.ObjectAttributes{}, fmt.Errorf("unexpected object")
	}
	return thanosobjstore.ObjectAttributes{Size: b.size}, nil
}
func (b *v2RemoteBucket) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	if name != b.name {
		return nil, fmt.Errorf("unexpected object")
	}
	return b.object.NewRangeReader(ctx, off, length)
}

type v2RemoteTransport struct {
	base            http.RoundTripper
	requests, bytes atomic.Int64
}

func (t *v2RemoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	response, err := t.base.RoundTrip(req)
	if response != nil && response.Body != nil {
		response.Body = &v2RemoteBody{ReadCloser: response.Body, bytes: &t.bytes}
	}
	return response, err
}

type v2RemoteBody struct {
	io.ReadCloser
	bytes *atomic.Int64
}

func (b *v2RemoteBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes.Add(int64(n))
	return n, err
}
