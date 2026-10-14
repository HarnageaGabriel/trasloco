package vars

import (
	"fmt"
	"maps"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// Evaluate constructs all consumer tables. It returns no graph on validation
// failure. The pipeline must be fully expanded: template references and
// conditional insertions are a resolver-contract violation (TRL-VAR-0014).
func Evaluate(pipeline *ast.Pipeline, context Context, findings *diag.Collector) (*Graph, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("evaluate variables: nil pipeline")
	}
	if findings == nil {
		findings = diag.NewCollector()
	}
	start := len(findings.List())
	if err := rejectUnexpanded(pipeline, findings); err != nil {
		return nil, err
	}
	groups := orderedGroups(context.Groups)
	if err := validateContext(pipeline, context, groups, findings); err != nil {
		return nil, err
	}
	secrets := secretNames(context)
	withheld := context.withholdsSecrets()
	g := &Graph{secretsWithheld: withheld, agentOS: strings.ToLower(strings.TrimSpace(context.AgentOS))}
	if withheld {
		// One policy for every secret source: group members never reach the
		// graph, and the UI and queue sources below skip them too.
		groups = withholdGroupSecrets(groups)
	}
	root := Table{Scope: Scope{Level: "pipeline", Step: -1}}
	for i, v := range context.Predefined {
		ingest(&root, plainCandidate(v, SourceSystem, "system", i, root.Scope), findings)
	}
	for i, v := range context.UIPlain {
		ingest(&root, plainCandidate(v, SourceUI, v.ID, i, root.Scope), findings)
	}
	if !withheld {
		for i, v := range context.UISecrets {
			ingest(&root, secretCandidate(v, SourceUI, v.ID, i, root.Scope), findings)
		}
	}
	for i, q := range context.Queue {
		isSecret := secrets[fold(q.Name)] || q.Secret
		if withheld && isSecret {
			continue
		}
		ingest(&root, queueCandidate(q, isSecret, i, root.Scope), findings)
	}
	appendVariables(&root, pipeline.Variables, groups, findings)
	g.Tables = append(g.Tables, cloneTable(root))

	switch pipeline.Shape {
	case ast.ShapeStages:
		previous := ""
		for _, item := range listItems(pipeline.Stages) {
			stage, ok := item.(*ast.StageDef)
			if !ok {
				continue
			}
			evaluateStage(g, root, stage, previous, groups, findings)
			previous = scalar(stage.Stage)
		}
	case ast.ShapeJobs:
		evaluateJobs(g, root, "", pipeline.Jobs, groups, findings)
	case ast.ShapeSteps:
		plan := jobPlan{units: matrixUnits(pipeline.Strategy, pipeline.Steps)}
		evaluateJobPlan(g, root, "", plan, false, groups, findings)
	}
	for _, finding := range findings.List()[start:] {
		if finding.Code == "TRL-VAR-0009" {
			return nil, &diag.Error{Finding: finding}
		}
	}
	return g, nil
}

// secretNames is the folded set of every name that is a secret in any provider
// source, so a queue-time value for it can never carry plaintext.
func secretNames(context Context) map[string]bool {
	names := map[string]bool{} //trasloco:unordered-ok membership lookups only
	for _, v := range context.UISecrets {
		names[fold(v.Name)] = true
	}
	for _, group := range context.Groups {
		for _, v := range group.Secrets {
			names[fold(v.Name)] = true
		}
	}
	return names
}

// withholdGroupSecrets returns the groups without their secret members.
func withholdGroupSecrets(groups []VariableGroup) []VariableGroup {
	result := make([]VariableGroup, len(groups))
	for i, group := range groups {
		group.Secrets = nil
		result[i] = group
	}
	return result
}

