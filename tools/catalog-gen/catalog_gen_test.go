package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const retirementDates = `<p>Tasks are retired on October 15, 2026 and removed on March 15, 2027.</p>`

func TestExtractTasksFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/task.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractTasks(context.Background(), []namedData{{name: "ExampleTaskV2", data: data}})
	if err != nil {
		t.Fatalf("extractTasks() error = %v", err)
	}
	version := got.Tasks[0].Versions[0]
	if version.Major != 2 || version.Minor != 3 || version.Patch != 4 {
		t.Fatalf("version = %d.%d.%d", version.Major, version.Minor, version.Patch)
	}
	if !version.HasDeprecationMessage || version.Inputs[0].Type != "pickList" {
		t.Fatalf("extracted facts = %#v", version)
	}
	if diff := strings.Join(version.Inputs[0].Options, ","); diff != "fast,safe" {
		t.Fatalf("options = %q", diff)
	}
	encoded := string(data)
	if strings.Contains(stringValue(got), "ignored prose") || !strings.Contains(encoded, "ignored prose") {
		t.Fatal("fixture did not prove prose exclusion")
	}
}

func TestLocalTaskFilesSkipsGenerated(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"ExampleV1", "_generated"} {
		path := filepath.Join(root, "Tasks", dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile("testdata/task.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "task.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := localTaskFiles(context.Background(), root)
	if err != nil || len(files) != 1 || files[0].name != "ExampleV1" {
		t.Fatalf("localTaskFiles() = %#v, %v", files, err)
	}
}

func TestResolveActionsAnnotatedTagAndRuntimePolicy(t *testing.T) {
	actionYAML, err := os.ReadFile("testdata/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/matching-refs/tags/"):
			_, _ = w.Write([]byte(`[{"ref":"refs/tags/v1.9.0","object":{"type":"commit","sha":"cccccccccccccccccccccccccccccccccccccccc"}},{"ref":"refs/tags/v2.0.0","object":{"type":"tag","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"ref":"refs/tags/v3-beta","object":{"type":"commit","sha":"dddddddddddddddddddddddddddddddddddddddd"}}]`))
		case strings.HasSuffix(r.URL.Path, "/git/tags/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"):
			_, _ = w.Write([]byte(`{"object":{"type":"tag","sha":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}`))
		case strings.Contains(r.URL.Path, "/git/tags/"):
			_, _ = w.Write([]byte(`{"object":{"type":"commit","sha":"` + commit + `"}}`))
		case strings.Contains(r.URL.Path, "/bad/action.yml"):
			_, _ = w.Write([]byte("runs:\n  using: node20\n"))
		case strings.Contains(r.URL.Path, "/good/action.yml"):
			_, _ = w.Write(actionYAML)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := resolveActions(context.Background(), httpClient{client: server.Client()}, server.URL, server.URL, []string{"o/r/good", "o/r/bad"})
	if err != nil {
		t.Fatalf("resolveActions() error = %v", err)
	}
	if len(got.Actions) != 1 || got.Actions[0].Version != "v2.0.0" || got.Actions[0].SHA != commit {
		t.Fatalf("actions = %#v", got.Actions)
	}
	if len(got.Rejected) != 1 || got.Rejected[0].RunsUsing != "node20" {
		t.Fatalf("rejected = %#v", got.Rejected)
	}
}

func TestRetirementFixture(t *testing.T) {
	html := retirementDates + `<table><tr><th>Task</th><th>Replacement</th></tr><tr><td>OldTask@2</td><td>NewTask@3</td></tr><tr><td>Gone@1</td><td>None</td></tr></table>`
	got, err := parseRetirementHTML([]byte(html))
	if err != nil {
		t.Fatalf("parseRetirementHTML() error = %v", err)
	}
	if len(got) != 2 || got[1].Task != "OldTask@2" || got[1].ReplacementKind != "task" {
		t.Fatalf("retirements = %#v", got)
	}
	if _, err := parseRetirementHTML([]byte("<html></html>")); err == nil {
		t.Fatal("parseRetirementHTML(empty) error = nil")
	}
	if _, err := parseRetirementHTML([]byte(strings.Replace(html, "March 15, 2027", "March 16, 2027", 1))); err == nil {
		t.Fatal("parseRetirementHTML(changed date) error = nil")
	}
}

func TestRemoteTaskFilesAndTruncatedTree(t *testing.T) {
	fixture, err := os.ReadFile("testdata/task.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_, _ = w.Write([]byte(`{"tree":[{"path":"Tasks/ZedV1/task.json","type":"blob"},{"path":"Tasks/_generated/task.json","type":"blob"},{"path":"Tasks/AlphaV1/task.json","type":"blob"},{"path":"README.md","type":"blob"}],"truncated":false}`))
		case strings.HasSuffix(r.URL.Path, "/task.json"):
			_, _ = w.Write(fixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	files, err := remoteTaskFiles(context.Background(), server.URL, server.URL)
	if err != nil {
		t.Fatalf("remoteTaskFiles() error = %v", err)
	}
	if len(files) != 2 || files[0].name != "AlphaV1" || files[1].name != "ZedV1" {
		t.Fatalf("remoteTaskFiles() = %#v", files)
	}

	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tree":[],"truncated":true}`))
	}))
	defer truncated.Close()
	if _, err := remoteTaskFiles(context.Background(), truncated.URL, truncated.URL); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("remoteTaskFiles(truncated) error = %v", err)
	}
}

