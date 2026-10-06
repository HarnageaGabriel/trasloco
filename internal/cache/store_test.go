package cache

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentPutGet(t *testing.T) {
	store, err := Open(Options{Dir: filepath.Join(t.TempDir(), "cache"), MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var group sync.WaitGroup
	for i := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			key := fmt.Sprintf("key-%d", i)
			body := []byte(fmt.Sprintf("body-%d", i))
			if _, putErr := store.Put(key, http.StatusOK, http.Header{"Etag": {key}}, body); putErr != nil {
				t.Errorf("Put: %v", putErr)
				return
			}
			got, ok, getErr := store.Get(key)
			if getErr != nil || !ok || string(got.Body) != string(body) {
				t.Errorf("Get = %q, %v, %v; want %q, true, nil", got.Body, ok, getErr, body)
			}
		}()
	}
	group.Wait()
}

func TestKeyNeverContainsCredential(t *testing.T) {
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/items?access_token=secret&q=x", nil)
	one := Key(request, "scope")
	request.URL.RawQuery = "access_token=other&q=x"
	two := Key(request, "scope")
	if one != two {
		t.Fatalf("secret query changed key: %q != %q", one, two)
	}
}

func TestEviction(t *testing.T) {
	store, _ := Open(Options{Dir: t.TempDir(), MaxBytes: 5})
	if _, err := store.Put("one", 200, nil, []byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("two", 200, nil, []byte("5678")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("one"); err != nil || ok {
		t.Fatalf("evicted Get = %v, %v; want false, nil", ok, err)
	}
}
