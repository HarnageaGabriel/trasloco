package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinkPaginationLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Link", "<"+serverURL(request)+">; rel=\"next\"")
		_, _ = io.WriteString(writer, "[]")
	}))
	defer server.Close()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/items", nil)
	pages, gotError := 0, false
	//nolint:bodyclose // Each yielded response is closed in the loop.
	for response, err := range LinkPages(context.Background(), server.Client(), request) {
		if err != nil {
			gotError = strings.Contains(err.Error(), "pagination loop")
			continue
		}
		pages++
		_ = response.Body.Close()
	}
	if pages != 1 || !gotError {
		t.Fatalf("pages = %d, loop error = %v; want 1, true", pages, gotError)
	}
}

func TestAzurePaginationLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("x-ms-continuationtoken", "same")
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/items", nil)
	gotError := false
	//nolint:bodyclose // Each yielded response is closed in the loop.
	for response, err := range AzureDevOpsPages(context.Background(), server.Client(), request) {
		if response != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			gotError = strings.Contains(err.Error(), "pagination loop")
		}
	}
	if !gotError {
		t.Fatal("AzureDevOpsPages() did not detect repeated token")
	}
}

func serverURL(request *http.Request) string {
	return "http://" + request.Host + request.URL.Path
}
