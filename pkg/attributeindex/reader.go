package attributeindex

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// RangeSource is satisfied by objstore.BucketReader. It is kept small so the
// format can be tested against an in-memory source without object-store setup.
type RangeSource interface {
	GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error)
}

// Reader owns query-lifetime decoded pages. Returned data is cloned, so callers
// cannot retain a view into a reader-owned buffer.
type Reader struct {
	source           RangeSource
	object           string
	size             int64
	entityCount      uint32
	requiredFeatures uint16
	metadata         Metadata
	keys             []Key
	pages            []pageDescriptor

	decoderMu sync.Mutex
	decoder   *zstd.Decoder

	mu              sync.Mutex
	entities        []Entity
	hasEntities     bool
	dictionaries    [][]Value
	hasDictionaries bool
	postings        [][][]uint32
	hasPostings     bool
	columns         [][]uint32
	hasColumns      bool
	datasetMappings [][]uint32
	hasDatasetMaps  bool
	closed          bool

	// Memory budget tracking for compressed response buffers, decoded pages,
	// and decoded data retained by caches.
	decodedBytes    int64
	maxDecodedBytes int64
}

// Open reads only the fixed header, footer, and root directory. It does not
// fetch entity data.
func Open(ctx context.Context, source RangeSource, object string, size int64) (*Reader, error) {
	if source == nil {
		return nil, fmt.Errorf("attribute index range source is nil")
	}
	if size < headerSize+footerSize {
		return nil, fmt.Errorf("attribute index is too small: %d", size)
	}
	header, err := readRange(ctx, source, object, 0, headerSize)
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if string(header[:8]) != string(headerMagic[:]) || binary.LittleEndian.Uint16(header[8:10]) != Version {
		return nil, fmt.Errorf("unsupported attribute index header")
	}
	footer, err := readRange(ctx, source, object, size-footerSize, footerSize)
	if err != nil {
		return nil, fmt.Errorf("reading footer: %w", err)
	}
	if string(footer[:8]) != string(footerMagic[:]) || binary.LittleEndian.Uint16(footer[8:10]) != Version {
		return nil, fmt.Errorf("unsupported attribute index footer")
	}
	requiredFeatures := binary.LittleEndian.Uint16(header[10:12])
	if requiredFeatures != binary.LittleEndian.Uint16(footer[10:12]) || requiredFeatures&featurePageCompression == 0 || requiredFeatures&^(featureDatasetMappings|featurePageCompression) != 0 {
		return nil, fmt.Errorf("unsupported attribute index required features %d", requiredFeatures)
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
	mappingPages := 0
	for _, page := range pages {
		if page.kind == pageDatasetMapping {
			mappingPages++
		}
	}
	if (requiredFeatures&featureDatasetMappings != 0 && mappingPages != 1) ||
		(requiredFeatures&featureDatasetMappings == 0 && mappingPages != 0) {
		return nil, fmt.Errorf("invalid dataset mapping feature and page combination")
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
	// Default memory budgets: 256MB for compressed response buffers, decoded
	// pages, and decoded data retained in query-lifetime caches.
	maxDecodedBytes := int64(256 << 20)
	decoder, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(uint64(maxPageLen)),
		zstd.WithDecoderMaxWindow(uint64(maxPageLen)),
		zstd.WithDecodeAllCapLimit(true),
	)
	if err != nil {
		return nil, fmt.Errorf("creating attribute index Zstd decoder: %w", err)
	}
	return &Reader{
		source:           source,
		object:           object,
		size:             size,
		entityCount:      entityCount,
		requiredFeatures: requiredFeatures,
		metadata:         metadata,
		keys:             keys,
		pages:            pages,
		decoder:          decoder,
		// Reserve the decoder's configured maximum scratch space as part of
		// this Reader's budget. It is deliberately conservative: Zstd may grow
		// scratch lazily, but it must never make the reader exceed its budget.
		decodedBytes:    maxPageLen,
		maxDecodedBytes: maxDecodedBytes,
	}, nil
}

func (r *Reader) Metadata() Metadata { return r.metadata }

// DatasetIDs applies all matchers to complete entities, then returns the
// sorted, unique real containing-block dataset positions they reference.
// Payloads without the required mapping feature return an explicit error.
func (r *Reader) DatasetIDs(ctx context.Context, matchers []Matcher) ([]uint32, error) {
	mappings, err := r.DatasetMappings(ctx)
	if err != nil {
		return nil, err
	}
	candidate, _, _, err := r.candidateIDsSelective(ctx, matchers, []Key{})
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, 0)
	for entityID, matches := range candidate {
		if matches {
			ids = append(ids, mappings[entityID]...)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// DatasetMappings returns the real containing-block dataset positions for each
// entity. It is intentionally separate from Entities so discovery results never
// expose dataset references as attributes.
func (r *Reader) DatasetMappings(ctx context.Context) ([][]uint32, error) {
	if r.requiredFeatures&featureDatasetMappings == 0 {
		return nil, ErrDatasetMappingsUnavailable
	}
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.hasDatasetMaps {
		mappings := cloneDatasetMappings(r.datasetMappings)
		r.mu.Unlock()
		return mappings, nil
	}
	r.mu.Unlock()
	pageIdx, ok := r.pageIndex(pageDatasetMapping, ^uint32(0))
	if !ok {
		return nil, fmt.Errorf("AttributeIndexV1 requires a dataset mapping page")
	}
	pageIndexes := []int{pageIdx}
	pages, err := r.fetchPages(ctx, pageIndexes)
	if err != nil {
		return nil, fmt.Errorf("fetching dataset mapping page: %w", err)
	}
	keepPages := false
	defer func() {
		if !keepPages {
			r.releasePageReservations(pageIndexes)
		}
	}()
	mappings, err := decodeDatasetMappings(pages[0], r.entityCount)
	if err != nil {
		return nil, fmt.Errorf("decoding dataset mapping page: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	if !r.hasDatasetMaps {
		r.datasetMappings, r.hasDatasetMaps = mappings, true
		keepPages = true
	}
	return cloneDatasetMappings(r.datasetMappings), nil
}

// Close releases all decoded query-lifetime pages. It does not close the
// RangeSource, whose lifetime belongs to the caller. Close is idempotent.
func (r *Reader) Close() error {
	r.mu.Lock()
	r.entities, r.hasEntities = nil, false
	r.dictionaries, r.hasDictionaries = nil, false
	r.postings, r.hasPostings = nil, false
	r.columns, r.hasColumns = nil, false
	r.datasetMappings, r.hasDatasetMaps = nil, false
	r.decodedBytes = 0
	r.closed = true
	r.mu.Unlock()

	// DecodeAll is serialized by decoderMu. Do not close its scratch buffers
	// while a page decode is using them.
	r.decoderMu.Lock()
	if r.decoder != nil {
		r.decoder.Close()
		r.decoder = nil
	}
	r.decoderMu.Unlock()
	return nil
}

func (r *Reader) ensureOpen() error {
	if r.closed {
		return fmt.Errorf("attribute index reader is closed")
	}
	return nil
}

// trackDecoded accounts for page and cache memory against the reader budget.
// Call with negative bytes to release. Must be called with mu held.
func (r *Reader) trackDecoded(bytes int64) error {
	if bytes < 0 {
		if -bytes > r.decodedBytes {
			return fmt.Errorf("decoded memory accounting underflow")
		}
		r.decodedBytes += bytes
		return nil
	}
	if bytes > r.maxDecodedBytes-r.decodedBytes {
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
		return nil, fmt.Errorf("AttributeIndexV1 is missing its entity page")
	}
	pageIndexes := []int{pageIdx}
	pageBuffers, err := r.fetchPages(ctx, pageIndexes)
	if err != nil {
		return nil, fmt.Errorf("fetching entity page: %w", err)
	}
	keepPages := false
	defer func() {
		if !keepPages {
			r.releasePageReservations(pageIndexes)
		}
	}()
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
		keepPages = true
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
		return nil, fmt.Errorf("AttributeIndexV1 is missing its dictionary page")
	}
	pageIndexes := []int{pageIdx}
	pageBuffers, err := r.fetchPages(ctx, pageIndexes)
	if err != nil {
		return nil, fmt.Errorf("fetching dictionary page: %w", err)
	}
	keepPages := false
	defer func() {
		if !keepPages {
			r.releasePageReservations(pageIndexes)
		}
	}()
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
		keepPages = true
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
		return nil, fmt.Errorf("AttributeIndexV1 is missing its postings page")
	}
	pageIndexes := []int{pageIdx}
	pageBuffers, err := r.fetchPages(ctx, pageIndexes)
	if err != nil {
		return nil, fmt.Errorf("fetching postings page: %w", err)
	}
	keepPages := false
	defer func() {
		if !keepPages {
			r.releasePageReservations(pageIndexes)
		}
	}()
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
		keepPages = true
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
	keepPages := false
	defer func() {
		if !keepPages {
			r.releasePageReservations(columnPageIndexes)
		}
	}()

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
			return nil, fmt.Errorf("AttributeIndexV1 is missing forward column %d", i)
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
		keepPages = true
	}
	columns = cloneColumns(r.columns)
	r.mu.Unlock()
	return columns, nil
}

// ForwardColumnsFor reads only the requested scoped-key pages. It is the
// selective counterpart to ForwardColumns; callers receive columns in key order.
func (r *Reader) ForwardColumnsFor(ctx context.Context, keys []Key) ([][]uint32, error) {
	// A previous full-column query has already paid to decode these pages.
	// Reuse it rather than issuing another ranged read and decompression.
	r.mu.Lock()
	if err := r.ensureOpen(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.hasColumns {
		result := make([][]uint32, len(keys))
		for i, key := range keys {
			if keyID, found := slices.BinarySearchFunc(r.keys, key, compareKey); found {
				result[i] = slices.Clone(r.columns[keyID])
			}
		}
		r.mu.Unlock()
		return result, nil
	}
	r.mu.Unlock()

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
	defer r.releasePageReservations(pageIndexes)

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

// fetchPages fetches and decompresses pages independently. It retains a
// decoded-page reservation for every returned buffer; callers must either turn
// that reservation into a cache entry or release it with
// releasePageReservations.
func (r *Reader) fetchPages(ctx context.Context, pageIndexes []int) (result [][]byte, err error) {
	if len(pageIndexes) == 0 {
		return nil, nil
	}
	pages := make([]pageDescriptor, len(pageIndexes))
	for i, idx := range pageIndexes {
		if idx < 0 || idx >= len(r.pages) {
			return nil, fmt.Errorf("page index %d out of bounds", idx)
		}
		pages[i] = r.pages[idx]
	}
	ranges, err := PlanPageRanges(pages, RangePlanOptions{MaxGap: 64 << 10, MaxLength: 16 << 20})
	if err != nil {
		return nil, fmt.Errorf("planning page ranges: %w", err)
	}

	// Reserve compressed response buffers before issuing requests. This budget
	// is shared by all calls on this Reader, unlike FetchRanges' task-local
	// semaphore.
	var encodedBytes int64
	for _, range_ := range ranges {
		if range_.Length > r.maxDecodedBytes-encodedBytes {
			return nil, fmt.Errorf("compressed page ranges exceed memory budget %d", r.maxDecodedBytes)
		}
		encodedBytes += range_.Length
	}
	if err := r.reservePageMemory(encodedBytes); err != nil {
		return nil, err
	}
	reservedEncoded := encodedBytes
	reservedDecoded := int64(0)
	defer func() {
		if err != nil {
			r.releasePageMemory(reservedEncoded + reservedDecoded)
		}
	}()

	rangeBuffers, err := FetchRanges(ctx, r.source, r.object, ranges, FetchOptions{MaxConcurrent: 10, MaxBytesInFlight: 64 << 20})
	if err != nil {
		return nil, fmt.Errorf("fetching page ranges: %w", err)
	}
	result = make([][]byte, len(pageIndexes))
	for rangeIdx, rangeBuffer := range rangeBuffers {
		range_ := ranges[rangeIdx]
		for _, localPageIdx := range range_.PageIndexes {
			if localPageIdx < 0 || localPageIdx >= len(pageIndexes) {
				return nil, fmt.Errorf("page index %d out of bounds (have %d pages)", localPageIdx, len(pageIndexes))
			}
			originalPageIdx := pageIndexes[localPageIdx]
			page := r.pages[originalPageIdx]
			offsetInRange := page.offset - range_.Offset
			if offsetInRange < 0 || offsetInRange+int64(page.length) > int64(len(rangeBuffer)) {
				return nil, fmt.Errorf("page %d offset %d outside range buffer", originalPageIdx, offsetInRange)
			}
			stored := rangeBuffer[offsetInRange : offsetInRange+int64(page.length)]
			// The checksum is intentionally checked over the stored frame, before
			// it is handed to the decompressor.
			if checksum(stored) != page.crc32 {
				return nil, fmt.Errorf("page %d checksum mismatch", originalPageIdx)
			}
			if err := r.reservePageMemory(int64(page.decodedLength)); err != nil {
				return nil, fmt.Errorf("reserving decoded page %d: %w", originalPageIdx, err)
			}
			reservedDecoded += int64(page.decodedLength)
			decoded, err := r.decodePage(ctx, page, stored)
			if err != nil {
				return nil, fmt.Errorf("decompressing page %d: %w", originalPageIdx, err)
			}
			result[localPageIdx] = decoded
		}
		// All pages referencing this response buffer have been decoded. Let it
		// go before processing another coalesced range and release its budget.
		rangeBuffers[rangeIdx] = nil
		r.releasePageMemory(range_.Length)
		reservedEncoded -= range_.Length
	}
	return result, nil
}

func (r *Reader) decodePage(ctx context.Context, page pageDescriptor, stored []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if page.codec != pageCodecZstd {
		return nil, fmt.Errorf("unsupported page codec %d", page.codec)
	}
	r.decoderMu.Lock()
	defer r.decoderMu.Unlock()
	if r.decoder == nil {
		return nil, fmt.Errorf("attribute index reader is closed")
	}
	// DecodeAll's cap limit turns this descriptor-validated capacity into a
	// hard output bound instead of relying on a length check after allocation.
	decoded, err := r.decoder.DecodeAll(stored, make([]byte, 0, page.decodedLength))
	if err != nil {
		return nil, err
	}
	if len(decoded) != int(page.decodedLength) {
		return nil, fmt.Errorf("decoded length %d does not match descriptor %d", len(decoded), page.decodedLength)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (r *Reader) reservePageMemory(bytes int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return err
	}
	return r.trackDecoded(bytes)
}

func (r *Reader) releasePageMemory(bytes int64) {
	if bytes == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Close discards all reservations. A concurrent fetch can then finish and
	// release its local reservation harmlessly.
	if r.closed {
		return
	}
	_ = r.trackDecoded(-bytes)
}

func (r *Reader) releasePageReservations(pageIndexes []int) {
	var bytes int64
	for _, pageIndex := range pageIndexes {
		if pageIndex >= 0 && pageIndex < len(r.pages) {
			bytes += int64(r.pages[pageIndex].decodedLength)
		}
	}
	r.releasePageMemory(bytes)
}

func cloneDatasetMappings(mappings [][]uint32) [][]uint32 {
	result := make([][]uint32, len(mappings))
	for i := range mappings {
		result[i] = slices.Clone(mappings[i])
	}
	return result
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