func validateContext(pipeline *ast.Pipeline, context Context, groups []VariableGroup, findings *diag.Collector) error {
	for _, q := range context.Queue {
		if !context.LimitQueueTimeVariables {
			continue
		}
		allowed := false
		for _, v := range context.UIPlain {
			if sameName(v.Name, q.Name) && v.Settable {
				allowed = true
				break
			}
		}
		for _, v := range context.UISecrets {
			if sameName(v.Name, q.Name) && v.Settable {
				allowed = true
				break
			}
		}
		if !allowed {
			d := diag.MustNew("TRL-VAR-0010", diag.WithArgs(map[string]any{"name": q.Name}))
			findings.Add(d)
			return &diag.Error{Finding: d}
		}
	}
	for _, group := range groups {
		if !group.Authorized && referencesGroup(pipeline, group.Name) {
			d := diag.MustNew("TRL-VAR-0011", diag.WithArgs(map[string]any{"group": group.Name}))
			findings.Add(d)
			return &diag.Error{Finding: d}
		}
	}
	return nil
}

func referencesGroup(pipeline *ast.Pipeline, name string) bool {
	if variablesReferenceGroup(pipeline.Variables, name) {
		return true
	}
	for _, stageNode := range listItems(pipeline.Stages) {
		stage, ok := stageNode.(*ast.StageDef)
		if !ok {
			continue
		}
		if variablesReferenceGroup(stage.Variables, name) || jobsReferenceGroup(stage.Jobs, name) {
			return true
		}
	}
	return jobsReferenceGroup(pipeline.Jobs, name)
}

func jobsReferenceGroup(jobs *ast.List[ast.Job], name string) bool {
	for _, jobNode := range listItems(jobs) {
		switch job := jobNode.(type) {
		case *ast.JobDef:
			if variablesReferenceGroup(job.Variables, name) {
				return true
			}
		case *ast.Deployment:
			if variablesReferenceGroup(job.Variables, name) {
				return true
			}
		}
	}
	return false
}

func variablesReferenceGroup(variables *ast.Variables, name string) bool {
	if variables == nil || variables.List == nil {
		return false
	}
	for _, item := range variables.List.Items {
		group, ok := item.Value.(*ast.VariableGroup)
		if ok && sameName(scalar(group.Group), name) {
			return true
		}
	}
	return false
}

func orderedGroups(groups []VariableGroup) []VariableGroup {
	return append([]VariableGroup(nil), groups...)
}

// dependsNames returns the producer names of a dependsOn node and whether the
// key was written at all (an explicit empty list is written).
func dependsNames(dependsOn *ast.DependsOn) ([]string, bool) {
	if dependsOn == nil {
		return nil, false
	}
	var names []string
	for _, item := range listItems(dependsOn.Names) {
		names = append(names, scalar(item))
	}
	return names, true
}

func evaluateStage(g *Graph, parent Table, stage *ast.StageDef, previous string, groups []VariableGroup, findings *diag.Collector) {
	name := scalar(stage.Stage)
	names, explicit := dependsNames(stage.DependsOn)
	node := Node{Level: "stage", Stage: name, DependsOn: names, Explicit: explicit}
	if !explicit && previous != "" {
		node.DependsOn, node.Implicit = []string{previous}, true
	}
	g.Nodes = append(g.Nodes, node)
	table := cloneTable(parent)
	table.Scope = Scope{Level: "stage", Stage: name, Step: -1}
	appendVariables(&table, stage.Variables, groups, findings)
	g.Tables = append(g.Tables, cloneTable(table))
	evaluateJobs(g, table, name, stage.Jobs, groups, findings)
}

func evaluateJobs(g *Graph, parent Table, stage string, jobs *ast.List[ast.Job], groups []VariableGroup, findings *diag.Collector) {
	for _, jobNode := range listItems(jobs) {
		var plan jobPlan
		deployment := false
		var dependsOn *ast.DependsOn
		switch job := jobNode.(type) {
		case *ast.JobDef:
			plan = jobPlan{name: scalar(job.Job), variables: job.Variables, units: matrixUnits(job.Strategy, job.Steps)}
			dependsOn = job.DependsOn
		case *ast.Deployment:
			plan = jobPlan{name: scalar(job.Deployment), variables: job.Variables, units: deploymentUnits(job)}
			dependsOn = job.DependsOn
			deployment = true
		default:
			continue
		}
		names, explicit := dependsNames(dependsOn)
		g.Nodes = append(g.Nodes, Node{Level: "job", Stage: stage, Job: plan.name, DependsOn: names, Explicit: explicit})
		evaluateJobPlan(g, parent, stage, plan, deployment, groups, findings)
	}
}

