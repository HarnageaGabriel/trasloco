// Package hosted identifies documented Microsoft-hosted agent images.
package hosted

import "strings"

// imageOS maps the documented Microsoft-hosted agent image names
// (pool.vmImage) to their operating system. Names are lower case; matching is
// case-insensitive, as Azure Pipelines treats vmImage names.
//
// Source: the YAML VM Image Label column of
// https://learn.microsoft.com/en-us/azure/devops/pipelines/agents/hosted
// (page updated 2026-10-05). Retired images (windows-2019, ubuntu-20.04,
// macOS-13) are deliberately absent and stay unknown. Recheck the page when
// images are added or retired.
var imageOS = map[string]string{
	"ubuntu-latest":       "linux",
	"ubuntu-26.04":        "linux",
	"ubuntu-24.04":        "linux",
	"ubuntu-22.04":        "linux",
	"windows-latest":      "windows",
	"windows-2025-vs2026": "windows",
	"windows-2025":        "windows",
	"windows-2022":        "windows",
	"macos-latest":        "macos",
	"macos-26":            "macos",
	"macos-15":            "macos",
	"macos-14":            "macos",
}

// ImageOS returns the operating system for a documented Microsoft-hosted
// pool.vmImage name. Unknown, empty, and expression-valued names are rejected.
func ImageOS(name string) (os string, ok bool) {
	os, ok = imageOS[strings.ToLower(strings.TrimSpace(name))]
	return os, ok
}
