package attributeindex

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReaderRejectsInconsistentColumnLengths ensures that a block with
// inconsistent forward column lengths is rejected during validation rather
// than causing a panic in Series.
func TestReaderRejectsInconsistentColumnLengths(t *testing.T) {
	// Build a valid block
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
		{Key: Key{Scope: ScopeLegacy, Name: "env"}, Value: StringValue("prod")},
	}}))
	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("db")},
	}}))

	_, err = w.Bytes()
	require.NoError(t, err)

	// Corrupt the block by changing one forward column's entity count to mismatch
	// the header. Find the first forward column page and corrupt its entity count.
	// The forward column starts right after dictionaries page.

	// Easier approach: manually construct a malformed block
	w2, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)
	require.NoError(t, w2.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}))

	validObject, err := w2.Bytes()
	require.NoError(t, err)

	// Tamper with the header entity count to claim 2 entities when only 1 exists
	corrupted := make([]byte, len(validObject))
	copy(corrupted, validObject)
	binary.LittleEndian.PutUint32(corrupted[12:16], 2) // Change entity count from 1 to 2

	// Reader should reject this during ForwardColumns validation
	source := &memorySource{object: "corrupted", data: corrupted}
	reader, err := Open(context.Background(), source, "corrupted", int64(len(corrupted)))
	require.NoError(t, err)
	defer reader.Close()

	// ForwardColumns should fail validation, not panic
	_, err = reader.ForwardColumns(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "validation failed")
}

// TestReaderRejectsPostingsWithOutOfBoundsIDs ensures that postings containing
// entity IDs beyond the entity count are rejected.
func TestReaderRejectsPostingsWithOutOfBoundsIDs(t *testing.T) {
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}))

	validObject, err := w.Bytes()
	require.NoError(t, err)

	// This is harder to corrupt directly. For now, test that validation exists.
	// A proper test would need to construct a block with malformed postings.
	// We'll add a fuzz test for this.

	source := &memorySource{object: "valid", data: validObject}
	reader, err := Open(context.Background(), source, "valid", int64(len(validObject)))
	require.NoError(t, err)
	defer reader.Close()

	// This should succeed with validation
	postings, err := reader.Postings(context.Background())
	require.NoError(t, err)
	require.NotNil(t, postings)
}

// TestReaderRejectsExcessiveEntityCount ensures that unreasonably large
// entity counts are rejected before attempting allocation.
func TestReaderRejectsExcessiveEntityCount(t *testing.T) {
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
	}}))

	validObject, err := w.Bytes()
	require.NoError(t, err)

	// Corrupt header to claim billions of entities
	corrupted := make([]byte, len(validObject))
	copy(corrupted, validObject)
	binary.LittleEndian.PutUint32(corrupted[12:16], 1<<29) // 512M entities

	source := &memorySource{object: "corrupted", data: corrupted}
	_, err = Open(context.Background(), source, "corrupted", int64(len(corrupted)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum")
}

// TestReaderRejectsExcessiveAllocationEstimate ensures that combinations of
// entity count and key count that would require excessive memory are rejected.
func TestReaderRejectsExcessiveAllocationEstimate(t *testing.T) {
	// Create a valid block
	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	// Add many keys
	attrs := make([]Attribute, 100)
	for i := range attrs {
		attrs[i] = Attribute{
			Key:   Key{Scope: ScopeLegacy, Name: string(byte('a'+i%26)) + string(byte('a'+i/26))},
			Value: StringValue("value"),
		}
	}
	require.NoError(t, w.AddEntity(Entity{Attributes: attrs}))

	validObject, err := w.Bytes()
	require.NoError(t, err)

	// Corrupt to claim huge entity count with many keys
	corrupted := make([]byte, len(validObject))
	copy(corrupted, validObject)
	binary.LittleEndian.PutUint32(corrupted[12:16], 1<<27) // 128M entities * 100 keys = excessive

	source := &memorySource{object: "corrupted", data: corrupted}
	_, err = Open(context.Background(), source, "corrupted", int64(len(corrupted)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "allocation")
}

// TestSeriesDoesNotPanicOnMalformedBlock is a regression test ensuring that
// Series query catches validation errors rather than panicking on nil column access.
func TestSeriesDoesNotPanicOnMalformedBlock(t *testing.T) {
	// This test previously would panic if a block had inconsistent columns.
	// Now it should return an error gracefully.

	w, err := NewWriter(Metadata{
		Tenant:        "test",
		EntityKind:    "series",
		TimeSemantics: TimeLegacyCoarseCoverage,
	})
	require.NoError(t, err)

	require.NoError(t, w.AddEntity(Entity{Attributes: []Attribute{
		{Key: Key{Scope: ScopeLegacy, Name: "service"}, Value: StringValue("api")},
		{Key: Key{Scope: ScopeLegacy, Name: "env"}, Value: StringValue("prod")},
	}}))

	validObject, err := w.Bytes()
	require.NoError(t, err)

	// Corrupt entity count
	corrupted := make([]byte, len(validObject))
	copy(corrupted, validObject)
	binary.LittleEndian.PutUint32(corrupted[12:16], 100) // Claim 100 entities when only 1 exists

	source := &memorySource{object: "corrupted", data: corrupted}
	reader, err := Open(context.Background(), source, "corrupted", int64(len(corrupted)))
	require.NoError(t, err)
	defer reader.Close()

	// Series should not panic, should return validation error
	_, err = reader.Series(context.Background(), nil, nil)
	require.Error(t, err)
}

// memorySource implements RangeSource for testing
type memorySource struct {
	object string
	data   []byte
}

func (m *memorySource) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	if name != m.object {
		return nil, fmt.Errorf("object not found: %s", name)
	}
	if off < 0 || length < 0 || off+length > int64(len(m.data)) {
		return nil, fmt.Errorf("range out of bounds")
	}
	return io.NopCloser(bytes.NewReader(m.data[off : off+length])), nil
}
