package hosted

import "strings"

// hostedPoolName is the name of the Microsoft-hosted pool. A job that declares
// this pool still runs on Microsoft-hosted agents; it is not a self-hosted pool.
//
// Source: the "Azure Pipelines" hosted pool in
// https://learn.microsoft.com/en-us/azure/devops/pipelines/agents/pools-queues
// ("The Azure Pipelines hosted pool provides Microsoft-hosted agents.").
const hostedPoolName = "azure pipelines"

// IsHostedPoolName reports whether a pool name is the Microsoft-hosted
// "Azure Pipelines" pool. Comparison is case-insensitive and trims surrounding
// whitespace, matching how pool names are written in YAML.
func IsHostedPoolName(name string) bool {
	return strings.ToLower(strings.TrimSpace(name)) == hostedPoolName
}
