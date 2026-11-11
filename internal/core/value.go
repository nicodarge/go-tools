// Package core holds what the checkers share: the parsed-value model, a strict streaming JSON
// reader, the packument reader and the refusal mechanism.
package core

import (
	"math"
	"strconv"
	"strings"
)

// Kind is what sort of value a Value holds.
type Kind uint8

// The kinds of Value.
const (
	Null Kind = iota
	Bool
	Num
	Str
	List
	Map
)

// How a YAML scalar was written; a JSON string is plain.
const (
	StylePlain uint8 = iota
	StyleSingle
	StyleDouble
	StyleLiteral
	StyleFolded
)

// Value is a parsed JSON or YAML value. A nil *Value reads as a JSON null.
type Value struct {
	K     Kind
	Style uint8
	B     bool
	Int   bool
	S     string // a string's text; a number's canonical spelling
	L     []*Value
	M     *OMap
}

// OMap is a mapping that keeps its keys in the order they were written.
type OMap struct {
	Keys []string
	Vals map[string]*Value
}

// NewMap returns an empty OMap.
func NewMap() *OMap { return &OMap{Vals: map[string]*Value{}} }

// Has says whether key is present.
func (m *OMap) Has(key string) bool {
	_, ok := m.Vals[key]
	return ok
}

// Get returns the value of key, nil if absent.
func (m *OMap) Get(key string) *Value { return m.Vals[key] }

// Set stores v under key, keeping the position of an existing key.
func (m *OMap) Set(key string, v *Value) {
	if _, ok := m.Vals[key]; !ok {
		m.Keys = append(m.Keys, key)
	}
	m.Vals[key] = v
}

// Len is the number of keys.
func (m *OMap) Len() int { return len(m.Keys) }

// KindOf is the kind of v, Null for nil.
func KindOf(v *Value) Kind {
	if v == nil {
		return Null
	}
	return v.K
}

// StrValue is a string Value.
func StrValue(s string) *Value { return &Value{K: Str, S: s} }

// MapValue is a mapping Value.
func MapValue(m *OMap) *Value { return &Value{K: Map, M: m} }

// IsStr says whether v is a string.
func IsStr(v *Value) bool { return KindOf(v) == Str }

// IsMap says whether v is a mapping.
func IsMap(v *Value) bool { return KindOf(v) == Map }

// Truthy says whether v is neither empty, zero, false nor null.
func Truthy(v *Value) bool {
	switch KindOf(v) {
	case Null:
		return false
	case Bool:
		return v.B
	case Num:
		return v.S != "0" && v.S != "f0" && v.S != "f-0"
	case Str:
		return v.S != ""
	case List:
		return len(v.L) > 0
	case Map:
		return v.M.Len() > 0
	}
	return true
}

// MGet is v.M.Get(key); anything but a mapping is an internal error.
func MGet(v *Value, key string) *Value {
	if KindOf(v) != Map {
		Crash("AttributeError")
	}
	return v.M.Get(key)
}

// MHas is v.M.Has(key); anything but a mapping is an internal error.
func MHas(v *Value, key string) bool {
	if KindOf(v) != Map {
		Crash("TypeError")
	}
	return v.M.Has(key)
}

// ItemsOf is the mapping v, or an empty one when v is empty; any other kind is an internal error.
func ItemsOf(v *Value) *OMap {
	if !Truthy(v) {
		return NewMap()
	}
	if KindOf(v) != Map {
		Crash("AttributeError")
	}
	return v.M
}

// GetItems is ItemsOf of the key of a mapping.
func GetItems(entry *Value, key string) *OMap { return ItemsOf(MGet(entry, key)) }

