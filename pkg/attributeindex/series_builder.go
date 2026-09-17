package attributeindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	phlaremodel "github.com/grafana/pyroscope/v2/pkg/model"
)

var (
	// ErrSeriesBuilderClosed is returned when a builder is used after Close.
	ErrSeriesBuilderClosed = errors.New("attribute index series builder is closed")

	// ErrSeriesBuilderLimit is returned before retaining input that would exceed
	// a configured builder resource limit.
	ErrSeriesBuilderLimit = errors.New("attribute index series builder limit exceeded")
)

// BuilderLimits bounds the data retained by a SeriesBuilder. All limits must
// be positive. Limits are deliberately explicit: AttributeIndexV1 currently
// constructs its payload in memory, so callers must select limits appropriate
// for the block-writing task rather than accepting unbounded tenant input.
type BuilderLimits struct {
	// MaxEntities is the number of distinct complete label sets.
	MaxEntities uint32
	// MaxDatasetReferences is the number of distinct entity-to-dataset edges.
	MaxDatasetReferences uint64
	// MaxBuildBytes is an accounting limit for retained canonical entities,
	// lookup keys, and dataset references. It does not include the transient
	// encoder working set.
	MaxBuildBytes uint64
	// MaxOutputBytes is the largest encoded payload returned by Bytes or
	// written by WriteTo.
	MaxOutputBytes uint64
}

// DefaultBuilderLimits provides conservative process-local limits for normal
// block construction. Deployments that compact unusually large tenants should
// set task-level limits explicitly and benchmark them before enabling writes.
func DefaultBuilderLimits() BuilderLimits {
	return BuilderLimits{
		MaxEntities:          4_000_000,
		MaxDatasetReferences: 16_000_000,
		MaxBuildBytes:        512 << 20,
		MaxOutputBytes:       512 << 20,
	}
}

func (l BuilderLimits) valid() error {
	if l.MaxEntities == 0 {
		return fmt.Errorf("%w: max entities must be positive", ErrSeriesBuilderLimit)
	}
	if l.MaxDatasetReferences == 0 {
		return fmt.Errorf("%w: max dataset references must be positive", ErrSeriesBuilderLimit)
	}
	if l.MaxBuildBytes == 0 {
		return fmt.Errorf("%w: max build bytes must be positive", ErrSeriesBuilderLimit)
	}
	if l.MaxOutputBytes == 0 {
		return fmt.Errorf("%w: max output bytes must be positive", ErrSeriesBuilderLimit)
	}
	return nil
}

// SeriesBuilder adapts complete persisted series labels to AttributeIndexV1
// legacy string attributes. It snapshots labels at AddSeries time, uses full
// canonical entity equality (never a fingerprint) to deduplicate them, and
// unions all referenced real dataset positions.
//
// SeriesBuilder is not safe for concurrent use. Bytes and WriteTo close it
// automatically; call Close when abandoning a builder before encoding. Close
// releases all retained label data and is idempotent.
type SeriesBuilder struct {
	metadata Metadata
	limits   BuilderLimits

	entities    map[string][]seriesBuilderEntity
	entityCount uint32
	references  uint64
	buildBytes  uint64
	closed      bool
}

type seriesBuilderEntity struct {
	entity     Entity
	datasetIDs []uint32
}

// NewSeriesBuilder creates a bounded, dataset-aware AttributeIndexV1 builder.
// labels supplied to AddSeries must be the complete persisted labels for a
// series, including internal labels such as __profile_type__.
func NewSeriesBuilder(metadata Metadata, limits BuilderLimits) (*SeriesBuilder, error) {
	if err := metadata.valid(); err != nil {
		return nil, err
	}
	if metadata.TimeSemantics == TimeNativeExactActivity {
		return nil, fmt.Errorf("native exact activity requires activity pages, which AttributeIndexV1 writer does not implement yet")
	}
	if err := limits.valid(); err != nil {
		return nil, err
	}
	return &SeriesBuilder{
		metadata: metadata,
		limits:   limits,
		entities: make(map[string][]seriesBuilderEntity),
	}, nil
}

