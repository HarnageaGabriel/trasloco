package emit

import (
	"fmt"
	"strconv"
	"strings"

	goyaml "go.yaml.in/yaml/v3"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/mapping"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// Fixed key order of the model structs (emit-gha.md 4.1); the writer records
// output elements in this order. The owner maps below are only looked up.
var (
	jobKeyOrder  = []string{"name", "permissions", "needs", "if", "runs-on", "timeout-minutes", "continue-on-error", "env"}
	stepKeyOrder = []string{"name", "if", "uses", "with", "shell", "working-directory", "env", "timeout-minutes", "continue-on-error", "run"}
)

// owned is a merge target: the qualified node ID that owns each scalar key.
type owned map[string]string

// mergedMap holds one dynamic mapping field merged key-wise. keys keeps the
// first-occurrence order; entries is only looked up, never iterated (INV-01).
type mergedMap struct {
	keys    []string
	entries map[string]mergedEntry
}

type mergedEntry struct {
	node  string
	value *goyaml.Node
}

// put merges one entry: a rewrite by the same node replaces the value, a key
// written by a different node is an error.
func (m *mergedMap) put(node, key string, value *goyaml.Node) error {
	if previous, ok := m.entries[key]; ok {
		if previous.node != node {
			return fmt.Errorf("mapping: key %q written by %q and %q", key, previous.node, node)
		}
		m.entries[key] = mergedEntry{node: node, value: value}
		return nil
	}
	if m.entries == nil {
		m.entries = map[string]mergedEntry{}
	}
	m.keys = append(m.keys, key)
	m.entries[key] = mergedEntry{node: node, value: value}
	return nil
}

// node builds the ordered mapping node of the merged entries.
func (m *mergedMap) node() *goyaml.Node {
	pairs := make([]model.Pair, 0, len(m.keys))
	for _, key := range m.keys {
		pairs = append(pairs, model.Pair{Key: key, Value: m.entries[key].value})
	}
	return model.Map(pairs...)
}

// mergedStep, mergedJob and mergedWorkflow hold the merged mapper fragments
// plus the owner of each scalar key.
type mergedStep struct {
	step        model.Step
	owner       owned
	usesComment string
	with        mergedMap
	env         mergedMap
	variant     mapping.StepFragmentKind
	variantNode string
	code        string
}

type mergedJob struct {
	job         model.Job
	owner       owned
	permissions mergedMap
	env         mergedMap
	steps       map[string]*mergedStep
}

type mergedWorkflow struct {
	on  mergedMap
	env mergedMap
}

// mergedFragments is the lookup structure of all mapper fragments.
type mergedFragments struct {
	workflow mergedWorkflow
	jobs     map[string]*mergedJob
}

// putScalar applies a scalar value and records its owner. A rewrite by the
// same node replaces the value; a write by a different node is an error.
func putScalar(owner owned, node, key string, present bool, assign func()) error {
	if !present {
		return nil
	}
	if previous, ok := owner[key]; ok && previous != node {
		return fmt.Errorf("mapping: key %q written by %q and %q", key, previous, node)
	}
	assign()
	owner[key] = node
	return nil
}

// putEntries validates and merges the entries of one mapping field.
func putEntries(m *mergedMap, entries []mapping.Entry) error {
	for _, entry := range entries {
		if entry.Node == "" || entry.Key == "" || entry.Value == nil {
			return fmt.Errorf("mapping: entry %q needs a node, a key and a value", entry.Key)
		}
		if err := m.put(entry.Node, entry.Key, entry.Value); err != nil {
			return err
		}
	}
	return nil
}

// mergeFragments applies the fragments in concern order. Scalar keys and
// entry keys written by two different nodes are errors; a rewrite by the same
// node replaces the earlier value. INV-02 is enforced here: the top-level
// permissions stay deny-all and no job permission grants read-all or
// write-all.
func mergeFragments(fragments []mapping.Fragment) (mergedFragments, error) {
	merged := mergedFragments{jobs: map[string]*mergedJob{}}
	job := func(id string) *mergedJob {
		if merged.jobs[id] == nil {
			merged.jobs[id] = &mergedJob{owner: owned{}, steps: map[string]*mergedStep{}}
		}
		return merged.jobs[id]
	}
	for _, fragment := range fragments {
		for _, f := range fragment.Workflow {
			if len(f.Permissions) > 0 {
				return mergedFragments{}, fmt.Errorf("mapping: top-level permissions must stay deny-all (INV-02)")
			}
			if err := putEntries(&merged.workflow.on, f.On); err != nil {
				return mergedFragments{}, err
			}
			if err := putEntries(&merged.workflow.env, f.Env); err != nil {
				return mergedFragments{}, err
			}
		}
		for _, f := range fragment.Jobs {
			target := job(f.JobID)
			for _, entry := range f.Permissions {
				if strings.EqualFold(entry.Key, "read-all") || strings.EqualFold(entry.Key, "write-all") {
					return mergedFragments{}, fmt.Errorf("mapping: job %q permission %q violates INV-02", f.JobID, entry.Key)
				}
			}
			if err := putEntries(&target.permissions, f.Permissions); err != nil {
				return mergedFragments{}, err
			}
			if err := putEntries(&target.env, f.Env); err != nil {
				return mergedFragments{}, err
			}
			put, j, dst := target.owner, f.Job, &target.job
			if err := putScalar(put, f.Node, "if", j.If != "", func() { dst.If = j.If }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "runs-on", j.RunsOn != "", func() { dst.RunsOn = j.RunsOn }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "timeout-minutes", j.TimeoutMinutes != 0, func() { dst.TimeoutMinutes = j.TimeoutMinutes }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "continue-on-error", j.ContinueOnError, func() { dst.ContinueOnError = true }); err != nil {
				return mergedFragments{}, err
			}
		}
		for _, f := range fragment.Steps {
			target := job(f.JobID)
			if target.steps[f.StepID] == nil {
				target.steps[f.StepID] = &mergedStep{owner: owned{}}
			}
			step := target.steps[f.StepID]
			if f.Kind != mapping.StepCode {
				if step.variant != mapping.StepCode && step.variantNode != f.Node {
					return mergedFragments{}, fmt.Errorf("mapping: step %q variant written by %q and %q", f.StepID, step.variantNode, f.Node)
				}
				if f.Kind == mapping.StepPlaceholder && f.Code == "" {
					return mergedFragments{}, fmt.Errorf("mapping: step %q placeholder needs a code", f.StepID)
				}
				step.variant, step.variantNode, step.code = f.Kind, f.Node, f.Code
			}
			if err := putEntries(&step.with, f.With); err != nil {
				return mergedFragments{}, err
			}
			if err := putEntries(&step.env, f.Env); err != nil {
				return mergedFragments{}, err
			}
			if f.UsesComment != "" {
				if previous, ok := step.owner["uses"]; ok && previous != f.Node {
					return mergedFragments{}, fmt.Errorf("mapping: key %q written by %q and %q", "uses", previous, f.Node)
				}
				step.usesComment = f.UsesComment
			}
			put, s, dst := step.owner, f.Step, &step.step
			if err := putScalar(put, f.Node, "name", s.Name != "", func() { dst.Name = s.Name }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "if", s.If != "", func() { dst.If = s.If }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "uses", s.Uses != "", func() { dst.Uses = s.Uses }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "shell", s.Shell != "", func() { dst.Shell = s.Shell }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "working-directory", s.WorkingDirectory != "", func() { dst.WorkingDirectory = s.WorkingDirectory }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "timeout-minutes", s.TimeoutMinutes != 0, func() { dst.TimeoutMinutes = s.TimeoutMinutes }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "continue-on-error", s.ContinueOnError, func() { dst.ContinueOnError = true }); err != nil {
				return mergedFragments{}, err
			}
			if err := putScalar(put, f.Node, "run", s.Run != "", func() { dst.Run = s.Run }); err != nil {
				return mergedFragments{}, err
			}
		}
	}
	return merged, nil
}

// recordKeys records the present scalar keys of owner under base, in the
// fixed key order, followed by the per-entry elements of the mapping fields.
func (w *fileWriter) recordKeys(base string, keys []string, owner owned, maps map[string]*mergedMap) {
	for _, key := range keys {
		id, ok := owner[key]
		if !ok {
			continue
		}
		w.record(id, base+"/"+key)
		if m := maps[key]; m != nil {
			w.recordEntries(base+"/"+key, m)
		}
	}
}

// recordEntries records one output element per merged entry, attributed to
// the entry's own node, in first-occurrence order.
func (w *fileWriter) recordEntries(base string, m *mergedMap) {
	for _, key := range m.keys {
		w.record(m.entries[key].node, base+"/"+pointerEscape(key))
	}
}

// writeMappedJob renders a job from its merged fragments (see
// mapping.Fragment). nodeID owns the keys the emitter itself writes: name,
// needs, the mapping container keys, and the deny-all permissions and
// placeholder runner a fragment did not set.
func (w *fileWriter) writeMappedJob(nodeID, jobKey string, job ir.Job, base model.Job, merged *mergedJob) {
	out := merged.job
	out.Name, out.Needs = base.Name, base.Needs
	if len(merged.permissions.keys) > 0 {
		out.Permissions = merged.permissions.node()
	} else {
		out.Permissions = model.EmptyMap()
	}
	if out.RunsOn == "" {
		out.RunsOn = placeholderRunsOn
	}
	if len(merged.env.keys) > 0 {
		out.Env = merged.env.node()
	}
	owner := owned{}
	for key, id := range merged.owner { //trasloco:unordered-ok only copied into a lookup map
		owner[key] = id
	}
	if out.Name != "" {
		owner["name"] = nodeID
	}
	if len(out.Needs) > 0 {
		owner["needs"] = nodeID
	}
	owner["permissions"] = nodeID
	if _, ok := owner["runs-on"]; !ok {
		owner["runs-on"] = nodeID
	}
	if len(merged.env.keys) > 0 {
		owner["env"] = nodeID
	}
	path := "/jobs/" + pointerEscape(jobKey)
	w.record(nodeID, path)
	w.recordKeys(path, jobKeyOrder, owner, map[string]*mergedMap{
		"permissions": &merged.permissions,
		"env":         &merged.env,
	})

	items := make([]*goyaml.Node, 0, len(job.Steps))
	for _, irStep := range job.Steps {
		stepID := qualifyID("step", job.ID, irStep.ID)
		mapped := merged.steps[irStep.ID]
		if mapped != nil && mapped.variant == mapping.StepOmit {
			continue
		}
		step, stepOwner := placeholderStep(manualConversionCode), owned{"name": stepID, "shell": stepID, "run": stepID}
		var usesComment string
		if mapped != nil {
			switch mapped.variant {
			case mapping.StepPlaceholder:
				step = placeholderStep(mapped.code)
				stepOwner = owned{"name": mapped.variantNode, "shell": mapped.variantNode, "run": mapped.variantNode}
			default:
				step, stepOwner = mapped.step, mapped.owner
				if len(mapped.with.keys) > 0 {
					step.With = mapped.with.node()
				}
				if len(mapped.env.keys) > 0 {
					step.Env = mapped.env.node()
				}
				usesComment = mapped.usesComment
			}
		}
		item, err := encodeStruct(step)
		if err != nil {
			w.defect = err
			return
		}
		if usesComment != "" && step.Uses != "" {
			setUsesComment(item, usesComment)
		}
		items = append(items, item)
		stepPath := path + "/steps/" + strconv.Itoa(len(items)-1)
		w.record(stepID, stepPath)
		stepMaps := map[string]*mergedMap{}
		if mapped != nil && mapped.variant == mapping.StepCode {
			if len(mapped.with.keys) > 0 {
				stepOwner["with"] = stepID
				stepMaps["with"] = &mapped.with
			}
			if len(mapped.env.keys) > 0 {
				stepOwner["env"] = stepID
				stepMaps["env"] = &mapped.env
			}
		}
		w.recordKeys(stepPath, stepKeyOrder, stepOwner, stepMaps)
	}
	jobNode, err := encodeStruct(out)
	if err != nil {
		w.defect = err
		return
	}
	if len(items) > 0 {
		jobNode.Content = append(jobNode.Content,
			&goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!!str", Value: "steps"}, model.Seq(items...))
	}
	w.jobKeys = append(w.jobKeys, model.Pair{Key: jobKey, Value: jobNode})
}

// setUsesComment attaches the trailing version comment to the uses value of
// an encoded step (emit-gha.md 4.1 rule 6).
func setUsesComment(item *goyaml.Node, comment string) {
	for index := 0; index+1 < len(item.Content); index += 2 {
		if item.Content[index].Value == "uses" {
			item.Content[index+1].LineComment = comment
			return
		}
	}
}
