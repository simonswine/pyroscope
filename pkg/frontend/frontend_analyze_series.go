package frontend

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
)

func (f *Frontend) AnalyzeSeries(context.Context, *connect.Request[querierv1.AnalyzeSeriesRequest]) (*connect.Response[querierv1.AnalyzeSeriesResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("time series analysis requires the V2 query backend"))
}