// AddSeries adds a complete persisted series label set for datasetID. It is a
// convenience form for callers without a cancellable build context.
func (b *SeriesBuilder) AddSeries(datasetID uint32, labels phlaremodel.LabelSet) error {
	return b.AddSeriesContext(context.Background(), datasetID, labels)
}

// AddSeriesContext adds a complete persisted series label set for datasetID.
// Label names and values are copied before this method returns, so callers may
// immediately reuse their label backing storage.
func (b *SeriesBuilder) AddSeriesContext(ctx context.Context, datasetID uint32, labels phlaremodel.LabelSet) error {
	if err := b.usable(ctx); err != nil {
		return err
	}
	if labels == nil {
		return fmt.Errorf("attribute index series labels must not be nil")
	}
	if labels.Len() > 1<<20 {
		return fmt.Errorf("%w: label count %d exceeds maximum %d", ErrSeriesBuilderLimit, labels.Len(), 1<<20)
	}

	attributes := make([]Attribute, labels.Len())
	var entityBytes uint64
	for i := range attributes {
		if i&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		name, value := labels.At(i)
		if name == "" {
			return fmt.Errorf("attribute name must not be empty")
		}
		// Account before copying an attacker-controlled label. The fixed part
		// covers Attribute, Key, Value, and the slice entry, conservatively.
		const attributeOverhead = 64
		labelBytes := uint64(len(name)) + uint64(len(value)) + attributeOverhead
		if labelBytes < uint64(len(name)) || entityBytes > math.MaxUint64-labelBytes {
			return fmt.Errorf("%w: label data size overflows", ErrSeriesBuilderLimit)
		}
		entityBytes += labelBytes
		attributes[i] = Attribute{
			Key:   Key{Scope: ScopeLegacy, Name: strings.Clone(name)},
			Value: StringValue(value),
		}
	}
	slices.SortFunc(attributes, func(a, c Attribute) int { return compareKey(a.Key, c.Key) })
	for i := range attributes {
		if i > 0 && compareKey(attributes[i-1].Key, attributes[i].Key) == 0 {
			return fmt.Errorf("duplicate attribute %d:%q", attributes[i].Key.Scope, attributes[i].Key.Name)
		}
	}
	entity := Entity{Attributes: attributes}
	key := canonicalEntityKey(entity)
	bucket := b.entities[key]
	for i := range bucket {
		if !equalEntity(bucket[i].entity, entity) {
			continue
		}
		pos, found := slices.BinarySearch(bucket[i].datasetIDs, datasetID)
		if found {
			return nil
		}
		if err := b.reserveReference(); err != nil {
			return err
		}
		bucket[i].datasetIDs = append(bucket[i].datasetIDs, 0)
		copy(bucket[i].datasetIDs[pos+1:], bucket[i].datasetIDs[pos:])
		bucket[i].datasetIDs[pos] = datasetID
		b.entities[key] = bucket
		return nil
	}
	if b.entityCount == b.limits.MaxEntities {
		return fmt.Errorf("%w: entity count reaches %d", ErrSeriesBuilderLimit, b.limits.MaxEntities)
	}
	keyBytes := uint64(len(key))
	if keyBytes > math.MaxUint64-entityBytes {
		return fmt.Errorf("%w: canonical entity key size overflows", ErrSeriesBuilderLimit)
	}
	if err := b.reserve(entityBytes+keyBytes, 1); err != nil {
		return err
	}
	b.entities[key] = append(bucket, seriesBuilderEntity{
		entity:     entity,
		datasetIDs: []uint32{datasetID},
	})
	b.entityCount++
	return nil
}

