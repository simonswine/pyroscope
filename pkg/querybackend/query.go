package querybackend

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/tracing"
	"github.com/prometheus/prometheus/model/labels"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/block"
	"github.com/grafana/pyroscope/v2/pkg/util"
)

// TODO(kolesnikovae): We have a procedural definition of our queries,
//  thus we have handlers. Instead, in order to enable pipelining and
//  reduce the boilerplate, we should define query execution plans.

const (
	// maxProfileIDsToLog is the maximum number of profile IDs to log in trace spans.
	maxProfileIDsToLog = 10
)

var (
	handlerMutex  = new(sync.RWMutex)
	queryHandlers = map[queryv1.QueryType]queryHandler{}
)

type queryHandler func(*queryContext, *queryv1.Query) (*queryv1.Report, error)

func registerQueryHandler(t queryv1.QueryType, h queryHandler) {
	handlerMutex.Lock()
	defer handlerMutex.Unlock()
	if _, ok := queryHandlers[t]; ok {
		panic(fmt.Sprintf("%s: handler already registered", t))
	}
	queryHandlers[t] = h
}

func getQueryHandler(t queryv1.QueryType) (queryHandler, error) {
	handlerMutex.RLock()
	defer handlerMutex.RUnlock()
	handler, ok := queryHandlers[t]
	if !ok {
		return nil, fmt.Errorf("unknown query type %s", t)
	}
	return handler, nil
}

var (
	depMutex          = new(sync.RWMutex)
	queryDependencies = map[queryv1.QueryType][]block.Section{}
)

func registerQueryDependencies(t queryv1.QueryType, deps ...block.Section) {
	depMutex.Lock()
	defer depMutex.Unlock()
	if _, ok := queryDependencies[t]; ok {
		panic(fmt.Sprintf("%s: dependencies already registered", t))
	}
	queryDependencies[t] = deps
}

func registerQueryType(
	qt queryv1.QueryType,
	rt queryv1.ReportType,
	q queryHandler,
	a aggregatorProvider,
	alwaysAggregate bool, // this option will always call the aggregate method for this report type, so it will also run when there is only one report
	deps ...block.Section,
) {
	registerQueryReportType(qt, rt)
	registerQueryHandler(qt, q)
	registerQueryDependencies(qt, deps...)
	registerAggregator(rt, a, alwaysAggregate)
}

type blockContext struct {
	ctx             context.Context
	log             log.Logger
	req             *request
	agg             *reportAggregator
	obj             *block.Object
	grp             *errgroup.Group
	execCollector   *blockExecutionCollector
	weightCollector *queryWeightCollector
	includeStripped bool
}

func (b *blockContext) execute() error {
	startTime := time.Now()

	var span *tracing.Span
	span, b.ctx = tracing.StartSpanFromContext(b.ctx, "blockContext.execute")
	defer span.Finish()

	if idxs := b.datasetIndices(); len(idxs) > 0 {
		if err := b.lookupDatasets(idxs); err != nil {
			if b.obj.IsNotExists(err) {
				level.Warn(b.log).Log("msg", "object not found", "err", err)
				return nil
			}
			return fmt.Errorf("failed to lookup datasets: %w", err)
		}
		// Only accumulate datasets resolved from the index lookup; Format0
		// datasets were already counted by the query frontend at planning time.
		b.weightCollector.addDatasets(b.obj.Metadata().Datasets)
	}

	md := b.obj.Metadata()
	for _, ds := range md.Datasets {
		q := b.newQueryContext(ds)
		for _, query := range b.req.src.Query {
			q.grp.Go(util.RecoverPanic(func() error {
				return q.execute(query)
			}))
		}
		if err := q.grp.Wait(); err != nil {
			return err
		}
	}

	if b.execCollector != nil {
		b.execCollector.record(&queryv1.BlockExecution{
			BlockId:           md.Id,
			StartTimeNs:       startTime.UnixNano(),
			EndTimeNs:         time.Now().UnixNano(),
			DatasetsProcessed: int64(len(md.Datasets)),
			Size:              md.Size,
			Shard:             md.Shard,
			CompactionLevel:   md.CompactionLevel,
		})
	}

	return nil
}

