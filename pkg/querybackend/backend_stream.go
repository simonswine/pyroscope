package querybackend

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/grafana/pyroscope/v2/pkg/util"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// StreamingQueryHandler consumes a stream without retaining the full response.
// visit is serial within an invocation and must not retain mutable chunk state.
type StreamingQueryHandler interface {
	InvokeStream(context.Context, *queryv1.InvokeRequest, func(*queryv1.InvokeStreamResponse) error) error
}

func (q *QueryBackend) InvokeStream(req *queryv1.InvokeRequest, stream queryv1.QueryBackendService_InvokeStreamServer) error {
	if err := metadatastream.Validate(req); err != nil {
		return err
	}
	if req.QueryPlan == nil || req.QueryPlan.Root == nil {
		return status.Error(codes.InvalidArgument, "missing query plan")
	}
	// Commit the attempt before delivering data. Automatic gRPC retries must not
	// replay a partially consumed child into its parent's result stream.
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	ctx := stream.Context()
	started := time.Now()
	kind := req.Query[0].QueryType
	var bytes atomic.Uint64
	var mu sync.Mutex
	send := func(c *queryv1.InvokeStreamResponse) error {
		if c.QueryType != kind {
			return status.Error(codes.Internal, "unexpected metadata query type")
		}
		if c.Diagnostics != nil {
			bytes.Add(c.Diagnostics.GetExecutionNode().GetStats().GetBytesFetched())
			return nil
		}
		if c.SizeVT() > metadatastream.DefaultChunkBytes {
			return status.Error(codes.ResourceExhausted, "metadata chunk exceeds byte limit")
		}
		mu.Lock()
		defer mu.Unlock()
		return stream.Send(c)
	}
	root := req.QueryPlan.Root
	switch root.Type {
	case queryv1.QueryNode_MERGE:
		backend, ok := q.backendClient.(StreamingQueryHandler)
		if !ok {
			return status.Error(codes.Unimplemented, "backend client does not support InvokeStream")
		}
		for _, child := range root.Children {
			if child == nil {
				return status.Error(codes.InvalidArgument, "nil child query plan")
			}
		}
		g, gctx := errgroup.WithContext(ctx)
		// Bound fan-out goroutines as well as transport buffering.
		g.SetLimit(16)
		for _, child := range root.Children {
			childReq := req.CloneVT()
			childReq.QueryPlan = &queryv1.QueryPlan{Root: child}
			g.Go(util.RecoverPanic(func() error { return backend.InvokeStream(gctx, childReq, send) }))
		}
		if err := g.Wait(); err != nil {
			return err
		}
	case queryv1.QueryNode_READ:
		resp, err := q.read(ctx, req.CloneVT(), root.Blocks)
		if err != nil {
			return err
		}
		encoder := metadatastream.NewEncoder(kind, metadatastream.DefaultChunkBytes, send)
		for _, report := range resp.Reports {
			switch kind {
			case queryv1.QueryType_QUERY_SERIES_LABELS:
				for _, set := range report.GetSeriesLabels().GetSeriesLabels() {
					if set == nil {
						return status.Error(codes.Internal, "nil metadata label set")
					}
					if err = encoder.Add(set.Labels); err != nil {
						return err
					}
				}
			case queryv1.QueryType_QUERY_LABEL_NAMES:
				for _, name := range report.GetLabelNames().GetLabelNames() {
					if err = encoder.Add(metadatastream.Element{&typesv1.LabelPair{Name: name}}); err != nil {
						return err
					}
				}
			case queryv1.QueryType_QUERY_LABEL_VALUES:
				for _, value := range report.GetLabelValues().GetLabelValues() {
					if err = encoder.Add(metadatastream.Element{&typesv1.LabelPair{Name: req.Query[0].LabelValues.LabelName, Value: value}}); err != nil {
						return err
					}
				}
			}
		}
		if err = encoder.Flush(); err != nil {
			return err
		}
		bytes.Add(resp.GetDiagnostics().GetExecutionNode().GetStats().GetBytesFetched())
	default:
		return status.Error(codes.InvalidArgument, "unsupported query plan node")
	}
	// Keep the terminal statistics bounded; do not attach an unbounded plan tree.
	return stream.Send(&queryv1.InvokeStreamResponse{QueryType: kind, Diagnostics: &queryv1.Diagnostics{ExecutionNode: &queryv1.ExecutionNode{Stats: &queryv1.ExecutionStats{BytesFetched: bytes.Load(), ProcessingDurationNs: time.Since(started).Nanoseconds()}}}})
}
