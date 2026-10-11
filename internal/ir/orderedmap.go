package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"iter"
)

// OrderedMap is a string-keyed collection that retains insertion order.
//
// Set replaces an existing key in place, so the first insertion determines
// its position. JSON decoding rejects duplicate keys rather than choosing one.
// The zero value is ready to use.
type OrderedMap[V any] []OrderedEntry[V]

// OrderedEntry is one key/value pair of an OrderedMap. Fields are exported so
// go-cmp can diff IR values without options.
type OrderedEntry[V any] struct {
	Key   string
	Value V
}

// Get returns the value associated with key.
func (m OrderedMap[V]) Get(key string) (V, bool) {
	for _, entry := range m {
		if entry.Key == key {
			return entry.Value, true
		}
	}
	var zero V
	return zero, false
}

// Set associates value with key while preserving the key's first position.
//
// Set is linear in the map size; use OrderedMapBuilder to build large maps.
func (m *OrderedMap[V]) Set(key string, value V) {
	for index := range *m {
		if (*m)[index].Key == key {
			(*m)[index].Value = value
			return
		}
	}
	*m = append(*m, OrderedEntry[V]{Key: key, Value: value})
}

// Len returns the number of entries.
func (m OrderedMap[V]) Len() int { return len(m) }

// All yields entries in insertion order.
func (m OrderedMap[V]) All() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		for _, entry := range m {
			if !yield(entry.Key, entry.Value) {
				return
			}
		}
	}
}

// MarshalJSON encodes entries as a JSON object in insertion order.
func (m OrderedMap[V]) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for index, entry := range m {
		if index != 0 {
			out.WriteByte(',')
		}
		key, err := json.Marshal(entry.Key)
		if err != nil {
			return nil, fmt.Errorf("encode ordered object key: %w", err)
		}
		value, err := json.Marshal(entry.Value)
		if err != nil {
			return nil, fmt.Errorf("encode ordered object value for %q: %w", entry.Key, err)
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// UnmarshalJSON decodes an object in source order and rejects duplicate keys.
func (m *OrderedMap[V]) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode ordered object: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("decode ordered object: expected object")
	}
	decoded := make(OrderedMap[V], 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return fmt.Errorf("decode ordered object key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("decode ordered object: expected string key")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("decode ordered object: duplicate key %q", key)
		}
		seen[key] = struct{}{}
		var value V
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode ordered object value for %q: %w", key, err)
		}
		decoded = append(decoded, OrderedEntry[V]{Key: key, Value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode ordered object end: %w", err)
	}
	*m = decoded
	return nil
}
