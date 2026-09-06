package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"

	"github.com/sollniss/camsync"
)

func flags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.BoolP("recursive", "r", false, "")
	fs.String("sidecar-style", "ext", "")
	fs.StringP("output", "o", "table", "")
	return fs
}

func TestDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user config file
	c, err := Load(flags(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Recursive || c.SidecarStyle != "ext" || c.Output != "table" || c.Style() != camsync.SidecarByExt {
		t.Errorf("defaults = %+v", c)
	}
}

func TestPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, AppDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("sidecar-style: name\nrecursive: true\noutput: json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// File alone.
	c, err := Load(flags(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Recursive || c.SidecarStyle != "name" || c.Output != "json" {
		t.Errorf("from file = %+v", c)
	}

	// Environment beats the file.
	t.Setenv("CAMSYNC_SIDECAR_STYLE", "ext")
	t.Setenv("CAMSYNC_RECURSIVE", "false")
	c, err = Load(flags(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Recursive || c.SidecarStyle != "ext" || c.Output != "json" {
		t.Errorf("env over file = %+v", c)
	}

	// A flag the user set beats the environment; an unset flag does not.
	fs := flags()
	if err := fs.Parse([]string{"--sidecar-style=name"}); err != nil {
		t.Fatal(err)
	}
	c, err = Load(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SidecarStyle != "name" || c.Recursive || c.Output != "json" {
		t.Errorf("flag over env = %+v", c)
	}

	// Explicit file.
	other := filepath.Join(home, "other.json")
	if err := os.WriteFile(other, []byte(`{"output": "table"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(flags(), other)
	if err != nil {
		t.Fatal(err)
	}
	if c.Output != "table" {
		t.Errorf("explicit file = %+v", c)
	}
	if _, err := Load(flags(), filepath.Join(home, "missing.yaml")); err == nil {
		t.Error("explicit missing file should fail")
	}
}

func TestValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CAMSYNC_OUTPUT", "xml")
	if _, err := Load(flags(), ""); err == nil {
		t.Error("bad output should fail")
	}
	t.Setenv("CAMSYNC_OUTPUT", "json")
	t.Setenv("CAMSYNC_SIDECAR_STYLE", "weird")
	if _, err := Load(flags(), ""); err == nil {
		t.Error("bad sidecar style should fail")
	}
}

// write puts a config file in the user config directory and returns its path.
func write(t *testing.T, home, name, body string) string {
	t.Helper()
	dir := filepath.Join(home, AppDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// An empty variable must count as unset, or exporting one would silently
// blank out the config file.
func TestEmptyEnvIsUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	write(t, home, "config.yaml", "sidecar-style: name\noutput: json\n")

	t.Setenv("CAMSYNC_SIDECAR_STYLE", "")
	c, err := Load(flags(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SidecarStyle != "name" || c.Output != "json" {
		t.Errorf("empty env should not override the file: %+v", c)
	}
}

func TestFormats(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"config.yaml", "output: json\n"},
		{"config.yml", "output: json\n"},
		{"config.json", `{"output": "json"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			write(t, home, tc.name, tc.body)
			c, err := Load(flags(), "")
			if err != nil {
				t.Fatal(err)
			}
			if c.Output != "json" {
				t.Errorf("%s not loaded: %+v", tc.name, c)
			}
		})
	}
}

// json is searched before yaml, matching the order viper used.
func TestSearchOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	write(t, home, "config.yaml", "sidecar-style: name\n")
	write(t, home, "config.json", `{"sidecar-style": "ext"}`)
	c, err := Load(flags(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SidecarStyle != "ext" {
		t.Errorf("config.json should win over config.yaml: %+v", c)
	}
}

func TestFileErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct{ name, file, body string }{
		{"unknown key", "bad.yaml", "recursiv: true\n"},
		{"non-boolean", "bool.yaml", "recursive: sometimes\n"},
		{"unsupported format", "cfg.toml", "output = \"json\"\n"},
		{"malformed", "broken.yaml", "output: [unterminated\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(home, tc.file)
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(flags(), p); err == nil {
				t.Errorf("%s should fail", tc.name)
			}
		})
	}
}

func TestKeysAreCaseInsensitive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	write(t, home, "config.yaml", "Sidecar-Style: name\nRECURSIVE: true\n")
	c, err := Load(flags(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SidecarStyle != "name" || !c.Recursive {
		t.Errorf("keys should be case-insensitive: %+v", c)
	}
}

// A bad boolean must be reported rather than silently falling back to false.
func TestBadEnvBool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CAMSYNC_RECURSIVE", "yes please")
	if _, err := Load(flags(), ""); err == nil {
		t.Error("unparseable boolean should fail")
	}
}
