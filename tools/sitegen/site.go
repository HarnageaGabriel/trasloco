package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
)

//go:embed templates/*.tmpl assets/style.css
var embedded embed.FS

const (
	repoURL        = "https://github.com/HarnageaGabriel/trasloco"
	siteName       = "trasloco"
	maxDescription = 155
	dateLayout     = "2006-01-02"
)

// Config carries every input of the generator that does not come from the catalog rows.
type Config struct {
	RetirementDate  string
	RemovalDate     string
	AnnouncementURL string
	// Generated is the data generation date (YYYY-MM-DD), the only date the output depends on.
	Generated string
	BaseURL   string
	// RewriteAvailable states whether "trasloco modernize --write" ships; pages say "planned" until it does.
	RewriteAvailable bool

	basePath string
}

type file struct {
	Path string
	Data []byte
}

type classInfo struct {
	Letter string
	Slug   string
	Label  string
}

func classList() []classInfo {
	return []classInfo{
		{"A", "drop-in-replacement", "drop-in replacement"},
		{"B", "replacement-with-input-changes", "replacement with input changes"},
		{"C", "manual-migration", "manual migration"},
		{"D", "no-replacement", "no replacement: platform retired or external tool"},
	}
}

func classByLetter(letter string) (classInfo, bool) {
	for _, info := range classList() {
		if info.Letter == letter {
			return info, true
		}
	}
	return classInfo{}, false
}

type sourceLink struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

type faq struct {
	Question string
	Answer   string
}

type example struct {
	Before string
	After  string
}

type entry struct {
	Task            string
	Name            string
	Major           int
	Slug            string
	Path            string
	URL             string
	GUID            string
	Class           string
	ClassLabel      string
	ClassSlug       string
	Action          string
	Replacement     []string
	ReplacementKind string
	Sources         []sourceLink
	Verify          []string
	Auto            string
	AutoText        string
	RemovedText     string
	ReplacementText string
	Title           string
	Heading         string
	Description     string
	Grep            string
	Example         *example
	FAQ             []faq
}

type classGroup struct {
	classInfo
	Count   int
	Entries []*entry
}

type headData struct {
	Title       string
	Description string
	Canonical   string
	OGType      string
	OGImage     string
	NoIndex     bool
	Base        string
	LD          []string
}

type pageData struct {
	Head    headData
	Base    string
	Cfg     *Config
	E       *entry
	Entries []*entry
	Groups  []classGroup
	Total   int
	FAQ     []faq
}

func (c *Config) normalize() error {
	for _, field := range [][2]string{{"retirement date", c.RetirementDate}, {"removal date", c.RemovalDate}, {"generation date", c.Generated}} {
		if _, err := time.Parse(dateLayout, field[1]); err != nil {
			return fmt.Errorf("invalid %s %q: want YYYY-MM-DD", field[0], field[1])
		}
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid base URL %q", c.BaseURL)
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	c.basePath = strings.TrimRight(parsed.Path, "/")
	if c.AnnouncementURL == "" {
		return errors.New("missing announcement URL")
	}
	return nil
}

func (c *Config) abs(p string) string { return c.BaseURL + p }

// buildEntries derives one entry per modernization row, sorted by task name then major.
func buildEntries(cat *catalog.Catalog, cfg *Config) ([]*entry, error) {
	kinds := make(map[string]string, len(cat.Retirements))
	for _, retired := range cat.Retirements {
		kinds[strings.ToLower(retired.Task)] = retired.ReplacementKind
	}
	entries := make([]*entry, 0, len(cat.Modernizations))
	seen := make(map[string]bool, len(cat.Modernizations))
	for i := range cat.Modernizations {
		row := &cat.Modernizations[i]
		at := strings.LastIndex(row.Task, "@")
		if at <= 0 {
			return nil, fmt.Errorf("task %q is not name@major", row.Task)
		}
		major, err := strconv.Atoi(row.Task[at+1:])
		if err != nil {
			return nil, fmt.Errorf("task %q has an invalid major: %w", row.Task, err)
		}
		info, ok := classByLetter(row.Class)
		if !ok {
			return nil, fmt.Errorf("task %q has unknown class %q", row.Task, row.Class)
		}
		name := row.Task[:at]
		slug := strings.ToLower(name) + "-" + strconv.Itoa(major)
		if seen[slug] {
			return nil, fmt.Errorf("duplicate task page %q", slug)
		}
		seen[slug] = true
		e := &entry{
			Task:            row.Task,
			Name:            name,
			Major:           major,
			Slug:            slug,
			Path:            "/tasks/" + slug + "/",
			GUID:            row.GUID,
			Class:           row.Class,
			ClassLabel:      info.Label,
			ClassSlug:       info.Slug,
			Action:          row.Action,
			Replacement:     append([]string{}, row.Replacement...),
			ReplacementKind: kinds[strings.ToLower(row.Task)],
			Verify:          append([]string{}, row.Verify...),
		}
		e.URL = cfg.abs(e.Path)
		for _, source := range row.Sources {
			e.Sources = append(e.Sources, sourceLink{URL: source, Label: sourceLabel(source, cfg.AnnouncementURL)})
		}
		fillFacts(e, cfg)
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a != b {
			return a < b
		}
		return entries[i].Major < entries[j].Major
	})
	return entries, nil
}

