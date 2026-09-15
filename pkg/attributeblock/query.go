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
	entities, err := r.matchingEntities(ctx, matchers)
	if err != nil {
		return nil, err
	}
	keys := make([]Key, 0)
	for _, entity := range entities {
		for _, attribute := range entity.Attributes {
			keys = append(keys, attribute.Key)
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
	entities, err := r.matchingEntities(ctx, matchers)
	if err != nil {
		return nil, err
	}
	values := make([]Value, 0)
	for _, entity := range entities {
		if attribute, ok := findAttribute(entity.Attributes, key); ok {
			values = append(values, Value{Type: attribute.Value.Type, Data: slices.Clone(attribute.Value.Data)})
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
	entities, err := r.matchingEntities(ctx, matchers)
	if err != nil {
		return nil, err
	}
	result := make([]Entity, 0, len(entities))
	for _, entity := range entities {
		if projection == nil {
			result = append(result, cloneEntity(entity))
			continue
		}
		attributes := make([]Attribute, 0, len(projection))
		for _, key := range projection {
			if attribute, ok := findAttribute(entity.Attributes, key); ok {
				attributes = append(attributes, cloneAttribute(attribute))
			}
		}
		slices.SortFunc(attributes, func(a, b Attribute) int { return compareKey(a.Key, b.Key) })
		result = append(result, Entity{Attributes: attributes})
	}
	slices.SortFunc(result, compareEntity)
	return compactEntities(result), nil
}

func (r *Reader) matchingEntities(ctx context.Context, matchers []Matcher) ([]Entity, error) {
	compiled := make([]*regexp.Regexp, len(matchers))
	for i := range matchers {
		if err := matchers[i].valid(); err != nil {
			return nil, fmt.Errorf("invalid matcher %d: %w", i, err)
		}
		if matchers[i].Operator == MatchRegexp || matchers[i].Operator == MatchNotRegexp {
			compiled[i], _ = regexp.Compile("^(?:" + matchers[i].Regexp + ")$")
		}
	}
	entities, err := r.Entities(ctx)
	if err != nil {
		return nil, err
	}
	result := entities[:0]
	for _, entity := range entities {
		matched := true
		for i, matcher := range matchers {
			attribute, present := findAttribute(entity.Attributes, matcher.Key)
			if !matches(attribute.Value, present, matcher, compiled[i]) {
				matched = false
				break
			}
		}
		if matched {
			result = append(result, entity)
		}
	}
	return result, nil
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
