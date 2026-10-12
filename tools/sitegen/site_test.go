package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/google/go-cmp/cmp"
)

var (
	update    = flag.Bool("update", false, "rewrite golden files")
	goldenDir = flag.String("golden-dir", "testdata/golden", "golden directory used with -update")
)

const (
	testAnnouncement = "https://learn.microsoft.com/en-us/azure/devops/pipelines/tasks/task-retirement"
	testBase         = "https://example.test/site"
)

func testConfig() Config {
	return Config{
		RetirementDate:  "2026-10-15",
		RemovalDate:     "2027-03-15",
		AnnouncementURL: testAnnouncement,
		Generated:       "2026-10-12",
		BaseURL:         testBase,
	}
}

func fixtureCatalog() *catalog.Catalog {
	task := func(name string) string {
		return "https://github.com/microsoft/azure-pipelines-tasks/blob/abc123/Tasks/" + name + "/task.json"
	}
	return &catalog.Catalog{
		Retirements: []catalog.RetiredTask{
			{Task: "Chef@1", Name: "Chef", Major: 1, ReplacementKind: "external"},
			{Task: "CopyFiles@1", Name: "CopyFiles", Major: 1, ReplacementKind: "task", Replacements: []string{"CopyFiles@2"}},
		},
		Modernizations: []catalog.Modernization{
			{Task: "Chef@1", GUID: "g-chef", Class: "D", Action: `Use the "Chef" extension or a script step.`, Sources: []string{testAnnouncement, task("Chef")}},
			{Task: "AndroidBuild@1", GUID: "g-android", Class: "C", Replacement: []string{"Gradle@4"}, Action: "Migrate this step manually to Gradle@4.", Sources: []string{testAnnouncement, task("AndroidBuild"), task("GradleV4")}},
			{Task: "InvokeRESTAPI@0", GUID: "g-rest", Class: "B", Replacement: []string{"InvokeRESTAPI@1"}, Action: "Replace InvokeRESTAPI@0 with InvokeRESTAPI@1.", Sources: []string{testAnnouncement}, Verify: []string{"V1"}},
			{Task: "CopyFiles@1", GUID: "g-copy", Class: "A", Replacement: []string{"CopyFiles@2"}, Action: "Replace CopyFiles@1 with CopyFiles@2.", Sources: []string{testAnnouncement, task("CopyFiles"), "https://example.test/other"}},
		},
	}
}

func generateFixture(t *testing.T, cfg Config) map[string][]byte {
	t.Helper()
	files, err := generate(fixtureCatalog(), cfg)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		out[f.Path] = f.Data
	}
	return out
}

