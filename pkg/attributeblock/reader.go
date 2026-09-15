package attributeblock

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// RangeSource is satisfied by objstore.BucketReader. It is kept small so the
// format can be tested against an in-memory source without object-store setup.
type RangeSource interface {
	GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error)
}

// Reader owns query-lifetime page buffers. Its returned entities own their
// value bytes, so Close may be called immediately after Entities returns.
type Reader struct {
	source   RangeSource
	object   string
	size     int64
	metadata Metadata
	pages    []pageDescriptor
}

// Open reads only the fixed header, footer, and root directory. It does not
// fetch entity data.
func Open(ctx context.Context, source RangeSource, object string, size int64) (*Reader, error) {
	if source == nil {
		return nil, fmt.Errorf("attribute block range source is nil")
	}
	if size < headerSize+footerSize {
		return nil, fmt.Errorf("attribute block is too small: %d", size)
	}
	header, err := readRange(ctx, source, object, 0, headerSize)
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if string(header[:8]) != string(headerMagic[:]) || binary.LittleEndian.Uint16(header[8:10]) != Version {
		return nil, fmt.Errorf("unsupported attribute block header")
	}
	footer, err := readRange(ctx, source, object, size-footerSize, footerSize)
	if err != nil {
		return nil, fmt.Errorf("reading footer: %w", err)
	}
	if string(footer[:8]) != string(footerMagic[:]) || binary.LittleEndian.Uint16(footer[8:10]) != Version {
		return nil, fmt.Errorf("unsupported attribute block footer")
	}
	directoryOffset := int64(binary.LittleEndian.Uint64(footer[12:20]))
	directoryLength := int64(binary.LittleEndian.Uint32(footer[20:24]))
	if directoryOffset < headerSize || directoryLength <= 0 || directoryOffset > size-footerSize-directoryLength {
		return nil, fmt.Errorf("invalid root directory range")
	}
	directory, err := readRange(ctx, source, object, directoryOffset, directoryLength)
	if err != nil {
		return nil, fmt.Errorf("reading root directory: %w", err)
	}
	if checksum(directory) != binary.LittleEndian.Uint32(footer[24:28]) {
		return nil, fmt.Errorf("root directory checksum mismatch")
	}
	metadata, pages, err := decodeDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("decoding root directory: %w", err)
	}
	for i, page := range pages {
		if int64(page.length) > size-page.offset || page.offset+int64(page.length) > directoryOffset {
			return nil, fmt.Errorf("page %d lies outside object data", i)
		}
	}
	return &Reader{source: source, object: object, size: size, metadata: metadata, pages: pages}, nil
}

func (r *Reader) Metadata() Metadata { return r.metadata }

// Entities fetches and validates the entity page. Future readers will select
// only the column/row-group pages needed by a query through this same path.
func (r *Reader) Entities(ctx context.Context) ([]Entity, error) {
	if len(r.pages) != 1 {
		return nil, fmt.Errorf("AttributeBlockV1 requires exactly one entity page")
	}
	page := r.pages[0]
	data, err := readRange(ctx, r.source, r.object, page.offset, int64(page.length))
	if err != nil {
		return nil, fmt.Errorf("reading entity page: %w", err)
	}
	if checksum(data) != page.crc32 {
		return nil, fmt.Errorf("entity page checksum mismatch")
	}
	entities, err := decodeEntities(data)
	if err != nil {
		return nil, fmt.Errorf("decoding entity page: %w", err)
	}
	return entities, nil
}

func readRange(ctx context.Context, source RangeSource, object string, off, length int64) ([]byte, error) {
	if off < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range %d:%d", off, length)
	}
	response, err := source.GetRange(ctx, object, off, length)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Close() }()
	data := make([]byte, length)
	if _, err := io.ReadFull(response, data); err != nil {
		return nil, fmt.Errorf("short range read at %d for %d bytes: %w", off, length, err)
	}
	var extra [1]byte
	if n, err := response.Read(extra[:]); n != 0 || (err != nil && err != io.EOF) {
		return nil, fmt.Errorf("range read at %d returned more than %d bytes", off, length)
	}
	return data, nil
}
