package queryfrontend

import (
	"context"
	"errors"
	"net/http"

	"github.com/grafana/dskit/tenant"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	"github.com/grafana/pyroscope/v2/pkg/frontend/querysession"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/queryplan"
	"github.com/grafana/pyroscope/v2/pkg/validation"
	prommodel "github.com/prometheus/common/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MetadataWebSocket serves the new v2 metadata read path independently of the
// legacy unary router. State belongs to this query-frontend, not query-backends.
func (q *QueryFrontend) MetadataWebSocket(w http.ResponseWriter, r *http.Request) {
	q.metadataWSOnce.Do(func() {
		config := q.metadataWSConfig
		if config == (querysession.Config{}) {
			config = querysession.DefaultConfig()
		}
		q.metadataWS, q.metadataWSErr = querysession.New(config, q.streamMetadata)
	})
	if q.metadataWSErr != nil {
		http.Error(w, "invalid metadata streaming configuration", http.StatusServiceUnavailable)
		return
	}
	q.metadataWS.ServeHTTP(w, r)
}

// CloseMetadataWebSockets releases frontend-owned state during service shutdown.
func (q *QueryFrontend) CloseMetadataWebSockets() {
	q.metadataWSOnce.Do(func() { q.metadataWSErr = errors.New("query frontend stopped") })
	if q.metadataWS != nil {
		q.metadataWS.Close()
	}
}

func (q *QueryFrontend) streamMetadata(ctx context.Context, r querysession.Request, visit func(metadatastream.Element) error) error {
	tenants, err := tenant.TenantIDs(ctx)
	if err != nil {
		return err
	}
	empty, err := validation.SanitizeTimeRange(q.limits, tenants, &r.Start, &r.End)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	selector, err := buildLabelSelectorFromMatchers(r.Matchers)
	if err != nil {
		return err
	}
	query := &queryv1.Query{}
	switch r.Type {
	case "Series":
		names := make([]string, 0, len(r.LabelNames))
		for _, name := range r.LabelNames {
			if prommodel.LegacyValidation.IsValidLabelName(name) {
				names = append(names, name)
			}
		}
		if len(r.LabelNames) > 0 && len(names) == 0 {
			return nil
		}
		query.QueryType = queryv1.QueryType_QUERY_SERIES_LABELS
		query.SeriesLabels = &queryv1.SeriesLabelsQuery{LabelNames: names}
	case "LabelNames":
		query.QueryType = queryv1.QueryType_QUERY_LABEL_NAMES
		query.LabelNames = &queryv1.LabelNamesQuery{}
	case "LabelValues":
		if r.Name == "" {
			return errors.New("label name is required")
		}
		query.QueryType = queryv1.QueryType_QUERY_LABEL_VALUES
		query.LabelValues = &queryv1.LabelValuesQuery{LabelName: r.Name}
	default:
		return errors.New("unsupported metadata query type")
	}
	req := &queryv1.QueryRequest{StartTime: r.Start, EndTime: r.End, LabelSelector: selector, Query: []*queryv1.Query{query}}
	blocks, err := q.QueryMetadata(ctx, req)
	if err != nil {
		return err
	}
	if len(blocks) == 0 {
		return nil
	}
	var metadataBytes int
	for _, b := range blocks {
		metadataBytes += b.SizeVT()
	}
	q.metrics.fetchedBytesTotal.WithLabelValues(tenant.JoinTenantIDs(tenants), "metastore").Add(float64(metadataBytes))
	backend, ok := q.querybackend.(interface {
		InvokeStream(context.Context, *queryv1.InvokeRequest, func(*queryv1.InvokeStreamResponse) error) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "query backend does not support InvokeStream")
	}
	p := queryplan.Build(blocks, 4, 20)
	if q.queryPlanType == "balanced" {
		p = queryplan.BuildBalanced(blocks, 4, 20)
	}
	return backend.InvokeStream(ctx, &queryv1.InvokeRequest{Tenant: tenants, StartTime: r.Start, EndTime: r.End, LabelSelector: selector, Query: req.Query, QueryPlan: p, Options: &queryv1.InvokeOptions{SanitizeOnMerge: q.limits.QuerySanitizeOnMerge(tenants[0])}}, func(c *queryv1.InvokeStreamResponse) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c == nil || c.QueryType != query.QueryType {
			return errors.New("unexpected metadata chunk")
		}
		if c.SizeVT() > metadatastream.DefaultChunkBytes {
			return querysession.ErrLimit
		}
		if c.Diagnostics != nil {
			stats := c.Diagnostics.GetExecutionNode().GetStats()
			q.metrics.fetchedBytesTotal.WithLabelValues(tenant.JoinTenantIDs(tenants), "object_storage").Add(float64(stats.GetBytesFetched()))
			return nil
		}
		return metadatastream.Decode(c, func(e metadatastream.Element) error {
			// Match unary metadata's default legacy-name behavior, projecting before
			// frontend deduplication. UTF-8 negotiation can be added independently.
			if r.Type == "LabelNames" {
				if !prommodel.LegacyValidation.IsValidLabelName(e[0].Name) {
					return nil
				}
			}
			if r.Type == "Series" {
				filtered := make(metadatastream.Element, 0, len(e))
				for _, pair := range e {
					if prommodel.LegacyValidation.IsValidLabelName(pair.Name) {
						filtered = append(filtered, pair)
					}
				}
				e = filtered
			}
			return visit(e)
		})
	})
}
