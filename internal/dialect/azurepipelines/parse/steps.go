package parse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

var (
	guidPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
)

var stepKeywords = []string{
	"task", "script", "bash", "pwsh", "powershell", "checkout", "download",
	"downloadBuild", "getPackage", "publish", "template", "reviewApp",
	"upload",
}

// step parses one item of a steps list; the selecting keyword decides the kind.
func (p *parser) step(n *yamlx.Node) (ast.Step, bool) {
	m := p.open(n, "step")
	if m == nil {
		return nil, false
	}
	defer m.close()
	key, ok := m.selectKey(stepKeywords...)
	if !ok {
		return nil, false
	}
	switch key {
	case "task":
		out := new(ast.TaskStep)
		p.fillTask(out, m)
		return out, true
	case "script", "bash", "pwsh", "powershell":
		out := new(ast.ScriptStep)
		p.fillScript(out, m, scriptKind(key))
		return out, true
	case "checkout":
		out := new(ast.CheckoutStep)
		p.fillCheckout(out, m)
		return out, true
	case "download":
		out := new(ast.DownloadStep)
		p.fillDownload(out, m)
		return out, true
	case "downloadBuild":
		out := new(ast.DownloadBuildStep)
		p.fillDownloadBuild(out, m)
		return out, true
	case "getPackage":
		out := new(ast.GetPackageStep)
		p.fillGetPackage(out, m)
		return out, true
	case "publish", "upload":
		out := new(ast.PublishStep)
		p.fillPublish(out, m, key)
		return out, true
	case "reviewApp":
		out := new(ast.ReviewAppStep)
		p.fillReviewApp(out, m)
		return out, true
	default:
		out := new(ast.StepTemplate)
		p.fillStepTemplate(out, m)
		return out, true
	}
}

func scriptKind(keyword string) ast.ScriptKind {
	switch keyword {
	case "bash":
		return ast.ScriptBash
	case "pwsh":
		return ast.ScriptPwsh
	case "powershell":
		return ast.ScriptPowerShell
	default:
		return ast.ScriptCmd
	}
}

// fillStepProps reads the properties common to every step but template references.
func (p *parser) fillStepProps(out *ast.StepProps, m *mapping) {
	out.Name = m.str("name")
	p.resourceIdentifier(out.Name, "step")
	out.DisplayName = m.str("displayName")
	out.Condition = m.expression("condition")
	out.ContinueOnError = m.boolean("continueOnError")
	out.Enabled = m.boolean("enabled")
	out.Env = m.strMap("env")
	out.TimeoutInMinutes = m.str("timeoutInMinutes")
	p.integerRange(out.TimeoutInMinutes, "timeoutInMinutes", 0, -1)
	out.RetryCountOnTaskFailure = m.str("retryCountOnTaskFailure")
	p.integerRange(out.RetryCountOnTaskFailure, "retryCountOnTaskFailure", 0, 10)
	if e := m.take("target"); e != nil {
		out.Target = p.target(e.value, e.name)
	}
}

func (p *parser) allowed(value *ast.Scalar, key string, choices ...string) {
	if value == nil || len(value.Directives) != 0 || strings.Contains(value.Value, "$(") {
		return
	}
	for _, choice := range choices {
		if strings.EqualFold(strings.TrimSpace(value.Value), choice) {
			return
		}
	}
	p.add("TRL-PARSE-0012", value.Source, fmt.Sprintf("%s: %q is not an allowed value", key, value.Value),
		map[string]any{"key": key, "value": value.Value, "allowed": strings.Join(choices, ", ")})
}

func (p *parser) integerRange(value *ast.Scalar, key string, minimum, maximum int) {
	if value == nil || len(value.Directives) != 0 || strings.Contains(value.Value, "$(") {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(value.Value))
	if err == nil && n >= minimum && (maximum < 0 || n <= maximum) {
		return
	}
	allowed := fmt.Sprintf("integer >= %d", minimum)
	if maximum >= 0 {
		allowed = fmt.Sprintf("integer %d..%d", minimum, maximum)
	}
	p.add("TRL-PARSE-0012", value.Source, fmt.Sprintf("%s: %q is not %s", key, value.Value, allowed),
		map[string]any{"key": key, "value": value.Value, "allowed": allowed})
}

func (p *parser) fillStepTemplate(out *ast.StepTemplate, m *mapping) {
	out.Pos = pos(m.node)
	p.fillTemplateRef(&out.TemplateRef, m)
	out.Inserts, out.Extra = finish(m, p.fillStepTemplate)
}