func sourceLabel(source, announcement string) string {
	switch {
	case source == announcement:
		return "Official task retirement announcement"
	case strings.HasSuffix(source, "/task.json") && strings.Contains(source, "/Tasks/"):
		return "Task definition: " + path.Base(path.Dir(source)) + " (task.json)"
	default:
		return source
	}
}

func joinAnd(items []string) string { return strings.Join(items, " and ") }

func fillFacts(e *entry, cfg *Config) {
	repl := joinAnd(e.Replacement)
	e.RemovedText = fmt.Sprintf("%s: retirement date %s, removal date %s. Pipelines that still use it fail after removal.", e.Task, cfg.RetirementDate, cfg.RemovalDate)
	if repl != "" {
		e.ReplacementText = "Replace " + e.Task + " with " + repl + "."
		e.Title = fmt.Sprintf("%s is retired: replace it with %s | %s", e.Task, repl, siteName)
		e.Heading = fmt.Sprintf("%s is retired: replace it with %s", e.Task, repl)
	} else {
		e.ReplacementText = "There is no replacement task for " + e.Task + ". " + e.Action
		e.Title = fmt.Sprintf("%s is retired: what to do | %s", e.Task, siteName)
		e.Heading = e.Task + " is retired: what to do"
	}
	switch e.Class {
	case "A", "B":
		if cfg.RewriteAvailable {
			e.Auto = "available"
			e.AutoText = "Yes. trasloco modernize --write rewrites this step to " + repl + "."
		} else {
			e.Auto = "planned"
			e.AutoText = "Not yet. Automatic rewrite is planned for this kind of task (" + e.ClassLabel + "). Today, trasloco modernize --check finds the step and you change it by hand."
		}
	default:
		e.Auto = "manual"
		e.AutoText = "No. This task needs a manual migration. trasloco modernize --check still finds the step."
	}
	e.Description = describe(e, cfg)
	e.Grep = "grep -rniwF '" + e.Task + "' --include='*.yml' --include='*.yaml' ."
	if e.Class == "A" && len(e.Replacement) == 1 {
		e.Example = &example{Before: "- task: " + e.Task, After: "- task: " + e.Replacement[0]}
	}
	e.FAQ = []faq{
		{"When is " + e.Task + " removed?", e.RemovedText},
		{"What replaces " + e.Task + "?", e.ReplacementText},
		{"Can trasloco fix " + e.Task + " automatically?", e.AutoText},
	}
}

func describe(e *entry, cfg *Config) string {
	base := fmt.Sprintf("%s retired %s, removed %s.", e.Task, cfg.RetirementDate, cfg.RemovalDate)
	var candidates []string
	if len(e.Replacement) > 0 {
		repl := joinAnd(e.Replacement)
		candidates = []string{
			fmt.Sprintf("%s Replace it with %s (%s). Find it with trasloco modernize --check.", base, repl, e.ClassLabel),
			fmt.Sprintf("%s Replace it with %s. Find it with trasloco.", base, repl),
			fmt.Sprintf("%s Replace it with %s.", base, repl),
		}
	} else {
		candidates = []string{
			fmt.Sprintf("%s No replacement task: read what to do. Find it with trasloco modernize --check.", base),
			fmt.Sprintf("%s No replacement task: read what to do.", base),
		}
	}
	for _, candidate := range candidates {
		if utf8.RuneCountInString(candidate) <= maxDescription {
			return candidate
		}
	}
	runes := []rune(candidates[len(candidates)-1])
	return string(runes[:maxDescription-3]) + "..."
}

