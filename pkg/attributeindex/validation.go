package attributeindex

import (
	"fmt"
)

// validatePageConsistency performs cross-page validation that cannot be done
// when reading individual pages. It checks that:
// - All forward columns have the same entity count
// - Entity count from header matches column lengths
// - Postings IDs are within entity bounds (if postings provided)
// - Dictionary value IDs referenced in postings exist
// Postings can be nil if only validating columns.
func validatePageConsistency(entityCount uint32, keys []Key, dictionaries [][]Value, postings [][][]uint32, columns [][]uint32) error {
	if len(keys) != len(dictionaries) {
		return fmt.Errorf("key count %d does not match dictionary count %d", len(keys), len(dictionaries))
	}
	if postings != nil && len(keys) != len(postings) {
		return fmt.Errorf("key count %d does not match postings count %d", len(keys), len(postings))
	}
	if len(keys) != len(columns) {
		return fmt.Errorf("key count %d does not match column count %d", len(keys), len(columns))
	}

	// Validate entity count consistency across all columns
	for i, column := range columns {
		if uint32(len(column)) != entityCount {
			return fmt.Errorf("forward column %d has length %d but header declares entity count %d", i, len(column), entityCount)
		}
	}

	// Validate postings structure and bounds (if provided)
	if postings != nil {
		for keyIndex := range keys {
			expectedPostingCount := len(dictionaries[keyIndex]) + 1 // presence + one per value
			if len(postings[keyIndex]) != expectedPostingCount {
				return fmt.Errorf("key %d: postings count %d does not match dictionary size %d + 1", keyIndex, len(postings[keyIndex]), len(dictionaries[keyIndex]))
			}

			// Validate all entity IDs in all postings are within bounds
			for postingIndex, posting := range postings[keyIndex] {
				for _, entityID := range posting {
					if entityID >= entityCount {
						return fmt.Errorf("key %d posting %d: entity ID %d exceeds entity count %d", keyIndex, postingIndex, entityID, entityCount)
					}
				}
			}
		}
	}

	// Validate forward column value IDs are within dictionary bounds
	for keyIndex := range keys {
		maxValueID := uint32(len(dictionaries[keyIndex]))
		for entityID, valueID := range columns[keyIndex] {
			if valueID > maxValueID {
				return fmt.Errorf("key %d entity %d: value ID %d exceeds dictionary size %d", keyIndex, entityID, valueID, maxValueID)
			}
		}
	}

	return nil
}

// validateEntityCountBounds checks that entity count is reasonable before
// allocating large slices.
const maxEntityCount = 1 << 28 // 256M entities, ~1GB minimum per uint32 slice

func validateEntityCountBounds(entityCount uint32) error {
	if entityCount > maxEntityCount {
		return fmt.Errorf("entity count %d exceeds maximum %d", entityCount, maxEntityCount)
	}
	return nil
}

// validateAllocationBounds estimates memory requirements before decoding pages
// to prevent excessive allocations from malformed data.
func validateAllocationBounds(entityCount uint32, keyCount int) error {
	// Each forward column needs entityCount * 4 bytes minimum
	// Each dictionary needs roughly entityCount/10 values in worst case (high estimate)
	// Postings similarly need entityCount * 4 bytes per key worst case

	if keyCount > 1<<20 {
		return fmt.Errorf("key count %d exceeds maximum 1M", keyCount)
	}

	// Rough estimate: each key needs ~3 * entityCount * 4 bytes (column + postings presence + average posting)
	// Plus dictionaries are typically much smaller
	estimatedBytes := uint64(keyCount) * uint64(entityCount) * 12
	const maxAllocation = 16 << 30 // 16GB budget

	if estimatedBytes > maxAllocation {
		return fmt.Errorf("estimated allocation %d bytes exceeds budget %d for %d keys and %d entities", estimatedBytes, maxAllocation, keyCount, entityCount)
	}

	return nil
}
