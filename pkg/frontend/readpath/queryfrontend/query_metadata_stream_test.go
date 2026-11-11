package queryfrontend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-kit/log"
	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/frontend"
	"github.com/grafana/pyroscope/v2/pkg/frontend/querysession"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/grafana/pyroscope/v2/pkg/tenant"
	"github.com/grafana/pyroscope/v2/pkg/test/mocks/mockfrontend"
	"github.com/grafana/pyroscope/v2/pkg/test/mocks/mockmetastorev1"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type streamMetadataBackend struct {
	stream func(context.Context, *queryv1.InvokeRequest, func(*queryv1.InvokeStreamResponse) error) error
}

func (s streamMetadataBackend) Invoke(context.Context, *queryv1.InvokeRequest) (*queryv1.InvokeResponse, error) {
	return nil, errors.New("unary Invoke must not be used")
}
func (s streamMetadataBackend) InvokeStream(ctx context.Context, r *queryv1.InvokeRequest, visit func(*queryv1.InvokeStreamResponse) error) error {
	return s.stream(ctx, r, visit)
}
func TestStreamMetadataOperations(t *testing.T) {
	for _, kind := range []string{"Series", "LabelNames", "LabelValues"} {
		t.Run(kind, func(t *testing.T) {
			limits := mockfrontend.NewMockLimits(t)
			limits.On("MaxQueryLookback", "t").Return(time.Duration(0))
			limits.On("MaxQueryLength", "t").Return(time.Duration(0))
			limits.On("QuerySanitizeOnMerge", "t").Return(true)
			meta := new(mockmetastorev1.MockMetadataQueryServiceClient)
			meta.On("QueryMetadata", mock.Anything, mock.Anything).Return(&metastorev1.QueryMetadataResponse{Blocks: []*metastorev1.BlockMeta{{Id: "block"}}}, nil)
			backend := streamMetadataBackend{stream: func(_ context.Context, r *queryv1.InvokeRequest, send func(*queryv1.InvokeStreamResponse) error) error {
				require.Equal(t, []string{"t"}, r.Tenant)
				require.NotNil(t, r.QueryPlan.Root)
				encoder := metadatastream.NewEncoder(r.Query[0].QueryType, 1024, send)
				e := metadatastream.Element{&typesv1.LabelPair{Name: "service_name", Value: "example"}}
				if kind == "LabelNames" {
					e[0].Value = ""
				}
				require.NoError(t, encoder.Add(e))
				return encoder.Flush()
			}}
			q := NewQueryFrontend(log.NewNopLogger(), limits, frontend.Config{}, meta, nil, backend, nil, nil, nil)
			var actual []metadatastream.Element
			err := q.streamMetadata(tenant.InjectTenantID(context.Background(), "t"), querysession.Request{Type: kind, Start: 1000, End: 2000, Name: "service_name", Matchers: []string{`{service_name="example"}`}}, func(e metadatastream.Element) error { actual = append(actual, e); return nil })
			require.NoError(t, err)
			require.Len(t, actual, 1)
			require.Equal(t, "service_name", actual[0][0].Name)
		})
	}
}
