package debug

import (
	"context"
	"os"
	"testing"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"

	ingestv1 "github.com/grafana/pyroscope/api/gen/proto/go/ingester/v1"
	"github.com/grafana/pyroscope/pkg/iter"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
	phlareobj "github.com/grafana/pyroscope/pkg/objstore"
	"github.com/grafana/pyroscope/pkg/objstore/providers/gcs"
	"github.com/grafana/pyroscope/pkg/phlaredb"
	"github.com/grafana/pyroscope/pkg/phlaredb/block"
)

func TestReproducePanic3164(t *testing.T) {
	ctx := context.Background()

	bucketClient, err :=
		gcs.NewBucketClient(
			ctx,
			gcs.Config{
				BucketName: "grafana-pyroscope-investigations",
			},
			"test-bucket",
			log.NewNopLogger(),
		)
	require.NoError(t, err)

	bucket := phlareobj.NewPrefixedBucket(
		phlareobj.NewBucket(bucketClient),
		"20240409-pyroscope-issue-3164/blocks",
	)

	r, err := bucket.Get(ctx, "01HV1DNE80RTHZAR4ZSN95N7X4/meta.json")
	require.NoError(t, err)

	meta, err := block.Read(r)
	require.NoError(t, err)

	q := phlaredb.NewSingleBlockQuerierFromMeta(
		ctx,
		bucket,
		meta,
	)

	ps, err := phlaremodel.ParseProfileTypeSelector("process_cpu:cpu:nanoseconds:cpu:nanoseconds")
	require.NoError(t, err)

	// this mimicks trace with id 0838abfd8fe3825cf076f072c74f41ad
	pit, err := q.SelectMatchingProfiles(ctx, &ingestv1.SelectProfilesRequest{
		LabelSelector: `{pod="query-frontend-6b85db5699-fvqq4", span_name="HTTP POST - prometheus_api_v1_query_range"}`,
		// This series seems to be causing the issue consistently
		// partition_id = 8356834866473141348
		// {"SeriesRef":267340,"SeriesIndex":18799,"Labels":{"__name__":"process_cpu","__period_type__":"cpu","__period_unit__":"nanoseconds","__profile_type__":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","__service_name__":"mimir-prod-41/query-frontend","__type__":"cpu","__unit__":"nanoseconds","cluster":"prod-au-southeast-1","container":"query-frontend","gossip_ring_member":"true","instance":"10.13.85.217:80","job":"mimir-prod-41/query-frontend","name":"query-frontend","namespace":"mimir-prod-41","node":"ip-10-60-208-95.ap-southeast-2.compute.internal","pod":"query-frontend-6b85db5699-fvqq4","pod_template_hash":"6b85db5699","service_name":"mimir-prod-41/query-frontend","span_name":"HTTP POST - prometheus_api_v1_query_range"}}
		Type:  ps,
		Start: 0,
		End:   71266246900000000,
	})
	require.NoError(t, err)

	// retrieve the profiles
	profiles := make([]phlaredb.Profile, 0, 1000)
	for pit.Next() {
		profiles = append(profiles, pit.At())
	}
	require.NoError(t, pit.Err())
	q.Sort(profiles)

	// this crashes with maxNodes > 0
	profile, err := q.MergePprof(ctx, iter.NewSliceIterator(profiles), 1, nil)
	require.NoError(t, err)

	d, err := profile.MarshalVT()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile("profile.pb", d, 0644))

}