func groupEntries(entries []*entry) []classGroup {
	groups := make([]classGroup, 0, len(classList()))
	for _, info := range classList() {
		group := classGroup{classInfo: info}
		for _, e := range entries {
			if e.Class == info.Letter {
				group.Entries = append(group.Entries, e)
			}
		}
		group.Count = len(group.Entries)
		groups = append(groups, group)
	}
	return groups
}

func parseTemplates() (*template.Template, error) {
	funcs := template.FuncMap{"h": html.EscapeString, "join": strings.Join}
	return template.New("site").Funcs(funcs).ParseFS(embedded, "templates/*.tmpl")
}

func render(tmpl *template.Template, name string, data *pageData) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func marshalLD(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Config) person() map[string]any {
	return map[string]any{"@type": "Organization", "name": siteName, "url": repoURL}
}

func (c *Config) breadcrumb(items ...[2]string) map[string]any {
	list := make([]any, 0, len(items))
	for i, item := range items {
		list = append(list, map[string]any{"@type": "ListItem", "position": i + 1, "name": item[0], "item": item[1]})
	}
	return map[string]any{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": list}
}

func (c *Config) article(headline, description, canonical string) map[string]any {
	return map[string]any{
		"@context":         "https://schema.org",
		"@type":            "TechArticle",
		"headline":         headline,
		"description":      description,
		"inLanguage":       "en",
		"datePublished":    c.Generated,
		"dateModified":     c.Generated,
		"mainEntityOfPage": canonical,
		"author":           c.person(),
		"publisher":        c.person(),
	}
}

func faqLD(items []faq) map[string]any {
	list := make([]any, 0, len(items))
	for _, item := range items {
		list = append(list, map[string]any{
			"@type":          "Question",
			"name":           item.Question,
			"acceptedAnswer": map[string]any{"@type": "Answer", "text": item.Answer},
		})
	}
	return map[string]any{"@context": "https://schema.org", "@type": "FAQPage", "mainEntity": list}
}

func (c *Config) application() map[string]any {
	return map[string]any{
		"@context":            "https://schema.org",
		"@type":               "SoftwareApplication",
		"name":                siteName,
		"applicationCategory": "DeveloperApplication",
		"operatingSystem":     "Windows, macOS, Linux",
		"description":         "Open-source command-line tool that migrates CI/CD pipelines between platforms and finds Azure Pipelines tasks that are being retired.",
		"url":                 c.abs("/"),
		"codeRepository":      repoURL,
		"license":             "https://www.apache.org/licenses/LICENSE-2.0",
	}
}

func buildLD(values ...map[string]any) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, err := marshalLD(value)
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}

