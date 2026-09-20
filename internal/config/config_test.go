package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Store != "~/.bakon/repo" {
		t.Errorf("store default = %q", cfg.Store)
	}
	if cfg.Retention.MaxVersions != DefaultMaxVersions {
		t.Errorf("max default = %d", cfg.Retention.MaxVersions)
	}
}

func TestLoadOverrides(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	body := "editor = \"nano\"\nstore = \"/data/repo\"\n\n[retention]\nmax_versions = 5\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Editor != "nano" || cfg.Store != "/data/repo" || cfg.Retention.MaxVersions != 5 {
		t.Errorf("loaded = %+v", cfg)
	}
}

func TestLoadPartialKeepsDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte("store = \"/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Store != "/x" || cfg.Retention.MaxVersions != DefaultMaxVersions {
		t.Errorf("partial = %+v", cfg)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := map[string]string{
		"~":           home,
		"~/x/repo":    filepath.Join(home, "x/repo"),
		"/abs/~/repo": "/abs/~/repo",
		"":            "",
	}
	for in, want := range cases {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEffectiveEditor(t *testing.T) {
	cfg := &Config{Editor: "vim"}
	if got := cfg.EffectiveEditor(); got != "vim" {
		t.Errorf("editor = %q", got)
	}
	cfg = &Config{}
	t.Setenv("EDITOR", "ed")
	if got := cfg.EffectiveEditor(); got != "ed" {
		t.Errorf("env editor = %q", got)
	}
}
