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

	// Memory budget tracking for decoded/cached data
	decodedBytes    int64
	maxDecodedBytes int64
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
	// Default memory budgets: 256MB for decoded/cached data
	// This is separate from FetchRanges in-flight budget
	maxDecodedBytes := int64(256 << 20)
	return &Reader{
		source:          source,
		object:          object,
		size:            size,
		entityCount:     entityCount,
		metadata:        metadata,
		keys:            keys,
		pages:           pages,
		maxDecodedBytes: maxDecodedBytes,
	}, nil
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
	r.decodedBytes = 0
	r.closed = true
	return nil
}

func (r *Reader) ensureOpen() error {
	if r.closed {
		return fmt.Errorf("attribute block reader is closed")
	}
	return nil
}

// trackDecoded accounts for decoded page data against the memory budget.
// Call with negative bytes to release. Must be called with mu held.
func (r *Reader) trackDecoded(bytes int64) error {
	if r.decodedBytes+bytes > r.maxDecodedBytes {
		return fmt.Errorf("decoded memory %d + %d exceeds budget %d",
			r.decodedBytes, bytes, r.maxDecodedBytes)
	}
	r.decodedBytes += bytes
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
	pageIdx, ok := r.pageIndex(pageEntity, ^uint32(0))
	if !ok {
		return nil, fmt.Errorf("AttributeBlockV1 is missing its entity page")
	}
	pageBuffers, err := r.fetchPages(ctx, []int{pageIdx})
	if err != nil {
		return nil, fmt.Errorf("fetching entity page: %w", err)
	}
	entities, err := decodeEntities(pageBuffers[0])
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
	pageIdx, ok := r.pageIndex(pageDictionary, ^uint32(0))
	if !ok {
		return nil, fmt.Errorf("AttributeBlockV1 is missing its dictionary page")
	}
	pageBuffers, err := r.fetchPages(ctx, []int{pageIdx})
	if err != nil {
		return nil, fmt.Errorf("fetching dictionary page: %w", err)
	}
	values, err := decodeDictionaries(pageBuffers[0], r.keys)
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
	pageIdx, ok := r.pageIndex(pagePostings, ^uint32(0))
	if !ok {
		return nil, fmt.Errorf("AttributeBlockV1 is missing its postings page")
	}
	pageBuffers, err := r.fetchPages(ctx, []int{pageIdx})
	if err != nil {
		return nil, fmt.Errorf("fetching postings page: %w", err)
	}
	postings, err := decodePostings(pageBuffers[0], dictionaries)
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
	// Collect all forward column page indices
	columnPageIndexes := make([]int, 0, len(r.keys))
	for i, page := range r.pages {
		if page.kind == pageForwardColumn {
			columnPageIndexes = append(columnPageIndexes, i)
		}
	}

	// Fetch all column pages via bounded pipeline
	pageBuffers, err := r.fetchPages(ctx, columnPageIndexes)
	if err != nil {
		return nil, fmt.Errorf("fetching forward column pages: %w", err)
	}

	// Decode each column page
	columns := make([][]uint32, len(r.keys))
	for i, pageIdx := range columnPageIndexes {
		page := r.pages[pageIdx]
		if page.keyID >= uint32(len(columns)) {
			return nil, fmt.Errorf("forward column keyID %d exceeds key count %d", page.keyID, len(columns))
		}
		column, err := decodeForwardColumn(pageBuffers[i], dictionaries[page.keyID])
		if err != nil {
			return nil, fmt.Errorf("decoding forward column page: %w", err)
		}
		columns[page.keyID] = column
	}

	// Verify all columns are present
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

	// Map requested keys to their keyIDs and find corresponding page indices
	type keyMapping struct {
		resultIdx int
		keyID     int
		pageIdx   int
	}
	mappings := make([]keyMapping, 0, len(keys))
	for i, key := range keys {
		keyID, found := slices.BinarySearchFunc(r.keys, key, compareKey)
		if !found {
			continue
		}
		pageIdx, found := r.pageIndex(pageForwardColumn, uint32(keyID))
		if !found {
			continue
		}
		mappings = append(mappings, keyMapping{resultIdx: i, keyID: keyID, pageIdx: pageIdx})
	}

	if len(mappings) == 0 {
		return make([][]uint32, len(keys)), nil
	}

	// Fetch all needed pages via bounded pipeline
	pageIndexes := make([]int, len(mappings))
	for i, m := range mappings {
		pageIndexes[i] = m.pageIdx
	}
	pageBuffers, err := r.fetchPages(ctx, pageIndexes)
	if err != nil {
		return nil, fmt.Errorf("fetching forward column pages: %w", err)
	}

	// Decode and place each column in the result
	result := make([][]uint32, len(keys))
	for i, m := range mappings {
		column, err := decodeForwardColumn(pageBuffers[i], dictionaries[m.keyID])
		if err != nil {
			return nil, fmt.Errorf("decoding forward column page: %w", err)
		}
		// Validate column length matches header entity count
		if uint32(len(column)) != r.entityCount {
			return nil, fmt.Errorf("forward column for key %d:%s has length %d but header declares entity count %d",
				m.keyID, keys[m.resultIdx].Name, len(column), r.entityCount)
		}
		result[m.resultIdx] = column
	}

	return result, nil
}

