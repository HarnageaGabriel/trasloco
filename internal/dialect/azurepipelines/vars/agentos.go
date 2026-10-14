package vars

import "github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/hosted"

// AgentOSForVMImage returns the agent operating system for a Microsoft-hosted
// pool.vmImage name, for use as Context.AgentOS. Only documented hosted images
// are mapped. Anything else (an empty name, a self-hosted image, a value that
// still contains an expression) returns "", which the evaluator treats as an
// unknown agent OS.
func AgentOSForVMImage(image string) string {
	os, _ := hosted.ImageOS(image)
	return os
}
