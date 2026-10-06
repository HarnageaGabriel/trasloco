// Package config loads and validates the trasloco configuration.
//
// Sources are merged with the precedence flags > environment > file >
// defaults. The merged result is validated against an embedded JSON Schema
// and then against Go-level semantic rules; every problem is reported with
// the path of the offending field.
package config

// SchemaVersion is the only configuration version understood by this build.
const SchemaVersion = 1

// Config is the typed representation of trasloco.yaml.
type Config struct {
	Version     int          `koanf:"version" json:"version"`
	Source      *Endpoint    `koanf:"source" json:"source,omitempty"`
	Target      *Target      `koanf:"target" json:"target,omitempty"`
	Conversion  Conversion   `koanf:"conversion" json:"conversion"`
	Policy      Policy       `koanf:"policy" json:"policy"`
	Secrets     Secrets      `koanf:"secrets" json:"secrets"`
	RepoContent string       `koanf:"repoContent" json:"repoContent,omitempty"`
	Topology    string       `koanf:"topology" json:"topology,omitempty"`
	Identities  string       `koanf:"identities" json:"identities,omitempty"`
	Credentials []Credential `koanf:"credentials" json:"credentials,omitempty"`
	Output      Output       `koanf:"output" json:"output"`
}

// Endpoint describes the platform migrated from.
type Endpoint struct {
	Platform string   `koanf:"platform" json:"platform"`
	URL      string   `koanf:"url" json:"url,omitempty"`
	Select   []string `koanf:"select" json:"select,omitempty"`
	Auth     Auth     `koanf:"auth" json:"auth"`
}

// Target describes the platform migrated to.
type Target struct {
	Platform string            `koanf:"platform" json:"platform"`
	URL      string            `koanf:"url" json:"url,omitempty"`
	Owner    string            `koanf:"owner" json:"owner,omitempty"`
	Project  string            `koanf:"project" json:"project,omitempty"`
	Auth     Auth              `koanf:"auth" json:"auth"`
	RepoMap  map[string]string `koanf:"repoMap" json:"repoMap,omitempty"`
}

// Auth selects an authentication method. Secret material is only ever
// referenced (environment variable or file), never embedded.
type Auth struct {
	Method           string `koanf:"method" json:"method,omitempty"`
	TokenEnv         string `koanf:"tokenEnv" json:"tokenEnv,omitempty"`
	TokenFile        string `koanf:"tokenFile" json:"tokenFile,omitempty"`
	AppID            string `koanf:"appId" json:"appId,omitempty"`
	InstallationID   string `koanf:"installationId" json:"installationId,omitempty"`
	PrivateKeyEnv    string `koanf:"privateKeyEnv" json:"privateKeyEnv,omitempty"`
	PrivateKeyFile   string `koanf:"privateKeyFile" json:"privateKeyFile,omitempty"`
	ClientID         string `koanf:"clientId" json:"clientId,omitempty"`
	ClientSecretEnv  string `koanf:"clientSecretEnv" json:"clientSecretEnv,omitempty"`
	ClientSecretFile string `koanf:"clientSecretFile" json:"clientSecretFile,omitempty"`
	TenantID         string `koanf:"tenantId" json:"tenantId,omitempty"`
}

// Conversion controls pipeline conversion behaviour.
type Conversion struct {
	Mode          string `koanf:"mode" json:"mode,omitempty"`
	OnUnsupported string `koanf:"onUnsupported" json:"onUnsupported,omitempty"`
}

// Policy holds security policy applied to converted output.
type Policy struct {
	ActionPinning       string            `koanf:"actionPinning" json:"actionPinning,omitempty"`
	AllowedActionOwners []string          `koanf:"allowedActionOwners" json:"allowedActionOwners,omitempty"`
	MaxPermissions      map[string]string `koanf:"maxPermissions" json:"maxPermissions,omitempty"`
	Delivery            string            `koanf:"delivery" json:"delivery,omitempty"`
}

// Secrets lists where secret values may be read from.
type Secrets struct {
	From []SecretSource `koanf:"from" json:"from,omitempty"`
}

// SecretSource is one location secret values are resolved from.
type SecretSource struct {
	Kind   string `koanf:"kind" json:"kind"`
	Prefix string `koanf:"prefix" json:"prefix,omitempty"`
	Path   string `koanf:"path" json:"path,omitempty"`
}

// Credential is a named credential in a rotation pool.
type Credential struct {
	Actor       string `koanf:"actor" json:"actor,omitempty"`
	TargetScope string `koanf:"targetScope" json:"targetScope,omitempty"`
	Name        string `koanf:"name" json:"name"`
	Auth        Auth   `koanf:"auth" json:"auth"`
}

// Output controls where artifacts are written.
type Output struct {
	Dir          string `koanf:"dir" json:"dir,omitempty"`
	EncryptCache bool   `koanf:"encryptCache" json:"encryptCache,omitempty"`
}

// Defaults returns the configuration used when nothing else is specified.
func Defaults() Config {
	return Config{
		Version:    SchemaVersion,
		Conversion: Conversion{Mode: "strict", OnUnsupported: "fail"},
		Policy:     Policy{ActionPinning: "sha", Delivery: "branch-pr"},
		Output:     Output{Dir: "out"},
	}
}
