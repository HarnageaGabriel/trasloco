package resolve

import (
	"context"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

func (r *resolver) validate(ctx context.Context, p *ast.Pipeline) error {
	if p.Parameters != nil && p.Parameters.List != nil {
		seen := map[string]struct{}{}
		for _, item := range p.Parameters.List.Items {
			if item.Value != nil && item.Value.Name != nil {
				name := item.Value.Name.Value
				key := strings.ToUpper(name)
				if _, duplicate := seen[key]; duplicate {
					r.addAt("TRL-VAL-0008", item.Value.Name.Pos, "duplicate parameter "+name, map[string]any{"name": name})
				} else {
					seen[key] = struct{}{}
				}
			}
		}
	}
	if p.Stages != nil {
		if err := r.validateStages(ctx, p.Stages); err != nil {
			return err
		}
	}
	if p.Jobs != nil {
		return r.validateJobs(ctx, p.Jobs)
	}
	return nil
}

func (r *resolver) validateStages(ctx context.Context, stages *ast.List[ast.Stage]) error {
	var nodes []namedDeps
	index := map[string]int{}
	for _, item := range stages.Items {
		stage, ok := item.Value.(*ast.StageDef)
		if !ok || stage.Stage == nil {
			continue
		}
		name := stage.Stage.Value
		if _, duplicate := index[strings.ToUpper(name)]; duplicate {
			r.addAt("TRL-VAL-0008", stage.Stage.Pos, "duplicate stage "+name, map[string]any{"name": name})
		} else {
			index[strings.ToUpper(name)] = len(nodes)
		}
		nodes = append(nodes, namedDeps{name, depends(stage.DependsOn), stage.Pos})
		if stage.Jobs != nil {
			if err := r.validateJobs(ctx, stage.Jobs); err != nil {
				return err
			}
		}
	}
	return r.validateDeps(ctx, nodes, index, "stage")
}

func (r *resolver) validateJobs(ctx context.Context, jobs *ast.List[ast.Job]) error {
	var nodes []namedDeps
	index := map[string]int{}
	for _, item := range jobs.Items {
		var name *ast.Scalar
		var dependency *ast.DependsOn
		switch job := item.Value.(type) {
		case *ast.JobDef:
			name, dependency = job.Job, job.DependsOn
		case *ast.Deployment:
			name, dependency = job.Deployment, job.DependsOn
		}
		if name == nil || name.Value == "" {
			continue
		}
		if _, duplicate := index[strings.ToUpper(name.Value)]; duplicate {
			r.addAt("TRL-VAL-0008", name.Pos, "duplicate job "+name.Value, map[string]any{"name": name.Value})
		} else {
			index[strings.ToUpper(name.Value)] = len(nodes)
		}
		nodes = append(nodes, namedDeps{name.Value, depends(dependency), name.Pos})
	}
	return r.validateDeps(ctx, nodes, index, "job")
}

type namedDeps struct {
	name         string
	dependencies []string
	pos          ast.Pos
}

func depends(value *ast.DependsOn) []string {
	if value == nil || value.Names == nil {
		return nil
	}
	out := []string{}
	for _, item := range value.Names.Items {
		if item.Value != nil {
			out = append(out, item.Value.Value)
		}
	}
	return out
}

// validateDeps reports unknown dependencies and the first dependency cycle, in
// time linear in the number of nodes and edges.
func (r *resolver) validateDeps(ctx context.Context, nodes []namedDeps, index map[string]int, kind string) error {
	for _, node := range nodes {
		for _, dependency := range node.dependencies {
			if _, ok := index[strings.ToUpper(dependency)]; !ok {
				r.addAt("TRL-VAL-0010", node.pos, kind+" dependency does not exist: "+dependency, map[string]any{"name": dependency})
				break
			}
		}
	}
	const (
		unseen = iota
		active
		done
	)
	state := make([]uint8, len(nodes))
	type frame struct{ node, next int }
	for start := range nodes {
		if state[start] != unseen {
			continue
		}
		stack := []frame{{node: start}}
		state[start] = active
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			top := &stack[len(stack)-1]
			deps := nodes[top.node].dependencies
			if top.next >= len(deps) {
				state[top.node] = done
				stack = stack[:len(stack)-1]
				continue
			}
			dependency := deps[top.next]
			top.next++
			next, ok := index[strings.ToUpper(dependency)]
			if !ok {
				continue
			}
			switch state[next] {
			case active:
				// The node whose edge closes the cycle carries the diagnostic.
				r.addAt("TRL-VAL-0009", nodes[top.node].pos, kind+" dependency graph contains a cycle", map[string]any{"name": nodes[top.node].name})
				return nil
			case unseen:
				state[next] = active
				stack = append(stack, frame{node: next})
			}
		}
	}
	return nil
}
