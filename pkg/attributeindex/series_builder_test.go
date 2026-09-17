package attributeindex

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	phlaremodel "github.com/grafana/pyroscope/v2/pkg/model"
)

func TestSeriesBuilderSnapshotsAndDeduplicatesCompleteLabels(t *testing.T) {
	builder := newTestSeriesBuilder(t, DefaultBuilderLimits())
	labels := phlaremodel.LabelsFromStrings("service", "api", "__profile_type__", "cpu")
	require.NoError(t, builder.AddSeries(9, labels))

	// The caller is free to reuse label pair storage after AddSeries.
	labels[0].Name = "mutated"
	labels[0].Value = "value"
	require.NoError(t, builder.AddSeries(3, phlaremodel.LabelsFromStrings("__profile_type__", "cpu", "service", "api")))
	require.NoError(t, builder.AddSeries(9, phlaremodel.LabelsFromStrings("service", "api", "__profile_type__", "cpu")))

	payload, err := builder.Bytes(context.Background())
	require.NoError(t, err)
	reader, err := Open(context.Background(), &memoryRanges{data: payload}, PayloadName, int64(len(payload)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	entities, err := reader.Entities(context.Background())
	require.NoError(t, err)
	require.Equal(t, []Entity{{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "__profile_type__"}, Value: StringValue("cpu")},
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}}, entities)
	ids, err := reader.DatasetIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, []uint32{3, 9}, ids)
}

func TestSeriesBuilderRejectsLimitsBeforeRetainingInput(t *testing.T) {
	limits := DefaultBuilderLimits()
	limits.MaxEntities = 1
	limits.MaxDatasetReferences = 1
	limits.MaxBuildBytes = 1024
	builder := newTestSeriesBuilder(t, limits)
	require.NoError(t, builder.AddSeries(1, phlaremodel.LabelsFromStrings("service", "api")))

	err := builder.AddSeries(2, phlaremodel.LabelsFromStrings("service", "api"))
	require.ErrorIs(t, err, ErrSeriesBuilderLimit)
	err = builder.AddSeries(3, phlaremodel.LabelsFromStrings("service", "worker"))
	require.ErrorIs(t, err, ErrSeriesBuilderLimit)

	payload, err := builder.Bytes(context.Background())
	require.NoError(t, err)
	reader, err := Open(context.Background(), &memoryRanges{data: payload}, PayloadName, int64(len(payload)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	ids, err := reader.DatasetIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, []uint32{1}, ids)
}

func TestSeriesBuilderHonorsCancellationAndClosesAfterWriteError(t *testing.T) {
	builder := newTestSeriesBuilder(t, DefaultBuilderLimits())
	require.NoError(t, builder.AddSeries(1, phlaremodel.LabelsFromStrings("service", "api")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := builder.Bytes(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, builder.AddSeries(2, phlaremodel.LabelsFromStrings("service", "worker")), ErrSeriesBuilderClosed)

	builder = newTestSeriesBuilder(t, DefaultBuilderLimits())
	require.NoError(t, builder.AddSeries(1, phlaremodel.LabelsFromStrings("service", "api")))
	_, err = builder.WriteTo(context.Background(), failingWriter{})
	require.ErrorContains(t, err, "write failed")
	require.ErrorIs(t, builder.AddSeries(2, phlaremodel.LabelsFromStrings("service", "worker")), ErrSeriesBuilderClosed)
}

func TestSeriesBuilderWriteToChecksOutputLimitAndShortWrites(t *testing.T) {
	limits := DefaultBuilderLimits()
	limits.MaxOutputBytes = 1
	builder := newTestSeriesBuilder(t, limits)
	require.NoError(t, builder.AddSeries(1, phlaremodel.LabelsFromStrings("service", "api")))
	_, err := builder.WriteTo(context.Background(), io.Discard)
	require.ErrorIs(t, err, ErrSeriesBuilderLimit)
	require.ErrorIs(t, builder.AddSeries(2, phlaremodel.LabelsFromStrings("service", "worker")), ErrSeriesBuilderClosed)

	builder = newTestSeriesBuilder(t, DefaultBuilderLimits())
	require.NoError(t, builder.AddSeries(1, phlaremodel.LabelsFromStrings("service", "api")))
	_, err = builder.WriteTo(context.Background(), shortWriter{})
	require.ErrorIs(t, err, io.ErrShortWrite)
}

func TestSeriesBuilderRejectsDuplicateLabels(t *testing.T) {
	builder := newTestSeriesBuilder(t, DefaultBuilderLimits())
	err := builder.AddSeries(1, phlaremodel.LabelsFromStrings("service", "api", "service", "worker"))
	require.ErrorContains(t, err, "duplicate attribute")
}

func newTestSeriesBuilder(t *testing.T, limits BuilderLimits) *SeriesBuilder {
	t.Helper()
	builder, err := NewSeriesBuilder(Metadata{
		Tenant:        "tenant-a",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	}, limits)
	require.NoError(t, err)
	t.Cleanup(builder.Close)
	return builder
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
