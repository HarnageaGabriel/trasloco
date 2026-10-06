package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

const (
	// DefaultFile is the file looked up in the working directory when no
	// explicit path is given. Its absence is not an error.
	DefaultFile = "trasloco.yaml"

	// EnvPrefix prefixes every environment variable that sets configuration.
	EnvPrefix = "TRASLOCO_"

	// EnvDelimiter separates nesting levels in environment variable names,
	// e.g. TRASLOCO_SOURCE__AUTH__TOKENENV sets source.auth.tokenEnv.
	EnvDelimiter = "__"

	keyDelim = "."
)

// reservedEnv lists TRASLOCO_* variables consumed elsewhere (not configuration).
var reservedEnv = map[string]struct{}{
	"CONFIG":     {},
	"LOG_FORMAT": {},
	"LOG_LEVEL":  {},
	"NO_COLOR":   {},
}

// Options controls how configuration is assembled.
type Options struct {
	// Path is the configuration file. When empty, DefaultFile is used if it
	// exists. When set, the file must exist.
	Path string
	// Environ returns the environment as KEY=VALUE pairs. Defaults to os.Environ.
	Environ func() []string
	// Flags, together with FlagKeys, supplies the highest-precedence layer.
	// Only flags explicitly set on the command line are applied.
	Flags *pflag.FlagSet
	// FlagKeys maps a flag name to the dotted configuration key it overrides.
	FlagKeys map[string]string
}

// Load merges defaults, the configuration file, the environment and flags (in
// increasing precedence), then validates the result.
func Load(opts Options) (*Config, error) {
	k := koanf.New(keyDelim)

	defaults, err := toMap(Defaults())
	if err != nil {
		return nil, err
	}
	if err := k.Load(mapProvider(defaults), nil); err != nil {
		return nil, fmt.Errorf("load defaults: %w", err)
	}

	if err := loadFile(k, opts.Path); err != nil {
		return nil, err
	}

	envOpt := env.Opt{Prefix: EnvPrefix, TransformFunc: transformEnv, EnvironFunc: opts.Environ}
	if err := k.Load(env.Provider(keyDelim, envOpt), nil); err != nil {
		return nil, fmt.Errorf("load environment: %w", err)
	}

	if opts.Flags != nil {
		cb := func(f *pflag.Flag) (string, any) {
			if !f.Changed {
				return "", nil
			}
			key, ok := opts.FlagKeys[f.Name]
			if !ok {
				return "", nil
			}
			return key, posflag.FlagVal(opts.Flags, f)
		}
		if err := k.Load(posflag.ProviderWithFlag(opts.Flags, keyDelim, nil, cb), nil); err != nil {
			return nil, fmt.Errorf("load flags: %w", err)
		}
	}

	var cfg Config
	dc := &mapstructure.DecoderConfig{
		TagName:          "koanf",
		ErrorUnused:      true,
		WeaklyTypedInput: true,
		Result:           &cfg,
	}
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "koanf", DecoderConfig: dc}); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// loadFile reads, schema-validates and merges the configuration file.
func loadFile(k *koanf.Koanf, path string) error {
	explicit := path != ""
	if !explicit {
		path = DefaultFile
	}
	p := file.Provider(path)
	data, err := p.ReadBytes()
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	doc, err := yaml.Parser().Unmarshal(data)
	if err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	// Validate the file on its own so unknown keys and literal secrets are
	// reported against what the user wrote, not against merged defaults.
	if err := ValidateDocument(doc); err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("config %s: %w", path, ve)
		}
		return err
	}
	if err := k.Load(mapProvider(doc), nil); err != nil {
		return fmt.Errorf("merge config %s: %w", path, err)
	}
	return nil
}

// mapProvider adapts an in-memory map to a koanf.Provider.
type mapProvider map[string]any

func (m mapProvider) ReadBytes() ([]byte, error) {
	return nil, errors.New("map provider does not support ReadBytes")
}

func (m mapProvider) Read() (map[string]any, error) { return m, nil }

func toMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode defaults: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode defaults: %w", err)
	}
	return m, nil
}

// transformEnv turns TRASLOCO_SOURCE__AUTH__TOKENENV=x into source.auth.tokenEnv.
// Names are matched case- and underscore-insensitively against the Config
// struct so the camelCase keys remain addressable from upper-case variables.
func transformEnv(name, value string) (string, any) {
	rest := strings.TrimPrefix(name, EnvPrefix)
	if _, ok := reservedEnv[rest]; ok || rest == "" {
		return "", nil
	}
	segs := strings.Split(rest, EnvDelimiter)
	path, kind := resolve(reflect.TypeFor[Config](), segs)
	if kind == reflect.Slice {
		parts := strings.Split(value, ",")
		list := make([]any, 0, len(parts))
		for _, s := range parts {
			if s = strings.TrimSpace(s); s != "" {
				list = append(list, s)
			}
		}
		return path, list
	}
	return path, value
}

func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

// resolve maps env-style segments to a canonical dotted key by walking t.
// Unknown segments are lower-cased and kept so validation can report them.
func resolve(t reflect.Type, segs []string) (string, reflect.Kind) {
	out := make([]string, 0, len(segs))
	kind := reflect.Invalid
	for i, seg := range segs {
		for t != nil && t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t == nil || t.Kind() != reflect.Struct {
			// Free-form map keys or unknown territory: keep the rest verbatim.
			for _, s := range segs[i:] {
				out = append(out, strings.ToLower(s))
			}
			if t != nil && t.Kind() == reflect.Map {
				kind = t.Elem().Kind()
			} else {
				kind = reflect.Invalid
			}
			return strings.Join(out, keyDelim), kind
		}
		field, ok := fieldByName(t, seg)
		if !ok {
			for _, s := range segs[i:] {
				out = append(out, strings.ToLower(s))
			}
			return strings.Join(out, keyDelim), reflect.Invalid
		}
		out = append(out, field.tag)
		t = field.typ
		kind = t.Kind()
	}
	return strings.Join(out, keyDelim), kind
}

type fieldInfo struct {
	tag string
	typ reflect.Type
}

func fieldByName(t reflect.Type, seg string) (fieldInfo, bool) {
	want := norm(seg)
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("koanf")
		if tag != "" && norm(tag) == want {
			return fieldInfo{tag: tag, typ: f.Type}, true
		}
	}
	return fieldInfo{}, false
}
