package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize/program"
	catalogschema "github.com/HarnageaGabriel/trasloco/schemas/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const schemaURL = "https://github.com/HarnageaGabriel/trasloco/schemas/catalog/v1.schema.json"

// Catalog is an immutable loaded catalog.
type Catalog struct {
	Tasks          []Task
	Retirements    []RetiredTask
	Modernizations []Modernization
	Mappings       []Mapping
	Actions        []LockedAction
	taskByName     map[string]*Task
	taskByID       map[string]*Task
	retired        map[string]*RetiredTask
	modernization  map[string]*Modernization
	mapping        map[string]*Mapping
	action         map[string]*LockedAction
}

// Load reads all optional catalog files from fsys, validates them, and builds indexes.
func Load(ctx context.Context, fsys fs.FS, collector *diag.Collector) (*Catalog, error) {
	c := &Catalog{}
	if err := ctx.Err(); err != nil {
		return c, err
	}
	var failures []error
	loadOne := func(name string, dst any) bool {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			return false
		}
		data, err := fs.ReadFile(fsys, name)
		if errors.Is(err, fs.ErrNotExist) {
			return false
		}
		if err != nil {
			failures = append(failures, catalogError(name, fmt.Sprintf("read catalog: %v", err), collector))
			return false
		}
		if err := validateAndDecode(data, dst); err != nil {
			failures = append(failures, catalogError(name, err.Error(), collector))
			return false
		}
		return true
	}
	var tasks TasksFile
	if loadOne("azure-pipelines/tasks.generated.yaml", &tasks) {
		c.Tasks = tasks.Tasks
	}
	var retirement RetirementFile
	if loadOne("azure-pipelines/tasks.retirement.yaml", &retirement) {
		c.Retirements = retirement.Tasks
	}
	var modernization ModernizationFile
	if loadOne("azure-pipelines/modernize.yaml", &modernization) {
		c.Modernizations = modernization.Tasks
	}
	var lock ActionsLock
	if loadOne("github-actions/actions.lock.yaml", &lock) {
		c.Actions = lock.Actions
		for _, action := range lock.Actions {
			if !RuntimeAllowed(action.RunsUsing) {
				failures = append(failures, runtimeError("github-actions/actions.lock.yaml", action, collector))
			}
		}
	}
	var mappingFiles []string
	if err := fs.WalkDir(fsys, "azure-pipelines/mappings", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return err
		}
		if !entry.IsDir() && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			mappingFiles = append(mappingFiles, name)
		}
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		failures = append(failures, catalogError("azure-pipelines/mappings", err.Error(), collector))
	}
	sort.Strings(mappingFiles)
	for _, name := range mappingFiles {
		var mapping Mapping
		if loadOne(name, &mapping) {
			c.Mappings = append(c.Mappings, mapping)
		}
	}
	failures = append(failures, c.buildIndexes(collector)...)
	if err := ctx.Err(); err != nil {
		return c, err
	}
	return c, errors.Join(failures...)
}

func compiledSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(catalogschema.V1))
	if err != nil {
		return nil, fmt.Errorf("parse catalog schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("add catalog schema: %w", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile catalog schema: %w", err)
	}
	return schema, nil
}

