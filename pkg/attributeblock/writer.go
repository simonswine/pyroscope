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
	page, err := encodeEntities(w.entities)
	if err != nil {
		return nil, err
	}
	if len(page) > maxPageLen {
		return nil, fmt.Errorf("entity page is %d bytes, exceeds limit %d", len(page), maxPageLen)
	}
	object := make([]byte, headerSize, headerSize+len(page)+512+footerSize)
	copy(object, headerMagic[:])
	binary.LittleEndian.PutUint16(object[8:10], Version)
	object = append(object, page...)
	directoryOffset := int64(len(object))
	directory := encodeDirectory(w.metadata, pageDescriptor{offset: headerSize, length: uint32(len(page)), crc32: checksum(page)})
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