// Bytes encodes the accumulated series using ctx for cancellation checks while
// materializing the deterministic AttributeIndexV1 payload. It always closes
// the builder before returning, releasing retained input on both success and
// failure paths.
func (b *SeriesBuilder) Bytes(ctx context.Context) ([]byte, error) {
	defer b.Close()
	if err := b.usable(ctx); err != nil {
		return nil, err
	}
	writer, err := NewWriter(b.metadata)
	if err != nil {
		return nil, err
	}
	var n uint32
	for _, bucket := range b.entities {
		for _, entry := range bucket {
			if n&0x3ff == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if err := writer.AddEntityWithDatasets(entry.entity, entry.datasetIDs); err != nil {
				return nil, fmt.Errorf("adding attribute index entity: %w", err)
			}
			n++
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := writer.BytesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("encoding attribute index: %w", err)
	}
	if uint64(len(payload)) > b.limits.MaxOutputBytes {
		return nil, fmt.Errorf("%w: encoded output %d exceeds %d bytes", ErrSeriesBuilderLimit, len(payload), b.limits.MaxOutputBytes)
	}
	return payload, nil
}

// WriteTo encodes and writes the complete payload. It detects short writes and
// always closes the builder, including when encoding or writing fails.
func (b *SeriesBuilder) WriteTo(ctx context.Context, dst io.Writer) (n int64, err error) {
	if dst == nil {
		b.Close()
		return 0, fmt.Errorf("attribute index destination must not be nil")
	}
	defer b.Close()
	payload, err := b.Bytes(ctx)
	if err != nil {
		return 0, err
	}
	written, err := dst.Write(payload)
	if err != nil {
		return int64(written), err
	}
	if written != len(payload) {
		return int64(written), io.ErrShortWrite
	}
	return int64(written), nil
}

// Empty reports whether the builder has no series entities.
func (b *SeriesBuilder) Empty() bool { return b == nil || b.entityCount == 0 }

// Close releases retained labels, entity mappings, and canonical lookup keys.
// It is idempotent.
func (b *SeriesBuilder) Close() {
	if b == nil || b.closed {
		return
	}
	clear(b.entities)
	b.entities = nil
	b.entityCount = 0
	b.references = 0
	b.buildBytes = 0
	b.closed = true
}

func (b *SeriesBuilder) usable(ctx context.Context) error {
	if b == nil || b.closed {
		return ErrSeriesBuilderClosed
	}
	if ctx == nil {
		return fmt.Errorf("attribute index build context must not be nil")
	}
	return ctx.Err()
}

func (b *SeriesBuilder) reserve(entityBytes, references uint64) error {
	// A reference consumes four bytes on disk, but account sixteen bytes to
	// include slice capacity growth and map/slice bookkeeping while building.
	const referenceAccountingBytes = 16
	if references > math.MaxUint64/referenceAccountingBytes {
		return fmt.Errorf("%w: dataset reference size overflows", ErrSeriesBuilderLimit)
	}
	retainedBytes := entityBytes + references*referenceAccountingBytes
	if retainedBytes < entityBytes || retainedBytes > b.limits.MaxBuildBytes || b.buildBytes > b.limits.MaxBuildBytes-retainedBytes {
		return fmt.Errorf("%w: retained entity data would exceed %d bytes", ErrSeriesBuilderLimit, b.limits.MaxBuildBytes)
	}
	if references > b.limits.MaxDatasetReferences-b.references {
		return fmt.Errorf("%w: dataset reference count would exceed %d", ErrSeriesBuilderLimit, b.limits.MaxDatasetReferences)
	}
	b.buildBytes += retainedBytes
	b.references += references
	return nil
}

func (b *SeriesBuilder) reserveReference() error {
	return b.reserve(0, 1)
}

func canonicalEntityKey(entity Entity) string {
	key := make([]byte, 0)
	for _, attribute := range entity.Attributes {
		key = append(key, byte(attribute.Key.Scope))
		key = appendUvarint(key, uint64(len(attribute.Key.Name)))
		key = append(key, attribute.Key.Name...)
		key = append(key, byte(attribute.Value.Type))
		key = appendUvarint(key, uint64(len(attribute.Value.Data)))
		key = append(key, attribute.Value.Data...)
	}
	return string(key)
}

func equalEntity(a, b Entity) bool {
	if len(a.Attributes) != len(b.Attributes) {
		return false
	}
	for i := range a.Attributes {
		if compareKey(a.Attributes[i].Key, b.Attributes[i].Key) != 0 || !a.Attributes[i].Value.equal(b.Attributes[i].Value) {
			return false
		}
	}
	return true
}
