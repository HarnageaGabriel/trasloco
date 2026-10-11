package ir

// OrderedMapBuilder builds an OrderedMap with O(1) Set. The first insertion of
// a key fixes its position; later Sets replace the value in place.
//
// The builder is single-use: do not call Set after Build. Build returns a copy
// of the entries, so maps from separate builds (or from a builder that is
// reused by mistake) never share a backing array. The zero value is ready.
type OrderedMapBuilder[V any] struct {
	entries OrderedMap[V]
	index   map[string]int
}

// Set associates value with key while preserving the key's first position.
func (b *OrderedMapBuilder[V]) Set(key string, value V) {
	if b.index == nil {
		b.index = make(map[string]int)
	}
	if position, ok := b.index[key]; ok {
		b.entries[position].Value = value
		return
	}
	b.index[key] = len(b.entries)
	b.entries = append(b.entries, OrderedEntry[V]{Key: key, Value: value})
}

// Len returns the number of distinct keys set so far.
func (b *OrderedMapBuilder[V]) Len() int { return len(b.entries) }

// Build returns the accumulated entries as an independent OrderedMap. An empty
// builder yields an empty non-nil map, matching OrderedMap[V]{}.
func (b *OrderedMapBuilder[V]) Build() OrderedMap[V] {
	out := make(OrderedMap[V], len(b.entries))
	copy(out, b.entries)
	return out
}
