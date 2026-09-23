package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Retention 全局保留策略；0 表示不限制版本数。
type Retention struct {
	MaxVersions int `toml:"max_versions"`
}

type Config struct {
	Editor    string    `toml:"editor"`
	Store     string    `toml:"store"`
	Retention Retention `toml:"retention"`
	Output    Output    `toml:"output"`
}

type Output struct {
	Color  string `toml:"color"`
	Format string `toml:"format"`
}

const DefaultMaxVersions = 100

// DefaultPath 返回配置文件位置：~/.bakon/config.toml。
func DefaultPath() string {
	if root := os.Getenv("BAKON_HOME"); root != "" {
		return filepath.Join(root, "config.toml")
	}
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".bakon", "config.toml")
		}
	}
	return filepath.Join(configHome(), "bakon", "config.toml")
}

func configHome() string {
	if root := os.Getenv("BAKON_HOME"); root != "" {
		return root
	}
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil {
		// Keep the historical location as a compatibility fallback when no
		// XDG directory was configured explicitly.
		return home
	}
	return ".config"
}

func dataHome() string {
	if root := os.Getenv("BAKON_HOME"); root != "" {
		return root
	}
	if root := os.Getenv("XDG_DATA_HOME"); root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return filepath.Join(".local", "share")
}

// DefaultStore returns the persistent repository location under XDG data.
func DefaultStore() string {
	if os.Getenv("BAKON_HOME") != "" {
		return filepath.Join(dataHome(), "repo")
	}
	if os.Getenv("XDG_DATA_HOME") != "" {
		return filepath.Join(dataHome(), "bakon", "repo")
	}
	// The literal keeps old config templates portable; ExpandHome resolves it
	// when the application opens the repository.
	return "~/.bakon/repo"
}

// Load 读取配置。文件不存在时返回默认值，不视为错误。
// 先填默认值再解码，保证未写出的字段仍取默认而非零值。
func Load(path string) (*Config, error) {
	cfg := &Config{
		Editor:    "",
		Store:     DefaultStore(),
		Retention: Retention{MaxVersions: DefaultMaxVersions},
		Output:    Output{Color: "auto", Format: "human"},
	}
	paths := []string{}
	// An explicitly selected file is an isolated project configuration. This
	// keeps --config deterministic; the default path receives system and user
	// layers below.
	if path == "" || path == DefaultPath() {
		if system := filepath.Join("/etc", "bakon", "config.toml"); system != path {
			paths = append(paths, system)
		}
		if user := DefaultPath(); user != path {
			paths = append(paths, user)
		}
	}
	if path != "" {
		paths = append(paths, path)
	}
	for _, candidate := range paths {
		data, err := os.ReadFile(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", candidate, err)
		}
	}
	applyEnv(cfg)
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("BAKON_EDITOR"); v != "" {
		cfg.Editor = v
	}
	if v := os.Getenv("BAKON_STORE"); v != "" {
		cfg.Store = v
	}
	if v := os.Getenv("BAKON_RETENTION_MAX_VERSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Retention.MaxVersions = n
		}
	}
	if v := os.Getenv("BAKON_FORMAT"); v != "" {
		cfg.Output.Format = v
	}
	if v := os.Getenv("BAKON_COLOR"); v != "" {
		cfg.Output.Color = v
	}
}

// Template 返回带注释的默认配置模板，供 bakon config init 使用。
func Template() string {
	return `# bakon 全局配置

# 编辑器命令，可带参数（如 "code -w"）。
# 优先级：此配置 > $VISUAL/$EDITOR > 缺省 vi
editor = ""

# 版本仓库位置（存放全部版本历史的 git 仓库）
store = "` + DefaultStore() + `"

[retention]
# 每个文件保留的最大版本数；0 = 不限制。
# 可被 index.json 中 per-file 的 max_versions 覆盖。
max_versions = 100

[output]
# auto | always | never
color = "auto"
# human | plain | json
format = "human"
`
}

// Init 写入默认配置模板。已存在时不覆盖，返回 written=false。
func Init(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(Template()), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ExpandHome 展开路径前缀的 ~（仅支持开头单独的 ~ 与 ~/ 形式）。
func ExpandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// EffectiveEditor 编辑器优先级：配置值 > $VISUAL/$EDITOR > vi。
// 设计文档只规定"配置缺省 vi"，环境变量回退是约定俗成的补充行为。
func (c *Config) EffectiveEditor() string {
	if c.Editor != "" {
		return c.Editor
	}
	for _, v := range []string{"BAKON_EDITOR", "VISUAL", "EDITOR"} {
		if e := os.Getenv(v); e != "" {
			return e
		}
	}
	return "vi"
}

// Validate checks values that would otherwise cause ambiguous CLI output.
func (c *Config) Validate() error {
	if c.Retention.MaxVersions < 0 {
		return fmt.Errorf("retention.max_versions must be >= 0")
	}
	if c.Output.Format != "human" && c.Output.Format != "plain" && c.Output.Format != "json" && c.Output.Format != "" {
		return fmt.Errorf("output.format must be human, plain, or json")
	}
	if c.Output.Color != "auto" && c.Output.Color != "always" && c.Output.Color != "never" && c.Output.Color != "" {
		return fmt.Errorf("output.color must be auto, always, or never")
	}
	return nil
}
