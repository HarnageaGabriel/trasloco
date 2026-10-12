package vars

import "strings"

// hostedImageOS maps the documented Microsoft-hosted agent image names
// (pool.vmImage) to their operating system. Names are lower case; matching is
// case-insensitive, as Azure Pipelines treats vmImage names.
//
// Source: the YAML VM Image Label column of
// https://learn.microsoft.com/en-us/azure/devops/pipelines/agents/hosted
// (page updated 2026-10-05). Retired images (windows-2019, ubuntu-20.04,
// macOS-13) are deliberately absent and stay unknown. Recheck the page when
// images are added or retired.
var hostedImageOS = map[string]string{
	"ubuntu-latest":       AgentOSLinux,
	"ubuntu-26.04":        AgentOSLinux,
	"ubuntu-24.04":        AgentOSLinux,
	"ubuntu-22.04":        AgentOSLinux,
	"windows-latest":      AgentOSWindows,
	"windows-2025-vs2026": AgentOSWindows,
	"windows-2025":        AgentOSWindows,
	"windows-2022":        AgentOSWindows,
	"macos-latest":        AgentOSMacOS,
	"macos-26":            AgentOSMacOS,
	"macos-15":            AgentOSMacOS,
	"macos-14":            AgentOSMacOS,
}

// AgentOSForVMImage returns the agent operating system for a Microsoft-hosted
// pool.vmImage name, for use as Context.AgentOS. Only documented hosted images
// are mapped. Anything else (an empty name, a self-hosted image, a value that
// still contains an expression) returns "", which the evaluator treats as an
// unknown agent OS.
func AgentOSForVMImage(image string) string {
	return hostedImageOS[strings.ToLower(strings.TrimSpace(image))]
}
