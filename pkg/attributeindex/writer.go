package attributeindex

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var attributeIndexEncoders = sync.Pool{
	New: func() any {
		encoder, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderConcurrency(1),
		)
		if err != nil {
			panic(fmt.Sprintf("creating AttributeIndexV1 Zstd encoder: %v", err))
		}
		return encoder
	},
}

// Writer builds an immutable AttributeIndexV1 object. Writer is not safe for
// concurrent use.
type Writer struct {
	metadata          Metadata
	entities          []Entity
	entityDatasetIDs  [][]uint32
	hasDatasetMapping bool
}

func NewWriter(metadata Metadata) (*Writer, error) {
	if err := metadata.valid(); err != nil {
		return nil, err
	}
	// This writer has no activity pages yet. Refusing the exact mode prevents
	// it from manufacturing timestamps while converting legacy inputs.
	if metadata.TimeSemantics == TimeNativeExactActivity {
		return nil, fmt.Errorf("native exact activity requires activity pages, which AttributeIndexV1 writer does not implement yet")
	}
	return &Writer{metadata: metadata}, nil
}

// AddEntity validates and copies a complete label set. Duplicate keys are
// rejected instead of picking an arbitrary value.
func (w *Writer) AddEntity(entity Entity) error {
	attributes := slices.Clone(entity.Attributes)
	slices.SortFunc(attributes, func(a, b Attribute) int { return compareKey(a.Key, b.Key) })
	for i := range attributes {
		if err := attributes[i].Key.valid(); err != nil {
			return err
		}
		if err := attributes[i].Value.valid(); err != nil {
			return err
		}
		attributes[i].Value.Data = slices.Clone(attributes[i].Value.Data)
		if i > 0 && compareKey(attributes[i-1].Key, attributes[i].Key) == 0 {
			return fmt.Errorf("duplicate attribute %d:%q", attributes[i].Key.Scope, attributes[i].Key.Name)
		}
	}
	w.entities = append(w.entities, Entity{Attributes: attributes})
	w.entityDatasetIDs = append(w.entityDatasetIDs, nil)
	return nil
}

