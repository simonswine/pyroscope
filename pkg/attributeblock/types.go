// Package attributeblock implements the on-object primitives for immutable
// AttributeBlockV1 indexes. It intentionally does not share DatasetFormat1 or
// profile-block encodings.
package attributeblock

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"
)

const (
	ObjectName = "attributes.bin"
	Version    = uint16(1)
)

// Scope identifies the source namespace of an attribute. It is part of a Key's
// identity; callers must not encode it into Name.
type Scope uint8

const (
	ScopeLegacy Scope = iota + 1
	ScopeResource
	ScopeInstrumentation
	ScopeProfile
	ScopeSample
)

func (s Scope) Valid() bool { return s >= ScopeLegacy && s <= ScopeSample }

// Key identifies an attribute in a scope.
type Key struct {
	Scope Scope
	Name  string
}

func (k Key) valid() error {
	if !k.Scope.Valid() {
		return fmt.Errorf("invalid attribute scope %d", k.Scope)
	}
	if k.Name == "" {
		return fmt.Errorf("attribute name must not be empty")
	}
	return nil
}

func compareKey(a, b Key) int {
	if n := cmp.Compare(a.Scope, b.Scope); n != 0 {
		return n
	}
	return cmp.Compare(a.Name, b.Name)
}

// ValueType is deliberately tagged: values with different types never compare
// equal, even where their textual representations are identical.
type ValueType uint8

const (
	ValueString ValueType = iota + 1
	ValueBool
	ValueInt64
	ValueFloat64
	ValueBytes
	ValueArray
	ValueMap
)

// Value contains the canonical representation for a typed value. Data is
// owned by the caller on input and copied by NewWriter. Bool values are one
// byte (zero or one); integer values are eight-byte little-endian two's
// complement. Float, array, and map payloads are reserved until their
// canonicalization rules are frozen, and are rejected rather than silently
// stringified.
type Value struct {
	Type ValueType
	Data []byte
}

func StringValue(v string) Value { return Value{Type: ValueString, Data: []byte(v)} }
func BytesValue(v []byte) Value  { return Value{Type: ValueBytes, Data: slices.Clone(v)} }
func BoolValue(v bool) Value {
	if v {
		return Value{Type: ValueBool, Data: []byte{1}}
	}
	return Value{Type: ValueBool, Data: []byte{0}}
}
func Int64Value(v int64) Value {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, uint64(v))
	return Value{Type: ValueInt64, Data: data}
}

func (v Value) valid() error {
	switch v.Type {
	case ValueString, ValueBytes:
		return nil
	case ValueBool:
		if len(v.Data) != 1 || v.Data[0] > 1 {
			return fmt.Errorf("bool value must be one byte containing zero or one")
		}
		return nil
	case ValueInt64:
		if len(v.Data) != 8 {
			return fmt.Errorf("int64 value must be eight bytes")
		}
		return nil
	case ValueFloat64, ValueArray, ValueMap:
		return fmt.Errorf("attribute value type %d is not supported by AttributeBlockV1", v.Type)
	default:
		return fmt.Errorf("invalid attribute value type %d", v.Type)
	}
}

func (v Value) equal(other Value) bool {
	return v.Type == other.Type && bytes.Equal(v.Data, other.Data)
}

// Attribute is one present value. Absence is represented by no Attribute for a
// Key, and is therefore distinct from empty values.
type Attribute struct {
	Key   Key
	Value Value
}

// Entity is the initial, series-oriented entity universe. Attributes must form
// a complete label set. Activity is intentionally not represented here: this
// first writer requires callers to explicitly state its coarse coverage mode.
type Entity struct {
	Attributes []Attribute
}

// TimeSemantics declares how the block can be used. LegacyCoarseCoverage must
// never be planned as an exact partial-window source.
type TimeSemantics uint8

const (
	TimeLegacyCoarseCoverage TimeSemantics = iota + 1
	TimeNativeExactActivity
)

// Metadata is persisted in the root directory.
type Metadata struct {
	Tenant        string
	EntityKind    string
	TimeSemantics TimeSemantics
}

func (m Metadata) valid() error {
	if m.Tenant == "" {
		return fmt.Errorf("tenant must not be empty")
	}
	if m.EntityKind != "series" {
		return fmt.Errorf("unsupported entity kind %q", m.EntityKind)
	}
	if m.TimeSemantics != TimeLegacyCoarseCoverage && m.TimeSemantics != TimeNativeExactActivity {
		return fmt.Errorf("invalid time semantics %d", m.TimeSemantics)
	}
	return nil
}
