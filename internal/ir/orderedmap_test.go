package ir

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestOrderedMapHelpersAndJSON(t *testing.T) {
	var values OrderedMap[string]
	values.Set("second", "2")
	values.Set("first", "1")
	values.Set("second", "updated")
	if values.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", values.Len())
	}
	if got, ok := values.Get("second"); !ok || got != "updated" {
		t.Fatalf("Get(second) = %q, %v", got, ok)
	}
	if _, ok := values.Get("missing"); ok {
		t.Fatal("Get(missing) reported present")
	}

	var keys []string
	for key := range values.All() {
		keys = append(keys, key)
	}
	if !reflect.DeepEqual(keys, []string{"second", "first"}) {
		t.Fatalf("All() keys = %v", keys)
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"second":"updated","first":"1"}` {
		t.Fatalf("MarshalJSON() = %s", encoded)
	}
	var decoded OrderedMap[string]
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, decoded) {
		t.Fatalf("round trip = %#v, want %#v", decoded, values)
	}
}

func TestOrderedMapAllStopsEarly(t *testing.T) {
	var values OrderedMap[int]
	values.Set("a", 1)
	values.Set("b", 2)
	count := 0
	for range values.All() {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("All() yielded %d entries, want 1", count)
	}
}

func TestOrderedMapCopiesDoNotAlias(t *testing.T) {
	var a OrderedMap[int]
	a.Set("x", 1)
	b := a
	b.Set("y", 2)
	a.Set("y", 3)
	if v, ok := a.Get("y"); !ok || v != 3 {
		t.Fatalf("a.Get(y) = %d, %v", v, ok)
	}
	if v, ok := b.Get("y"); !ok || v != 2 {
		t.Fatalf("b.Get(y) = %d, %v", v, ok)
	}
}

func TestOrderedMapCmpDiffs(t *testing.T) {
	a := OrderedMap[int]{{Key: "key", Value: 1}}
	b := OrderedMap[int]{{Key: "key", Value: 1}}
	if !cmp.Equal(a, b) {
		t.Fatal("equal maps not go-cmp equal")
	}
	b.Set("key", 2)
	diff := cmp.Diff(a, b)
	if !strings.Contains(diff, "Value") {
		t.Fatalf("diff does not show entry field: %q", diff)
	}
	if cmp.Equal(OrderedMap[int](nil), OrderedMap[int]{}) {
		t.Fatal("nil and empty maps became go-cmp equal")
	}
}

func TestOrderedMapBuilder(t *testing.T) {
	var b OrderedMapBuilder[string]
	b.Set("second", "2")
	b.Set("first", "1")
	b.Set("second", "updated")
	if b.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", b.Len())
	}
	built := b.Build()
	want := OrderedMap[string]{{Key: "second", Value: "updated"}, {Key: "first", Value: "1"}}
	if !cmp.Equal(built, want) {
		t.Fatalf("Build() diff: %s", cmp.Diff(want, built))
	}
	var empty OrderedMapBuilder[int]
	if got := empty.Build(); got == nil || len(got) != 0 {
		t.Fatalf("empty Build() = %#v, want empty non-nil", got)
	}
}

func TestOrderedMapBuilderNoAliasing(t *testing.T) {
	var b OrderedMapBuilder[int]
	b.Set("a", 1)
	first := b.Build()
	b.Set("a", 9)
	b.Set("b", 2)
	second := b.Build()
	if v, _ := first.Get("a"); v != 1 || first.Len() != 1 {
		t.Fatalf("first build changed: %#v", first)
	}
	second.Set("a", 5)
	if v, _ := b.Build().Get("a"); v != 9 {
		t.Fatalf("mutating a built map changed the builder")
	}
	if second.Len() != 2 {
		t.Fatalf("second build = %#v", second)
	}
}

func TestOrderedMapBuilderLinear(t *testing.T) {
	const count = 120_000
	var b OrderedMapBuilder[int]
	for i := 0; i < count; i++ {
		b.Set(strconv.Itoa(i), i)
	}
	for i := 0; i < count; i += 7 {
		b.Set(strconv.Itoa(i), -i)
	}
	built := b.Build()
	if built.Len() != count {
		t.Fatalf("Len() = %d, want %d", built.Len(), count)
	}
	if built[0].Key != "0" || built[count-1].Key != strconv.Itoa(count-1) || built[7].Value != -7 {
		t.Fatalf("order/replacement wrong: %v %v %v", built[0], built[count-1], built[7])
	}
}

func BenchmarkOrderedMapBuilderSet(b *testing.B) {
	for _, size := range []int{1_000, 120_000} {
		b.Run(fmt.Sprintf("keys-%d", size), func(b *testing.B) {
			for b.Loop() {
				var builder OrderedMapBuilder[int]
				for i := 0; i < size; i++ {
					builder.Set(strconv.Itoa(i), i)
				}
				_ = builder.Build()
			}
		})
	}
}

func TestOrderedMapRejectsInvalidJSON(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"a":1,"a":2}`, `{"a":}`} {
		var values OrderedMap[int]
		if err := json.Unmarshal([]byte(input), &values); err == nil {
			t.Fatalf("json.Unmarshal(%s) succeeded", input)
		}
	}

	var values OrderedMap[struct {
		Known string `json:"known"`
	}]
	err := json.Unmarshal([]byte(`{"a":{"unknown":true}}`), &values)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}