func (p *parser) fillScript(out *ast.ScriptStep, m *mapping, kind ast.ScriptKind) {
	out.Pos = pos(m.node)
	out.Kind = kind
	out.Script = m.str(kind.Keyword())
	if kind == ast.ScriptPwsh || kind == ast.ScriptPowerShell {
		out.ErrorActionPreference = m.str("errorActionPreference")
		p.allowed(out.ErrorActionPreference, "errorActionPreference", "stop", "continue", "silentlyContinue")
		out.IgnoreLASTEXITCODE = m.str("ignoreLASTEXITCODE")
	}
	out.FailOnStderr = m.str("failOnStderr")
	out.WorkingDirectory = m.str("workingDirectory")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, func(s *ast.ScriptStep, bm *mapping) { p.fillScript(s, bm, kind) })
}

func (p *parser) fillTask(out *ast.TaskStep, m *mapping) {
	out.Pos = pos(m.node)
	out.Task = m.str("task")
	if out.Task != nil {
		out.Ref = p.taskRef(out.Task)
	}
	out.Inputs = m.strMap("inputs")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, p.fillTask)
}

// taskRef parses `Name@version` and `GUID@version`. References that contain
// template expressions or macros are only known after expansion and are kept
// as written.
func (p *parser) taskRef(s *ast.Scalar) ast.TaskRef {
	raw := strings.TrimSpace(s.Value)
	if len(s.Directives) > 0 || strings.Contains(raw, "$(") {
		return ast.TaskRef{Name: raw, Major: -1, Dynamic: true, Valid: true}
	}
	ref := ast.TaskRef{Major: -1}
	at := strings.LastIndex(raw, "@")
	if at > 0 && at < len(raw)-1 && !strings.ContainsAny(raw, " \t") {
		name, version := raw[:at], raw[at+1:]
		if versionPattern.MatchString(version) {
			major, _, _ := strings.Cut(version, ".")
			number, err := strconv.Atoi(major)
			if err == nil {
				return ast.TaskRef{
					Name: name, IsGUID: guidPattern.MatchString(name), Version: version,
					Major: number, Valid: true,
				}
			}
		}
		ref.Name = name
	}
	p.add("TRL-PARSE-0015", s.Source, fmt.Sprintf("task reference %q is not Name@Version or GUID@Version", raw),
		map[string]any{"reference": raw})
	return ref
}

func (p *parser) fillCheckout(out *ast.CheckoutStep, m *mapping) {
	out.Pos = pos(m.node)
	out.Checkout = m.str("checkout")
	out.Clean = m.boolean("clean")
	out.FetchDepth = m.str("fetchDepth")
	out.FetchFilter = m.str("fetchFilter")
	out.FetchTags = m.str("fetchTags")
	out.Lfs = m.str("lfs")
	out.PersistCredentials = m.str("persistCredentials")
	out.Submodules = m.str("submodules")
	p.allowed(out.Submodules, "submodules", "true", "false", "recursive")
	out.Path = m.str("path")
	out.SparseCheckoutDirectories = m.str("sparseCheckoutDirectories")
	out.SparseCheckoutPatterns = m.str("sparseCheckoutPatterns")
	out.WorkspaceRepo = m.boolean("workspaceRepo")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, p.fillCheckout)
}

func (p *parser) fillDownload(out *ast.DownloadStep, m *mapping) {
	out.Pos = pos(m.node)
	out.Download = m.str("download")
	out.Artifact = m.str("artifact")
	out.Patterns = m.str("patterns")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, p.fillDownload)
}

func (p *parser) fillDownloadBuild(out *ast.DownloadBuildStep, m *mapping) {
	out.Pos = pos(m.node)
	out.DownloadBuild = m.str("downloadBuild")
	out.Artifact = m.str("artifact")
	out.Path = m.str("path")
	out.Patterns = m.str("patterns")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, p.fillDownloadBuild)
}

func (p *parser) fillGetPackage(out *ast.GetPackageStep, m *mapping) {
	out.Pos = pos(m.node)
	out.GetPackage = m.str("getPackage")
	out.Path = m.str("path")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, p.fillGetPackage)
}

func (p *parser) fillPublish(out *ast.PublishStep, m *mapping, keyword string) {
	out.Pos = pos(m.node)
	if keyword == "upload" {
		out.Upload = m.str(keyword)
	} else {
		out.Publish = m.str(keyword)
	}
	out.Artifact = m.str("artifact")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, func(s *ast.PublishStep, bm *mapping) { p.fillPublish(s, bm, keyword) })
}

func (p *parser) fillReviewApp(out *ast.ReviewAppStep, m *mapping) {
	out.Pos = pos(m.node)
	out.ReviewApp = m.str("reviewApp")
	p.fillStepProps(&out.StepProps, m)
	out.Inserts, out.Extra = finish(m, p.fillReviewApp)
}