// jobPlan is a job split into the units that run separately: matrix legs or
// deployment lifecycle hooks (one unit for an ordinary job).
type jobPlan struct {
	name      string
	variables *ast.Variables
	units     []jobUnit
}

type jobUnit struct {
	leg      string
	legVars  []legVariable
	steps    *ast.List[ast.Step]
	matrixed bool
	// keyPrefix is the documented resource-qualified output key prefix of a
	// deployment hook; keyUnresolved marks a hook whose output key depends on a
	// resource that is not statically known, so no key is invented for it.
	keyPrefix     string
	keyUnresolved bool
}

type legVariable struct {
	name, value string
	origin      diag.Source
}

func evaluateJobPlan(g *Graph, parent Table, stage string, plan jobPlan, deployment bool, groups []VariableGroup, findings *diag.Collector) {
	for _, unit := range plan.units {
		table := cloneTable(parent)
		if plan.name != "" || unit.matrixed || unit.leg != "" {
			table.Scope = Scope{Level: "job", Stage: stage, Job: plan.name, Step: -1, Leg: unit.leg}
			appendVariables(&table, plan.variables, groups, findings)
			for i, v := range unit.legVars {
				addLeg(&table, v, i, findings)
			}
			g.Tables = append(g.Tables, cloneTable(table))
		}
		evaluateSteps(g, table, stage, plan.name, unit, deployment, findings)
	}
}

func addLeg(table *Table, v legVariable, index int, findings *diag.Collector) {
	if !ValidName(v.name) {
		findings.Add(diag.MustNew("TRL-VAR-0009", diag.WithSource(v.origin), diag.WithArgs(map[string]any{"name": displayName(v.name)})))
		return
	}
	ingest(table, Candidate{Name: v.name, ComparisonKey: key(v.name), Source: SourceYAML, SourceID: "matrix:" + table.Scope.Leg, DeclarationIndex: index, Phase: PhaseRuntime, Scope: table.Scope, Value: v.value, Available: !ReservedName(v.name), Origin: v.origin}, findings)
}

// matrixUnits expands strategy.matrix legs; without a matrix the job is one unit.
func matrixUnits(strategy *ast.Strategy, steps *ast.List[ast.Step]) []jobUnit {
	if strategy == nil || strategy.Matrix == nil || len(strategy.Matrix.Entries) == 0 {
		return []jobUnit{{steps: steps}}
	}
	var units []jobUnit
	for _, entry := range strategy.Matrix.Entries {
		unit := jobUnit{leg: scalar(entry.Key), steps: steps, matrixed: true}
		if entry.Value != nil {
			for _, inner := range entry.Value.Entries {
				unit.legVars = append(unit.legVars, legVariable{name: scalar(inner.Key), value: scalar(inner.Value), origin: inner.Key.Position()})
			}
		}
		units = append(units, unit)
	}
	return units
}

// deploymentResource returns the resource an environment targets. hasResource
// is false for an environment without a resource; known is false when the
// resource name is present but not a literal, so no output key can be derived
// from it.
func deploymentResource(env *ast.Environment) (name string, hasResource, known bool) {
	if env == nil {
		return "", false, false
	}
	if env.Short {
		parts := strings.Split(scalar(env.Name), ".")
		if len(parts) == 1 {
			return "", false, false
		}
		name = parts[1]
		known = len(parts) == 2
	} else {
		if env.ResourceName == nil || scalar(env.ResourceName) == "" {
			return "", false, false
		}
		name, known = scalar(env.ResourceName), true
	}
	if name == "" || strings.Contains(name, "${{") || strings.Contains(name, "$(") || strings.Contains(name, "$[") {
		known = false
	}
	return name, true, known
}

