// Package catalog embeds the catalog shipped with trasloco.
package catalog

import "embed"

// FS contains the Azure Pipelines and GitHub Actions catalog files.
//
//go:embed azure-pipelines github-actions
var FS embed.FS
