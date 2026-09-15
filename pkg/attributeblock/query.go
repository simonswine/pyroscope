package attributeblock

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
)

// MatchOperator describes a type-aware attribute predicate.
type MatchOperator uint8

const (
	MatchEqual MatchOperator = iota + 1
	MatchNotEqual
	MatchRegexp
	MatchNotRegexp
)

// Matcher applies to one scoped key. Regular expressions are anchored, matching
// Prometheus label-matcher semantics. Regex predicates only match string
// values; non-string values are not coerced.
type Matcher struct {
	Key      Key
	Operator MatchOperator
	Value    Value
	Regexp   string
}

func (m Matcher) valid() error {
	if err := m.Key.valid(); err != nil {
		return err
	}
	switch m.Operator {
	case MatchEqual, MatchNotEqual:
		if m.Regexp != "" {
			return fmt.Errorf("equality matcher must not have a regular expression")
		}
		return m.Value.valid()
	case MatchRegexp, MatchNotRegexp:
		if m.Value.Type != 0 || len(m.Value.Data) != 0 {
			return fmt.Errorf("regular expression matcher must not have a typed value")
		}
		_, err := regexp.Compile("^(?:" + m.Regexp + ")$")
		return err
	default:
		return fmt.Errorf("invalid matcher operator %d", m.Operator)
	}
}

// Names returns keys present on at least one entity satisfying all matchers.
func (r *Reader) Names(ctx context.Context, matchers []Matcher) ([]Key, error) {
	if len(matchers) == 0 {
		return r.Keys(), nil
	}
	// For Names, we need all columns to discover presence, so use the full path
	candidate, _, columns, err := r.candidateIDsSelective(ctx, matchers, nil)
	if err != nil {
		return nil, err
	}
	keys := make([]Key, 0)
	for keyID, column := range columns {
		for entityID, valueID := range column {
			if candidate[entityID] && valueID != 0 {
				keys = append(keys, r.keys[keyID])
				break
			}
		}
	}
	slices.SortFunc(keys, compareKey)
	return compactKeys(keys), nil
}

// Values returns typed values present for key on entities satisfying all
// matchers. The returned values are sorted by type and canonical bytes.
func (r *Reader) Values(ctx context.Context, key Key, matchers []Matcher) ([]Value, error) {
	if err := key.valid(); err != nil {
		return nil, err
	}
	if len(matchers) == 0 {
		i, found := slices.BinarySearchFunc(r.keys, key, compareKey)
		if !found {
			return nil, nil
		}
		dictionaries, err := r.Dictionaries(ctx)
		if err != nil {
			return nil, err
		}
		return cloneValues(dictionaries[i]), nil
	}
	// Use selective path: only fetch the target key's column plus matcher keys
	candidate, dictionaries, columns, err := r.candidateIDsSelective(ctx, matchers, []Key{key})
	if err != nil {
		return nil, err
	}
	keyID, found := slices.BinarySearchFunc(r.keys, key, compareKey)
	if !found {
		return nil, nil
	}
	column, hasColumn := columns[keyID]
	if !hasColumn {
		return nil, nil
	}
	values := make([]Value, 0)
	for entityID, valueID := range column {
		if candidate[entityID] && valueID != 0 {
			value := dictionaries[keyID][valueID-1]
			values = append(values, Value{Type: value.Type, Data: slices.Clone(value.Data)})
		}
	}
	slices.SortFunc(values, compareValue)
	return compactValues(values), nil
}

// Series applies matchers before projecting requested keys. A nil projection
// returns full entity label sets; an empty non-nil projection returns one empty
// set when at least one entity matches.
func (r *Reader) Series(ctx context.Context, matchers []Matcher, projection []Key) ([]Entity, error) {
	for _, key := range projection {
		if err := key.valid(); err != nil {
			return nil, err
		}
	}
	projectedKeys := projection
	if projectedKeys == nil {
		projectedKeys = r.keys
	}
	// Use selective query path that only fetches needed columns
	candidate, dictionaries, columns, err := r.candidateIDsSelective(ctx, matchers, projectedKeys)
	if err != nil {
		return nil, err
	}
	result := make([]Entity, 0)
	for entityID, selected := range candidate {
		if !selected {
			continue
		}
		attributes := make([]Attribute, 0, len(projectedKeys))
		for _, key := range projectedKeys {
			keyID, found := slices.BinarySearchFunc(r.keys, key, compareKey)
			if !found {
				continue
			}
			column, hasColumn := columns[keyID]
			if !hasColumn || column[entityID] == 0 {
				continue
			}
			value := dictionaries[keyID][column[entityID]-1]
			attributes = append(attributes, Attribute{Key: key, Value: Value{Type: value.Type, Data: slices.Clone(value.Data)}})
		}
		slices.SortFunc(attributes, func(a, b Attribute) int { return compareKey(a.Key, b.Key) })
		result = append(result, Entity{Attributes: attributes})
	}
	slices.SortFunc(result, compareEntity)
	return compactEntities(result), nil
}