// deploymentUnits turns every lifecycle hook of the chosen strategy into a unit.
// Output keys follow learn.microsoft.com/en-us/azure/devops/pipelines/process/deployment-jobs#support-for-output-variables:
// runOnce is <job> (Deploy_<resource> on a resource), canary is
// <hook>_<increment>, rolling is <hook>_<resource>. The keys of the runOnce
// hooks other than deploy are not documented and use the hook name. A key that
// needs a resource that is not statically known is left unresolved.
func deploymentUnits(job *ast.Deployment) []jobUnit {
	if job.Strategy == nil {
		return []jobUnit{{}}
	}
	lifecycle, canary := job.Strategy.RunOnce, false
	if lifecycle == nil {
		lifecycle = job.Strategy.Rolling
	}
	if lifecycle == nil {
		lifecycle, canary = job.Strategy.Canary, true
	}
	if lifecycle == nil {
		return []jobUnit{{}}
	}
	var increments []string
	if canary {
		for _, item := range listItems(lifecycle.Increments) {
			increments = append(increments, scalar(item))
		}
	}
	type hook struct {
		name         string
		steps        *ast.Hook
		perIncrement bool
	}
	hooks := []hook{{"preDeploy", lifecycle.PreDeploy, false}, {"deploy", lifecycle.Deploy, true}, {"routeTraffic", lifecycle.RouteTraffic, true}, {"postRouteTraffic", lifecycle.PostRouteTraffic, true}}
	if lifecycle.On != nil {
		hooks = append(hooks, hook{"on.failure", lifecycle.On.Failure, false}, hook{"on.success", lifecycle.On.Success, false})
	}
	resource, hasResource, resourceKnown := deploymentResource(job.Environment)
	rolling := job.Strategy.RunOnce == nil && job.Strategy.Rolling != nil
	var units []jobUnit
	for _, h := range hooks {
		if h.steps == nil {
			continue
		}
		var unit jobUnit
		switch {
		case canary && h.perIncrement && len(increments) > 0:
			for _, inc := range increments {
				units = append(units, jobUnit{leg: h.name + "_" + inc, steps: h.steps.Steps})
			}
			continue
		case h.name == "deploy" && !canary:
			unit = jobUnit{steps: h.steps.Steps}
		default:
			unit = jobUnit{leg: h.name, steps: h.steps.Steps}
		}
		switch {
		case rolling:
			// rolling: <lifecycle-hookname>_<resource-name>
			if hasResource && resourceKnown {
				unit.keyPrefix = h.name + "_" + resource
			} else {
				unit.keyUnresolved = true
			}
		case !canary && h.name == "deploy" && hasResource:
			// runOnce on a resource: Deploy_<resource-name>
			if resourceKnown {
				unit.keyPrefix = "Deploy_" + resource
			} else {
				unit.keyUnresolved = true
			}
		}
		units = append(units, unit)
	}
	if len(units) == 0 {
		return []jobUnit{{}}
	}
	return units
}

