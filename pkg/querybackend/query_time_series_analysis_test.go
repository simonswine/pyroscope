package querybackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

func TestTimeSeriesAnalysisAggregator(t *testing.T) {
	query := &queryv1.TimeSeriesAnalysisQuery{
		Step:    1,
		GroupBy: []string{"service_name"},
		Config: &querierv1.TimeSeriesAnalysisConfig{
			BaselineWindow:         3,
			ConfirmationWindow:     3,
			MinimumSustainedPoints: 2,
			MinimumRelativeChange:  0.25,
			ScoreThreshold:         3.5,
			RecoveryThresholdRatio: 0.5,
			FlatnessThreshold:      0.01,
		},
	}
	agg := newTimeSeriesAnalysisAggregator(&queryv1.InvokeRequest{StartTime: 0, EndTime: 8000}).(*timeSeriesAnalysisAggregator)
	report := &queryv1.Report{TimeSeriesAnalysis: &queryv1.TimeSeriesAnalysisReport{
		Query: query,
		TimeSeries: []*typesv1.Series{{
			Labels: []*typesv1.LabelPair{{Name: "service_name", Value: "api"}},
			Points: []*typesv1.Point{
				{Timestamp: 1000, Value: 100},
				{Timestamp: 2000, Value: 100},
				{Timestamp: 3000, Value: 100},
				{Timestamp: 4000, Value: 200},
				{Timestamp: 5000, Value: 105},
				{Timestamp: 6000, Value: 100},
				{Timestamp: 7000, Value: 100},
			},
		}},
	}}
	require.NoError(t, agg.aggregate(report))

	result := agg.build().TimeSeriesAnalysis
	require.Len(t, result.TimeSeries, 1)
	// Even a complete event is not analyzed during intermediate aggregation.
	require.Empty(t, result.Events)
	require.NoError(t, finalizeTimeSeriesAnalysis(result))
	require.Empty(t, result.TimeSeries)
	require.Len(t, result.Events, 1)
	event := result.Events[0]
	assert.Equal(t, querierv1.TimeSeriesEventType_TIME_SERIES_EVENT_TYPE_SPIKE, event.Type)
	assert.Equal(t, querierv1.TimeSeriesEventDuration_TIME_SERIES_EVENT_DURATION_SHORT_TERM, event.Duration)
	assert.Equal(t, int64(4000), event.StartTime)
	assert.Equal(t, int64(4000), event.EndTime)
	assert.Equal(t, "api", event.Labels[0].Value)
}