func TestGolden(t *testing.T) {
	site := generateFixture(t, testConfig())
	for _, name := range []string{"index.html", "tasks/copyfiles-1/index.html", "tasks/androidbuild-1/index.html", "tasks/chef-1/index.html", "feed.xml", "llms.txt", "sitemap.xml"} {
		t.Run(name, func(t *testing.T) {
			got, ok := site[name]
			if !ok {
				t.Fatalf("missing generated file %s", name)
			}
			golden := filepath.Join("testdata", "golden", filepath.FromSlash(name))
			if *update {
				target := filepath.Join(*goldenDir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			if diff := cmp.Diff(string(want), string(got)); diff != "" {
				t.Errorf("%s differs from golden (-want +got):\n%s", name, diff)
			}
		})
	}
}

func TestEveryRowHasPageSitemapAndLLMS(t *testing.T) {
	cat := fixtureCatalog()
	site := generateFixture(t, testConfig())
	for _, row := range cat.Modernizations {
		slug := strings.ToLower(strings.Replace(row.Task, "@", "-", 1))
		page := "tasks/" + slug + "/index.html"
		if _, ok := site[page]; !ok {
			t.Errorf("no page for %s", row.Task)
		}
		url := testBase + "/tasks/" + slug + "/"
		for name, want := range map[string]string{"sitemap.xml": url, "llms.txt": url, "index.html": `href="/site/tasks/` + slug + `/"`} {
			if !strings.Contains(string(site[name]), want) {
				t.Errorf("%s does not list %s", name, want)
			}
		}
		if !strings.Contains(string(site["llms-full.txt"]), "\n## "+row.Task+"\n") {
			t.Errorf("llms-full.txt has no block for %s", row.Task)
		}
	}
	for _, name := range []string{"404.html", "robots.txt", "data/modernize.json", "style.css", "llms-full.txt", "feed.xml", indexNowKey + ".txt"} {
		if len(site[name]) == 0 {
			t.Errorf("missing %s", name)
		}
	}
	if !strings.Contains(string(site["robots.txt"]), "Sitemap: "+testBase+"/sitemap.xml") {
		t.Errorf("robots.txt has no sitemap line: %q", site["robots.txt"])
	}
	if got := string(site[indexNowKey+".txt"]); got != indexNowKey {
		t.Errorf("IndexNow key file = %q, want %q", got, indexNowKey)
	}
}

func TestRealCatalog(t *testing.T) {
	cat, err := catalog.Load(context.Background(), os.DirFS("../../catalog"), diag.NewCollector())
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	header, err := readHeader(os.DirFS("../../catalog"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{RetirementDate: header.RetirementDate, RemovalDate: header.RemovalDate, AnnouncementURL: header.Source.RetirementURL, Generated: "2026-10-12", BaseURL: defaultBaseURL}
	files, err := generate(cat, cfg)
	if err != nil {
		t.Fatal(err)
	}
	site := make(map[string][]byte, len(files))
	for _, f := range files {
		site[f.Path] = f.Data
	}
	if len(cat.Modernizations) == 0 {
		t.Fatal("catalog has no modernization rows")
	}
	if got := len(cat.Modernizations); got != 73 {
		t.Fatalf("catalog has %d task pages, want 73", got)
	}
	descriptions := map[string]string{}
	for _, row := range cat.Modernizations {
		slug := strings.ToLower(strings.Replace(row.Task, "@", "-", 1))
		page, ok := site["tasks/"+slug+"/index.html"]
		if !ok {
			t.Fatalf("no page for %s", row.Task)
		}
		url := defaultBaseURL + "/tasks/" + slug + "/"
		for _, name := range []string{"sitemap.xml", "llms.txt"} {
			if !strings.Contains(string(site[name]), url) {
				t.Errorf("%s does not list %s", name, url)
			}
		}
		match := regexp.MustCompile(`<meta name="description" content="([^"]*)">`).FindSubmatch(page)
		if match == nil {
			t.Fatalf("%s: no description", row.Task)
		}
		desc := string(match[1])
		if n := utf8.RuneCountInString(desc); n > maxDescription {
			t.Errorf("%s: description is %d characters", row.Task, n)
		}
		if other, dup := descriptions[desc]; dup {
			t.Errorf("%s and %s share a description", row.Task, other)
		}
		descriptions[desc] = row.Task
		titleMatch := regexp.MustCompile(`<title>([^<]*)</title>`).FindSubmatch(page)
		if titleMatch == nil {
			t.Fatalf("%s: no title", row.Task)
		}
		if n := utf8.RuneCount(titleMatch[1]); n > 65 {
			t.Errorf("%s: title is %d characters: %s", row.Task, n, titleMatch[1])
		}
		if row.Class == "D" && strings.Contains(strings.ToLower(string(page)), "replace it with") {
			t.Errorf("%s: class D page says replace it with", row.Task)
		}
	}
	for name, data := range site {
		if strings.Contains(string(data), "trasloco modernize") {
			t.Errorf("%s mentions the unreleased command", name)
		}
	}
	checkPages(t, site)
	checkFeed(t, site["feed.xml"], 73)
	checkRelatedLinks(t, site)
}

func checkFeed(t *testing.T, data []byte, wantEntries int) {
	t.Helper()
	var feed struct {
		XMLName xml.Name `xml:"feed"`
		Entries []struct {
			ID      string `xml:"id"`
			Updated string `xml:"updated"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(data, &feed); err != nil {
		t.Fatalf("feed.xml is not well-formed: %v", err)
	}
	// RFC 4287: a feed-level author and a single rel="self" link.
	if !bytes.Contains(data, []byte("<author><name>")) || !bytes.Contains(data, []byte(`rel="self"`)) {
		t.Fatalf("feed.xml lacks a feed-level author or a rel=self link")
	}
	if feed.XMLName.Space != "http://www.w3.org/2005/Atom" || len(feed.Entries) != wantEntries {
		t.Fatalf("feed = namespace %q, %d entries; want Atom and %d", feed.XMLName.Space, len(feed.Entries), wantEntries)
	}
	for _, entry := range feed.Entries {
		if entry.ID == "" || entry.Updated == "" {
			t.Errorf("feed entry is missing id or updated: %+v", entry)
		}
	}
}

func checkRelatedLinks(t *testing.T, site map[string][]byte) {
	t.Helper()
	linkPattern := regexp.MustCompile(`href="[^"]*/tasks/([^/]+)/"`)
	for name, data := range site {
		if !strings.HasPrefix(name, "tasks/") || !strings.HasSuffix(name, "/index.html") {
			continue
		}
		page := string(data)
		start := strings.Index(page, `<h2 id="related">Related retired tasks</h2>`)
		if start < 0 {
			t.Errorf("%s has no related tasks section", name)
			continue
		}
		end := strings.Index(page[start:], "</section>")
		if end < 0 {
			t.Errorf("%s has an unterminated related tasks section", name)
			continue
		}
		for _, match := range linkPattern.FindAllStringSubmatch(page[start:start+end], -1) {
			target := "tasks/" + match[1] + "/index.html"
			if _, ok := site[target]; !ok {
				t.Errorf("%s links to missing related page %s", name, target)
			}
		}
	}
}

var (
	tagPattern    = regexp.MustCompile(`<(/?)([a-zA-Z][a-zA-Z0-9]*)(?:\s[^>]*)?>`)
	scriptPattern = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>`)
	ldPattern     = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)
)

func wellFormed(page string) string {
	voids := map[string]bool{"meta": true, "link": true, "br": true, "hr": true, "img": true, "input": true}
	var stack []string
	for _, m := range tagPattern.FindAllStringSubmatch(scriptPattern.ReplaceAllString(page, "<script></script>"), -1) {
		name := strings.ToLower(m[2])
		switch {
		case voids[name]:
		case m[1] == "":
			stack = append(stack, name)
		case len(stack) == 0 || stack[len(stack)-1] != name:
			return "unexpected </" + name + ">"
		default:
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		return "unclosed <" + stack[len(stack)-1] + ">"
	}
	return ""
}

func checkPages(t *testing.T, site map[string][]byte) {
	t.Helper()
	for name, data := range site {
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		page := string(data)
		if problem := wellFormed(page); problem != "" {
			t.Errorf("%s: %s", name, problem)
		}
		if !strings.Contains(page, `<html lang="en">`) {
			t.Errorf("%s: missing lang", name)
		}
		for _, m := range ldPattern.FindAllStringSubmatch(page, -1) {
			var value map[string]any
			if err := json.Unmarshal([]byte(m[1]), &value); err != nil {
				t.Errorf("%s: invalid JSON-LD: %v", name, err)
			}
			if _, exists := value["datePublished"]; exists {
				t.Errorf("%s: JSON-LD contains datePublished", name)
			}
		}
		if name != "404.html" && len(ldPattern.FindAllString(page, -1)) < 3 {
			t.Errorf("%s: want at least 3 JSON-LD blocks", name)
		}
	}
}

func TestHTMLAndJSON(t *testing.T) {
	site := generateFixture(t, testConfig())
	checkPages(t, site)
	var doc jsonDoc
	if err := json.Unmarshal(site["data/modernize.json"], &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count != 4 || len(doc.Tasks) != 4 || doc.Tasks[0].Task != "AndroidBuild@1" {
		t.Errorf("unexpected data: %+v", doc)
	}
	if bytes.Contains(site["data/modernize.json"], []byte(`"verify"`)) {
		t.Error("modernize.json contains the opaque verify field")
	}
	if !strings.Contains(string(site["tasks/chef-1/index.html"]), "the &#34;Chef&#34; extension") {
		t.Error("catalog text is not HTML-escaped")
	}
}

func TestFeedAndRelatedLinksAreDeterministic(t *testing.T) {
	first := generateFixture(t, testConfig())
	second := generateFixture(t, testConfig())
	checkFeed(t, first["feed.xml"], len(fixtureCatalog().Modernizations))
	checkRelatedLinks(t, first)
	if !bytes.Equal(first["feed.xml"], second["feed.xml"]) {
		t.Error("feed.xml differs between runs")
	}
	for name := range first {
		if strings.HasPrefix(name, "tasks/") && !bytes.Equal(first[name], second[name]) {
			t.Errorf("related links in %s differ between runs", name)
		}
	}
}

func TestRelatedSelection(t *testing.T) {
	entries := []*entry{
		{Task: "Alpha@1", Name: "Alpha", Slug: "alpha-1", Class: "A", Replacement: []string{"Next@1"}},
		{Task: "Alpha@2", Name: "Alpha", Slug: "alpha-2", Class: "B", Replacement: []string{"Other@1"}},
		{Task: "Bravo@1", Name: "Bravo", Slug: "bravo-1", Class: "A", Replacement: []string{"Next@1"}},
		{Task: "Charlie@1", Name: "Charlie", Slug: "charlie-1", Class: "A"},
		{Task: "Delta@1", Name: "Delta", Slug: "delta-1", Class: "A"},
		{Task: "Echo@1", Name: "Echo", Slug: "echo-1", Class: "A"},
		{Task: "Foxtrot@1", Name: "Foxtrot", Slug: "foxtrot-1", Class: "A"},
		{Task: "Golf@1", Name: "Golf", Slug: "golf-1", Class: "A"},
		{Task: "Hotel@1", Name: "Hotel", Slug: "hotel-1", Class: "A"},
	}
	addRelated(entries)
	got := make([]string, 0, len(entries[0].Related))
	for _, related := range entries[0].Related {
		got = append(got, related.Task)
	}
	want := []string{"Alpha@2", "Bravo@1", "Charlie@1", "Delta@1", "Echo@1", "Foxtrot@1", "Golf@1"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("related tasks differ (-want +got):\n%s", diff)
	}
}

func TestFAQJSONLDMatchesVisibleText(t *testing.T) {
	sectionPattern := regexp.MustCompile(`(?s)<h2 id="faq">.*?</h2>(.*?)</section>`)
	pairPattern := regexp.MustCompile(`(?s)<h3>(.*?)</h3>\s*<p>(.*?)</p>`)
	for name, data := range generateFixture(t, testConfig()) {
		if !strings.HasPrefix(name, "tasks/") {
			continue
		}
		section := sectionPattern.FindStringSubmatch(string(data))
		if section == nil {
			t.Fatalf("%s has no visible FAQ", name)
		}
		visible := make([]faq, 0, 3)
		for _, pair := range pairPattern.FindAllStringSubmatch(section[1], -1) {
			visible = append(visible, faq{Question: html.UnescapeString(pair[1]), Answer: html.UnescapeString(pair[2])})
		}
		structured := make([]faq, 0, len(visible))
		for _, match := range ldPattern.FindAllStringSubmatch(string(data), -1) {
			var value map[string]any
			if err := json.Unmarshal([]byte(match[1]), &value); err != nil || value["@type"] != "FAQPage" {
				continue
			}
			for _, raw := range value["mainEntity"].([]any) {
				question := raw.(map[string]any)
				answer := question["acceptedAnswer"].(map[string]any)
				structured = append(structured, faq{Question: question["name"].(string), Answer: answer["text"].(string)})
			}
		}
		if diff := cmp.Diff(visible, structured, cmp.AllowUnexported(faq{})); diff != "" {
			t.Errorf("%s visible and JSON-LD FAQ differ (-visible +structured):\n%s", name, diff)
		}
	}
}

func TestVerificationMeta(t *testing.T) {
	without := generateFixture(t, testConfig())
	for name, data := range without {
		if strings.Contains(string(data), "site-verification") || strings.Contains(string(data), "msvalidate.01") {
			t.Errorf("%s has verification metadata without flags", name)
		}
	}
	cfg := testConfig()
	cfg.GoogleVerification = `google-token-&quot;`
	cfg.BingVerification = `bing-token-&quot;`
	with := generateFixture(t, cfg)
	index := string(with["index.html"])
	for _, want := range []string{`name="google-site-verification" content="google-token-&amp;quot;"`, `name="msvalidate.01" content="bing-token-&amp;quot;"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index does not contain %q", want)
		}
	}
	if strings.Contains(string(with["tasks/copyfiles-1/index.html"]), "site-verification") || strings.Contains(string(with["tasks/copyfiles-1/index.html"]), "msvalidate.01") {
		t.Error("task page contains index-only verification metadata")
	}
}

func TestWellFormedDetectsProblems(t *testing.T) {
	for page, want := range map[string]string{
		"<div><p></div>":  "unexpected </div>",
		"<div>":           "unclosed <div>",
		"</div>":          "unexpected </div>",
		"<div><br></div>": "",
	} {
		if got := wellFormed(page); got != want {
			t.Errorf("wellFormed(%q) = %q, want %q", page, got, want)
		}
	}
}

func TestDeterministic(t *testing.T) {
	first, second := generateFixture(t, testConfig()), generateFixture(t, testConfig())
	if len(first) != len(second) {
		t.Fatal("file sets differ")
	}
	for name, data := range first {
		if !bytes.Equal(data, second[name]) {
			t.Errorf("%s differs between runs", name)
		}
	}
	other := testConfig()
	other.Generated = "2026-11-01"
	if bytes.Equal(first["sitemap.xml"], generateFixture(t, other)["sitemap.xml"]) {
		t.Error("sitemap ignores the date flag")
	}
}

func TestRewriteFlag(t *testing.T) {
	cfg := testConfig()
	cfg.ToolAvailable = true
	planned := string(generateFixture(t, cfg)["tasks/copyfiles-1/index.html"])
	if !strings.Contains(planned, "Automatic rewrite is planned") {
		t.Error("class A page does not say rewrite is planned")
	}
	cfg.RewriteAvailable = true
	site := generateFixture(t, cfg)
	if !strings.Contains(string(site["tasks/copyfiles-1/index.html"]), "trasloco modernize --write rewrites this step") {
		t.Error("class A page ignores -rewrite-available")
	}
	if !strings.Contains(string(site["tasks/androidbuild-1/index.html"]), "needs a manual migration") {
		t.Error("class C page must stay manual")
	}
}

func TestToolAvailabilityFlag(t *testing.T) {
	without := generateFixture(t, testConfig())
	for name, data := range without {
		if strings.Contains(string(data), "trasloco modernize") {
			t.Errorf("%s mentions the unreleased command", name)
		}
		if strings.HasPrefix(name, "tasks/") && strings.HasSuffix(name, "/index.html") && strings.Contains(string(data), releasesURL) {
			t.Errorf("%s links to releases while the tool is unavailable", name)
		}
	}
	cfg := testConfig()
	cfg.ToolAvailable = true
	taskPages := 0
	for name, data := range generateFixture(t, cfg) {
		if !strings.HasPrefix(name, "tasks/") || !strings.HasSuffix(name, "/index.html") {
			continue
		}
		taskPages++
		page := string(data)
		if !strings.Contains(page, "trasloco modernize --check") {
			t.Errorf("%s does not mention the released command", name)
		}
		if !strings.Contains(page, releasesURL) {
			t.Errorf("%s has no installation link", name)
		}
	}
	if taskPages == 0 {
		t.Fatal("generated site has no task pages")
	}
}

func TestClassDWithReplacement(t *testing.T) {
	cat := fixtureCatalog()
	cat.Modernizations = append(cat.Modernizations, catalog.Modernization{
		Task: "AzureCloudPowerShellDeployment@2", GUID: "g-cloud", Class: "D",
		Replacement: []string{"AzurePowerShell@5"}, Action: "Move the deployment to a supported platform.", Sources: []string{testAnnouncement},
	})
	files, err := generate(cat, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := make(map[string][]byte, len(files))
	for _, file := range files {
		site[file.Path] = file.Data
	}
	page := string(site["tasks/azurecloudpowershelldeployment-2/index.html"])
	if strings.Contains(strings.ToLower(page), "replace it with") {
		t.Error("class D page presents its closest task as a replacement")
	}
	for name, text := range map[string]string{
		"page": page, "index": string(site["index.html"]), "llms": string(site["llms.txt"]),
		"llms-full": string(site["llms-full.txt"]), "json": string(site["data/modernize.json"]),
	} {
		if !strings.Contains(text, "retired platform or external tool: no drop-in replacement") {
			t.Errorf("%s does not use the class D label", name)
		}
	}
	if !strings.Contains(page, "Closest task:") || !strings.Contains(string(site["llms-full.txt"]), "Closest task after migration:") {
		t.Error("class D closest task is not qualified by the migration")
	}
}

func TestApplicationStructuredData(t *testing.T) {
	cfg := testConfig()
	cfg.ToolAvailable = true
	page := string(generateFixture(t, cfg)["index.html"])
	for _, match := range ldPattern.FindAllStringSubmatch(page, -1) {
		var value map[string]any
		if err := json.Unmarshal([]byte(match[1]), &value); err != nil {
			t.Fatal(err)
		}
		if value["@type"] != "SoftwareApplication" {
			continue
		}
		offers, ok := value["offers"].(map[string]any)
		if !ok || offers["price"] != "0" || offers["priceCurrency"] != "USD" {
			t.Fatalf("invalid offers: %#v", value["offers"])
		}
		if value["applicationCategory"] != "DeveloperApplication" || value["operatingSystem"] != "Windows, macOS, Linux" {
			t.Fatalf("invalid application metadata: %#v", value)
		}
		if value["downloadUrl"] != releasesURL {
			t.Fatalf("invalid release metadata: %#v", value)
		}
		description, _ := value["description"].(string)
		if !strings.Contains(description, "modernize --check") || !strings.Contains(description, "Automatic upgrades are planned") {
			t.Fatalf("description overstates the released tool: %q", description)
		}
		if _, exists := value["datePublished"]; exists {
			t.Error("SoftwareApplication contains datePublished")
		}
		return
	}
	t.Fatal("no SoftwareApplication JSON-LD")
}

func TestDescriptionLimit(t *testing.T) {
	cfg := testConfig()
	e := &entry{Task: strings.Repeat("X", 200) + "@1", Replacement: []string{"Y@2"}, ClassLabel: "manual migration"}
	got := describe(e, &cfg)
	if n := utf8.RuneCountInString(got); n != maxDescription || !strings.HasSuffix(got, "...") {
		t.Errorf("description = %d characters %q", n, got)
	}
	none := describe(&entry{Task: "Chef@1", ClassLabel: "x"}, &cfg)
	if !strings.Contains(none, "No replacement task") {
		t.Errorf("description = %q", none)
	}
}

func TestInvalidInput(t *testing.T) {
	good := func() Config { return testConfig() }
	bad := []struct {
		name   string
		mutate func(*Config)
		cat    *catalog.Catalog
	}{
		{name: "date", mutate: func(c *Config) { c.Generated = "yesterday" }},
		{name: "retirement date", mutate: func(c *Config) { c.RetirementDate = "" }},
		{name: "base url", mutate: func(c *Config) { c.BaseURL = "no-scheme" }},
		{name: "announcement", mutate: func(c *Config) { c.AnnouncementURL = "" }},
		{name: "task ref", cat: &catalog.Catalog{Modernizations: []catalog.Modernization{{Task: "NoMajor", Class: "A"}}}},
		{name: "major", cat: &catalog.Catalog{Modernizations: []catalog.Modernization{{Task: "A@x", Class: "A"}}}},
		{name: "class", cat: &catalog.Catalog{Modernizations: []catalog.Modernization{{Task: "A@1", Class: "Z"}}}},
		{name: "duplicate", cat: &catalog.Catalog{Modernizations: []catalog.Modernization{{Task: "A@1", Class: "A"}, {Task: "a@1", Class: "A"}}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			cat := tc.cat
			if cat == nil {
				cat = fixtureCatalog()
			}
			if _, err := generate(cat, cfg); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestRun(t *testing.T) {
	out := t.TempDir()
	var stderr bytes.Buffer
	args := []string{"-out", out, "-catalog", "../../catalog", "-assets", "../../docs/assets", "-date", "2026-10-12", "-google-site-verification", "google-cli", "-bing-site-verification", "bing-cli"}
	if err := run(context.Background(), args, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range []string{"index.html", "og-image.png", "favicon.svg", "data/modernize.json", "tasks/azurecli-0/index.html"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(name))); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="google-site-verification" content="google-cli"`, `name="msvalidate.01" content="bing-cli"`} {
		if !bytes.Contains(index, []byte(want)) {
			t.Errorf("CLI verification flag did not render %q", want)
		}
	}
	for name, args := range map[string][]string{
		"no date":     {"-out", out},
		"bad flag":    {"-nope"},
		"bad catalog": {"-out", out, "-catalog", out, "-date", "2026-10-12"},
		"bad assets":  {"-out", out, "-catalog", "../../catalog", "-assets", out, "-date", "2026-10-12"},
		"bad date":    {"-out", out, "-catalog", "../../catalog", "-assets", "../../docs/assets", "-date", "x"},
		"unwritable":  {"-out", filepath.Join(out, "index.html", "x"), "-catalog", "../../catalog", "-assets", "../../docs/assets", "-date", "2026-10-12"},
	} {
		if err := run(context.Background(), args, &stderr); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReadHeaderErrors(t *testing.T) {
	if _, err := readHeader(fstest.MapFS{}); err == nil {
		t.Error("want a read error")
	}
	bad := fstest.MapFS{"azure-pipelines/modernize.yaml": {Data: []byte("tasks: [unterminated")}}
	if _, err := readHeader(bad); err == nil {
		t.Error("want a decode error")
	}
}
