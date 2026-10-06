package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/HarnageaGabriel/trasloco/schemas"
)

const schemaURL = "https://github.com/HarnageaGabriel/trasloco/schemas/config/v1.schema.json"

// FieldError is a single validation problem attached to a field path.
type FieldError struct {
	// Path is the dotted path of the field, e.g. "source.auth.tokenEnv".
	// It is empty for problems affecting the document as a whole.
	Path string
	// Message describes the problem.
	Message string
}

// Error implements the error interface.
func (e FieldError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ValidationError aggregates every problem found in a configuration.
type ValidationError struct {
	Problems []FieldError
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration")
	for _, p := range e.Problems {
		b.WriteString("\n  ")
		b.WriteString(p.Error())
	}
	return b.String()
}

var (
	schemaOnce     sync.Once
	compiledSchema *jsonschema.Schema
	errSchema      error
)

func schema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.ConfigV1))
		if err != nil {
			errSchema = fmt.Errorf("parse embedded config schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err := c.AddResource(schemaURL, doc); err != nil {
			errSchema = fmt.Errorf("load embedded config schema: %w", err)
			return
		}
		sch, err := c.Compile(schemaURL)
		if err != nil {
			errSchema = fmt.Errorf("compile embedded config schema: %w", err)
			return
		}
		compiledSchema = sch
	})
	return compiledSchema, errSchema
}

// ValidateDocument validates a decoded document (as produced by a YAML or
// JSON decoder) against the JSON Schema. It returns a *ValidationError when
// the document does not conform.
func ValidateDocument(doc any) error {
	sch, err := schema()
	if err != nil {
		return err
	}
	// The schema validator only understands JSON-typed values; normalise via JSON.
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode configuration for validation: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode configuration for validation: %w", err)
	}
	verr := sch.Validate(inst)
	if verr == nil {
		return nil
	}
	var jerr *jsonschema.ValidationError
	if !errors.As(verr, &jerr) {
		return verr
	}
	p := message.NewPrinter(language.English)
	var problems []FieldError
	collect(jerr, p, &problems)
	return &ValidationError{Problems: dedupe(problems)}
}

func collect(e *jsonschema.ValidationError, p *message.Printer, out *[]FieldError) {
	if len(e.Causes) == 0 {
		if additional, ok := e.ErrorKind.(*kind.AdditionalProperties); ok {
			base := e.InstanceLocation
			for _, property := range additional.Properties {
				path := append(append([]string(nil), base...), property)
				*out = append(*out, FieldError{
					Path:    strings.Join(path, "."),
					Message: "unknown key",
				})
			}
			return
		}
		*out = append(*out, FieldError{
			Path:    strings.Join(e.InstanceLocation, "."),
			Message: e.ErrorKind.LocalizedString(p),
		})
		return
	}
	for _, c := range e.Causes {
		collect(c, p, out)
	}
}

func dedupe(in []FieldError) []FieldError {
	seen := make(map[FieldError]struct{}, len(in))
	out := make([]FieldError, 0, len(in))
	for _, f := range in {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Validate runs the schema and the semantic rules against a typed Config.
func Validate(cfg *Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	var problems []FieldError
	if err := ValidateDocument(doc); err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			return err
		}
		problems = append(problems, ve.Problems...)
	}
	problems = append(problems, semantic(cfg)...)
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: dedupe(problems)}
}

type adder func(path, format string, args ...any)

// semantic holds the rules JSON Schema cannot express cleanly.
func semantic(cfg *Config) []FieldError {
	var p []FieldError
	add := func(path, format string, args ...any) {
		p = append(p, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
	}

	if cfg.Source != nil {
		checkURL(cfg.Source.URL, "source.url", add)
		checkAuth(&cfg.Source.Auth, "source.auth", add)
	}
	if cfg.Target != nil {
		checkURL(cfg.Target.URL, "target.url", add)
		checkAuth(&cfg.Target.Auth, "target.auth", add)
	}

	names := make(map[string]int, len(cfg.Credentials))
	for i := range cfg.Credentials {
		c := &cfg.Credentials[i]
		path := fmt.Sprintf("credentials.%d", i)
		if prev, dup := names[c.Name]; dup && c.Name != "" {
			add(path+".name", "duplicate credential name %q (also credentials.%d)", c.Name, prev)
		}
		names[c.Name] = i
		if c.Auth.Method == "" {
			add(path+".auth.method", "is required for pooled credentials")
		}
		checkAuth(&c.Auth, path+".auth", add)
	}

	for i, s := range cfg.Secrets.From {
		path := fmt.Sprintf("secrets.from.%d", i)
		if s.Kind == "file" && s.Path == "" {
			add(path+".path", "is required when kind is \"file\"")
		}
		if s.Kind == "env" && s.Path != "" {
			add(path+".path", "is not allowed when kind is \"env\"")
		}
	}
	return p
}

func checkURL(raw, path string, add adder) {
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		add(path, "must be an absolute http(s) URL, got %q", raw)
	}
}

// checkAuth enforces that the fields supplied match the chosen method.
func checkAuth(a *Auth, path string, add adder) {
	switch a.Method {
	case "token":
		if (a.TokenEnv == "") == (a.TokenFile == "") {
			add(path, "method \"token\" requires exactly one of tokenEnv or tokenFile")
		}
	case "github-app":
		if a.AppID == "" {
			add(path+".appId", "is required for method \"github-app\"")
		}
		if (a.PrivateKeyEnv == "") == (a.PrivateKeyFile == "") {
			add(path, "method \"github-app\" requires exactly one of privateKeyEnv or privateKeyFile")
		}
	case "client-credentials":
		if a.ClientID == "" {
			add(path+".clientId", "is required for method \"client-credentials\"")
		}
		if (a.ClientSecretEnv == "") == (a.ClientSecretFile == "") {
			add(path, "method \"client-credentials\" requires exactly one of clientSecretEnv or clientSecretFile")
		}
	}
}