// fetchPages fetches multiple pages using bounded FetchRanges. It plans ranges,
// executes bounded fetches, and validates checksums. Returned pages are in the
// same order as pageIndexes.
func (r *Reader) fetchPages(ctx context.Context, pageIndexes []int) ([][]byte, error) {
	if len(pageIndexes) == 0 {
		return nil, nil
	}

	// Build page descriptors for planning
	pages := make([]pageDescriptor, len(pageIndexes))
	for i, idx := range pageIndexes {
		if idx < 0 || idx >= len(r.pages) {
			return nil, fmt.Errorf("page index %d out of bounds", idx)
		}
		pages[i] = r.pages[idx]
	}

	// Plan coalesced ranges
	planOptions := RangePlanOptions{
		MaxGap:    64 << 10, // Coalesce pages within 64KB gaps
		MaxLength: 16 << 20, // Max 16MB per coalesced range
	}
	ranges, err := PlanPageRanges(pages, planOptions)
	if err != nil {
		return nil, fmt.Errorf("planning page ranges: %w", err)
	}

	// Fetch with bounded concurrency and memory
	fetchOptions := FetchOptions{
		MaxConcurrent:    10,
		MaxBytesInFlight: 64 << 20, // 64MB in-flight budget
	}
	rangeBuffers, err := FetchRanges(ctx, r.source, r.object, ranges, fetchOptions)
	if err != nil {
		return nil, fmt.Errorf("fetching page ranges: %w", err)
	}

	// Extract individual pages from coalesced buffers and validate checksums.
	// Note: range_.PageIndexes contains indices into the `pages` array we passed
	// to PlanPageRanges (0-based), not indices into r.pages.
	result := make([][]byte, len(pageIndexes))
	for rangeIdx, rangeBuffer := range rangeBuffers {
		range_ := ranges[rangeIdx]
		for _, localPageIdx := range range_.PageIndexes {
			// localPageIdx is an index into the pages array we built,
			// which corresponds to pageIndexes[localPageIdx]
			if localPageIdx >= len(pageIndexes) {
				return nil, fmt.Errorf("page index %d out of bounds (have %d pages)", localPageIdx, len(pageIndexes))
			}
			originalPageIdx := pageIndexes[localPageIdx]
			page := r.pages[originalPageIdx]

			// Extract page data from range buffer
			offsetInRange := page.offset - range_.Offset
			if offsetInRange < 0 || offsetInRange+int64(page.length) > int64(len(rangeBuffer)) {
				return nil, fmt.Errorf("page %d offset %d outside range buffer", originalPageIdx, offsetInRange)
			}
			pageData := rangeBuffer[offsetInRange : offsetInRange+int64(page.length)]

			// Validate checksum
			if checksum(pageData) != page.crc32 {
				return nil, fmt.Errorf("page %d checksum mismatch", originalPageIdx)
			}

			// Clone page data (caller owns the result)
			result[localPageIdx] = slices.Clone(pageData)
		}
	}

	return result, nil
}

// readPage is deprecated in favor of fetchPages but kept for backwards compatibility
// during migration. Direct use should be replaced with fetchPages.
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

// pageIndex finds the index of a page by kind and optional keyID.
// Pass ^uint32(0) for keyID to match any key.
func (r *Reader) pageIndex(kind pageKind, keyID uint32) (int, bool) {
	for i, page := range r.pages {
		if page.kind == kind && (keyID == ^uint32(0) || page.keyID == keyID) {
			return i, true
		}
	}
	return 0, false
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
