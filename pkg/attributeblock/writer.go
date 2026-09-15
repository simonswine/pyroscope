package attributeblock

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
)

// Writer builds an immutable AttributeBlockV1 object. Writer is not safe for
// concurrent use.
type Writer struct {
	metadata Metadata
	entities []Entity
}

func NewWriter(metadata Metadata) (*Writer, error) {
	if err := metadata.valid(); err != nil {
		return nil, err
	}
	// This writer has no activity pages yet. Refusing the exact mode prevents
	// it from manufacturing timestamps while converting legacy inputs.
	if metadata.TimeSemantics == TimeNativeExactActivity {
		return nil, fmt.Errorf("native exact activity requires activity pages, which AttributeBlockV1 writer does not implement yet")
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
	return nil
}

// Bytes returns a complete attributes.bin object. Identical writer input has a
// deterministic encoding. The entity payload is one independently verifiable
// page in this initial primitive; the directory permits row-group paging to be
// added without changing the footer contract.
func (w *Writer) Bytes() ([]byte, error) {
	entityPage, err := encodeEntities(w.entities)
	if err != nil {
		return nil, err
	}
	keys := w.keys()
	dictionaries, err := buildDictionaries(keys, w.entities)
	if err != nil {
		return nil, err
	}
	dictionaryPage := encodeDictionaries(dictionaries)
	postingsPage, err := encodePostings(keys, dictionaries, w.entities)
	if err != nil {
		return nil, err
	}
	if len(entityPage) > maxPageLen || len(dictionaryPage) > maxPageLen || len(postingsPage) > maxPageLen {
		return nil, fmt.Errorf("attribute page exceeds limit %d", maxPageLen)
	}
	object := make([]byte, headerSize, headerSize+len(entityPage)+len(dictionaryPage)+len(postingsPage)+512+footerSize)
	copy(object, headerMagic[:])
	binary.LittleEndian.PutUint16(object[8:10], Version)
	object = append(object, entityPage...)
	object = append(object, dictionaryPage...)
	object = append(object, postingsPage...)
	directoryOffset := int64(len(object))
	directory := encodeDirectory(w.metadata, keys, []pageDescriptor{
		{kind: pageEntity, offset: headerSize, length: uint32(len(entityPage)), crc32: checksum(entityPage)},
		{kind: pageDictionary, offset: headerSize + int64(len(entityPage)), length: uint32(len(dictionaryPage)), crc32: checksum(dictionaryPage)},
		{kind: pagePostings, offset: headerSize + int64(len(entityPage)+len(dictionaryPage)), length: uint32(len(postingsPage)), crc32: checksum(postingsPage)},
	})
	object = append(object, directory...)
	var footer [footerSize]byte
	copy(footer[:8], footerMagic[:])
	binary.LittleEndian.PutUint16(footer[8:10], Version)
	binary.LittleEndian.PutUint64(footer[12:20], uint64(directoryOffset))
	binary.LittleEndian.PutUint32(footer[20:24], uint32(len(directory)))
	binary.LittleEndian.PutUint32(footer[24:28], checksum(directory))
	object = append(object, footer[:]...)
	return object, nil
}

func (w *Writer) keys() []Key {
	keys := make([]Key, 0)
	for _, entity := range w.entities {
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