// datasetIndices selects the pseudo-datasets that resolve into real profile
// datasets. Attribute indexes are used only for the explicit experimental
// opt-in; the TSDB index remains the default and fallback for tenants whose
// blocks do not contain an attribute index.
func (b *blockContext) datasetIndices() []*metastorev1.Dataset {
	md := b.obj.Metadata()
	var tsdbIndices, attributeIndices []*metastorev1.Dataset
	for _, ds := range md.Datasets {
		switch block.DatasetFormat(ds.Format) {
		case block.DatasetFormat1:
			tsdbIndices = append(tsdbIndices, ds)
		case block.DatasetFormat2:
			attributeIndices = append(attributeIndices, ds)
		}
	}
	if len(tsdbIndices) == 0 && len(attributeIndices) == 0 {
		// The block's metadata explicitly lists datasets to be queried.
		return nil
	}
	if len(tsdbIndices)+len(attributeIndices) != len(md.Datasets) {
		// A metadata response containing explicit datasets and indexes is not
		// an index lookup response. Query its explicit datasets as-is.
		return nil
	}

	// Index-only requests execute directly against the selected indexes rather
	// than resolving profile datasets. Only explicitly supported handlers may
	// run against an attribute index.
	s := (&queryContext{blockContext: b}).sections()
	indexOnly := len(s) == 1 && s[0] == block.SectionTSDB
	if indexOnly {
		md.Datasets = tsdbIndices
		if supportsAttributeMetadataQueries(b.req.src.Query) {
			md.Datasets = b.preferredIndices(tsdbIndices, attributeIndices)
		}
		b.obj.SetMetadata(md)
		attributeCount := 0
		for _, ds := range md.Datasets {
			if block.DatasetFormat(ds.Format) == block.DatasetFormat2 {
				attributeCount++
			}
		}
		oteltrace.SpanFromContext(b.ctx).SetAttributes(
			attribute.Bool("dataset_index_query_index_only", true),
			attribute.Int("metadata_attribute_index_count", attributeCount),
			attribute.Int("metadata_tsdb_index_count", len(md.Datasets)-attributeCount),
		)
		return nil
	}

	return b.preferredIndices(tsdbIndices, attributeIndices)
}

func supportsAttributeMetadataQueries(queries []*queryv1.Query) bool {
	if len(queries) == 0 {
		return false
	}
	for _, query := range queries {
		switch query.QueryType {
		case queryv1.QueryType_QUERY_LABEL_NAMES, queryv1.QueryType_QUERY_LABEL_VALUES, queryv1.QueryType_QUERY_SERIES_LABELS:
		default:
			return false
		}
	}
	return true
}

func (b *blockContext) preferredIndices(tsdbIndices, attributeIndices []*metastorev1.Dataset) []*metastorev1.Dataset {
	if b.req.src.Options == nil || !b.req.src.Options.UseAttributeIndex || len(attributeIndices) == 0 {
		return tsdbIndices
	}

	// Metadata planning fetches both pseudo-dataset types. Prefer a valid
	// attribute index for each tenant, but retain TSDB lookup for old blocks
	// where that tenant has no attribute-index pseudo-dataset.
	attributeTenants := make(map[int32]struct{}, len(attributeIndices))
	for _, ds := range attributeIndices {
		attributeTenants[ds.Tenant] = struct{}{}
	}
	indices := append([]*metastorev1.Dataset(nil), attributeIndices...)
	for _, ds := range tsdbIndices {
		if _, ok := attributeTenants[ds.Tenant]; !ok {
			indices = append(indices, ds)
		}
	}
	return indices
}

