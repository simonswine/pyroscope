package attributeblock

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

var (
	headerMagic = [8]byte{'A', 'T', 'T', 'R', 'B', 'L', 'K', 1}
	footerMagic = [8]byte{'A', 'T', 'T', 'R', 'F', 'T', 'R', 1}
)

const (
	headerSize = 16
	footerSize = 32
	maxPageLen = 64 << 20
)

type pageDescriptor struct {
	offset int64
	length uint32
	crc32  uint32
}

func appendUvarint(dst []byte, n uint64) []byte {
	var b [binary.MaxVarintLen64]byte
	l := binary.PutUvarint(b[:], n)
	return append(dst, b[:l]...)
}

func appendBytes(dst []byte, value []byte) []byte {
	dst = appendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendString(dst []byte, value string) []byte { return appendBytes(dst, []byte(value)) }

func readUvarint(r *bytes.Reader) (uint64, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return 0, fmt.Errorf("reading varint: %w", err)
	}
	return n, nil
}

func readBytes(r *bytes.Reader, limit int) ([]byte, error) {
	n, err := readUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > uint64(limit) || n > uint64(r.Len()) {
		return nil, fmt.Errorf("invalid byte length %d", n)
	}
	value := make([]byte, n)
	if _, err := io.ReadFull(r, value); err != nil {
		return nil, fmt.Errorf("reading bytes: %w", err)
	}
	return value, nil
}

func readString(r *bytes.Reader, limit int) (string, error) {
	value, err := readBytes(r, limit)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func encodeDirectory(metadata Metadata, page pageDescriptor) []byte {
	b := make([]byte, 0, 64+len(metadata.Tenant)+len(metadata.EntityKind))
	b = append(b, byte(metadata.TimeSemantics))
	b = appendString(b, metadata.Tenant)
	b = appendString(b, metadata.EntityKind)
	b = appendUvarint(b, 1) // V1 has a single entity page; later versions page row groups.
	var fixed [16]byte
	binary.LittleEndian.PutUint64(fixed[0:8], uint64(page.offset))
	binary.LittleEndian.PutUint32(fixed[8:12], page.length)
	binary.LittleEndian.PutUint32(fixed[12:16], page.crc32)
	return append(b, fixed[:]...)
}

func decodeDirectory(b []byte) (Metadata, []pageDescriptor, error) {
	r := bytes.NewReader(b)
	semantics, err := r.ReadByte()
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("reading directory time semantics: %w", err)
	}
	tenant, err := readString(r, 1<<20)
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("reading directory tenant: %w", err)
	}
	kind, err := readString(r, 1024)
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("reading directory entity kind: %w", err)
	}
	count, err := readUvarint(r)
	if err != nil || count == 0 || count > 1<<20 {
		return Metadata{}, nil, fmt.Errorf("invalid page count %d", count)
	}
	pages := make([]pageDescriptor, count)
	for i := range pages {
		var fixed [16]byte
		if _, err := io.ReadFull(r, fixed[:]); err != nil {
			return Metadata{}, nil, fmt.Errorf("reading page descriptor: %w", err)
		}
		pages[i] = pageDescriptor{offset: int64(binary.LittleEndian.Uint64(fixed[0:8])), length: binary.LittleEndian.Uint32(fixed[8:12]), crc32: binary.LittleEndian.Uint32(fixed[12:16])}
		if pages[i].offset < headerSize || pages[i].length > maxPageLen {
			return Metadata{}, nil, fmt.Errorf("invalid page descriptor %d", i)
		}
	}
	if r.Len() != 0 {
		return Metadata{}, nil, fmt.Errorf("trailing directory bytes")
	}
	metadata := Metadata{Tenant: tenant, EntityKind: kind, TimeSemantics: TimeSemantics(semantics)}
	if err := metadata.valid(); err != nil {
		return Metadata{}, nil, err
	}
	return metadata, pages, nil
}

func checksum(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
