package normalize

import (
	"fmt"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func (c *conv) structure(out *ir.Pipeline, root *ast.Pipeline) {
	switch root.Shape {
	case ast.ShapeStages:
		if root.Stages == nil {
			return
		}
		previous := ""
		for _, item := range root.Stages.Items {
			if c.cancelled() {
				return
			}
			stage, ok := item.Value.(*ast.StageDef)
			if !ok {
				continue
			}
			if stage.Stage == nil {
				c.pa.lose(codeExt, "stage", "stage", stage.Source, "stage without an identifier was skipped")
				continue
			}
			c.addStage(out, stage, previous)
			previous = stage.Stage.Value
		}
	case ast.ShapeJobs:
		stage := ir.Stage{Metadata: c.newAcc(root.Source).in(root.Chain).metadata(), ID: implicitID, Name: "Default"}
		out.Stages = append(out.Stages, stage)
		if root.Jobs != nil {
			c.addJobs(out, root.Jobs, root.Pool, "")
		}
	case ast.ShapeSteps:
		stage := ir.Stage{Metadata: c.newAcc(root.Source).in(root.Chain).metadata(), ID: implicitID, Name: "Default"}
		out.Stages = append(out.Stages, stage)
		job := c.jobFromRoot(root)
		out.Jobs = append(out.Jobs, job)
		out.Stages[0].Jobs = append(out.Stages[0].Jobs, job.ID)
	}
}

// addStage maps one stage. Stage dependency defaults follow
// https://learn.microsoft.com/en-us/azure/devops/pipelines/process/stages :
// stages run in sequence, so an omitted dependsOn means "the previous stage",
// while an explicit `dependsOn: []` removes the dependency and lets the stage
// start in parallel. The IR makes both explicit edges (rule 8) and uses no
// barrier, which would otherwise force a wait on every earlier stage.
func (c *conv) addStage(out *ir.Pipeline, source *ast.StageDef, previous string) {
	a := c.newAcc(source.Source).in(source.Chain)
	id := source.Stage.Value
	stage := ir.Stage{ID: id, Name: scalarOr(source.DisplayName, id), If: c.condition(source.Condition, a)}
	if source.DependsOn == nil {
		if previous != "" {
			stage.DependsOn = []string{previous}
		}
	} else {
		stage.DependsOn = dependencyNames(source.DependsOn)
	}
	stage.Variables = c.variables(source.Variables, ir.ScopeStage, id)
	c.pushMacroScope(source.Variables)
	defer c.popMacroScope()
	if source.LockBehavior != nil {
		a.keep("lockBehavior", source.LockBehavior.Value)
		a.lose(codeExt, "stage", "lockBehavior", source.LockBehavior.Source, "stage lock behavior has no IR field")
	}
	if source.Group != nil {
		a.keep("group", source.Group.Value)
		a.lose(codeExt, "stage", "group", source.Group.Source, "stage group has no IR field")
	}
	if source.Trigger != nil || source.IsSkippable != nil || source.TemplateContext != nil {
		options := ir.OrderedMap[any]{}
		if source.Trigger != nil {
			options.Set("trigger", source.Trigger.Value)
		}
		if source.IsSkippable != nil {
			options.Set("isSkippable", source.IsSkippable.Value)
		}
		if source.TemplateContext != nil {
			options.Set("templateContext", anyValue(source.TemplateContext))
		}
		a.keep("stageOptions", options)
		a.lose(codeExt, "stage", "options", source.Source, "stage-specific options (including a manual trigger approval gate) have no IR fields")
	}
	c.unknown(a, "stage", source.Extra)
	stage.Metadata = a.metadata()
	out.Stages = append(out.Stages, stage)
	// Claim the scrubbed spelling in stage order, so job prefixes follow the
	// order the backstop de-duplicates stage IDs in.
	c.partText("stage", id)
	c.addJobs(out, source.Jobs, firstPool(source.Pool, c.rootPool), id)
}

// addJobs appends the jobs of the current stage. Job identifiers are unique per
// stage in Azure, so they are qualified as "<stage>.<job>" and `dependsOn` is
// resolved inside the stage. stageID is empty for the implicit stage.
func (c *conv) addJobs(out *ir.Pipeline, list *ast.List[ast.Job], inherited *ast.Pool, stageID string) {
	if list == nil {
		return
	}
	stageIndex := len(out.Stages) - 1
	qualify := func(m *ir.Metadata, id string) string {
		if stageID == "" {
			return id
		}
		// Both components are scrubbed before they are composed; a change is a
		// secretMaterial loss on the job and on the stage listing the new ID.
		stage, stageChanged := c.partText("stage", stageID)
		name, nameChanged := c.partText("job", id)
		if stageChanged || nameChanged {
			c.markSecret(m, diag.Source{})
			c.markSecret(&out.Stages[stageIndex].Metadata, diag.Source{})
		}
		return stage + "." + name
	}
	for _, item := range list.Items {
		if c.cancelled() {
			return
		}
		var job ir.Job
		switch source := item.Value.(type) {
		case *ast.JobDef:
			job = c.job(source, inherited)
		case *ast.Deployment:
			job = c.deployment(source, inherited)
		default:
			continue
		}
		if job.ID == "" {
			job.ID = fmt.Sprintf("job-%d", len(out.Jobs)+1)
			job.Name = job.ID
		}
		if stageID != "" {
			job.ID = qualify(&job.Metadata, job.ID)
			for i, need := range job.Needs {
				job.Needs[i] = qualify(&job.Metadata, need)
			}
			for i := range job.Variables {
				job.Variables[i].ScopeID = job.ID
				if job.Variables[i].SecretRef != nil {
					job.Variables[i].SecretRef.ScopeID = job.ID
				}
			}
		}
		out.Jobs = append(out.Jobs, job)
		out.Stages[stageIndex].Jobs = append(out.Stages[stageIndex].Jobs, job.ID)
	}
}

func dependencyNames(value *ast.DependsOn) []string {
	if value == nil || value.Names == nil {
		return nil
	}
	var out []string
	for _, item := range value.Names.Items {
		if item.Value != nil {
			out = append(out, item.Value.Value)
		}
	}
	return out
}

func firstPool(primary, fallback *ast.Pool) *ast.Pool {
	if primary != nil {
		return primary
	}
	return fallback
}
