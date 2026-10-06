package httpx

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"strings"
	"testing"
)

// Source: ADR0007, synthetic pagination and failure vectors.
func TestPaginationVariants(t *testing.T) {
	for _, kind := range []string{"link", "azure", "cloud", "dc", "offset"} {
		for _, failure := range []string{"", "loop", "network", "invalid"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				calls := 0
				base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if failure == "network" {
						return nil, errors.New("network")
					}
					response := testResponse(r, 200)
					next := calls == 1 || failure == "loop"
					body := "{}"
					switch kind {
					case "link":
						if next {
							response.Header.Set("Link", `</next>; rel="next"`)
						}
						if failure == "invalid" {
							response.Header.Set("Link", `bad; rel="next"`)
						}
					case "azure":
						if next {
							response.Header.Set("x-ms-continuationtoken", "next")
						}
					case "cloud":
						if next {
							body = `{"next":"https://example.com/next"}`
						} else {
							body = `{"next":""}`
						}
					case "dc":
						if next {
							body = `{"isLastPage":false,"nextPageStart":2}`
						} else {
							body = `{"isLastPage":true}`
						}
					case "offset":
						if next {
							body = "more"
						} else {
							body = "done"
						}
					}
					if failure == "invalid" && (kind == "cloud" || kind == "dc") {
						body = "bad JSON"
					}
					response.Body = io.NopCloser(strings.NewReader(body))
					return response, nil
				})
				client := &http.Client{Transport: base}
				r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/first", nil)
				var pages iter.Seq2[*http.Response, error]
				switch kind {
				case "link":
					pages = LinkPages(r.Context(), client, r) //nolint:bodyclose // closed in loop below
				case "azure":
					pages = AzureDevOpsPages(r.Context(), client, r) //nolint:bodyclose // closed in loop below
				case "cloud":
					pages = BitbucketPages(r.Context(), client, r) //nolint:bodyclose // closed in loop below
				case "dc":
					pages = BitbucketDCPages(r.Context(), client, r) //nolint:bodyclose // closed in loop below
				case "offset":
					pages = OffsetPages(r.Context(), client, r, 10, func(b []byte) bool { return string(b) == "more" }) //nolint:bodyclose // closed in loop below
				}
				count := 0
				failed := false
				//nolint:bodyclose // Every yielded response is closed below.
				for response, err := range pages {
					if response != nil {
						_, _ = io.Copy(io.Discard, response.Body)
						_ = response.Body.Close()
						count++
					}
					if err != nil {
						failed = true
					}
					if count == 3 {
						break
					}
				}
				if failure == "" && count != 2 {
					t.Fatalf("pages=%d", count)
				}
				if failure == "network" && !failed {
					t.Fatal("network error hidden")
				}
			})
		}
	}
}

func TestContinuationOriginsAndInvalidPages(t *testing.T) {
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/first", nil)
	r.Header.Set("Authorization", "secret")
	other, err := pageRequest(r.Context(), r, "https://other.example/next")
	if err != nil {
		t.Fatal(err)
	}
	if other.Header.Get("Authorization") != "" || !anonymousFrom(other) {
		t.Fatal("cross-origin continuation leaked")
	}
	if _, err := pageRequest(r.Context(), r, "http://[::1"); err == nil {
		t.Fatal("invalid URL")
	}
	response := testResponse(r, 200)
	defer func() { _ = response.Body.Close() }()
	response.Header.Set("Link", `<http://[::1>; rel="next"`)
	if _, err := nextLink(response); err == nil {
		t.Fatal("invalid link URL")
	}
	response.Header.Set("Link", `nonsense, </prev>; rel="prev"`)
	if next, err := nextLink(response); err != nil || next != "" {
		t.Fatal(next, err)
	}
	response.Body = io.NopCloser(errorReader{errors.New("broken")})
	if err := inspectJSON(response, &struct{}{}); err == nil {
		t.Fatal("read error")
	}
	if _, err := readPageBody(io.LimitReader(zeroReader{}, (16<<20)+1)); err == nil {
		t.Fatal("unbounded page")
	}
	//nolint:bodyclose // Invalid options yield only an error.
	for response, err := range OffsetPages(context.Background(), nil, r, 0, nil) {
		if response != nil || err == nil {
			t.Fatal("invalid pagination options")
		}
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