// AddEntityWithDatasets associates a complete entity with the real dataset
// positions containing it. References must be sorted and unique on disk;
// callers may supply them in any order.
func (w *Writer) AddEntityWithDatasets(entity Entity, datasetIDs []uint32) error {
	if len(datasetIDs) == 0 {
		return fmt.Errorf("attribute index entity must reference at least one dataset")
	}
	if err := w.AddEntity(entity); err != nil {
		return err
	}
	ids := slices.Clone(datasetIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	w.entityDatasetIDs[len(w.entityDatasetIDs)-1] = ids
	w.hasDatasetMapping = true
	return nil
}

// Bytes returns a complete attribute-index payload. Identical writer input has a
// deterministic encoding. The entity payload is one independently verifiable
// page in this initial primitive; the directory permits row-group paging to be
// added without changing the footer contract.
func (w *Writer) Bytes() ([]byte, error) {
	entities, mappings, err := w.entitiesForEncoding()
	if err != nil {
		return nil, err
	}
	entityPage, err := encodeEntities(entities)
	if err != nil {
		return nil, err
	}
	keys := keysForEntities(entities)
	dictionaries, err := buildDictionaries(keys, entities)
	if err != nil {
		return nil, err
	}
	postingsPage, err := encodePostings(keys, dictionaries, entities)
	if err != nil {
		return nil, err
	}
	columnPages, err := encodeForwardColumns(keys, dictionaries, entities)
	if err != nil {
		return nil, err
	}
	var mappingPage []byte
	if w.hasDatasetMapping {
		mappingPage, err = encodeDatasetMappings(mappings)
		if err != nil {
			return nil, err
		}
	}
	features := featurePageCompression
	if w.hasDatasetMapping {
		features |= featureDatasetMappings
	}
	object := make([]byte, headerSize)
	copy(object, headerMagic[:])
	binary.LittleEndian.PutUint16(object[8:10], Version)
	binary.LittleEndian.PutUint16(object[10:12], features)
	binary.LittleEndian.PutUint32(object[12:16], uint32(len(entities)))
	pages := make([]pageDescriptor, 0, len(columnPages)+4)
	encoder := attributeIndexEncoders.Get().(*zstd.Encoder)
	defer attributeIndexEncoders.Put(encoder)
	appendPage := func(kind pageKind, keyID uint32, data []byte) error {
		if len(data) > maxPageLen {
			return fmt.Errorf("attribute page decoded length %d exceeds limit %d", len(data), maxPageLen)
		}
		// Every data page is a self-contained frame. In particular, no frame
		// inherits a dictionary or state from a preceding page.
		encoded := encoder.EncodeAll(data, nil)
		if len(encoded) == 0 || len(encoded) > maxPageLen {
			return fmt.Errorf("attribute page encoded length %d exceeds limit %d", len(encoded), maxPageLen)
		}
		pages = append(pages, pageDescriptor{
			kind: kind, codec: pageCodecZstd, keyID: keyID, offset: int64(len(object)),
			length: uint32(len(encoded)), decodedLength: uint32(len(data)), crc32: checksum(encoded),
		})
		object = append(object, encoded...)
		return nil
	}
	if err := appendPage(pageEntity, ^uint32(0), entityPage); err != nil {
		return nil, err
	}
	if err := appendPage(pageDictionary, ^uint32(0), encodeDictionaries(dictionaries)); err != nil {
		return nil, err
	}
	if err := appendPage(pagePostings, ^uint32(0), postingsPage); err != nil {
		return nil, err
	}
	for i, page := range columnPages {
		if err := appendPage(pageForwardColumn, uint32(i), page); err != nil {
			return nil, err
		}
	}
	if w.hasDatasetMapping {
		if err := appendPage(pageDatasetMapping, ^uint32(0), mappingPage); err != nil {
			return nil, err
		}
	}
	directoryOffset := int64(len(object))
	directory := encodeDirectory(w.metadata, keys, pages)
	object = append(object, directory...)
	var footer [footerSize]byte
	copy(footer[:8], footerMagic[:])
	binary.LittleEndian.PutUint16(footer[8:10], Version)
	binary.LittleEndian.PutUint16(footer[10:12], features)
	binary.LittleEndian.PutUint64(footer[12:20], uint64(directoryOffset))
	binary.LittleEndian.PutUint32(footer[20:24], uint32(len(directory)))
	binary.LittleEndian.PutUint32(footer[24:28], checksum(directory))
	return append(object, footer[:]...), nil
}

func (w *Writer) entitiesForEncoding() ([]Entity, [][]uint32, error) {
	if !w.hasDatasetMapping {
		return w.entities, nil, nil
	}
	type entry struct {
		entity Entity
		ids    []uint32
	}
	entries := make([]entry, len(w.entities))
	for i := range w.entities {
		if len(w.entityDatasetIDs[i]) == 0 {
			return nil, nil, fmt.Errorf("entity %d is missing dataset mappings", i)
		}
		entries[i] = entry{entity: w.entities[i], ids: w.entityDatasetIDs[i]}
	}
	slices.SortFunc(entries, func(a, b entry) int { return compareEntity(a.entity, b.entity) })
	entities := make([]Entity, 0, len(entries))
	mappings := make([][]uint32, 0, len(entries))
	for _, entry := range entries {
		if len(entities) > 0 && compareEntity(entities[len(entities)-1], entry.entity) == 0 {
			last := len(mappings) - 1
			mappings[last] = append(mappings[last], entry.ids...)
			continue
		}
		entities = append(entities, entry.entity)
		mappings = append(mappings, slices.Clone(entry.ids))
	}
	for i := range mappings {
		slices.Sort(mappings[i])
		mappings[i] = slices.Compact(mappings[i])
	}
	return entities, mappings, nil
}

func keysForEntities(entities []Entity) []Key {
	keys := make([]Key, 0)
	for _, entity := range entities {
		for _, attribute := range entity.Attributes {
			keys = append(keys, attribute.Key)
		}
	}
	slices.SortFunc(keys, compareKey)
	return compactKeys(keys)
}

func buildDictionaries(keys []Key, entities []Entity) ([][]Value, error) {
	values := make([][]Value, len(keys))
	for _, entity := range entities {
		for _, attribute := range entity.Attributes {
			i, ok := slices.BinarySearchFunc(keys, attribute.Key, compareKey)
			if !ok {
				return nil, fmt.Errorf("attribute key missing from directory")
			}
			values[i] = append(values[i], attribute.Value)
		}
	}
	for i := range values {
		slices.SortFunc(values[i], compareValue)
		values[i] = compactValues(values[i])
	}
	return values, nil
}

func encodeDictionaries(values [][]Value) []byte {
	b := appendUvarint(nil, uint64(len(values)))
	for _, dictionary := range values {
		b = appendUvarint(b, uint64(len(dictionary)))
		for _, value := range dictionary {
			b = append(b, byte(value.Type))
			b = appendBytes(b, value.Data)
		}
	}
	return b
}

// encodePostings stores sorted entity IDs for presence and each dictionary
// value. Dictionary positions are local to this immutable block.
func encodePostings(keys []Key, dictionaries [][]Value, entities []Entity) ([]byte, error) {
	b := appendUvarint(nil, uint64(len(keys)))
	for keyIndex, key := range keys {
		presence := make([]uint32, 0)
		valuePostings := make([][]uint32, len(dictionaries[keyIndex]))
		for entityID, entity := range entities {
			attribute, found := findAttribute(entity.Attributes, key)
			if !found {
				continue
			}
			presence = append(presence, uint32(entityID))
			valueID, found := slices.BinarySearchFunc(dictionaries[keyIndex], attribute.Value, compareValue)
			if !found {
				return nil, fmt.Errorf("attribute value missing from dictionary")
			}
			valuePostings[valueID] = append(valuePostings[valueID], uint32(entityID))
		}
		b = appendPosting(b, presence)
		for _, posting := range valuePostings {
			b = appendPosting(b, posting)
		}
	}
	return b, nil
}

func appendPosting(b []byte, posting []uint32) []byte {
	b = appendUvarint(b, uint64(len(posting)))
	var previous uint32
	for i, entityID := range posting {
		delta := entityID
		if i > 0 {
			delta -= previous
		}
		b = appendUvarint(b, uint64(delta))
		previous = entityID
	}
	return b
}

func encodeDatasetMappings(mappings [][]uint32) ([]byte, error) {
	b := appendUvarint(nil, uint64(len(mappings)))
	for entityID, ids := range mappings {
		if len(ids) == 0 {
			return nil, fmt.Errorf("entity %d has no dataset mappings", entityID)
		}
		b = appendUvarint(b, uint64(len(ids)))
		var previous uint32
		for i, id := range ids {
			if i > 0 && id <= previous {
				return nil, fmt.Errorf("entity %d dataset mappings are not strictly sorted", entityID)
			}
			delta := id
			if i > 0 {
				delta -= previous
			}
			b = appendUvarint(b, uint64(delta))
			previous = id
		}
	}
	return b, nil
}

// encodeForwardColumns stores a dictionary-local value ID for every key and
// entity. Zero is ABSENT; nonzero IDs are the dictionary index plus one.
func encodeForwardColumns(keys []Key, dictionaries [][]Value, entities []Entity) ([][]byte, error) {
	pages := make([][]byte, len(keys))
	for keyIndex, key := range keys {
		page := appendUvarint(nil, uint64(len(entities)))
		for _, entity := range entities {
			attribute, found := findAttribute(entity.Attributes, key)
			if !found {
				page = appendUvarint(page, 0)
				continue
			}
			valueID, found := slices.BinarySearchFunc(dictionaries[keyIndex], attribute.Value, compareValue)
			if !found {
				return nil, fmt.Errorf("attribute value missing from dictionary")
			}
			page = appendUvarint(page, uint64(valueID+1))
		}
		pages[keyIndex] = page
	}
	return pages, nil
}

func decodeForwardColumn(page []byte, dictionary []Value) ([]uint32, error) {
	r := bytes.NewReader(page)
	entityCount, err := readUvarint(r)
	if err != nil || entityCount > 1<<32-1 {
		return nil, fmt.Errorf("invalid forward column entity count %d", entityCount)
	}
	column := make([]uint32, entityCount)
	for i := range column {
		valueID, err := readUvarint(r)
		if err != nil || valueID > uint64(len(dictionary)) {
			return nil, fmt.Errorf("invalid forward value ID for entity %d", i)
		}
		column[i] = uint32(valueID)
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing forward column page bytes")
	}
	return column, nil
}

func decodeForwardColumns(page []byte, dictionaries [][]Value) ([][]uint32, error) {
	r := bytes.NewReader(page)
	entityCount, err := readUvarint(r)
	if err != nil || entityCount > 1<<32-1 {
		return nil, fmt.Errorf("invalid forward column entity count %d", entityCount)
	}
	keyCount, err := readUvarint(r)
	if err != nil || keyCount != uint64(len(dictionaries)) {
		return nil, fmt.Errorf("invalid forward column key count %d", keyCount)
	}
	columns := make([][]uint32, len(dictionaries))
	for i := range columns {
		columns[i] = make([]uint32, entityCount)
		for j := range columns[i] {
			valueID, err := readUvarint(r)
			if err != nil || valueID > uint64(len(dictionaries[i])) {
				return nil, fmt.Errorf("invalid forward value ID for key %d entity %d", i, j)
			}
			columns[i][j] = uint32(valueID)
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing forward column page bytes")
	}
	return columns, nil
}

func decodeDatasetMappings(page []byte, entityCount uint32) ([][]uint32, error) {
	r := bytes.NewReader(page)
	count, err := readUvarint(r)
	if err != nil || count != uint64(entityCount) {
		return nil, fmt.Errorf("invalid dataset mapping entity count %d", count)
	}
	mappings := make([][]uint32, entityCount)
	for entityID := range mappings {
		count, err := readUvarint(r)
		if err != nil || count == 0 || count > 1<<32-1 {
			return nil, fmt.Errorf("invalid dataset reference count for entity %d: %d", entityID, count)
		}
		mappings[entityID] = make([]uint32, count)
		var previous uint32
		for i := range mappings[entityID] {
			delta, err := readUvarint(r)
			if err != nil || delta > uint64(^uint32(0)) || (i > 0 && uint64(previous)+delta > uint64(^uint32(0))) {
				return nil, fmt.Errorf("invalid dataset reference for entity %d", entityID)
			}
			id := uint32(delta)
			if i > 0 {
				id += previous
				if id <= previous {
					return nil, fmt.Errorf("dataset references for entity %d are not strictly sorted", entityID)
				}
			}
			mappings[entityID][i] = id
			previous = id
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing dataset mapping page bytes")
	}
	return mappings, nil
}

func decodePostings(page []byte, dictionaries [][]Value) ([][][]uint32, error) {
	r := bytes.NewReader(page)
	keyCount, err := readUvarint(r)
	if err != nil || keyCount != uint64(len(dictionaries)) {
		return nil, fmt.Errorf("invalid postings key count %d", keyCount)
	}
	postings := make([][][]uint32, len(dictionaries))
	for i := range postings {
		postings[i] = make([][]uint32, len(dictionaries[i])+1)
		for j := range postings[i] {
			posting, err := readPosting(r)
			if err != nil {
				return nil, fmt.Errorf("reading posting for key %d value %d: %w", i, j, err)
			}
			postings[i][j] = posting
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing postings page bytes")
	}
	return postings, nil
}

func readPosting(r *bytes.Reader) ([]uint32, error) {
	count, err := readUvarint(r)
	if err != nil || count > 1<<32-1 {
		return nil, fmt.Errorf("invalid posting length %d", count)
	}
	posting := make([]uint32, count)
	var previous uint32
	for i := range posting {
		delta, err := readUvarint(r)
		if err != nil || delta > uint64(^uint32(0)) {
			return nil, fmt.Errorf("invalid posting delta %d", delta)
		}
		entityID := uint32(delta)
		if i > 0 {
			if uint64(previous)+delta > uint64(^uint32(0)) {
				return nil, fmt.Errorf("posting entity ID overflow")
			}
			entityID += previous
		}
		if i > 0 && entityID <= previous {
			return nil, fmt.Errorf("posting is not strictly sorted")
		}
		posting[i] = entityID
		previous = entityID
	}
	return posting, nil
}

func encodeEntities(entities []Entity) ([]byte, error) {
	if len(entities) > 1<<32-1 {
		return nil, fmt.Errorf("entity count %d exceeds uint32 limit", len(entities))
	}
	b := appendUvarint(nil, uint64(len(entities)))
	for _, entity := range entities {
		b = appendUvarint(b, uint64(len(entity.Attributes)))
		for _, attribute := range entity.Attributes {
			b = append(b, byte(attribute.Key.Scope))
			b = appendString(b, attribute.Key.Name)
			b = append(b, byte(attribute.Value.Type))
			b = appendBytes(b, attribute.Value.Data)
		}
	}
	return b, nil
}

func decodeDictionaries(page []byte, keys []Key) ([][]Value, error) {
	r := bytes.NewReader(page)
	keyCount, err := readUvarint(r)
	if err != nil || keyCount != uint64(len(keys)) {
		return nil, fmt.Errorf("invalid dictionary key count %d", keyCount)
	}
	values := make([][]Value, len(keys))
	for i := range values {
		count, err := readUvarint(r)
		if err != nil || count > 1<<32-1 {
			return nil, fmt.Errorf("invalid value count for key %d: %d", i, count)
		}
		values[i] = make([]Value, count)
		for j := range values[i] {
			typ, err := r.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("reading dictionary value type: %w", err)
			}
			data, err := readBytes(r, maxPageLen)
			if err != nil {
				return nil, fmt.Errorf("reading dictionary value: %w", err)
			}
			values[i][j] = Value{Type: ValueType(typ), Data: data}
			if err := values[i][j].valid(); err != nil || (j > 0 && compareValue(values[i][j-1], values[i][j]) >= 0) {
				return nil, fmt.Errorf("invalid dictionary value for key %d", i)
			}
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing dictionary page bytes")
	}
	return values, nil
}

func decodeEntities(page []byte) ([]Entity, error) {
	r := bytes.NewReader(page)
	count, err := readUvarint(r)
	if err != nil || count > 1<<32-1 {
		return nil, fmt.Errorf("invalid entity count %d", count)
	}
	entities := make([]Entity, count)
	for i := range entities {
		attributeCount, err := readUvarint(r)
		if err != nil || attributeCount > 1<<20 {
			return nil, fmt.Errorf("invalid attribute count for entity %d: %d", i, attributeCount)
		}
		entities[i].Attributes = make([]Attribute, attributeCount)
		for j := range entities[i].Attributes {
			scope, err := r.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("reading entity %d attribute %d scope: %w", i, j, err)
			}
			name, err := readString(r, 1<<20)
			if err != nil {
				return nil, fmt.Errorf("reading entity %d attribute %d name: %w", i, j, err)
			}
			valueType, err := r.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("reading entity %d attribute %d value type: %w", i, j, err)
			}
			data, err := readBytes(r, maxPageLen)
			if err != nil {
				return nil, fmt.Errorf("reading entity %d attribute %d value: %w", i, j, err)
			}
			attribute := Attribute{Key: Key{Scope: Scope(scope), Name: name}, Value: Value{Type: ValueType(valueType), Data: data}}
			if err := attribute.Key.valid(); err != nil {
				return nil, err
			}
			if err := attribute.Value.valid(); err != nil {
				return nil, err
			}
			if j > 0 && compareKey(entities[i].Attributes[j-1].Key, attribute.Key) >= 0 {
				return nil, fmt.Errorf("entity %d attributes are not strictly sorted", i)
			}
			entities[i].Attributes[j] = attribute
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing entity page bytes")
	}
	return entities, nil
}
