package attributeindex

import (
	"context"
	"fmt"
	"regexp"
	"slices"
)

// candidateIDsSelective is an optimized version of candidateIDs that only
// fetches the forward columns needed for filtering. Dictionaries and postings
// are still loaded entirely (single-page limitation), but column fetches are
// selective.
//
// This is phase B work: making queries selective. The returned columns map
// contains only the keys actually needed, not all keys.
func (r *Reader) candidateIDsSelective(ctx context.Context, matchers []Matcher, projectedKeys []Key) ([]bool, [][]Value, map[int][]uint32, error) {
	// Compile regex matchers
	compiled := make([]*regexp.Regexp, len(matchers))
	for i := range matchers {
		if err := matchers[i].valid(); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid matcher %d: %w", i, err)
		}
		if matchers[i].Operator == MatchRegexp || matchers[i].Operator == MatchNotRegexp {
			compiled[i], _ = regexp.Compile("^(?:" + matchers[i].Regexp + ")$")
		}
	}

	// Load dictionaries and postings (still single-page, can't be selective yet)
	dictionaries, err := r.Dictionaries(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	postings, err := r.Postings(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	// Identify which keys we actually need columns for:
	// 1. Keys referenced in matchers (for filtering)
	// 2. Keys in the projection (for materialization)
	neededKeys := make(map[Key]struct{})
	for _, matcher := range matchers {
		neededKeys[matcher.Key] = struct{}{}
	}
	if projectedKeys != nil {
		for _, key := range projectedKeys {
			neededKeys[key] = struct{}{}
		}
	} else {
		// nil projection means all keys
		for _, key := range r.keys {
			neededKeys[key] = struct{}{}
		}
	}

	// Map keys to their indices and build selective fetch list
	keyIndices := make(map[Key]int, len(neededKeys))
	fetchKeys := make([]Key, 0, len(neededKeys))
	for key := range neededKeys {
		if keyIndex, found := slices.BinarySearchFunc(r.keys, key, compareKey); found {
			keyIndices[key] = keyIndex
			fetchKeys = append(fetchKeys, key)
		}
	}
	slices.SortFunc(fetchKeys, compareKey)

	// Fetch only needed columns using ForwardColumnsFor
	fetchedColumns, err := r.ForwardColumnsFor(ctx, fetchKeys)
	if err != nil {
		return nil, nil, nil, err
	}

	// Build sparse column map (keyIndex -> column)
	columns := make(map[int][]uint32, len(fetchKeys))
	for i, key := range fetchKeys {
		if fetchedColumns[i] != nil {
			columns[keyIndices[key]] = fetchedColumns[i]
		}
	}

	// Use validated entity count from header
	entityCount := int(r.entityCount)
	candidate := make([]bool, entityCount)
	for i := range candidate {
		candidate[i] = true
	}

	// Apply matchers using postings (presence can use postings, no column needed)
	for i, matcher := range matchers {
		matcherCandidate := r.postingCandidateSelective(entityCount, matcher, compiled[i], dictionaries, postings, columns)
		for entityID := range candidate {
			candidate[entityID] = candidate[entityID] && matcherCandidate[entityID]
		}
	}

	return candidate, dictionaries, columns, nil
}

// postingCandidateSelective evaluates one matcher using postings and optionally columns.
// columns is a sparse map containing only the needed keys.
func (r *Reader) postingCandidateSelective(entityCount int, matcher Matcher, re *regexp.Regexp, dictionaries [][]Value, postings [][][]uint32, columns map[int][]uint32) []bool {
	result := make([]bool, entityCount)
	keyIndex, found := slices.BinarySearchFunc(r.keys, matcher.Key, compareKey)
	if !found {
		// Key doesn't exist - treat as absent for all entities
		return absentCandidate(result, matcher, re)
	}

	presence := postings[keyIndex][0]
	mark := func(posting []uint32) {
		for _, entityID := range posting {
			if int(entityID) < len(result) {
				result[entityID] = true
			}
		}
	}

	switch matcher.Operator {
	case MatchEqual:
		if valueID, ok := slices.BinarySearchFunc(dictionaries[keyIndex], matcher.Value, compareValue); ok {
			mark(postings[keyIndex][valueID+1])
		}
		if matcher.Value.Type == ValueString && len(matcher.Value.Data) == 0 {
			for entityID := range result {
				if !containsEntity(presence, uint32(entityID)) {
					result[entityID] = true
				}
			}
		}
	case MatchNotEqual:
		for entityID := range result {
			result[entityID] = true
		}
		if valueID, ok := slices.BinarySearchFunc(dictionaries[keyIndex], matcher.Value, compareValue); ok {
			for _, entityID := range postings[keyIndex][valueID+1] {
				result[entityID] = false
			}
		}
		if matcher.Value.Type == ValueString && len(matcher.Value.Data) == 0 {
			for entityID := range result {
				if !containsEntity(presence, uint32(entityID)) {
					result[entityID] = false
				}
			}
		}
	case MatchRegexp, MatchNotRegexp:
		for valueID, value := range dictionaries[keyIndex] {
			if value.Type == ValueString && re.Match(value.Data) {
				mark(postings[keyIndex][valueID+1])
			}
		}
		if re.Match(nil) {
			for entityID := range result {
				if !containsEntity(presence, uint32(entityID)) {
					result[entityID] = true
				}
			}
		}
		if matcher.Operator == MatchNotRegexp {
			for entityID := range result {
				result[entityID] = !result[entityID]
			}
		}
	}
	return result
}
