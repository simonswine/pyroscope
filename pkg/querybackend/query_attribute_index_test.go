package querybackend

import (
	"context"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/block"
)

func TestBlockContextDatasetIndices_PrefersAttributeIndexPerTenant(t *testing.T) {
	tsdbA := &metastorev1.Dataset{Format: uint32(block.DatasetFormat1), Tenant: 1}
	attributeA := &metastorev1.Dataset{Format: uint32(block.DatasetFormat2), Tenant: 1}
	tsdbB := &metastorev1.Dataset{Format: uint32(block.DatasetFormat1), Tenant: 2}
	metadata := &metastorev1.BlockMeta{Datasets: []*metastorev1.Dataset{tsdbA, attributeA, tsdbB}}

	context := &blockContext{
		ctx: context.Background(),
		obj: block.NewObject(nil, metadata),
		req: &request{src: &queryv1.InvokeRequest{Options: &queryv1.InvokeOptions{UseAttributeIndex: true}}},
	}
	assert.ElementsMatch(t, []*metastorev1.Dataset{attributeA, tsdbB}, context.datasetIndices())

	context.req.src.Options.UseAttributeIndex = false
	assert.ElementsMatch(t, []*metastorev1.Dataset{tsdbA, tsdbB}, context.datasetIndices())
}

func TestAttributeIndexMatchers(t *testing.T) {
	matchers := attributeIndexMatchers([]*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, "equal", "value"),
		labels.MustNewMatcher(labels.MatchNotEqual, "not-equal", "value"),
		labels.MustNewMatcher(labels.MatchRegexp, "regexp", "value.*"),
		labels.MustNewMatcher(labels.MatchNotRegexp, "not-regexp", "value.*"),
	})
	require.Equal(t, []attributeindex.Matcher{
		{Key: attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: "equal"}, Operator: attributeindex.MatchEqual, Value: attributeindex.StringValue("value")},
		{Key: attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: "not-equal"}, Operator: attributeindex.MatchNotEqual, Value: attributeindex.StringValue("value")},
		{Key: attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: "regexp"}, Operator: attributeindex.MatchRegexp, Regexp: "value.*"},
		{Key: attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: "not-regexp"}, Operator: attributeindex.MatchNotRegexp, Regexp: "value.*"},
	}, matchers)
}
