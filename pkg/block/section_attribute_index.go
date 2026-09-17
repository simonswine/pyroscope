package block

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"

	metastorev1 "github.com/grafana/pyroscope/api/gen/proto/go/metastore/v1"
	"github.com/grafana/pyroscope/v2/pkg/attributeindex"
	"github.com/grafana/pyroscope/v2/pkg/block/metadata"
)

// openAttributeIndex opens the payload through an offset-bounded range source.
// Unlike the profile and TSDB sections, AttributeIndexV1 is page-oriented, so
// opening it reads only its header, footer, and root directory.
func openAttributeIndex(ctx context.Context, s *Dataset) (err error) {
	offset, size, err := s.attributeIndexRange()
	if err != nil {
		return err
	}
	if !metadata.DatasetHasLabel(s.meta, s.obj.meta.StringTable,
		metadata.LabelNameTenantDataset, metadata.LabelValueAttributeIndex) {
		return fmt.Errorf("attribute index dataset is missing %s=%q marker",
			metadata.LabelNameTenantDataset, metadata.LabelValueAttributeIndex)
	}

	reader, err := attributeindex.Open(ctx, attributeIndexRangeSource{
		object: s.obj,
		offset: offset,
		size:   size,
	}, s.obj.path, size)
	if err != nil {
		return fmt.Errorf("opening AttributeIndexV1: %w", err)
	}
	defer func() {
		if err != nil {
			_ = reader.Close()
		}
	}()
	if reader.Metadata().Tenant != s.tenant {
		return fmt.Errorf("attribute index tenant %q does not match dataset tenant %q",
			reader.Metadata().Tenant, s.tenant)
	}
	if err := validateAttributeIndexReferences(ctx, s.obj, s.tenant, reader); err != nil {
		return err
	}
	s.attributeIndex = reader
	return nil
}

// attributeIndexRange validates the single absolute payload range encoded by
// DatasetFormat2. Payload-internal offsets remain relative to this range.
func (s *Dataset) attributeIndexRange() (int64, int64, error) {
	if len(s.meta.TableOfContents) != 1 || s.meta.Size == 0 {
		return 0, 0, fmt.Errorf("invalid attribute index table of contents")
	}
	offset := s.meta.TableOfContents[0]
	if offset > math.MaxInt64 || s.meta.Size > math.MaxInt64 || offset > math.MaxInt64-s.meta.Size {
		return 0, 0, fmt.Errorf("attribute index range overflows int64")
	}
	end := offset + s.meta.Size
	if s.obj.meta.Size > 0 && end > s.obj.meta.Size {
		return 0, 0, fmt.Errorf("attribute index range %d:%d lies outside block size %d", offset, s.meta.Size, s.obj.meta.Size)
	}
	return int64(offset), int64(s.meta.Size), nil
}

// validateAttributeIndexReferences verifies that local attribute-index dataset
// IDs resolve only to real datasets of the index tenant in the complete block
// metadata. It deliberately reads the mapping page, not the whole payload.
func validateAttributeIndexReferences(ctx context.Context, obj *Object, tenant string, reader *attributeindex.Reader) error {
	md, err := obj.ReadMetadata(ctx)
	if err != nil {
		return fmt.Errorf("reading complete block metadata for attribute index: %w", err)
	}
	mappings, err := reader.DatasetMappings(ctx)
	if err != nil {
		return fmt.Errorf("reading attribute index dataset mappings: %w", err)
	}
	for entityID, datasetIDs := range mappings {
		for _, datasetID := range datasetIDs {
			if uint64(datasetID) >= uint64(len(md.Datasets)) {
				return fmt.Errorf("attribute index entity %d references dataset %d outside block metadata", entityID, datasetID)
			}
			dataset := md.Datasets[datasetID]
			if DatasetFormat(dataset.Format) != DatasetFormat0 || dataset.Name == 0 {
				return fmt.Errorf("attribute index entity %d references non-profile dataset %d", entityID, datasetID)
			}
			if dataset.Tenant <= 0 || int(dataset.Tenant) >= len(md.StringTable) || md.StringTable[dataset.Tenant] != tenant {
				return fmt.Errorf("attribute index entity %d references dataset %d outside tenant %q", entityID, datasetID, tenant)
			}
		}
	}
	return nil
}

// attributeIndexRangeSource translates payload-relative requests into bounded,
// absolute block.bin ranges. It never permits a reader to escape the embedded
// payload, even when its directory is corrupt.
type attributeIndexRangeSource struct {
	object *Object
	offset int64
	size   int64
}

func (s attributeIndexRangeSource) GetRange(ctx context.Context, object string, off, length int64) (io.ReadCloser, error) {
	if object != s.object.path {
		return nil, fmt.Errorf("unexpected attribute index object %q", object)
	}
	if off < 0 || length < 0 || off > s.size || length > s.size-off {
		return nil, fmt.Errorf("attribute index range %d:%d lies outside payload size %d", off, length, s.size)
	}
	absoluteOffset := s.offset + off
	if absoluteOffset < s.offset {
		return nil, fmt.Errorf("attribute index absolute range overflows")
	}
	if buf := s.object.buf; buf != nil {
		end := absoluteOffset + length
		if absoluteOffset < 0 || end < absoluteOffset || end > int64(len(buf.B)) {
			return nil, fmt.Errorf("attribute index range %d:%d lies outside loaded block", absoluteOffset, length)
		}
		return io.NopCloser(bytes.NewReader(buf.B[absoluteOffset:end])), nil
	}
	return s.object.storage.GetRange(ctx, s.object.path, absoluteOffset, length)
}

// NewAttributeIndexDataset returns the metadata entry for an embedded
// AttributeIndexV1 payload. Dataset IDs referenced by the payload are positions
// in the containing BlockMeta.Datasets array, never positions in a filtered
// metadata response.
func NewAttributeIndexDataset(tenant int32, minTime, maxTime int64, offset, size uint64, strings *metadata.StringTable) *metastorev1.Dataset {
	labels := metadata.NewLabelBuilder(strings).
		WithLabelSet(metadata.LabelNameTenantDataset, metadata.LabelValueAttributeIndex).
		Build()
	return &metastorev1.Dataset{
		Format:          uint32(DatasetFormat2),
		Tenant:          tenant,
		Name:            0,
		MinTime:         minTime,
		MaxTime:         maxTime,
		TableOfContents: []uint64{offset},
		Size:            size,
		Labels:          labels,
	}
}