func validateAndDecode(data []byte, dst any) error {
	var yamlDoc any
	if err := yaml.Unmarshal(data, &yamlDoc); err != nil {
		return fmt.Errorf("decode YAML: %w", err)
	}
	yamlDoc = normalizeYAML(yamlDoc)
	jsonData, err := json.Marshal(yamlDoc)
	if err != nil {
		return fmt.Errorf("convert YAML to JSON: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("decode JSON instance: %w", err)
	}
	schema, err := compiledSchema()
	if err != nil {
		return err
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("schema validation: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("strict decode: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple YAML documents")
		}
		return fmt.Errorf("decode trailing YAML: %w", err)
	}
	return nil
}

func normalizeYAML(value any) any {
	switch typed := value.(type) {
	case time.Time:
		if typed.Hour() == 0 && typed.Minute() == 0 && typed.Second() == 0 && typed.Nanosecond() == 0 {
			return typed.Format(time.DateOnly)
		}
		return typed.Format(time.RFC3339Nano)
	case []any:
		for i := range typed {
			typed[i] = normalizeYAML(typed[i])
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeYAML(item)
		}
		return typed
	default:
		return value
	}
}

func (c *Catalog) buildIndexes(collector *diag.Collector) []error {
	c.taskByName = make(map[string]*Task, len(c.Tasks))
	c.taskByID = make(map[string]*Task, len(c.Tasks))
	c.retired = make(map[string]*RetiredTask, len(c.Retirements))
	c.modernization = make(map[string]*Modernization, len(c.Modernizations))
	c.mapping = make(map[string]*Mapping, len(c.Mappings))
	c.action = make(map[string]*LockedAction, len(c.Actions))
	var failures []error
	for i := range c.Tasks {
		task := &c.Tasks[i]
		nameKey, idKey := fold(task.Name), fold(task.ID)
		if _, exists := c.taskByName[nameKey]; exists {
			failures = append(failures, catalogError("azure-pipelines/tasks.generated.yaml", "duplicate task name "+task.Name, collector))
		} else {
			c.taskByName[nameKey] = task
		}
		if previous, exists := c.taskByID[idKey]; !exists || fold(task.Name) < fold(previous.Name) {
			// The official catalog contains renamed task aliases that intentionally
			// share an ID. Keep GUID lookup deterministic without collapsing names.
			c.taskByID[idKey] = task
		}
		majors := make(map[int]struct{}, len(task.Versions))
		for _, version := range task.Versions {
			if _, exists := majors[version.Major]; exists {
				failures = append(failures, catalogError("azure-pipelines/tasks.generated.yaml", fmt.Sprintf("duplicate task major %s@%d", task.Name, version.Major), collector))
			}
			majors[version.Major] = struct{}{}
		}
	}
	for i := range c.Retirements {
		entry := &c.Retirements[i]
		name, major, ok := splitRef(entry.Task)
		if !ok || !strings.EqualFold(entry.Task, entry.Name+"@"+strconv.Itoa(entry.Major)) {
			failures = append(failures, catalogError("azure-pipelines/tasks.retirement.yaml", "retirement task disagrees with name and major: "+entry.Task, collector))
			continue
		}
		key := refKey(name, major)
		if _, exists := c.retired[key]; exists {
			failures = append(failures, catalogError("azure-pipelines/tasks.retirement.yaml", "duplicate retirement "+entry.Task, collector))
		} else {
			c.retired[key] = entry
		}
	}
	for i := range c.Modernizations {
		entry := &c.Modernizations[i]
		if _, err := program.Parse(entry.Program, entry.Class); err != nil {
			failures = append(failures, catalogError("azure-pipelines/modernize.yaml", fmt.Sprintf("invalid modernization program for %s (class %s): %v", entry.Task, entry.Class, err), collector))
		}
		name, major, ok := splitRef(entry.Task)
		if !ok {
			continue
		}
		key := refKey(name, major)
		if _, exists := c.modernization[key]; exists {
			failures = append(failures, catalogError("azure-pipelines/modernize.yaml", "duplicate modernization "+entry.Task, collector))
		} else {
			c.modernization[key] = entry
		}
	}
	failures = append(failures, c.checkModernizations(collector)...)
	for i := range c.Mappings {
		mapping := &c.Mappings[i]
		name, major, ok := splitRef(mapping.Task)
		key := refKey(name, major)
		if !ok {
			continue
		}
		if _, exists := c.mapping[key]; exists {
			failures = append(failures, catalogError(path.Join("azure-pipelines/mappings", mapping.Task), "duplicate mapping "+mapping.Task, collector))
			continue
		}
		c.mapping[key] = mapping
		if len(c.Tasks) == 0 {
			continue
		}
		task, found := c.ResolveTask(name)
		if !found {
			failures = append(failures, catalogError("azure-pipelines/mappings", "mapping task does not exist: "+mapping.Task, collector))
			continue
		}
		version, found := task.Version(major)
		if !found {
			failures = append(failures, catalogError("azure-pipelines/mappings", "mapping task major does not exist: "+mapping.Task, collector))
			continue
		}
		for _, input := range mapping.Inputs {
			if _, found := version.ResolveInput(input.From); !found {
				failures = append(failures, catalogError("azure-pipelines/mappings", fmt.Sprintf("mapping input %q does not exist in %s", input.From, mapping.Task), collector))
			}
		}
	}
	for i := range c.Actions {
		action := &c.Actions[i]
		key := fold(action.Name)
		if _, exists := c.action[key]; exists {
			failures = append(failures, catalogError("github-actions/actions.lock.yaml", "duplicate action "+action.Name, collector))
		} else {
			c.action[key] = action
		}
	}
	return failures
}

func (c *Catalog) checkModernizations(collector *diag.Collector) []error {
	if len(c.Retirements) == 0 && len(c.Modernizations) == 0 {
		return nil
	}
	var failures []error
	retiredKeys := make([]string, 0, len(c.retired))
	for key := range c.retired {
		retiredKeys = append(retiredKeys, key)
	}
	sort.Strings(retiredKeys)
	for _, key := range retiredKeys {
		retired := c.retired[key]
		modernization, ok := c.modernization[key]
		if !ok {
			failures = append(failures, catalogError("azure-pipelines/modernize.yaml", "missing modernization for "+retired.Task, collector))
			continue
		}
		if !equalFoldRefs(modernization.Replacement, retired.Replacements) {
			failures = append(failures, catalogError("azure-pipelines/modernize.yaml", "replacement disagrees with retirement for "+retired.Task, collector))
		}
	}
	modernizationKeys := make([]string, 0, len(c.modernization))
	for key := range c.modernization {
		modernizationKeys = append(modernizationKeys, key)
	}
	sort.Strings(modernizationKeys)
	for _, key := range modernizationKeys {
		modernization := c.modernization[key]
		if _, ok := c.retired[key]; !ok {
			failures = append(failures, catalogError("azure-pipelines/modernize.yaml", "modernization is not retired: "+modernization.Task, collector))
		}
		for _, replacement := range modernization.Replacement {
			name, major, ok := splitRef(replacement)
			if !ok {
				continue
			}
			task, found := c.ResolveTask(name)
			if !found {
				// AzureLoadTest is an extension task cited by the approved spec and is
				// intentionally absent from the pinned built-in task catalog.
				if key == "quickperftest@1" && fold(replacement) == "azureloadtest@1" {
					continue
				}
				failures = append(failures, catalogError("azure-pipelines/modernize.yaml", "replacement task does not exist: "+replacement, collector))
				continue
			}
			if _, found := task.Version(major); !found {
				failures = append(failures, catalogError("azure-pipelines/modernize.yaml", "replacement task major does not exist: "+replacement, collector))
			}
		}
	}
	return failures
}

func equalFoldRefs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	want := make(map[string]int, len(right))
	for _, ref := range right {
		want[fold(ref)]++
	}
	for _, ref := range left {
		key := fold(ref)
		if want[key] == 0 {
			return false
		}
		want[key]--
	}
	return true
}