func (b *blockContext) lookupDatasets(indices []*metastorev1.Dataset) error {
	oteltrace.SpanFromContext(b.ctx).SetAttributes(attribute.Bool("dataset_index_query", true))
	oteltrace.SpanFromContext(b.ctx).SetAttributes(attribute.Int("dataset_index_count", len(indices)))

	// As query execution has not started yet, we can safely open datasets.
	datasets := make([]*block.Dataset, len(indices))
	for i, ds := range indices {
		datasets[i] = block.NewDataset(ds, b.obj)
	}
	defer func() {
		for _, d := range datasets {
			_ = d.Close()
		}
	}()

	g, ctx := errgroup.WithContext(b.ctx)
	var md *metastorev1.BlockMeta
	g.Go(func() (err error) {
		md, err = b.obj.ReadMetadata(ctx)
		return err
	})
	for i, d := range datasets {
		section := block.SectionDatasetIndex
		if block.DatasetFormat(indices[i].Format) == block.DatasetFormat2 {
			section = block.SectionAttributeIndex
		}
		g.Go(func() error {
			return d.Open(ctx, section)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	datasetIDs := make(map[uint32]struct{})
	for i, d := range datasets {
		var ids []uint32
		var err error
		if block.DatasetFormat(indices[i].Format) == block.DatasetFormat2 {
			ids, err = d.AttributeIndex().DatasetIDs(ctx, attributeIndexMatchers(b.req.matchers))
		} else {
			var seriesIDs map[uint32]struct{}
			seriesIDs, err = getSeriesIDs(d.Index(), b.req.matchers...)
			for id := range seriesIDs {
				datasetIDs[id] = struct{}{}
			}
		}
		if err != nil {
			return err
		}
		for _, id := range ids {
			datasetIDs[id] = struct{}{}
		}
	}

	var j int
	for i := range md.Datasets {
		if _, ok := datasetIDs[uint32(i)]; ok {
			md.Datasets[j] = md.Datasets[i]
			j++
		}
	}
	md.Datasets = md.Datasets[:j]
	b.obj.SetMetadata(md)

	oteltrace.SpanFromContext(b.ctx).AddEvent("dataset index lookup complete")
	return nil
}

func attributeIndexMatchers(matchers []*labels.Matcher) []attributeindex.Matcher {
	attributes := make([]attributeindex.Matcher, 0, len(matchers))
	for _, matcher := range matchers {
		attribute := attributeindex.Matcher{
			Key: attributeindex.Key{Scope: attributeindex.ScopeLegacy, Name: matcher.Name},
		}
		switch matcher.Type {
		case labels.MatchEqual:
			attribute.Operator = attributeindex.MatchEqual
			attribute.Value = attributeindex.StringValue(matcher.Value)
		case labels.MatchNotEqual:
			attribute.Operator = attributeindex.MatchNotEqual
			attribute.Value = attributeindex.StringValue(matcher.Value)
		case labels.MatchRegexp:
			attribute.Operator = attributeindex.MatchRegexp
			attribute.Regexp = matcher.Value
		case labels.MatchNotRegexp:
			attribute.Operator = attributeindex.MatchNotRegexp
			attribute.Regexp = matcher.Value
		}
		attributes = append(attributes, attribute)
	}
	return attributes
}

func (b *blockContext) newQueryContext(ds *metastorev1.Dataset) *queryContext {
	q := &queryContext{blockContext: b, ds: block.NewDataset(ds, b.obj)}
	q.grp, q.ctx = errgroup.WithContext(b.ctx)
	return q
}

type queryContext struct {
	*blockContext
	ctx context.Context
	grp *errgroup.Group
	ds  *block.Dataset
}

func (q *queryContext) execute(query *queryv1.Query) error {
	// Handlers for one dataset run concurrently. Keep each handler's tracing
	// context local while sharing the reference-counted dataset and group.
	local := *q
	q = &local
	var span *tracing.Span
	span, q.ctx = tracing.StartSpanFromContext(q.ctx, "executeQuery."+util.ToCamel(query.QueryType.String()))
	defer span.Finish()
	handle, err := getQueryHandler(query.QueryType)
	if err != nil {
		return err
	}

	sections := q.sections()
	if block.DatasetFormat(q.ds.Metadata().Format) == block.DatasetFormat2 {
		if !supportsAttributeMetadataQueries(q.req.src.Query) {
			return fmt.Errorf("attribute index cannot execute non-metadata queries")
		}
		sections = []block.Section{block.SectionAttributeMetadata}
	}
	if err = q.ds.Open(q.ctx, sections...); err != nil {
		if q.obj.IsNotExists(err) {
			level.Warn(q.log).Log("msg", "object not found", "err", err)
			return nil
		}
		return fmt.Errorf("failed to initialize query context: %w", err)
	}
	defer func() {
		_ = q.ds.CloseWithError(err)
	}()

	r, err := handle(q, query)
	if err != nil {
		return err
	}
	if r != nil {
		r.ReportType = QueryReportType(query.QueryType)
		return q.agg.aggregateReport(r)
	}

	return nil
}

func (q *queryContext) sections() []block.Section {
	sections := make(map[block.Section]struct{}, 3)
	for _, qt := range q.req.src.Query {
		for _, s := range queryDependencies[qt.QueryType] {
			sections[s] = struct{}{}
		}
	}
	unique := make([]block.Section, 0, len(sections))
	for s := range sections {
		unique = append(unique, s)
	}
	return unique
}
