// Package metadatastream implements bounded, independently decodable metadata chunks.
package metadatastream

import (
	"errors"
	"fmt"
	"unique"

	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/model/attributetable"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const DefaultChunkBytes = 4 << 20

type Element []*typesv1.LabelPair

func Validate(req *queryv1.InvokeRequest) error {
	if req == nil || len(req.Query) != 1 || req.Query[0] == nil {
		return status.Error(codes.InvalidArgument, "InvokeStream requires exactly one metadata query")
	}
	q := req.Query[0]
	switch q.QueryType {
	case queryv1.QueryType_QUERY_SERIES_LABELS:
		if q.SeriesLabels != nil {
			return nil
		}
	case queryv1.QueryType_QUERY_LABEL_NAMES:
		if q.LabelNames != nil {
			return nil
		}
	case queryv1.QueryType_QUERY_LABEL_VALUES:
		if q.LabelValues != nil {
			return nil
		}
	}
	return status.Error(codes.InvalidArgument, "InvokeStream supports only Series, LabelNames, and LabelValues")
}

// Encoder owns its chunks; Send must not mutate them. Each Add is atomic: an
// element is never split across chunks. Size accounting includes table overhead.
type Encoder struct {
	kind                                        queryv1.QueryType
	limit                                       int
	send                                        func(*queryv1.InvokeStreamResponse) error
	chunk                                       *queryv1.InvokeStreamResponse
	table                                       *attributetable.Table
	tableBytes, seriesBytes, refsBytes, entries int
}

func NewEncoder(kind queryv1.QueryType, limit int, send func(*queryv1.InvokeStreamResponse) error) *Encoder {
	e := &Encoder{kind: kind, limit: limit, send: send}
	e.reset()
	return e
}
func (e *Encoder) reset() {
	e.chunk = &queryv1.InvokeStreamResponse{QueryType: e.kind}
	e.table = attributetable.New()
	e.tableBytes, e.seriesBytes, e.refsBytes, e.entries = 0, 0, 0, 0
}
func (e *Encoder) size() int {
	n := 1 + protowire.SizeVarint(uint64(e.kind))
	n += 1 + protowire.SizeBytes(e.tableBytes)
	n += e.seriesBytes
	if e.refsBytes > 0 {
		n += 1 + protowire.SizeBytes(e.refsBytes)
	}
	return n
}
func (e *Encoder) Add(element Element) error {
	refs := make([]int64, 0, len(element))
	// Build a candidate transactionally, tracking the table's incremental wire size.
	oldEntries := e.entries
	oldTableBytes := e.tableBytes
	for _, p := range element {
		if p == nil {
			return errors.New("nil metadata label")
		}
		ref := e.table.LookupOrAdd(attributetable.Key{Key: unique.Make(p.Name), Value: unique.Make(p.Value)})
		if ref == int64(e.entries) {
			e.entries++
			e.tableBytes += 1 + protowire.SizeBytes(len(p.Name)) + 1 + protowire.SizeBytes(len(p.Value))
		}
		refs = append(refs, ref)
	}
	oldSeries, oldRefs := e.seriesBytes, e.refsBytes
	var set *queryv1.MetadataLabelSet
	if e.kind == queryv1.QueryType_QUERY_SERIES_LABELS {
		set = &queryv1.MetadataLabelSet{AttributeRefs: refs}
		e.seriesBytes += 1 + protowire.SizeBytes(set.SizeVT())
	} else {
		if len(refs) != 1 {
			return errors.New("string metadata element must have one entry")
		}
		e.refsBytes += protowire.SizeVarint(uint64(refs[0]))
	}
	if e.size() > e.limit {
		// The table includes candidate entries; trim it to the prior chunk before sending.
		table := e.table.Build(nil)
		table.Keys, table.Values = table.Keys[:oldEntries], table.Values[:oldEntries]
		e.chunk.AttributeTable = table
		e.tableBytes, e.seriesBytes, e.refsBytes = oldTableBytes, oldSeries, oldRefs
		if len(e.chunk.Series) == 0 && len(e.chunk.AttributeRefs) == 0 {
			return status.Error(codes.ResourceExhausted, "metadata element exceeds chunk byte limit")
		}
		if err := e.emit(); err != nil {
			return err
		}
		e.reset()
		return e.Add(element)
	}
	if set != nil {
		e.chunk.Series = append(e.chunk.Series, set)
	} else {
		e.chunk.AttributeRefs = append(e.chunk.AttributeRefs, refs[0])
	}
	return nil
}
func (e *Encoder) emit() error {
	if e.chunk.SizeVT() > e.limit {
		return fmt.Errorf("metadata chunk exceeds limit: %d > %d", e.chunk.SizeVT(), e.limit)
	}
	return e.send(e.chunk)
}
func (e *Encoder) Flush() error {
	if len(e.chunk.Series) == 0 && len(e.chunk.AttributeRefs) == 0 {
		return nil
	}
	e.chunk.AttributeTable = e.table.Build(nil)
	if err := e.emit(); err != nil {
		return err
	}
	e.reset()
	return nil
}

// Decode rejects malformed tables rather than silently dropping bad references.
func Decode(c *queryv1.InvokeStreamResponse, visit func(Element) error) error {
	if c == nil {
		return errors.New("nil metadata chunk")
	}
	t := c.AttributeTable
	if t == nil {
		t = &queryv1.AttributeTable{}
	}
	if len(t.Keys) != len(t.Values) {
		return errors.New("metadata table lengths differ")
	}
	resolve := func(refs []int64) (Element, error) {
		out := make(Element, 0, len(refs))
		for _, r := range refs {
			if r < 0 || r >= int64(len(t.Keys)) {
				return nil, errors.New("invalid metadata reference")
			}
			out = append(out, &typesv1.LabelPair{Name: t.Keys[r], Value: t.Values[r]})
		}
		return out, nil
	}
	switch c.QueryType {
	case queryv1.QueryType_QUERY_SERIES_LABELS:
		if len(c.AttributeRefs) > 0 {
			return errors.New("unexpected string references")
		}
		for _, s := range c.Series {
			if s == nil {
				return errors.New("nil label set")
			}
			v, err := resolve(s.AttributeRefs)
			if err != nil {
				return err
			}
			if err = visit(v); err != nil {
				return err
			}
		}
	case queryv1.QueryType_QUERY_LABEL_NAMES, queryv1.QueryType_QUERY_LABEL_VALUES:
		if len(c.Series) > 0 {
			return errors.New("unexpected series")
		}
		for _, r := range c.AttributeRefs {
			v, err := resolve([]int64{r})
			if err != nil {
				return err
			}
			if err = visit(v); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported metadata chunk type")
	}
	return nil
}
