package config

import (
	"strings"
	"testing"
)

func TestValidateSemantic(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		path string
	}{
		{
			name: "duplicate credential",
			edit: func(c *Config) {
				c.Credentials = []Credential{
					{Name: "pool", Auth: Auth{Method: "oidc"}},
					{Name: "pool", Auth: Auth{Method: "oidc"}},
				}
			},
			path: "credentials.1.name",
		},
		{
			name: "file source needs path",
			edit: func(c *Config) {
				c.Secrets.From = []SecretSource{{Kind: "file"}}
			},
			path: "secrets.from.0.path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			tt.edit(&cfg)
			err := Validate(&cfg)
			if err == nil || !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("Validate() error = %v, want path %q", err, tt.path)
			}
		})
	}
}
