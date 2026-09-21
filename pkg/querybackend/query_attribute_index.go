package querybackend

import (
	"context"
	"fmt"
	"slices"

	"github.com/prometheus/prometheus/model/labels"

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/model"
)

func attributeLabelNames(ctx context.Context, reader *attributeindex.Reader, matchers []*labels.Matcher) ([]string, error) {
	keys, err := reader.Names(ctx, attributeIndexMatchers(matchers))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		if key.Scope == attributeindex.ScopeLegacy {
			names = append(names, key.Name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func attributeLabelValues(ctx context.Context, reader *attributeindex.Reader, name string, matchers []*labels.Matcher) ([]string, error) {
	values, err := reader.Values(ctx, attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: name}, attributeIndexMatchers(matchers))
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value.Type != attributeindex.ValueString {
			return nil, fmt.Errorf("legacy attribute %q has non-string value", name)
		}
		// TSDB's filtered path uses LabelValueFor, which treats an explicit
		// empty value as absent. Its unfiltered dictionary path retains it.
		if len(matchers) > 0 && len(value.Data) == 0 {
			continue
		}
		result = append(result, string(value.Data))
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func attributeSeriesLabels(ctx context.Context, reader *attributeindex.Reader, matchers []*labels.Matcher, by ...string) ([]*typesv1.Labels, error) {
	// Intersect projection with available legacy keys. Unlike the generic reader,
	// the TSDB handler returns no series when all requested names are unknown.
	projection := make([]attributeindex.Key, 0)
	for _, key := range reader.Keys() {
		if key.Scope == attributeindex.ScopeLegacy && (len(by) == 0 || slices.Contains(by, key.Name)) {
			projection = append(projection, key)
		}
	}
	if len(by) > 0 && len(projection) == 0 {
		return nil, nil
	}
	entities, err := reader.Series(ctx, attributeIndexMatchers(matchers), projection)
	if err != nil {
		return nil, err
	}
	result := make([]*typesv1.Labels, 0, len(entities))
	for _, entity := range entities {
		set := &typesv1.Labels{Labels: make([]*typesv1.LabelPair, 0, len(entity.Attributes))}
		for _, attr := range entity.Attributes {
			if attr.Value.Type != attributeindex.ValueString {
				return nil, fmt.Errorf("legacy attribute %q has non-string value", attr.Key.Name)
			}
			// Legacy series output omits empty values; discovery still includes them.
			if len(attr.Value.Data) > 0 {
				set.Labels = append(set.Labels, &typesv1.LabelPair{Name: attr.Key.Name, Value: string(attr.Value.Data)})
			}
		}
		slices.SortFunc(set.Labels, model.CompareLabelPairs2)
		result = append(result, set)
	}
	slices.SortFunc(result, model.CompareLabels)
	return slices.CompactFunc(result, func(a, b *typesv1.Labels) bool { return model.CompareLabels(a, b) == 0 }), nil
}
