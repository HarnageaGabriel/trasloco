package normalize

import (
	"fmt"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func (c *conv) collectResources(root *ast.Pipeline) {
	if root.Resources == nil {
		return
	}
	if root.Resources.Containers != nil {
		c.containerByAlias = make(map[string]*ast.ContainerResource, len(root.Resources.Containers.Items))
		for _, item := range root.Resources.Containers.Items {
			if item.Value != nil {
				if item.Value.Container != nil {
					if _, exists := c.containerByAlias[item.Value.Container.Value]; !exists {
						c.containerByAlias[item.Value.Container.Value] = item.Value
					}
				}
			}
		}
	}
	if root.Resources.Repositories != nil {
		c.repoByAlias = make(map[string]*ast.RepositoryResource, len(root.Resources.Repositories.Items))
		for _, item := range root.Resources.Repositories.Items {
			if item.Value != nil {
				if item.Value.Repository != nil && item.Value.Ref != nil {
					c.repoByAlias[item.Value.Repository.Value] = item.Value
				}
			}
		}
	}
}

func (c *conv) resourcesExtension(a *acc, root *ast.Pipeline) {
	r := root.Resources
	if r == nil {
		return
	}
	if r.Repositories != nil {
		resources := resourceList(r.Repositories.Items, func(v *ast.RepositoryResource) any {
			return orderedScalars("repository", v.Repository, "name", v.Name, "type", scalarEnum(v.Type), "endpoint", v.Endpoint, "ref", v.Ref)
		})
		a.keep("resources.repositories", a.scrubRetained("resource", r.Repositories.Source, resources))
		a.lose(codeExt, "resource", "repositories", r.Repositories.Source, "repository resource inventory has no standalone IR field")
	}
	if r.Containers != nil {
		resources := resourceList(r.Containers.Items, func(v *ast.ContainerResource) any {
			out := orderedScalars("container", v.Container, "image", v.Image, "endpoint", v.Endpoint, "type", v.Type, "azureSubscription", v.AzureSubscription, "resourceGroup", v.ResourceGroup, "registry", v.Registry, "repository", v.Repository, "options", v.Options)
			if v.Env != nil {
				var env ir.OrderedMapBuilder[any]
				for _, entry := range v.Env.Entries {
					if entry.Key != nil && entry.Value != nil {
						env.Set(entry.Key.Value, entry.Value.Value)
					}
				}
				out.Set("env", env.Build())
			}
			if v.Ports != nil {
				out.Set("ports", listValues(v.Ports))
			}
			if v.Volumes != nil {
				out.Set("volumes", listValues(v.Volumes))
			}
			if v.LocalImage != nil {
				out.Set("localImage", v.LocalImage.Value)
			}
			if v.MapDockerSocket != nil {
				out.Set("mapDockerSocket", v.MapDockerSocket.Value)
			}
			return out
		})
		a.keep("resources.containers", a.scrubRetained("resource", r.Containers.Source, resources))
		a.lose(codeExt, "resource", "containers", r.Containers.Source, "container resource inventory (image, env, ports, volumes, options) is retained in the extension; unreferenced resources have no IR container")
	}
	if r.Pipelines != nil {
		resources := resourceList(r.Pipelines.Items, func(v *ast.PipelineResource) any {
			out := orderedScalars("pipeline", v.Pipeline, "source", v.Source, "project", v.Project, "branch", v.Branch, "version", v.Version)
			if v.Tags != nil {
				out.Set("tags", listValues(v.Tags))
			}
			return out
		})
		a.keep("resources.pipelines", a.scrubRetained("resource", r.Pipelines.Source, resources))
		a.lose(codeExt, "resource", "pipelines", r.Pipelines.Source, "pipeline resource inventory has no standalone IR field")
	}
	if r.Builds != nil {
		resources := resourceList(r.Builds.Items, func(v *ast.BuildResource) any {
			return orderedScalars("build", v.Build, "source", v.Source, "type", v.Type, "connection", v.Connection, "version", v.Version, "branch", v.Branch)
		})
		a.keep("resources.builds", a.scrubRetained("resource", r.Builds.Source, resources))
		a.lose(codeExt, "resource", "builds", r.Builds.Source, "build resources have no portable IR resource model")
	}
	if r.Packages != nil {
		resources := resourceList(r.Packages.Items, func(v *ast.PackageResource) any {
			return orderedScalars("package", v.Package, "name", v.Name, "type", v.Type, "version", v.Version, "connection", v.Connection, "tag", v.Tag)
		})
		a.keep("resources.packages", a.scrubRetained("resource", r.Packages.Source, resources))
		a.lose(codeExt, "resource", "packages", r.Packages.Source, "package resources have no portable IR resource model")
	}
	if r.Webhooks != nil {
		resources := resourceList(r.Webhooks.Items, func(v *ast.WebhookResource) any {
			out := orderedScalars("webhook", v.Webhook, "connection", v.Connection, "type", v.Type)
			if v.Filters != nil {
				var filters []any
				for _, f := range v.Filters.Items {
					if f.Value != nil {
						filters = append(filters, orderedScalars("path", f.Value.Path, "value", f.Value.Value))
					}
				}
				out.Set("filters", filters)
			}
			return out
		})
		a.keep("resources.webhooks", a.scrubRetained("resource", r.Webhooks.Source, resources))
		a.lose(codeExt, "resource", "webhooks", r.Webhooks.Source, "webhook resource inventory has no standalone IR field")
	}
	if r.Legacy != nil {
		a.keep("resources.legacy", "preserved")
		a.lose(codeExt, "resource", "legacy", r.Legacy.Source, "legacy resource syntax has no portable IR model")
	}
	c.unknown(a, "resources", r.Extra)
}

func resourceList[T any](items []ast.ListItem[*T], convert func(*T) any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		if item.Value != nil {
			out = append(out, convert(item.Value))
		}
	}
	return out
}

