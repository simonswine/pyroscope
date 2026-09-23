package queryfrontend

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/grafana/dskit/tenant"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	"github.com/grafana/pyroscope/v2/pkg/model/timeseriesanalysis"
	"github.com/grafana/pyroscope/v2/pkg/validation"
)

func (q *QueryFrontend) AnalyzeSeries(
	ctx context.Context,
	c *connect.Request[querierv1.AnalyzeSeriesRequest],
) (*connect.Response[querierv1.AnalyzeSeriesResponse], error) {
	tenantIDs, err := tenant.TenantIDs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	empty, err := validation.SanitizeTimeRange(q.limits, tenantIDs, &c.Msg.Start, &c.Msg.End)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if empty {
		return connect.NewResponse(&querierv1.AnalyzeSeriesResponse{}), nil
	}
	if c.Msg.Step < 0.001 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("step must be >= 1ms"))
	}
	if err := analysisConfig(c.Msg.Config).WithDefaults().Validate(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	labelSelector, err := buildLabelSelectorWithProfileType(c.Msg.LabelSelector, c.Msg.ProfileTypeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	stepMillis := time.Duration(c.Msg.Step * float64(time.Second)).Milliseconds()
	report, err := q.querySingle(ctx, &queryv1.QueryRequest{
		StartTime:     c.Msg.Start - stepMillis,
		EndTime:       c.Msg.End,
		LabelSelector: labelSelector,
		Query: []*queryv1.Query{{
			QueryType: queryv1.QueryType_QUERY_TIME_SERIES_ANALYSIS,
			TimeSeriesAnalysis: &queryv1.TimeSeriesAnalysisQuery{
				Step:    c.Msg.Step,
				GroupBy: c.Msg.GroupBy,
				Limit:   c.Msg.GetLimit(),
				Config:  c.Msg.Config,
			},
		}},
	}, nil)
	if err != nil {
		return nil, err
	}
	if report == nil || report.TimeSeriesAnalysis == nil {
		return connect.NewResponse(&querierv1.AnalyzeSeriesResponse{}), nil
	}
	return connect.NewResponse(&querierv1.AnalyzeSeriesResponse{Events: report.TimeSeriesAnalysis.Events}), nil
}

func analysisConfig(config *querierv1.TimeSeriesAnalysisConfig) timeseriesanalysis.Config {
	if config == nil {
		return timeseriesanalysis.Config{}
	}
	return timeseriesanalysis.Config{
		BaselineWindow:         int(config.BaselineWindow),
		ConfirmationWindow:     int(config.ConfirmationWindow),
		MinimumSustainedPoints: int(config.MinimumSustainedPoints),
		MinimumRelativeChange:  config.MinimumRelativeChange,
		ScoreThreshold:         config.ScoreThreshold,
		RecoveryThresholdRatio: config.RecoveryThresholdRatio,
		FlatnessThreshold:      config.FlatnessThreshold,
	}
}
