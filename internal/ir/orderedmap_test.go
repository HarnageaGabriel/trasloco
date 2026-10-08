package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
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
