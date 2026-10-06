package ir

import (
	"fmt"
	"strings"
)

func (v *irValidator) indexOrigins() {
	v.origins = make(map[string]*Origin)
	p := v.pipeline
	if p == nil {
		return
	}
	v.origins[""] = p.Origin
	variables := func(path string, values []Variable) {
		for i := range values {
			v.origins[fmt.Sprintf("%s[%d]", path, i)] = values[i].Origin
		}
	}
	raw := func(path string, value *Raw) {
		if value != nil {
			v.origins[path] = value.Origin
		}
	}
	cond := func(path string, value *Cond) {
		if value != nil {
			raw(path+".opaque", value.Opaque)
		}
	}
	variables("variables", p.Variables)
	for i := range p.Inputs {
		v.origins[fmt.Sprintf("inputs[%d]", i)] = p.Inputs[i].Origin
	}
	for i := range p.Triggers {
		path := fmt.Sprintf("triggers[%d]", i)
		v.origins[path] = p.Triggers[i].Origin
		raw(path+".other", p.Triggers[i].Other)
	}
	for i := range p.Stages {
		path := fmt.Sprintf("stages[%d]", i)
		v.origins[path] = p.Stages[i].Origin
		variables(path+".variables", p.Stages[i].Variables)
		cond(path+".if", p.Stages[i].If)
	}
	for i := range p.Jobs {
		job := &p.Jobs[i]
		path := fmt.Sprintf("jobs[%d]", i)
		v.origins[path] = job.Origin
		variables(path+".variables", job.Variables)
		cond(path+".if", job.If)
		if job.Matrix != nil {
			raw(path+".matrix.opaque", job.Matrix.Opaque)
		}
		for j := range job.Steps {
			stepPath := fmt.Sprintf("%s.steps[%d]", path, j)
			v.origins[stepPath] = job.Steps[j].Origin
			raw(stepPath+".raw", job.Steps[j].Raw)
			cond(stepPath+".if", job.Steps[j].If)
		}
	}
}

func (v *irValidator) originAt(path string) *Origin {
	for {
		if origin, ok := v.origins[path]; ok {
			return origin
		}
		index := strings.LastIndexAny(path, ".[")
		if index < 0 {
			return v.origins[""]
		}
		path = path[:index]
	}
}

func (v *irValidator) jobPath(id string) string {
	for i := range v.pipeline.Jobs {
		if v.pipeline.Jobs[i].ID == id {
			return fmt.Sprintf("jobs[%d]", i)
		}
	}
	return "jobs"
}

func (v *irValidator) crossesBarrier(jobID, need string) bool {
	from, to := -1, -1
	for i := range v.pipeline.Stages {
		stage := &v.pipeline.Stages[i]
		if stage.ID == v.jobStage[jobID] {
			from = i
		}
		if stage.ID == v.jobStage[need] {
			to = i
		}
	}
	if from < 0 || to <= from {
		return false
	}
	for i := from + 1; i <= to; i++ {
		if v.pipeline.Stages[i].Barrier {
			return true
		}
	}
	return false
}

func (v *irValidator) concurrency(path string, value *Concurrency, jobID string) {
	if value == nil {
		return
	}
	if !oneOf(value.Mode, ConcurrencyCancelInProgress, ConcurrencyQueueLatest, ConcurrencySerialize) {
		v.add("TRL-VAL-0012", path+".mode", "invalid concurrency mode")
	}
	v.expr(path+".group", &value.Group, jobID)
}

func (v *irValidator) validateStageDAG() {
	stages := make(map[string]int, len(v.pipeline.Stages))
	for i := range v.pipeline.Stages {
		stages[v.pipeline.Stages[i].ID] = i
	}
	state := make(map[string]uint8, len(stages))
	var visit func(string)
	visit = func(id string) {
		if state[id] == 2 {
			return
		}
		index, exists := stages[id]
		if !exists {
			return
		}
		if state[id] == 1 {
			v.add("TRL-VAL-0009", fmt.Sprintf("stages[%d].dependsOn", index), "stage dependency cycle")
			return
		}
		state[id] = 1
		for _, dependency := range v.pipeline.Stages[index].DependsOn {
			visit(dependency)
		}
		state[id] = 2
	}
	for i := range v.pipeline.Stages {
		visit(v.pipeline.Stages[i].ID)
	}
}