// NumCanon is the canonical spelling of a JSON number, so 0 and -0 are one value and 1.0 differs from 1.
func NumCanon(text string) (canon string, isInt bool) {
	if !strings.ContainsAny(text, ".eE") {
		neg := strings.HasPrefix(text, "-")
		digits := strings.TrimPrefix(text, "-")
		if strings.Trim(digits, "0") == "" {
			return "0", true
		}
		if neg {
			return "-" + digits, true
		}
		return digits, true
	}
	f, _ := strconv.ParseFloat(text, 64)
	if math.IsInf(f, 0) {
		if f > 0 {
			return "finf", false
		}
		return "f-inf", false
	}
	if f == 0 {
		f = 0 // -0.0 and 0.0 are one float
	}
	return "f" + strconv.FormatFloat(f, 'g', -1, 64), false
}

// CanonEq compares two values by structure and by the exact kind of each scalar, so true, 1 and
// 1.0 differ, and so do `false` and `'false'` in YAML.
func CanonEq(a, b *Value) bool {
	if KindOf(a) != KindOf(b) {
		return false
	}
	if a == nil {
		return true
	}
	switch a.K {
	case Null:
		return true
	case Bool:
		return a.B == b.B
	case Num:
		return a.S == b.S
	case Str:
		return a.S == b.S && a.Style == b.Style
	case List:
		if len(a.L) != len(b.L) {
			return false
		}
		for i := range a.L {
			if !CanonEq(a.L[i], b.L[i]) {
				return false
			}
		}
		return true
	case Map:
		if a.M.Len() != b.M.Len() {
			return false
		}
		for _, k := range a.M.Keys {
			other, ok := b.M.Vals[k]
			if !ok || !CanonEq(a.M.Vals[k], other) {
				return false
			}
		}
		return true
	}
	return false
}

func numFloat(v *Value) float64 {
	switch v.K {
	case Bool:
		if v.B {
			return 1
		}
		return 0
	}
	switch v.S {
	case "finf":
		return math.Inf(1)
	case "f-inf":
		return math.Inf(-1)
	}
	f, _ := strconv.ParseFloat(strings.TrimPrefix(v.S, "f"), 64)
	return f
}

// PyEq compares two values as numbers compare, equating true, 1 and 1.0, and ignores how a YAML
// scalar was written.
func PyEq(a, b *Value) bool {
	ka, kb := KindOf(a), KindOf(b)
	numeric := func(k Kind) bool { return k == Bool || k == Num }
	if numeric(ka) && numeric(kb) {
		if ka == Num && kb == Num && a.Int && b.Int {
			return a.S == b.S
		}
		return numFloat(a) == numFloat(b)
	}
	if ka != kb {
		return false
	}
	switch ka {
	case Null:
		return true
	case Str:
		return a.S == b.S
	case List:
		if len(a.L) != len(b.L) {
			return false
		}
		for i := range a.L {
			if !PyEq(a.L[i], b.L[i]) {
				return false
			}
		}
		return true
	case Map:
		if a.M.Len() != b.M.Len() {
			return false
		}
		for _, k := range a.M.Keys {
			other, ok := b.M.Vals[k]
			if !ok || !PyEq(a.M.Vals[k], other) {
				return false
			}
		}
		return true
	}
	return false
}

// SameKey says whether key is present in both mappings or neither, and canonically equal.
func SameKey(a, b *Value, key string) bool {
	ha, hb := MHas(a, key), MHas(b, key)
	return ha == hb && (!ha || CanonEq(a.M.Get(key), b.M.Get(key)))
}

// NeedDict is the mapping v, or a refusal naming what.
func NeedDict(v *Value, what string) *OMap {
	if KindOf(v) != Map {
		Reject(what + " is not a mapping")
	}
	return v.M
}

// NeedOnlyKeys refuses an entry carrying a key outside allowed.
func NeedOnlyKeys(entry *OMap, allowed []string, what string) {
	for _, key := range entry.Keys {
		found := false
		for _, a := range allowed {
			if a == key {
				found = true
				break
			}
		}
		if !found {
			Rejectf("%s carries %s", what, Q(key))
		}
	}
}

// InList says whether s is in list.
func InList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
