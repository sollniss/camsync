// Package config merges defaults, the config file, environment variables and
// command-line flags, in that order of increasing precedence, into a plain
// Config struct that the rest of the program consumes.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/sollniss/camsync"
)

const (
	// EnvPrefix is the prefix of environment variables, e.g.
	// CAMSYNC_SIDECAR_STYLE=name. An empty variable counts as unset.
	EnvPrefix = "CAMSYNC"
	// FileName is the config file name without extension; yaml, yml and json
	// are accepted.
	FileName = "config"
	// AppDir is the sub-directory of the user's config directory.
	AppDir = "camsync"
)

// Config holds every setting a user may persist. Keys match the flag names.
type Config struct {
	Recursive    bool
	SidecarStyle string
	Output       string
}

// Outputs lists the accepted values of Output.
var Outputs = []string{"table", "json"}

// setting describes one configuration key. Values from the file, the
// environment and the flags all arrive as strings and go through apply, so
// every source reports the same errors.
type setting struct {
	key   string
	apply func(*Config, string) error
}

var settings = []setting{
	{"recursive", func(c *Config, s string) error {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("recursive must be true or false, not %q", s)
		}
		c.Recursive = b
		return nil
	}},
	{"sidecar-style", func(c *Config, s string) error { c.SidecarStyle = s; return nil }},
	{"output", func(c *Config, s string) error { c.Output = s; return nil }},
}

// defaults returns the configuration used when nothing else supplies a value.
func defaults() *Config {
	return &Config{Recursive: false, SidecarStyle: "ext", Output: "table"}
}

// Dir returns the directory searched for the config file, normally
// $XDG_CONFIG_HOME/camsync or ~/.config/camsync.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, AppDir), nil
}

// extensions are tried in this order when searching the config directory, so
// that config.json wins over config.yaml when both exist.
var extensions = []string{".json", ".yaml", ".yml"}

// find returns the config file in dir, or "" when there is none.
func find(dir string) string {
	for _, ext := range extensions {
		p := filepath.Join(dir, FileName+ext)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Load builds the configuration. flags may be nil; when given, flags the
// user set override everything else. file selects an explicit config file;
// when empty the default location is searched and a missing file is fine.
func Load(flags *pflag.FlagSet, file string) (*Config, error) {
	c := defaults()

	if file == "" {
		if dir, err := Dir(); err == nil {
			file = find(dir)
		}
	}
	if file != "" {
		if err := applyFile(c, file); err != nil {
			return nil, err
		}
	}
	if err := applyEnv(c); err != nil {
		return nil, err
	}
	if err := applyFlags(c, flags); err != nil {
		return nil, err
	}
	return c, c.Validate()
}

// applyFile decodes path and applies every key it holds. Unknown keys are an
// error, so a typo does not silently do nothing.
func applyFile(c *Config, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	raw := map[string]any{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(b, &raw)
	case ".json":
		err = json.Unmarshal(b, &raw)
	default:
		return fmt.Errorf("unsupported config format %q in %s (want .yaml, .yml or .json)", filepath.Ext(path), path)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	for k, v := range raw {
		key := strings.ToLower(strings.TrimSpace(k))
		i := slices.IndexFunc(settings, func(s setting) bool { return s.key == key })
		if i < 0 {
			return fmt.Errorf("%s: unknown setting %q (want %s)", path, k, strings.Join(keys(), ", "))
		}
		s, err := text(v)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", path, settings[i].key, err)
		}
		if err := settings[i].apply(c, s); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// text renders a decoded scalar as the string form the settings expect.
func text(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", errors.New("missing value")
	}
	return "", errors.New("want a string or a boolean")
}

// applyEnv applies CAMSYNC_* variables. An unset or empty variable is
// ignored, so exporting an empty value does not blank out the config file.
func applyEnv(c *Config) error {
	for _, s := range settings {
		v, ok := os.LookupEnv(envName(s.key))
		if !ok || v == "" {
			continue
		}
		if err := s.apply(c, v); err != nil {
			return fmt.Errorf("%s: %w", envName(s.key), err)
		}
	}
	return nil
}

// envName maps a setting key to its environment variable, e.g. sidecar-style
// to CAMSYNC_SIDECAR_STYLE.
func envName(key string) string {
	return EnvPrefix + "_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// applyFlags applies only the flags the user actually set, so that an unset
// flag sitting at its default does not override the file or the environment.
func applyFlags(c *Config, flags *pflag.FlagSet) error {
	if flags == nil {
		return nil
	}
	for _, s := range settings {
		f := flags.Lookup(s.key)
		if f == nil || !f.Changed {
			continue
		}
		if err := s.apply(c, f.Value.String()); err != nil {
			return fmt.Errorf("--%s: %w", s.key, err)
		}
	}
	return nil
}

// keys lists the setting names for error messages.
func keys() []string {
	out := make([]string, len(settings))
	for i, s := range settings {
		out[i] = s.key
	}
	return out
}

// Validate checks the values, wherever they came from.
func (c *Config) Validate() error {
	if _, err := camsync.ParseSidecarStyle(c.SidecarStyle); err != nil {
		return err
	}
	if slices.Contains(Outputs, c.Output) {
		return nil
	}
	return fmt.Errorf("unknown output format %q (want %s)", c.Output, strings.Join(Outputs, " or "))
}

// Style returns SidecarStyle parsed; call after Validate.
func (c *Config) Style() camsync.SidecarStyle {
	s, _ := camsync.ParseSidecarStyle(c.SidecarStyle)
	return s
}
