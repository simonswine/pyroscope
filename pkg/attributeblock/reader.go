package attributeblock

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"sync"
)

// RangeSource is satisfied by objstore.BucketReader. It is kept small so the
// format can be tested against an in-memory source without object-store setup.
type RangeSource interface {
	GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error)
}

// Reader owns query-lifetime decoded pages. Returned data is cloned, so callers
// cannot retain a view into a reader-owned buffer.
type Reader struct {
	source      RangeSource
	object      string
	size        int64
	entityCount uint32
	metadata    Metadata
	keys        []Key
	pages       []pageDescriptor

	mu              sync.Mutex
	entities        []Entity
	hasEntities     bool
	dictionaries    [][]Value
	hasDictionaries bool
	postings        [][][]uint32
	hasPostings     bool
	columns         [][]uint32
	hasColumns      bool
	closed          bool
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
	metadata, keys, pages, err := decodeDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("decoding root directory: %w", err)
	}
	for i, page := range pages {
		if int64(page.length) > size-page.offset || page.offset+int64(page.length) > directoryOffset {
			return nil, fmt.Errorf("page %d lies outside object data", i)
		}
	}
	entityCount := binary.LittleEndian.Uint32(header[12:16])
	if err := validateEntityCountBounds(entityCount); err != nil {
		return nil, err
	}
	if err := validateAllocationBounds(entityCount, len(keys)); err != nil {
		return nil, err
	}
	return &Reader{source: source, object: object, size: size, entityCount: entityCount, metadata: metadata, keys: keys, pages: pages}, nil
}

func (r *Reader) Metadata() Metadata { return r.metadata }

// Close releases all decoded query-lifetime pages. It does not close the
// RangeSource, whose lifetime belongs to the caller. Close is idempotent.
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entities, r.hasEntities = nil, false
	r.dictionaries, r.hasDictionaries = nil, false
	r.postings, r.hasPostings = nil, false
	r.columns, r.hasColumns = nil, false
	r.closed = true
	return nil
}

func (r *Reader) ensureOpen() error {
	if r.closed {
		return fmt.Errorf("attribute block reader is closed")
	}
	return nil
}

// Keys returns the scoped attribute-name directory without fetching data pages.
func (r *Reader) Keys() []Key { return slices.Clone(r.keys) }

// Entities fetches and validates the entity page.
func (r *Reader) Entities(ctx context.Context) ([]Entity, error) {
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.hasEntities {
		entities := cloneEntities(r.entities)
		r.mu.Unlock()
		return entities, nil
	}
	r.mu.Unlock()
	page, ok := r.page(pageEntity)
	if !ok {
		return nil, fmt.Errorf("AttributeBlockV1 is missing its entity page")
	}
	data, err := r.readPage(ctx, page, "entity")
	if err != nil {
		return nil, err
	}
	entities, err := decodeEntities(data)
	if err != nil {
		return nil, fmt.Errorf("decoding entity page: %w", err)
	}
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if !r.hasEntities {
		r.entities, r.hasEntities = entities, true
	}
	entities = cloneEntities(r.entities)
	r.mu.Unlock()
	return entities, nil
}

// Dictionaries fetches typed values independently of entity data. Dictionaries
// are aligned with Keys.
func (r *Reader) Dictionaries(ctx context.Context) ([][]Value, error) {
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.hasDictionaries {
		values := cloneDictionaries(r.dictionaries)
		r.mu.Unlock()
		return values, nil
	}
	r.mu.Unlock()
	page, ok := r.page(pageDictionary)
	if !ok {
		return nil, fmt.Errorf("AttributeBlockV1 is missing its dictionary page")
	}
	data, err := r.readPage(ctx, page, "dictionary")
	if err != nil {
		return nil, err
	}
	values, err := decodeDictionaries(data, r.keys)
	if err != nil {
		return nil, fmt.Errorf("decoding dictionary page: %w", err)
	}
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if !r.hasDictionaries {
		r.dictionaries, r.hasDictionaries = values, true
	}
	values = cloneDictionaries(r.dictionaries)
	r.mu.Unlock()
	return values, nil
}

// Postings returns per-key postings aligned with Keys and Dictionaries. The
// zeroth posting for every key is its presence posting.
func (r *Reader) Postings(ctx context.Context) ([][][]uint32, error) {
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.hasPostings {
		postings := clonePostings(r.postings)
		r.mu.Unlock()
		return postings, nil
	}
	r.mu.Unlock()
	dictionaries, err := r.Dictionaries(ctx)
	if err != nil {
		return nil, err
	}
	page, ok := r.page(pagePostings)
	if !ok {
		return nil, fmt.Errorf("AttributeBlockV1 is missing its postings page")
	}
	data, err := r.readPage(ctx, page, "postings")
	if err != nil {
		return nil, err
	}
	postings, err := decodePostings(data, dictionaries)
	if err != nil {
		return nil, fmt.Errorf("decoding postings page: %w", err)
	}
	// Validate postings IDs are within entity bounds
	for keyIndex, keyPostings := range postings {
		for postingIndex, posting := range keyPostings {
			for _, entityID := range posting {
				if entityID >= r.entityCount {
					return nil, fmt.Errorf("postings validation failed: key %d posting %d contains entity ID %d exceeding entity count %d", keyIndex, postingIndex, entityID, r.entityCount)
				}
			}
		}
	}
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if !r.hasPostings {
		r.postings, r.hasPostings = postings, true
	}
	postings = clonePostings(r.postings)
	r.mu.Unlock()
	return postings, nil
}

