package block

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
)

func TestCompactionAttributeIndexRunDedup(t *testing.T) {
	ctx := context.Background()
	md := attributeindex.Metadata{Tenant: "tenant", EntityKind: "series", TimeSemantics: attributeindex.TimeLegacyCoarseCoverage}
	builder, err := attributeindex.NewSeriesBuilder(md, attributeindex.DefaultBuilderLimits())
	require.NoError(t, err)
	defer builder.Close()
	p := &CompactionPlan{attributeIndex: builder}
	row := ProfileEntry{Fingerprint: 42, labels: immutableLabels{{name: "service_name", value: "a"}}}
	require.NoError(t, p.addRowToAttributeIndex(ctx, row))
	require.NoError(t, p.addRowToAttributeIndex(ctx, row))
	// Reuse storage and force a fingerprint collision. Full equality must win.
	row.labels[0].value = "b"
	require.NoError(t, p.addRowToAttributeIndex(ctx, row))
	// The same series at the next dataset boundary must add another reference.
	p.currentDatasetIdx = 1
	p.lastAttributeLabels = nil
	require.NoError(t, p.addRowToAttributeIndex(ctx, row))
	actual, err := builder.Bytes(ctx)
	require.NoError(t, err)
	expectedBuilder, err := attributeindex.NewSeriesBuilder(md, attributeindex.DefaultBuilderLimits())
	require.NoError(t, err)
	defer expectedBuilder.Close()
	require.NoError(t, expectedBuilder.AddSeries(0, immutableLabels{{name: "service_name", value: "a"}}))
	require.NoError(t, expectedBuilder.AddSeries(0, row.labels))
	require.NoError(t, expectedBuilder.AddSeries(1, row.labels))
	expected, err := expectedBuilder.Bytes(ctx)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func TestCompactionAttributeIndexCleanupOnWriterFailure(t *testing.T) {
	p := newBlockCompaction("block", "tenant", 0, 1)
	// A regular file cannot be used as the temporary directory.
	path := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	_, err := p.Compact(context.Background(), nil, path, nil)
	require.Error(t, err)
	require.ErrorIs(t, p.attributeIndex.AddSeries(0, immutableLabels{{name: "service_name", value: "a"}}), attributeindex.ErrSeriesBuilderClosed)
	require.Nil(t, p.lastAttributeLabels)
}

func TestCompactionAttributeIndexErrors(t *testing.T) {
	md := attributeindex.Metadata{Tenant: "tenant", EntityKind: "series", TimeSemantics: attributeindex.TimeLegacyCoarseCoverage}
	limits := attributeindex.DefaultBuilderLimits()
	builder, err := attributeindex.NewSeriesBuilder(md, limits)
	require.NoError(t, err)
	defer builder.Close()
	p := &CompactionPlan{attributeIndex: builder}
	row := ProfileEntry{labels: immutableLabels{{name: "service_name", value: "a"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, p.addRowToAttributeIndex(ctx, row), context.Canceled)
	require.Nil(t, p.lastAttributeLabels)
	require.NoError(t, p.addRowToAttributeIndex(context.Background(), row))
	require.ErrorIs(t, p.addRowToAttributeIndex(ctx, row), context.Canceled)
	builder.Close()
	p.lastAttributeLabels = nil
	require.ErrorIs(t, p.addRowToAttributeIndex(context.Background(), row), attributeindex.ErrSeriesBuilderClosed)
}
