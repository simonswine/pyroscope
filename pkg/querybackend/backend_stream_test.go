package querybackend

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type metadataReadFunc func(context.Context, *queryv1.InvokeRequest) (*queryv1.InvokeResponse, error)

func (f metadataReadFunc) Invoke(ctx context.Context, r *queryv1.InvokeRequest) (*queryv1.InvokeResponse, error) {
	return f(ctx, r)
}

type metadataTransport struct {
	queryv1.QueryBackendServiceClient
}

func (c metadataTransport) Invoke(context.Context, *queryv1.InvokeRequest) (*queryv1.InvokeResponse, error) {
	return nil, status.Error(codes.Internal, "unary remote Invoke must not be used")
}
func (c metadataTransport) InvokeStream(ctx context.Context, r *queryv1.InvokeRequest, visit func(*queryv1.InvokeStreamResponse) error) error {
	s, err := c.QueryBackendServiceClient.InvokeStream(ctx, r)
	if err != nil {
		return err
	}
	for {
		chunk, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err = visit(chunk); err != nil {
			return err
		}
	}
}
func metadataRPC(t *testing.T, b *QueryBackend) queryv1.QueryBackendServiceClient {
	t.Helper()
	l := bufconn.Listen(1 << 20)
	s := grpc.NewServer(grpc.MaxSendMsgSize(metadatastream.DefaultChunkBytes + 1024))
	queryv1.RegisterQueryBackendServiceServer(s, b)
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { s.Stop(); _ = l.Close() })
	c, err := grpc.NewClient("passthrough:///metadata", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(metadatastream.DefaultChunkBytes+1024)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return queryv1.NewQueryBackendServiceClient(c)
}
func TestInvokeStreamAcrossMergeHops(t *testing.T) {
	sets := make([]*typesv1.Labels, 800)
	for i := range sets {
		sets[i] = &typesv1.Labels{Labels: []*typesv1.LabelPair{{Name: "large", Value: fmt.Sprintf("%d-%s", i, strings.Repeat("x", 8192))}}}
	}
	leaf := &QueryBackend{blockReader: metadataReadFunc(func(context.Context, *queryv1.InvokeRequest) (*queryv1.InvokeResponse, error) {
		return &queryv1.InvokeResponse{Reports: []*queryv1.Report{{SeriesLabels: &queryv1.SeriesLabelsReport{SeriesLabels: sets}}}, Diagnostics: &queryv1.Diagnostics{ExecutionNode: &queryv1.ExecutionNode{Stats: &queryv1.ExecutionStats{BytesFetched: 123}}}}, nil
	})}
	leafClient := metadataRPC(t, leaf)
	parent := &QueryBackend{backendClient: metadataTransport{leafClient}}
	parentClient := metadataRPC(t, parent)
	root := &QueryBackend{backendClient: metadataTransport{parentClient}}
	client := metadataRPC(t, root)
	read := &queryv1.QueryNode{Type: queryv1.QueryNode_READ}
	merge := &queryv1.QueryNode{Type: queryv1.QueryNode_MERGE, Children: []*queryv1.QueryNode{read, read}}
	req := &queryv1.InvokeRequest{Query: []*queryv1.Query{{QueryType: queryv1.QueryType_QUERY_SERIES_LABELS, SeriesLabels: &queryv1.SeriesLabelsQuery{}}}, QueryPlan: &queryv1.QueryPlan{Root: &queryv1.QueryNode{Type: queryv1.QueryNode_MERGE, Children: []*queryv1.QueryNode{merge}}}}
	stream, err := client.InvokeStream(context.Background(), req)
	require.NoError(t, err)
	count, chunks, diagnostics := 0, 0, 0
	for {
		c, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.LessOrEqual(t, c.SizeVT(), metadatastream.DefaultChunkBytes)
		if c.Diagnostics != nil {
			diagnostics++
			require.Equal(t, uint64(246), c.Diagnostics.ExecutionNode.Stats.BytesFetched)
			continue
		}
		chunks++
		require.NoError(t, metadatastream.Decode(c, func(e metadatastream.Element) error { count++; require.Len(t, e, 1); return nil }))
	}
	require.Equal(t, 1600, count)
	require.GreaterOrEqual(t, chunks, 2)
	require.Equal(t, 1, diagnostics)
}
func TestInvokeStreamRejectsUnsupportedQueries(t *testing.T) {
	client := metadataRPC(t, &QueryBackend{})
	s, err := client.InvokeStream(context.Background(), &queryv1.InvokeRequest{Query: []*queryv1.Query{{QueryType: queryv1.QueryType_QUERY_TREE}}})
	require.NoError(t, err)
	_, err = s.Recv()
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
