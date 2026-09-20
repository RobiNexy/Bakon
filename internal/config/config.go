package config

import (
	"os"
	"path/filepath"
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
}

const DefaultMaxVersions = 100

// DefaultPath 返回配置文件位置：~/.bakon/config.toml。
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".bakon/config.toml"
	}
	return filepath.Join(home, ".bakon", "config.toml")
}

// Load 读取配置。文件不存在时返回默认值，不视为错误。
// 先填默认值再解码，保证未写出的字段仍取默认而非零值。
func Load(path string) (*Config, error) {
	cfg := &Config{
		Editor:    "",
		Store:     "~/.bakon/repo",
		Retention: Retention{MaxVersions: DefaultMaxVersions},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Template 返回带注释的默认配置模板，供 bakon config init 使用。
func Template() string {
	return `# bakon 全局配置

# 编辑器命令，可带参数（如 "code -w"）。
# 优先级：此配置 > $VISUAL/$EDITOR > 缺省 vi
editor = "vim"

# 版本仓库位置（存放全部版本历史的 git 仓库）
store = "~/.bakon/repo"

[retention]
# 每个文件保留的最大版本数；0 = 不限制。
# 可被 index.json 中 per-file 的 max_versions 覆盖。
max_versions = 100
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