func evaluateSteps(g *Graph, parent Table, stage, job string, unit jobUnit, deployment bool, findings *diag.Collector) {
	leg := unit.leg
	state := cloneTable(parent)
	expander := newMacroExpander(&state)
	var once findingCache
	for index, step := range listItems(unit.steps) {
		props := stepProps(step)
		table := cloneTable(state)
		table.Scope = Scope{Level: "step", Stage: stage, Job: job, Step: index, Leg: leg}
		table.Env = stepEnv(props)
		g.Tables = append(g.Tables, table)
		expander.useTable(&table)
		stateChanged := false
		for _, event := range mutationsWithExpander(step, expander, stage, job, leg, index, g.agentOS) {
			if event.stepWarning {
				once.addName(findings, "TRL-VAR-0016", event.candidate.Origin, "(macro values may inject logging commands)")
				continue
			}
			if !ValidName(event.bareName) {
				// A logging command with an unusable name (a help string, say) is
				// reported as unprovable rather than aborting the evaluation.
				once.addName(findings, "TRL-VAR-0016", event.candidate.Origin, displayName(event.bareName))
				continue
			}
			if !applyMutation(&state, event, props, findings) {
				continue
			}
			stateChanged = true
			if event.unprovable || event.diagnostic {
				once.addName(findings, "TRL-VAR-0016", event.candidate.Origin, displayName(event.bareName))
			}
			if !event.output {
				continue
			}
			if unit.keyUnresolved {
				findings.Add(diag.MustNew("TRL-VAR-0015", diag.WithSource(event.candidate.Origin), diag.WithArgs(map[string]any{"name": displayName(event.outputName)})))
				continue
			}
			g.Outputs = append(g.Outputs, Output{Stage: stage, Job: job, StepName: event.stepName, Name: event.outputName, Deployment: deployment, Leg: leg, KeyPrefix: unit.keyPrefix, Candidate: cloneCandidate(event.candidate)})
		}
		if stateChanged {
			expander = newMacroExpander(&state)
		}
	}
}

func stepEnv(props ast.StepProps) []EnvMapping {
	if props.Env == nil {
		return nil
	}
	var result []EnvMapping
	for _, entry := range props.Env.Entries {
		if entry.Cond == nil {
			result = append(result, EnvMapping{Name: scalar(entry.Key), Value: scalar(entry.Value)})
		}
	}
	return result
}

func appendVariables(table *Table, variables *ast.Variables, groups []VariableGroup, findings *diag.Collector) {
	if variables == nil {
		return
	}
	index := 0
	if variables.Form == ast.VariablesMap && variables.Map != nil {
		for _, entry := range variables.Map.Entries {
			if entry.Cond == nil {
				addYAML(table, scalar(entry.Key), scalar(entry.Value), false, index, entry.Key.Position(), findings)
				index++
			}
		}
		return
	}
	if variables.List == nil {
		return
	}
	for _, item := range variables.List.Items {
		switch variable := item.Value.(type) {
		case *ast.VariableDef:
			readonly := variable.Readonly != nil && variable.Readonly.Known && variable.Readonly.Val
			addYAML(table, scalar(variable.Name), scalar(variable.Value), readonly, index, variable.Position(), findings)
			index++
		case *ast.VariableGroup:
			groupName := scalar(variable.Group)
			if !ValidGroupName(groupName) {
				findings.Add(diag.MustNew("TRL-VAR-0009", diag.WithSource(variable.Position()), diag.WithArgs(map[string]any{"name": displayName(groupName)})))
				continue
			}
			group, ok := findGroup(groups, groupName)
			if !ok {
				table.MissingGroups = append(table.MissingGroups, groupName)
				findings.Add(diag.MustNew("TRL-VAR-0006", diag.WithSource(variable.Position()), diag.WithArgs(map[string]any{"name": groupName})))
				continue
			}
			for _, v := range group.Plain {
				candidate := plainCandidate(v, SourceGroup, group.Name, index, table.Scope)
				candidate.CompileVisible = false
				candidate.Unordered = !group.OrderProven
				markCollision(table, &candidate, findings)
				ingest(table, candidate, findings)
				index++
			}
			for _, v := range group.Secrets {
				candidate := secretCandidate(v, SourceGroup, group.Name, index, table.Scope)
				candidate.Unordered = !group.OrderProven
				markCollision(table, &candidate, findings)
				ingest(table, candidate, findings)
				index++
			}
		}
	}
}