// generate renders the whole site. The result is sorted by path and depends only on its inputs.
func generate(cat *catalog.Catalog, cfg Config) ([]file, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	entries, err := buildEntries(cat, &cfg)
	if err != nil {
		return nil, err
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	var files []file
	add := func(p string, data []byte) { files = append(files, file{Path: p, Data: data}) }
	ogImage := cfg.abs("/og-image.png")

	groups := groupEntries(entries)
	indexTitle := fmt.Sprintf("Azure Pipelines task retirement (%s) and removal (%s): all %d tasks and replacements", cfg.RetirementDate, cfg.RemovalDate, len(entries))
	indexDesc := fmt.Sprintf("Azure Pipelines tasks retire on %s and are removed on %s. Look up each of the %d tasks, its replacement and how to find it.", cfg.RetirementDate, cfg.RemovalDate, len(entries))
	indexFAQ := []faq{
		{"When are the Azure Pipelines tasks retired and removed?", fmt.Sprintf("They are retired on %s and removed on %s. Pipelines that still use a removed task fail.", cfg.RetirementDate, cfg.RemovalDate)},
		{"How many task versions does this site cover?", fmt.Sprintf("%d task versions, one page each, with their replacement and the migration effort.", len(entries))},
	}
	indexLD, err := buildLD(
		cfg.article(indexTitle, indexDesc, cfg.abs("/")),
		faqLD(indexFAQ),
		cfg.breadcrumb([2]string{"trasloco: retired Azure Pipelines tasks", cfg.abs("/")}),
		cfg.application(),
	)
	if err != nil {
		return nil, err
	}
	index, err := render(tmpl, "index.tmpl", &pageData{
		Head:    headData{Title: indexTitle, Description: indexDesc, Canonical: cfg.abs("/"), OGType: "website", OGImage: ogImage, Base: cfg.basePath, LD: indexLD},
		Base:    cfg.basePath,
		Cfg:     &cfg,
		Entries: entries,
		Groups:  groups,
		Total:   len(entries),
		FAQ:     indexFAQ,
	})
	if err != nil {
		return nil, err
	}
	add("index.html", index)

	for _, e := range entries {
		ld, err := buildLD(
			cfg.article(e.Heading, e.Description, e.URL),
			faqLD(e.FAQ),
			cfg.breadcrumb([2]string{"Retired Azure Pipelines tasks", cfg.abs("/")}, [2]string{e.Task, e.URL}),
		)
		if err != nil {
			return nil, err
		}
		page, err := render(tmpl, "task.tmpl", &pageData{
			Head: headData{Title: e.Title, Description: e.Description, Canonical: e.URL, OGType: "article", OGImage: ogImage, Base: cfg.basePath, LD: ld},
			Base: cfg.basePath,
			Cfg:  &cfg,
			E:    e,
			FAQ:  e.FAQ,
		})
		if err != nil {
			return nil, err
		}
		add("tasks/"+e.Slug+"/index.html", page)
	}

	notFound, err := render(tmpl, "notfound.tmpl", &pageData{
		Head: headData{Title: "Page not found | " + siteName, Description: "This page does not exist. Browse all retired Azure Pipelines tasks.", NoIndex: true, Base: cfg.basePath},
		Base: cfg.basePath,
		Cfg:  &cfg,
	})
	if err != nil {
		return nil, err
	}
	add("404.html", notFound)

	data, err := dataJSON(entries, &cfg)
	if err != nil {
		return nil, err
	}
	add("data/modernize.json", data)
	add("llms.txt", []byte(llmsTxt(entries, &cfg)))
	add("llms-full.txt", []byte(llmsFull(entries, &cfg)))
	add("sitemap.xml", []byte(sitemap(entries, &cfg)))
	add("robots.txt", []byte("User-agent: *\nAllow: /\n\nSitemap: "+cfg.abs("/sitemap.xml")+"\n"))
	css, err := embedded.ReadFile("assets/style.css")
	if err != nil {
		return nil, err
	}
	add("style.css", css)

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

type jsonTask struct {
	Task             string       `json:"task"`
	Name             string       `json:"name"`
	Major            int          `json:"major"`
	GUID             string       `json:"guid"`
	Class            string       `json:"class"`
	ClassLabel       string       `json:"classLabel"`
	Replacement      []string     `json:"replacement"`
	ReplacementKind  string       `json:"replacementKind,omitempty"`
	Action           string       `json:"action"`
	AutomaticRewrite string       `json:"automaticRewrite"`
	Sources          []sourceLink `json:"sources"`
	Verify           []string     `json:"verify"`
	Page             string       `json:"page"`
}

type jsonDoc struct {
	SchemaVersion  int        `json:"schemaVersion"`
	RetirementDate string     `json:"retirementDate"`
	RemovalDate    string     `json:"removalDate"`
	Announcement   string     `json:"announcement"`
	Generated      string     `json:"generated"`
	Count          int        `json:"count"`
	Tasks          []jsonTask `json:"tasks"`
}

func dataJSON(entries []*entry, cfg *Config) ([]byte, error) {
	doc := jsonDoc{
		SchemaVersion:  1,
		RetirementDate: cfg.RetirementDate,
		RemovalDate:    cfg.RemovalDate,
		Announcement:   cfg.AnnouncementURL,
		Generated:      cfg.Generated,
		Count:          len(entries),
		Tasks:          make([]jsonTask, 0, len(entries)),
	}
	for _, e := range entries {
		doc.Tasks = append(doc.Tasks, jsonTask{
			Task: e.Task, Name: e.Name, Major: e.Major, GUID: e.GUID, Class: e.Class, ClassLabel: e.ClassLabel,
			Replacement: e.Replacement, ReplacementKind: e.ReplacementKind, Action: e.Action, AutomaticRewrite: e.Auto,
			Sources: e.Sources, Verify: e.Verify, Page: e.URL,
		})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func llmsTxt(entries []*entry, cfg *Config) string {
	var b strings.Builder
	b.WriteString("# trasloco: retired Azure Pipelines tasks\n\n")
	fmt.Fprintf(&b, "> Azure Pipelines tasks are retired on %s and removed on %s. This site lists %d retired task versions, their replacements and how to find them. Generated from the trasloco catalog; last verified %s.\n\n", cfg.RetirementDate, cfg.RemovalDate, len(entries), cfg.Generated)
	b.WriteString("Each task page answers: when it is removed, what replaces it, and whether trasloco can rewrite it automatically.\n\n")
	b.WriteString("## Start here\n\n")
	fmt.Fprintf(&b, "- [All retired tasks](%s): table of every task and its replacement\n", cfg.abs("/"))
	fmt.Fprintf(&b, "- [Official announcement](%s): Microsoft task retirement page\n\n", cfg.AnnouncementURL)
	b.WriteString("## Retired tasks\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- [%s](%s): %s\n", e.Task, e.URL, oneLine(e))
	}
	b.WriteString("\n## Data\n\n")
	fmt.Fprintf(&b, "- [modernize.json](%s): every row as JSON\n", cfg.abs("/data/modernize.json"))
	fmt.Fprintf(&b, "- [llms-full.txt](%s): all entries as plain Markdown facts\n", cfg.abs("/llms-full.txt"))
	fmt.Fprintf(&b, "- [Source code](%s): trasloco repository\n", repoURL)
	return b.String()
}

func oneLine(e *entry) string {
	if len(e.Replacement) > 0 {
		return "replace with " + joinAnd(e.Replacement) + " (" + e.ClassLabel + ")"
	}
	return e.ClassLabel
}

func llmsFull(entries []*entry, cfg *Config) string {
	var b strings.Builder
	b.WriteString("# trasloco: retired Azure Pipelines tasks (full facts)\n\n")
	fmt.Fprintf(&b, "Retired on %s, removed on %s. %d task versions. Last verified %s.\n", cfg.RetirementDate, cfg.RemovalDate, len(entries), cfg.Generated)
	for _, e := range entries {
		b.WriteString("\n## " + e.Task + "\n\n")
		b.WriteString("- Page: " + e.URL + "\n")
		b.WriteString("- Retired: " + cfg.RetirementDate + "\n")
		b.WriteString("- Removed: " + cfg.RemovalDate + "\n")
		if len(e.Replacement) > 0 {
			b.WriteString("- Replacement: " + joinAnd(e.Replacement) + "\n")
		} else {
			b.WriteString("- Replacement: none\n")
		}
		b.WriteString("- Migration: " + e.ClassLabel + "\n")
		b.WriteString("- Automatic rewrite by trasloco: " + e.AutoText + "\n")
		b.WriteString("- Action: " + e.Action + "\n")
		b.WriteString("- Find it: `" + e.Grep + "`\n")
		b.WriteString("- Sources:\n")
		for _, source := range e.Sources {
			b.WriteString("  - " + source.URL + "\n")
		}
	}
	return b.String()
}

func sitemap(entries []*entry, cfg *Config) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	write := func(loc string) {
		fmt.Fprintf(&b, "  <url><loc>%s</loc><lastmod>%s</lastmod></url>\n", html.EscapeString(loc), cfg.Generated)
	}
	write(cfg.abs("/"))
	for _, e := range entries {
		write(e.URL)
	}
	b.WriteString("</urlset>\n")
	return b.String()
}