func TestLatestTagFollowsLinkAndDeduplicates(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("cursor") == "next" {
			_, _ = w.Write([]byte(`[{"ref":"refs/tags/v2.0.0","object":{"type":"commit","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}]`))
			return
		}
		w.Header().Set("Link", `</repos/o/r/git/matching-refs/tags/?cursor=next>; rel="next"`)
		_, _ = w.Write([]byte(`[{"ref":"refs/tags/v1.0.0","object":{"type":"commit","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},{"ref":"refs/tags/v1.0.0","object":{"type":"commit","sha":"cccccccccccccccccccccccccccccccccccccccc"}}]`))
	}))
	defer server.Close()
	version, object, err := latestTag(context.Background(), httpClient{client: server.Client()}, server.URL, "o/r")
	if err != nil || version != "v2.0.0" || object.SHA[0] != 'b' || requests.Load() != 2 {
		t.Fatalf("latestTag() = %q, %#v, %v; requests=%d", version, object, err, requests.Load())
	}
}

func TestLatestTagStopsWithoutLinkWhenServerIgnoresPage(t *testing.T) {
	var requests atomic.Int32
	refs := make([]string, 100)
	for i := range refs {
		refs[i] = fmt.Sprintf(`{"ref":"refs/tags/v1.0.%d","object":{"type":"commit","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, i)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("[" + strings.Join(refs, ",") + "]"))
	}))
	defer server.Close()
	version, _, err := latestTag(context.Background(), httpClient{client: server.Client()}, server.URL, "o/r")
	if err != nil || version != "v1.0.99" || requests.Load() != 1 {
		t.Fatalf("latestTag() = %q, %v; requests=%d", version, err, requests.Load())
	}
}

func TestActionRuntimeFallsBackToActionYAML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "action.yml") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("runs:\n  using: composite\n"))
	}))
	defer server.Close()
	got, err := actionRuntime(context.Background(), httpClient{client: server.Client()}, server.URL, "o/r", "sub", strings.Repeat("a", 40))
	if err != nil || got != "composite" {
		t.Fatalf("actionRuntime() = %q, %v", got, err)
	}
}

func TestHTTPClientTokenScopeAndRateErrors(t *testing.T) {
	const token = "secret-token-value"
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("denied " + token))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := httpClient{client: server.Client(), token: token, apiHost: parsed.Host, allowHTTPToken: true}
	_, _, err = client.get(context.Background(), server.URL)
	if authorization != "Bearer "+token || err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("authorization=%q error=%v", authorization, err)
	}

	other, _ := url.Parse("http://untrusted.example/path")
	if client.shouldAuthorize(other) {
		t.Fatal("token authorized for an unrelated host")
	}
	official, _ := url.Parse("https://raw.githubusercontent.com/o/r/sha/action.yml")
	if !client.shouldAuthorize(official) {
		t.Fatal("token not authorized for official raw host")
	}
	insecureOfficial, _ := url.Parse("http://api.github.com/path")
	client.allowHTTPToken = false
	if client.shouldAuthorize(insecureOfficial) {
		t.Fatal("token authorized over HTTP")
	}
}

func TestHTTPClientRateLimitAndContext(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("slow down"))
			}))
			defer server.Close()
			_, _, err := (httpClient{client: server.Client()}).get(context.Background(), server.URL)
			if err == nil || !strings.Contains(err.Error(), "rate limit") {
				t.Fatalf("get() error = %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := (httpClient{client: &http.Client{Timeout: time.Second}}).get(ctx, "https://example.invalid")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("get(canceled) error = %v", err)
	}
}

func TestLookupDeterminismAndConflict(t *testing.T) {
	if value, ok, err := lookup(map[string]any{"name": "exact", "NAME": "exact"}, "name"); err != nil || !ok || value != "exact" {
		t.Fatalf("lookup(exact) = %#v, %v, %v", value, ok, err)
	}
	if _, _, err := lookup(map[string]any{"name": "exact", "NAME": "other"}, "name"); err == nil {
		t.Fatal("lookup(exact conflict) error = nil")
	}
	if value, ok, err := lookup(map[string]any{"NAME": "same", "Name": "same"}, "name"); err != nil || !ok || value != "same" {
		t.Fatalf("lookup(equal variants) = %#v, %v, %v", value, ok, err)
	}
	data := []byte(`{"Name":"Example","ID":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","Version":{"Major":1,"MAJOR":2,"Minor":0,"Patch":0}}`)
	_, err := extractTasks(context.Background(), []namedData{{name: "ExampleV1", data: data}})
	if err == nil || !strings.Contains(err.Error(), "task Example") || !strings.Contains(err.Error(), `key "major"`) {
		t.Fatalf("extractTasks(conflicting casing) error = %v", err)
	}
}

func TestRunOfflineSubcommands(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "Tasks", "ExampleV2")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/task.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "task.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	tasksOut := filepath.Join(root, "tasks.yaml")
	if err := run(context.Background(), []string{"tasks", "--src", root, "--out", tasksOut}); err != nil {
		t.Fatalf("run(tasks) error = %v", err)
	}
	page := filepath.Join(root, "retirement.html")
	html := retirementDates + `<table><tr><td>Old@1</td><td>Use a script</td></tr></table>`
	if err := os.WriteFile(page, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	retirementOut := filepath.Join(root, "retirement.yaml")
	if err := run(context.Background(), []string{"retirement", "--src", page, "--out", retirementOut}); err != nil {
		t.Fatalf("run(retirement) error = %v", err)
	}
	for _, name := range []string{tasksOut, retirementOut} {
		if info, err := os.Stat(name); err != nil || info.Size() == 0 {
			t.Fatalf("output %s info=%v error=%v", name, info, err)
		}
	}
}

func TestRunActionsAndNetworkRetirement(t *testing.T) {
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/matching-refs/tags/"):
			_, _ = w.Write([]byte(`[{"ref":"refs/tags/v1.2.3","object":{"type":"commit","sha":"` + commit + `"}}]`))
		case strings.HasSuffix(r.URL.Path, "/action.yml"):
			_, _ = w.Write([]byte("runs:\n  using: docker\n"))
		case r.URL.Path == "/retirement":
			_, _ = w.Write([]byte(retirementDates + `<table><tr><td>Old@1</td><td>None</td></tr></table>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	if err := run(context.Background(), []string{"actions", "--api-base", server.URL, "--raw-base", server.URL, "--names", "o/r", "--out", filepath.Join(root, "actions.yaml")}); err != nil {
		t.Fatalf("run(actions) error = %v", err)
	}
	if err := run(context.Background(), []string{"retirement", "--url", server.URL + "/retirement", "--out", filepath.Join(root, "retirement.yaml")}); err != nil {
		t.Fatalf("run(retirement network) error = %v", err)
	}
}

func TestDereferenceAndParsingFailures(t *testing.T) {
	client := httpClient{client: &http.Client{Timeout: time.Second}}
	if _, err := dereferenceTag(context.Background(), client, "https://example.invalid", "o/r", gitObject{Type: "commit", SHA: "short"}); err == nil {
		t.Fatal("dereferenceTag accepted a short SHA")
	}
	if _, err := dereferenceTag(context.Background(), client, "https://example.invalid", "o/r", gitObject{Type: "tree", SHA: strings.Repeat("a", 40)}); err == nil {
		t.Fatal("dereferenceTag accepted an unsupported object")
	}
	if _, ok := parseSemver("v1.two.3"); ok {
		t.Fatal("parseSemver accepted a non-numeric version")
	}
	if _, err := nextLink("https://example.test/page", `<broken; rel="next"`); err == nil {
		t.Fatal("nextLink accepted malformed target")
	}
}

func TestValueAndRetirementHelpers(t *testing.T) {
	if stringValue(true) != "true" || stringValue(false) != "false" || stringValue(nil) != "" {
		t.Fatal("stringValue returned an unexpected scalar conversion")
	}
	if !boolValue("TRUE") || boolValue(0.0) || !boolValue(2.0) {
		t.Fatal("boolValue returned an unexpected conversion")
	}
	if intValue("12") != 12 || intValue(3.0) != 3 || intValue(struct{}{}) != 0 {
		t.Fatal("intValue returned an unexpected conversion")
	}
	if canonicalType("MULTILINE") != "multiLine" || canonicalType("PickList") != "pickList" || canonicalType("string") != "string" {
		t.Fatal("canonicalType returned an unexpected conversion")
	}
	refs := uniqueRefs([]string{"Build@1", "build@1", "Test@2"})
	if len(refs) != 2 || len(taskRefs("Use Build@1 then Test@2")) != 2 || plainHTML([]byte("<b>A&amp;B</b>")) != "A&B" {
		t.Fatalf("retirement helpers returned refs=%v", refs)
	}
}

func TestRunAndHelpers(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("run(nil) error = nil")
	}
	if err := run(context.Background(), []string{"unknown"}); err == nil {
		t.Fatal("run(unknown) error = nil")
	}
	if _, ok := parseSemver("v1.2"); ok {
		t.Fatal("parseSemver accepted short version")
	}
	if got := splitNames("B/r,a/r,b/R,"); strings.Join(got, ",") != "a/r,B/r" {
		t.Fatalf("splitNames() = %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeYAML(ctx, filepath.Join(t.TempDir(), "out.yaml"), "", struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeYAML(canceled) error = %v", err)
	}
}