// markCollision flags a group member that collides with the effective candidate
// of the same name in the same scope when the order of the two groups is not
// proven on either side. A later unambiguous candidate clears the flag because
// ambiguity lives on the candidate, not on the binding.
func markCollision(table *Table, candidate *Candidate, findings *diag.Collector) {
	if i, ok := bindingIndex(table, candidate.ComparisonKey, nil); ok {
		binding := &table.Bindings[i]
		previous := binding.Candidates[len(binding.Candidates)-1]
		if previous.Source != SourceGroup || previous.Scope != candidate.Scope || previous.SourceID == candidate.SourceID {
			return
		}
		if !previous.Unordered && !candidate.Unordered {
			return
		}
		different := previous.Secret != candidate.Secret || previous.Value != candidate.Value
		if previous.SecretRef != nil && candidate.SecretRef != nil {
			different = different || *previous.SecretRef != *candidate.SecretRef
		}
		if different {
			candidate.Contested = true
			findings.Add(diag.MustNew("TRL-VAR-0004", diag.WithArgs(map[string]any{"name": candidate.Name})))
		}
	}
}

func addYAML(table *Table, name, value string, readonly bool, index int, source diag.Source, findings *diag.Collector) {
	if !ValidName(name) {
		findings.Add(diag.MustNew("TRL-VAR-0009", diag.WithSource(source), diag.WithArgs(map[string]any{"name": displayName(name)})))
		return
	}
	ingest(table, Candidate{Name: name, ComparisonKey: key(name), Source: SourceYAML, SourceID: source.File, DeclarationIndex: index, Phase: PhaseCompile, Scope: table.Scope, Value: value, Readonly: readonly, CompileVisible: true, Available: !ReservedName(name), Origin: source}, findings)
}

func plainCandidate(v PlainVariable, source SourceKind, sourceID string, index int, scope Scope) Candidate {
	if source == SourceSystem {
		v.Readonly = true
	}
	return Candidate{Name: v.Name, ComparisonKey: key(v.Name), Source: source, SourceID: sourceID, DeclarationIndex: index, Phase: PhaseRuntime, Scope: scope, Value: v.Value, Readonly: v.Readonly, SettableAtQueueTime: v.Settable, CompileVisible: v.CompileVisible, Available: !ReservedName(v.Name), Origin: v.Origin}
}

func secretCandidate(v SecretVariable, source SourceKind, sourceID string, index int, scope Scope) Candidate {
	return Candidate{Name: v.Name, ComparisonKey: key(v.Name), Source: source, SourceID: sourceID, DeclarationIndex: index, Phase: PhaseRuntime, Scope: scope, Secret: true, SecretRef: &SecretRef{Provider: v.Provider, ID: v.ID}, Readonly: v.Readonly, SettableAtQueueTime: v.Settable, Available: !ReservedName(v.Name), Origin: v.Origin}
}

// queueCandidate never carries plaintext for a secret name: the candidate holds
// a reference to the queue-time secret slot instead.
func queueCandidate(q QueueAssignment, secret bool, index int, scope Scope) Candidate {
	c := Candidate{Name: q.Name, ComparisonKey: key(q.Name), Source: SourceQueue, SourceID: "queue", DeclarationIndex: index, Phase: PhaseRuntime, Scope: scope, Available: !ReservedName(q.Name)}
	if secret {
		c.Secret = true
		c.SecretRef = &SecretRef{Provider: "queue", ID: q.Name}
		return c
	}
	c.Value = q.Value
	return c
}

// ingest is the single entry point for adding evaluator-sourced candidates.
// warningName differs from Candidate.Name only for qualified output variables.
// It validates the bare name against the name grammar: an invalid name is
// reported (TRL-VAR-0009) and the candidate is skipped.
func ingest(table *Table, candidate Candidate, findings *diag.Collector, warningName ...string) bool {
	return ingestWork(table, candidate, findings, nil, warningName...)
}

func ingestWork(table *Table, candidate Candidate, findings *diag.Collector, work *evaluationWork, warningName ...string) bool {
	name := candidate.Name
	if len(warningName) > 0 {
		name = warningName[0]
	}
	if !ValidName(name) {
		findings.Add(diag.MustNew("TRL-VAR-0009", diag.WithSource(candidate.Origin), diag.WithArgs(map[string]any{"name": displayName(name)})))
		return false
	}
	warnHyphen(name, candidate.Origin, findings)
	addWithWork(table, candidate, work)
	return true
}