// candidateIDs evaluates all predicates from postings. The returned columns
// are used directly for metadata materialization, avoiding entity-page reads.
func (r *Reader) candidateIDs(ctx context.Context, matchers []Matcher) ([]bool, [][]Value, [][]uint32, error) {
	compiled := make([]*regexp.Regexp, len(matchers))
	for i := range matchers {
		if err := matchers[i].valid(); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid matcher %d: %w", i, err)
		}
		if matchers[i].Operator == MatchRegexp || matchers[i].Operator == MatchNotRegexp {
			compiled[i], _ = regexp.Compile("^(?:" + matchers[i].Regexp + ")$")
		}
	}
	columns, err := r.ForwardColumns(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	dictionaries, err := r.Dictionaries(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	postings, err := r.Postings(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	// Use validated entity count from header instead of deriving from columns.
	// This prevents panics when columns have inconsistent lengths.
	entityCount := int(r.entityCount)
	candidate := make([]bool, entityCount)
	for i := range candidate {
		candidate[i] = true
	}
	for i, matcher := range matchers {
		matcherCandidate := postingCandidate(entityCount, r.keys, dictionaries, postings, matcher, compiled[i])
		for entityID := range candidate {
			candidate[entityID] = candidate[entityID] && matcherCandidate[entityID]
		}
	}
	return candidate, dictionaries, columns, nil
}

func postingCandidate(entityCount int, keys []Key, dictionaries [][]Value, postings [][][]uint32, matcher Matcher, re *regexp.Regexp) []bool {
	result := make([]bool, entityCount)
	keyIndex, found := slices.BinarySearchFunc(keys, matcher.Key, compareKey)
	if !found {
		// Every entity is absent, which is equivalent to an empty string for
		// legacy string matchers.
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

func absentCandidate(result []bool, matcher Matcher, re *regexp.Regexp) []bool {
	matches := matches(Value{}, false, matcher, re)
	for i := range result {
		result[i] = matches
	}
	return result
}

func containsEntity(posting []uint32, entityID uint32) bool {
	_, found := slices.BinarySearch(posting, entityID)
	return found
}

func matches(value Value, present bool, matcher Matcher, re *regexp.Regexp) bool {
	// Prometheus label matching treats a missing label as the empty string. Do
	// that only for string predicates; a missing typed value is never equal to
	// zero, false, or any other non-string value.
	if !present {
		switch matcher.Operator {
		case MatchEqual:
			return matcher.Value.Type == ValueString && len(matcher.Value.Data) == 0
		case MatchNotEqual:
			return matcher.Value.Type != ValueString || len(matcher.Value.Data) != 0
		case MatchRegexp:
			return re.Match(nil)
		case MatchNotRegexp:
			return !re.Match(nil)
		}
	}
	switch matcher.Operator {
	case MatchEqual:
		return value.equal(matcher.Value)
	case MatchNotEqual:
		return !value.equal(matcher.Value)
	case MatchRegexp:
		return value.Type == ValueString && re.Match(value.Data)
	case MatchNotRegexp:
		return value.Type != ValueString || !re.Match(value.Data)
	default:
		return false
	}
}

func findAttribute(attributes []Attribute, key Key) (Attribute, bool) {
	i, found := slices.BinarySearchFunc(attributes, key, func(attribute Attribute, key Key) int { return compareKey(attribute.Key, key) })
	if !found {
		return Attribute{}, false
	}
	return attributes[i], true
}

func compareValue(a, b Value) int {
	if a.Type != b.Type {
		return int(a.Type) - int(b.Type)
	}
	return bytes.Compare(a.Data, b.Data)
}

func compareEntity(a, b Entity) int {
	for i := range min(len(a.Attributes), len(b.Attributes)) {
		if n := compareKey(a.Attributes[i].Key, b.Attributes[i].Key); n != 0 {
			return n
		}
		if n := compareValue(a.Attributes[i].Value, b.Attributes[i].Value); n != 0 {
			return n
		}
	}
	return len(a.Attributes) - len(b.Attributes)
}

func compactKeys(keys []Key) []Key {
	return slices.CompactFunc(keys, func(a, b Key) bool { return compareKey(a, b) == 0 })
}
func compactValues(values []Value) []Value {
	return slices.CompactFunc(values, func(a, b Value) bool { return a.equal(b) })
}
func compactEntities(entities []Entity) []Entity {
	return slices.CompactFunc(entities, func(a, b Entity) bool { return compareEntity(a, b) == 0 })
}
func cloneValues(values []Value) []Value {
	result := make([]Value, len(values))
	for i := range values {
		result[i] = Value{Type: values[i].Type, Data: slices.Clone(values[i].Data)}
	}
	return result
}

func cloneAttribute(a Attribute) Attribute {
	return Attribute{Key: a.Key, Value: Value{Type: a.Value.Type, Data: slices.Clone(a.Value.Data)}}
}
func cloneEntity(e Entity) Entity {
	attributes := make([]Attribute, len(e.Attributes))
	for i := range e.Attributes {
		attributes[i] = cloneAttribute(e.Attributes[i])
	}
	return Entity{Attributes: attributes}
}