func orderedScalars(values ...any) ir.OrderedMap[any] {
	var out ir.OrderedMapBuilder[any]
	for i := 0; i+1 < len(values); i += 2 {
		key := values[i].(string)
		switch value := values[i+1].(type) {
		case *ast.Scalar:
			if value != nil {
				out.Set(key, value.Value)
			}
		case string:
			if value != "" {
				out.Set(key, value)
			}
		}
	}
	return out.Build()
}

func scalarEnum(value *ast.Enum) string {
	if value == nil {
		return ""
	}
	return value.Value
}

func (c *conv) resourceTriggers(root *ast.Pipeline) []ir.Trigger {
	var out []ir.Trigger
	if root.Resources == nil {
		return out
	}
	r := root.Resources
	if r.Pipelines != nil {
		for i, item := range r.Pipelines.Items {
			v := item.Value
			if v == nil || v.Trigger == nil || v.Trigger.Form == ast.ResourceTriggerNone || (v.Trigger.Enabled != nil && v.Trigger.Enabled.Known && !v.Trigger.Enabled.Val) {
				continue
			}
			id := scalarOr(v.Pipeline, fmt.Sprintf("pipeline-%d", i+1))
			ta := c.newAcc(v.Trigger.Source).in(v.Trigger.Chain)
			c.flag(ta, v.Trigger.Enabled, "pipeline-trigger", "enabled")
			if v.Trigger.Stages != nil {
				ta.keep("stages", listValues(v.Trigger.Stages))
				ta.lose(codeExt, "pipeline-trigger", "stages", v.Trigger.Stages.Source, "upstream stage filter has no IR field")
			}
			c.unknown(ta, "pipeline-trigger", v.Trigger.Extra)
			triggerID := "upstream-" + c.part(ta, "trigger", id, v.Trigger.Source)
			branches := c.filterGlobs(ta, v.Trigger.Branches)
			tags := c.filterGlobs(ta, v.Trigger.Tags)
			t := ir.Trigger{Metadata: ta.metadata(), ID: triggerID, Kind: ir.TriggerUpstream, Upstream: scalarOr(v.Source, id), Binding: ir.TriggerBinding{InYAML: true}}
			t.Filters.Branches, t.Filters.Tags = branches, tags
			out = append(out, t)
		}
	}
	if r.Repositories != nil {
		for i, item := range r.Repositories.Items {
			v := item.Value
			if v == nil || v.Trigger == nil || v.Trigger.Form == ast.TriggerNone {
				continue
			}
			id := scalarOr(v.Repository, fmt.Sprintf("repository-%d", i+1))
			scrubbedID, changed := c.partText("trigger", id)
			t := c.trigger("repository-"+scrubbedID, ir.TriggerPush, v.Trigger, v.Trigger.Pos)
			if changed {
				t.Fidelity = maxFidelity(t.Fidelity, diag.FidelityNeedsManual)
				c.addSecretLoss(&t.Metadata, v.Trigger.Source, nil)
			}
			out = append(out, t)
		}
	}
	if r.Webhooks != nil {
		for i, item := range r.Webhooks.Items {
			v := item.Value
			if v == nil {
				continue
			}
			id := scalarOr(v.Webhook, fmt.Sprintf("webhook-%d", i+1))
			a := c.newAcc(v.Source).in(v.Chain)
			if v.Filters != nil {
				a.keep("filters", len(v.Filters.Items))
				a.lose(codeExt, "webhook-trigger", "filters", v.Filters.Source, "webhook path/value filters have no portable trigger filter")
			}
			webhookID := "webhook-" + c.part(a, "trigger", id, v.Source)
			out = append(out, ir.Trigger{Metadata: a.metadata(), ID: webhookID, Kind: ir.TriggerWebhook, Binding: ir.TriggerBinding{InYAML: true}})
		}
	}
	appendOther := func(id, kind string, source *ast.ResourceTrigger) {
		if source == nil || source.Form == ast.ResourceTriggerNone || (source.Enabled != nil && source.Enabled.Known && !source.Enabled.Val) {
			return
		}
		oa := c.newAcc(source.Source).in(source.Chain)
		c.flag(oa, source.Enabled, kind+"-trigger", "enabled")
		detail := map[string]any{"resource": id}
		if source.Stages != nil {
			detail["stages"] = listValues(source.Stages)
			oa.lose(codeExt, kind+"-trigger", "stages", source.Stages.Source, "stage filter is retained only in the raw trigger payload")
		}
		for _, filter := range []struct {
			key string
			f   *ast.Filter
		}{{"branches", source.Branches}, {"tags", source.Tags}} {
			if globs := c.filterGlobs(oa, filter.f); len(globs) != 0 {
				detail[filter.key] = globs
				oa.lose(codeExt, kind+"-trigger", filter.key, source.Source, filter.key+" filter is retained only in the raw trigger payload")
			}
		}
		c.unknown(oa, kind+"-trigger", source.Extra)
		detail = oa.scrubRetained(kind+"-trigger", source.Source, detail).(map[string]any)
		raw := &ir.Raw{Provider: Platform, Kind: kind, Format: "json", JSON: rawJSON(detail), Origin: c.origin(source.Source, source.Chain)}
		otherID := kind + "-" + c.part(oa, "trigger", id, source.Source)
		out = append(out, ir.Trigger{Metadata: oa.metadata(), ID: otherID, Kind: ir.TriggerOther, Binding: ir.TriggerBinding{InYAML: true}, Other: raw})
	}
	if r.Containers != nil {
		for i, item := range r.Containers.Items {
			if item.Value != nil {
				appendOther(scalarOr(item.Value.Container, fmt.Sprintf("container-%d", i+1)), "container", item.Value.Trigger)
			}
		}
	}
	if r.Packages != nil {
		for i, item := range r.Packages.Items {
			if item.Value != nil {
				appendOther(scalarOr(item.Value.Package, fmt.Sprintf("package-%d", i+1)), "package", item.Value.Trigger)
			}
		}
	}
	if r.Builds != nil {
		for i, item := range r.Builds.Items {
			if item.Value != nil {
				appendOther(scalarOr(item.Value.Build, fmt.Sprintf("build-%d", i+1)), "build", item.Value.Trigger)
			}
		}
	}
	return out
}