func add(table *Table, candidate Candidate) {
	addWithWork(table, candidate, nil)
}

func addWithWork(table *Table, candidate Candidate, work *evaluationWork) {
	if table.bindingIndex == nil {
		// only the mutating path builds the index; readers never store it
		table.bindingIndex = make(map[string]int, len(table.Bindings)) //trasloco:unordered-ok lookup-only acceleration
		for i := range table.Bindings {
			if work != nil {
				work.bindingScans++
			}
			key := table.Bindings[i].Candidates[0].ComparisonKey
			if _, exists := table.bindingIndex[key]; !exists {
				table.bindingIndex[key] = i
				if work != nil {
					work.indexBuilds++
				}
			}
		}
	} else if table.sharedIndex {
		// the index still belongs to the table this one was cloned from:
		// copy it before writing this table's own bindings into it
		if work != nil {
			// the copy writes one entry per shared key
			work.indexBuilds += len(table.bindingIndex)
		}
		table.bindingIndex = maps.Clone(table.bindingIndex)
		table.sharedIndex = false
	}
	if i, ok := bindingIndex(table, candidate.ComparisonKey, work); ok {
		binding := &table.Bindings[i]
		if work != nil {
			work.candidateSteps++
		}
		if binding.Effective >= 0 && binding.Effective < len(binding.Candidates) {
			binding.Candidates[binding.Effective].Shadowed = true
		}
		candidate.Shadowed = false
		binding.Candidates = append(binding.Candidates, candidate)
		binding.note(len(binding.Candidates) - 1)
		binding.Effective = len(binding.Candidates) - 1
		binding.Ambiguous = candidate.Contested
		return
	}
	table.Bindings = append(table.Bindings, Binding{Name: candidate.Name, Candidates: []Candidate{candidate}, Effective: 0, Ambiguous: candidate.Contested})
	table.Bindings[len(table.Bindings)-1].note(0)
	table.bindingIndex[candidate.ComparisonKey] = len(table.Bindings) - 1
	if work != nil {
		work.indexBuilds++
	}
}

// bindingIndex finds a binding by comparison key. It never mutates the table: a
// table without an index, or an indexed position that does not validate, falls
// back to a (counted) linear scan.
func bindingIndex(table *Table, comparisonKey string, work *evaluationWork) (int, bool) {
	if table.bindingIndex != nil {
		i, ok := table.bindingIndex[comparisonKey]
		if !ok {
			return 0, false
		}
		// A shared index may name a position this table does not have, because
		// the table it was cloned from appended a binding after the clone. A
		// live position always carries the key, because bindings only grow at
		// the end and a key never moves.
		if i < len(table.Bindings) && table.Bindings[i].Candidates[0].ComparisonKey == comparisonKey {
			return i, true
		}
	}
	for i := range table.Bindings {
		if work != nil {
			work.bindingScans++
		}
		if table.Bindings[i].Candidates[0].ComparisonKey == comparisonKey {
			return i, true
		}
	}
	return 0, false
}

func findGroup(groups []VariableGroup, name string) (VariableGroup, bool) {
	for _, group := range groups {
		if sameName(group.Name, name) {
			return group, true
		}
	}
	return VariableGroup{}, false
}

func scalar(s *ast.Scalar) string {
	if s == nil {
		return ""
	}
	return s.Value
}

// listItems returns the plain values of a list. Conditional and whole-value
// insertion items hold no value and are diagnosed by rejectUnexpanded first.
func listItems[T any](list *ast.List[T]) []T {
	if list == nil {
		return nil
	}
	result := make([]T, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Cond != nil || item.Insert != nil {
			continue
		}
		result = append(result, item.Value)
	}
	return result
}
