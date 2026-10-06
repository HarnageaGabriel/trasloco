package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// LinkPages follows RFC 8288 rel="next" links verbatim. Relative links are
// resolved against the response request URL.
func LinkPages(ctx context.Context, client *http.Client, first *http.Request) iter.Seq2[*http.Response, error] {
	return func(yield func(*http.Response, error) bool) {
		next := first.URL.String()
		seen := make(map[string]struct{})
		for next != "" {
			if _, duplicate := seen[next]; duplicate {
				yield(nil, fmt.Errorf("pagination loop: URL %q repeated", next))
				return
			}
			seen[next] = struct{}{}
			request, err := pageRequest(ctx, first, next)
			if err != nil {
				yield(nil, err)
				return
			}
			response, err := client.Do(request) //nolint:gosec,bodyclose // Body ownership transfers to the sequence consumer.
			if err != nil {
				yield(nil, err)
				return
			}
			next, err = nextLink(response)
			if err != nil {
				drainAndClose(response.Body)
				yield(nil, err)
				return
			}
			if !yield(response, nil) {
				return
			}
		}
	}
}

// AzureDevOpsPages passes x-ms-continuationtoken back as continuationToken.
func AzureDevOpsPages(ctx context.Context, client *http.Client, first *http.Request) iter.Seq2[*http.Response, error] {
	//nolint:bodyclose // The sequence transfers body ownership to its consumer.
	return tokenPages(ctx, client, first, "x-ms-continuationtoken", "continuationToken")
}

func tokenPages(ctx context.Context, client *http.Client, first *http.Request, header, parameter string) iter.Seq2[*http.Response, error] {
	return func(yield func(*http.Response, error) bool) {
		var token string
		seen := make(map[string]struct{})
		for {
			request := first.Clone(ctx)
			request.Header = first.Header.Clone()
			query := request.URL.Query()
			if token != "" {
				query.Set(parameter, token)
			}
			request.URL.RawQuery = query.Encode()
			response, err := client.Do(request) //nolint:gosec,bodyclose // Body ownership transfers to the sequence consumer.
			if err != nil {
				yield(nil, err)
				return
			}
			next := response.Header.Get(header)
			if next != "" {
				if _, duplicate := seen[next]; duplicate {
					drainAndClose(response.Body)
					yield(nil, fmt.Errorf("pagination loop: token %q repeated", next))
					return
				}
				seen[next] = struct{}{}
			}
			if !yield(response, nil) {
				return
			}
			if next == "" {
				return
			}
			token = next
		}
	}
}

// BitbucketPages follows the opaque next URL in a Bitbucket Cloud envelope.
func BitbucketPages(ctx context.Context, client *http.Client, first *http.Request) iter.Seq2[*http.Response, error] {
	return func(yield func(*http.Response, error) bool) {
		next := first.URL.String()
		seen := make(map[string]struct{})
		for next != "" {
			if _, duplicate := seen[next]; duplicate {
				yield(nil, fmt.Errorf("pagination loop: URL %q repeated", next))
				return
			}
			seen[next] = struct{}{}
			request, err := pageRequest(ctx, first, next)
			if err != nil {
				yield(nil, err)
				return
			}
			response, err := client.Do(request) //nolint:gosec // Continuation state comes from the trusted API.
			if err != nil {
				yield(nil, err)
				return
			}
			var envelope struct {
				Next string `json:"next"`
			}
			if err := inspectJSON(response, &envelope); err != nil {
				drainAndClose(response.Body)
				yield(nil, err)
				return
			}
			next = envelope.Next
			if !yield(response, nil) {
				return
			}
		}
	}
}

// BitbucketDCPages follows nextPageStart until isLastPage.
func BitbucketDCPages(ctx context.Context, client *http.Client, first *http.Request) iter.Seq2[*http.Response, error] {
	return func(yield func(*http.Response, error) bool) {
		seen := make(map[int64]struct{})
		var start int64
		for {
			request := first.Clone(ctx)
			request.Header = first.Header.Clone()
			query := request.URL.Query()
			if start > 0 {
				query.Set("start", strconv.FormatInt(start, 10))
			}
			request.URL.RawQuery = query.Encode()
			response, err := client.Do(request) //nolint:gosec // Opaque next URLs are required by Bitbucket.
			if err != nil {
				yield(nil, err)
				return
			}
			var envelope struct {
				IsLastPage    bool  `json:"isLastPage"`
				NextPageStart int64 `json:"nextPageStart"`
			}
			if err := inspectJSON(response, &envelope); err != nil {
				drainAndClose(response.Body)
				yield(nil, err)
				return
			}
			if !envelope.IsLastPage {
				if _, duplicate := seen[envelope.NextPageStart]; duplicate {
					drainAndClose(response.Body)
					yield(nil, fmt.Errorf("pagination loop: start %d repeated", envelope.NextPageStart))
					return
				}
				seen[envelope.NextPageStart] = struct{}{}
			}
			if !yield(response, nil) {
				return
			}
			if envelope.IsLastPage {
				return
			}
			start = envelope.NextPageStart
		}
	}
}

// OffsetPages increments $skip by top while hasMore reports another page.
// The response body is restored before it is yielded.
func OffsetPages(ctx context.Context, client *http.Client, first *http.Request, top int, hasMore func([]byte) bool) iter.Seq2[*http.Response, error] {
	return func(yield func(*http.Response, error) bool) {
		if top <= 0 || hasMore == nil {
			yield(nil, errors.New("offset pagination requires positive top and hasMore callback"))
			return
		}
		for skip := 0; ; skip += top {
			request := first.Clone(ctx)
			request.Header = first.Header.Clone()
			query := request.URL.Query()
			query.Set("$top", strconv.Itoa(top))
			query.Set("$skip", strconv.Itoa(skip))
			request.URL.RawQuery = query.Encode()
			response, err := client.Do(request) //nolint:gosec // Offset URL remains on the caller-provided API origin.
			if err != nil {
				yield(nil, err)
				return
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				yield(nil, err)
				return
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			more := hasMore(body)
			if !yield(response, nil) || !more {
				return
			}
		}
	}
}

func nextLink(response *http.Response) (string, error) {
	for _, part := range splitLinkHeader(response.Header.Values("Link")) {
		sections := strings.Split(part, ";")
		if len(sections) < 2 {
			continue
		}
		target := strings.TrimSpace(sections[0])
		for _, attribute := range sections[1:] {
			if strings.TrimSpace(attribute) != `rel="next"` {
				continue
			}
			if len(target) < 2 || target[0] != '<' || target[len(target)-1] != '>' {
				return "", errors.New("invalid pagination Link target")
			}
			raw := target[1 : len(target)-1]
			parsed, err := url.Parse(raw)
			if err != nil {
				return "", fmt.Errorf("parse pagination Link: %w", err)
			}
			if !parsed.IsAbs() {
				parsed = response.Request.URL.ResolveReference(parsed)
			}
			return parsed.String(), nil
		}
	}
	return "", nil
}

func splitLinkHeader(values []string) []string {
	links := make([]string, 0, len(values))
	for _, value := range values {
		links = append(links, strings.Split(value, ",")...)
	}
	return links
}

func pageRequest(ctx context.Context, template *http.Request, rawURL string) (*http.Request, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse pagination URL: %w", err)
	}
	request := template.Clone(ctx)
	request.URL = parsed
	request.Header = template.Header.Clone()
	return request, nil
}

func inspectJSON(response *http.Response, target any) error {
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode pagination envelope: %w", err)
	}
	return nil
}
