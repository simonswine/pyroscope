package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/grafana/pyroscope/v2/pkg/model"
)

type queryAnalyzeSeriesParams struct {
	*queryParams
	ProfileType string
	GroupBy     []string
	Step        time.Duration
	Limit       int64
	Output      string
	Config      querierv1.TimeSeriesAnalysisConfig
}

func addQueryAnalyzeSeriesParams(cmd commander) *queryAnalyzeSeriesParams {
	p := &queryAnalyzeSeriesParams{queryParams: addQueryParams(cmd)}
	cmd.Flag("profile-type", "Profile type to query.").Default("process_cpu:cpu:nanoseconds:cpu:nanoseconds").StringVar(&p.ProfileType)
	cmd.Flag("group-by", "Label to group by. Can be specified multiple times.").StringsVar(&p.GroupBy)
	cmd.Flag("step", "Time series resolution.").Default("1m").DurationVar(&p.Step)
	cmd.Flag("limit", "Return events from the top N series by strongest score (0 means unlimited).").Int64Var(&p.Limit)
	cmd.Flag("output", "Output format: table or json.").Default("table").EnumVar(&p.Output, "table", "json")
	cmd.Flag("baseline-window", "Preceding observed points for the baseline (0 uses server default).").Uint32Var(&p.Config.BaselineWindow)
	cmd.Flag("confirmation-window", "Subsequent observed points for classification (0 uses server default).").Uint32Var(&p.Config.ConfirmationWindow)
	cmd.Flag("minimum-sustained-points", "Elevated points required for sustained events (0 uses server default).").Uint32Var(&p.Config.MinimumSustainedPoints)
	cmd.Flag("minimum-relative-change", "Minimum absolute fractional deviation (0 uses server default).").Float64Var(&p.Config.MinimumRelativeChange)
	cmd.Flag("score-threshold", "Minimum robust score (0 uses server default).").Float64Var(&p.Config.ScoreThreshold)
	cmd.Flag("recovery-threshold-ratio", "Fraction of minimum relative change used for recovery (0 uses server default).").Float64Var(&p.Config.RecoveryThresholdRatio)
	cmd.Flag("flatness-threshold", "Maximum normalized range considered flat (0 uses server default).").Float64Var(&p.Config.FlatnessThreshold)
	return p
}

func queryAnalyzeSeries(ctx context.Context, p *queryAnalyzeSeriesParams) error {
	from, to, err := p.parseFromTo()
	if err != nil {
		return err
	}
	if p.Step <= 0 {
		return fmt.Errorf("--step must be positive")
	}
	if p.Limit < 0 {
		return fmt.Errorf("--limit must be non-negative")
	}
	req := &querierv1.AnalyzeSeriesRequest{
		ProfileTypeID: p.ProfileType, LabelSelector: p.Query,
		Start: from.UnixMilli(), End: to.UnixMilli(),
		GroupBy: p.GroupBy, Step: p.Step.Seconds(), Config: &p.Config,
	}
	if p.Limit > 0 {
		req.Limit = &p.Limit
	}
	resp, err := p.queryClient().AnalyzeSeries(ctx, connect.NewRequest(req))
	if err != nil {
		return fmt.Errorf("failed to analyze series: %w", err)
	}
	logDiagnostics(p.phlareClient, resp.Header())
	return outputSeriesAnalysis(ctx, resp.Msg, p.Output)
}

func outputSeriesAnalysis(ctx context.Context, response *querierv1.AnalyzeSeriesResponse, format string) error {
	if format == outputJSON {
		data, err := (protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: true}).Marshal(response)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output(ctx), string(data))
		return err
	}
	table := newTableWriter(output(ctx))
	table.SetHeader([]string{"Labels", "Type", "Duration", "Start", "End", "Peak time", "Baseline", "Peak value", "Change", "Score"})
	for _, event := range response.Events {
		table.Append([]string{
			model.Labels(event.Labels).ToPrometheusLabels().String(),
			strings.TrimPrefix(event.Type.String(), "TIME_SERIES_EVENT_TYPE_"),
			strings.TrimPrefix(event.Duration.String(), "TIME_SERIES_EVENT_DURATION_"),
			time.UnixMilli(event.StartTime).UTC().Format(time.RFC3339Nano),
			time.UnixMilli(event.EndTime).UTC().Format(time.RFC3339Nano),
			time.UnixMilli(event.PeakTime).UTC().Format(time.RFC3339Nano),
			fmt.Sprintf("%.6g", event.Baseline), fmt.Sprintf("%.6g", event.PeakValue),
			fmt.Sprintf("%.2f%%", event.RelativeChange*100), fmt.Sprintf("%.6g", event.Score),
		})
	}
	table.Render()
	return nil
}