func catalogError(file, message string, collector *diag.Collector) error {
	finding := diag.MustNew("TRL-CAT-0005", diag.WithMessage(message), diag.WithSource(diag.Source{File: file}))
	if collector != nil {
		collector.Add(finding)
	}
	return &diag.Error{Finding: finding}
}

func runtimeError(file string, action LockedAction, collector *diag.Collector) error {
	finding := diag.MustNew("TRL-CAT-0004", diag.WithMessage(fmt.Sprintf("action %s uses disallowed runtime %s", action.Name, action.RunsUsing)), diag.WithSource(diag.Source{File: file}))
	if collector != nil {
		collector.Add(finding)
	}
	return &diag.Error{Finding: finding}
}

// ResolveTask resolves a task name, Name@major reference, or GUID case-insensitively.
func (c *Catalog) ResolveTask(ref string) (*Task, bool) {
	base := ref
	if name, _, ok := splitRef(ref); ok {
		base = name
	}
	if task, ok := c.taskByName[fold(base)]; ok {
		return task, true
	}
	task, ok := c.taskByID[fold(base)]
	return task, ok
}

// Version returns the task version for major.
func (t *Task) Version(major int) (*TaskVersion, bool) {
	for i := range t.Versions {
		if t.Versions[i].Major == major {
			return &t.Versions[i], true
		}
	}
	return nil, false
}

// ResolveInput resolves an input name or alias case-insensitively.
func (v *TaskVersion) ResolveInput(nameOrAlias string) (*Input, bool) {
	key := fold(nameOrAlias)
	for i := range v.Inputs {
		input := &v.Inputs[i]
		if fold(input.Name) == key {
			return input, true
		}
		for _, alias := range input.Aliases {
			if fold(alias) == key {
				return input, true
			}
		}
	}
	return nil, false
}

// LatestMajor returns the highest usable major for name.
func (c *Catalog) LatestMajor(name string) (int, bool) {
	task, ok := c.ResolveTask(name)
	if !ok {
		return 0, false
	}
	major, found := 0, false
	for _, version := range task.Versions {
		if version.Deprecated || version.Preview {
			continue
		}
		if _, retired := c.retired[refKey(task.Name, version.Major)]; retired {
			continue
		}
		if !found || version.Major > major {
			major, found = version.Major, true
		}
	}
	return major, found
}

// Retirement returns retirement metadata for a task major.
func (c *Catalog) Retirement(name string, major int) (*RetiredTask, bool) {
	entry, ok := c.retired[refKey(name, major)]
	return entry, ok
}

// Modernization returns modernization guidance for a task major.
func (c *Catalog) Modernization(name string, major int) (*Modernization, bool) {
	entry, ok := c.modernization[refKey(name, major)]
	return entry, ok
}

// Mapping returns the hand-written mapping for a task major.
func (c *Catalog) Mapping(name string, major int) (*Mapping, bool) {
	entry, ok := c.mapping[refKey(name, major)]
	return entry, ok
}

// Action returns a pinned allowed action by case-insensitive name.
func (c *Catalog) Action(name string) (*LockedAction, bool) {
	entry, ok := c.action[fold(name)]
	return entry, ok
}

func splitRef(ref string) (string, int, bool) {
	separator := strings.LastIndexByte(ref, '@')
	if separator <= 0 || separator == len(ref)-1 {
		return "", 0, false
	}
	major, err := strconv.Atoi(ref[separator+1:])
	return ref[:separator], major, err == nil && major >= 0
}

func refKey(name string, major int) string { return fold(name) + "@" + strconv.Itoa(major) }
func fold(value string) string             { return strings.ToLower(value) }