// ForwardColumns returns dictionary-local value references for each scoped key.
// A zero reference is ABSENT; n refers to Dictionaries()[key][n-1].
func (r *Reader) ForwardColumns(ctx context.Context) ([][]uint32, error) {
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.hasColumns {
		columns := cloneColumns(r.columns)
		r.mu.Unlock()
		return columns, nil
	}
	r.mu.Unlock()
	dictionaries, err := r.Dictionaries(ctx)
	if err != nil {
		return nil, err
	}
	columns := make([][]uint32, len(r.keys))
	for _, page := range r.pages {
		if page.kind != pageForwardColumn || page.keyID >= uint32(len(columns)) {
			continue
		}
		data, err := r.readPage(ctx, page, "forward column")
		if err != nil {
			return nil, err
		}
		column, err := decodeForwardColumn(data, dictionaries[page.keyID])
		if err != nil {
			return nil, fmt.Errorf("decoding forward column page: %w", err)
		}
		columns[page.keyID] = column
	}
	for i := range columns {
		if columns[i] == nil {
			return nil, fmt.Errorf("AttributeBlockV1 is missing forward column %d", i)
		}
	}
	// Validate cross-page consistency before caching
	if err := validatePageConsistency(r.entityCount, r.keys, dictionaries, nil, columns); err != nil {
		return nil, fmt.Errorf("forward column validation failed: %w", err)
	}
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if !r.hasColumns {
		r.columns, r.hasColumns = columns, true
	}
	columns = cloneColumns(r.columns)
	r.mu.Unlock()
	return columns, nil
}

// ForwardColumnsFor reads only the requested scoped-key pages. It is the
// selective counterpart to ForwardColumns; callers receive columns in key order.
func (r *Reader) ForwardColumnsFor(ctx context.Context, keys []Key) ([][]uint32, error) {
	dictionaries, err := r.Dictionaries(ctx)
	if err != nil {
		return nil, err
	}
	result := make([][]uint32, len(keys))
	for i, key := range keys {
		keyID, found := slices.BinarySearchFunc(r.keys, key, compareKey)
		if !found {
			continue
		}
		for _, page := range r.pages {
			if page.kind != pageForwardColumn || page.keyID != uint32(keyID) {
				continue
			}
			data, err := r.readPage(ctx, page, "forward column")
			if err != nil {
				return nil, err
			}
			result[i], err = decodeForwardColumn(data, dictionaries[keyID])
			if err != nil {
				return nil, fmt.Errorf("decoding forward column page: %w", err)
			}
			break
		}
	}
	return result, nil
}

func (r *Reader) readPage(ctx context.Context, page pageDescriptor, name string) ([]byte, error) {
	data, err := readRange(ctx, r.source, r.object, page.offset, int64(page.length))
	if err != nil {
		return nil, fmt.Errorf("reading %s page: %w", name, err)
	}
	if checksum(data) != page.crc32 {
		return nil, fmt.Errorf("%s page checksum mismatch", name)
	}
	return data, nil
}

func cloneEntities(entities []Entity) []Entity {
	result := make([]Entity, len(entities))
	for i := range entities {
		result[i] = cloneEntity(entities[i])
	}
	return result
}
func cloneDictionaries(dictionaries [][]Value) [][]Value {
	result := make([][]Value, len(dictionaries))
	for i := range dictionaries {
		result[i] = cloneValues(dictionaries[i])
	}
	return result
}
func clonePostings(postings [][][]uint32) [][][]uint32 {
	result := make([][][]uint32, len(postings))
	for i := range postings {
		result[i] = make([][]uint32, len(postings[i]))
		for j := range postings[i] {
			result[i][j] = slices.Clone(postings[i][j])
		}
	}
	return result
}
func cloneColumns(columns [][]uint32) [][]uint32 {
	result := make([][]uint32, len(columns))
	for i := range columns {
		result[i] = slices.Clone(columns[i])
	}
	return result
}

func (r *Reader) page(kind pageKind) (pageDescriptor, bool) {
	for _, page := range r.pages {
		if page.kind == kind {
			return page, true
		}
	}
	return pageDescriptor{}, false
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
