// Package version exposes build metadata injected through -ldflags.
package version

// Build metadata. Package-level variables are required for link-time
// injection, for example:
//
//	-X github.com/HarnageaGabriel/trasloco/internal/version.Version=1.2.3
var (
	// Version is the semantic version of the build.
	Version = "dev"
	// Commit is the VCS revision of the build.
	Commit = "unknown"
	// Date is the build timestamp (RFC 3339).
	Date = "unknown"
)

// Info is a snapshot of the build metadata.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Get returns the current build metadata.
func Get() Info {
	return Info{Version: Version, Commit: Commit, Date: Date}
}
