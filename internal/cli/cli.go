// Package cli 将 cobra 命令接线到 bakon 核心流程。
// 错误处理约定：cmd 层不打印错误（SilenceErrors），
// 统一由 main 打印并决定退出码；钩子失败映射到退出码 2。
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/RobiNexy/Bakon/internal/bakon"
	"github.com/RobiNexy/Bakon/internal/config"

	"github.com/spf13/cobra"
)

var configFlag string
var storeFlag string
var formatFlag string
var noColorFlag bool
var noHookFlag bool

var ErrDiffFound = errors.New("differences found")

// 构建信息由 scripts/build.sh 经 -ldflags -X 注入；
// 源码直编（go build）时保持 dev 占位。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func newApp() (*bakon.App, error) {
	app, err := bakon.NewApp(configFlag)
	if err != nil {
		return nil, err
	}
	if storeFlag != "" {
		app.Repo.Dir = config.ExpandHome(storeFlag)
		app.IndexPath = filepath.Join(app.Repo.Dir, "index.json")
	}
	if formatFlag != "" {
		app.Format = formatFlag
	}
	if noColorFlag {
		app.Color = "never"
	}
	app.NoHook = noHookFlag
	return app, nil
}

func atoiVer(s string) (int, error) {
	n, err := strconv.Atoi(s)
	// 0 为纳管基线版本，合法。
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid version number %q", s)
	}
	return n, nil
}

func outputFormat(app *bakon.App) string {
	if formatFlag != "" {
		return formatFlag
	}
	if app.Format == "human" && !isTTY(os.Stdout) {
		return "plain"
	}
	if app.Format == "" {
		return "human"
	}
	return app.Format
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func Execute() error {
	root := &cobra.Command{
		Use:           "bakon",
		Short:         "Effortless per-file version history backed by git",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configFlag, "config", config.DefaultPath(), "config file path")
	root.PersistentFlags().StringVar(&storeFlag, "store", "", "repository path")
	root.PersistentFlags().StringVarP(&formatFlag, "format", "", "", "output format: human, plain, or json")
	root.PersistentFlags().BoolVar(&noColorFlag, "no-color", false, "disable colored output")
	root.PersistentFlags().BoolVar(&noHookFlag, "no-hook", false, "skip configured hooks")

	root.AddCommand(
		editCmd(),
		logCmd(),
		diffCmd(),
		showCmd(),
		revertCmd(),
		lsCmd(),
		mvCmd(),
		pruneCmd(),
		hookCmd(),
		configCmd(),
		versionCmd(),
		pathCmd(),
		verifyCmd(),
		dumpCmd(),
	)
	return root.Execute()
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or initialize the global configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Show effective configuration and file locations",
			Args:  cobra.NoArgs,
			RunE: func(c *cobra.Command, args []string) error {
				app, err := newApp()
				if err != nil {
					return err
				}
				fmt.Printf("config:       %s\n", configFlag)
				fmt.Printf("editor:       %s\n", app.Editor)
				fmt.Printf("store:        %s\n", app.Repo.Dir)
				fmt.Printf("index:        %s\n", app.IndexPath)
				fmt.Printf("max_versions: %d (global, 0 = unlimited)\n", app.GlobalMax)
				return nil
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Write the default config template (never overwrites)",
			Args:  cobra.NoArgs,
			RunE: func(c *cobra.Command, args []string) error {
				written, err := config.Init(configFlag)
				if err != nil {
					return err
				}
				if written {
					fmt.Printf("bakon: wrote %s\n", configFlag)
				} else {
					fmt.Printf("bakon: already exists, left untouched: %s\n", configFlag)
				}
				return nil
			},
		},
	)
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if formatFlag == "json" {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "commit": commit, "built": date, "go": runtime.Version()})
			}
			fmt.Printf("bakon %s (commit: %s, built: %s, %s)\n",
				version, commit, date, runtime.Version())
			return nil
		},
	}
}

func editCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <file>",
		Short: "Open the editor; commit a new version if content changed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp()
			if err != nil {
				return err
			}
			res, err := app.Edit(args[0])
			if err != nil {
				return err
			}
			switch {
			case res.Adopted && res.Change > 0:
				fmt.Printf("bakon: tracked %s (baseline version 0), saved version %d\n",
					args[0], res.Change)
			case res.Adopted:
				fmt.Printf("bakon: tracked %s (baseline version 0 saved); no changes\n", args[0])
			case res.Change > 0:
				fmt.Printf("bakon: saved version %d of %s\n", res.Change, args[0])
			}
			return nil
		},
	}
}

func logCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "log <file>",
		Short: "List versions of a managed file (newest first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp()
			if err != nil {
				return err
			}
			infos, err := app.Log(args[0])
			if err != nil {
				return err
			}
			format := outputFormat(app)
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				for _, in := range infos {
					if err := enc.Encode(map[string]any{"ver": in.Ver, "time": in.Time.UTC().Format(time.RFC3339), "size": in.Size, "source": in.Source}); err != nil {
						return err
					}
				}
				return nil
			}
			if format == "plain" {
				for _, in := range infos {
					fmt.Printf("%d\t%s\t%d\t%s\n", in.Ver, in.Time.UTC().Format(time.RFC3339), in.Size, in.Source)
				}
				return nil
			}
			fmt.Println("#  ver   time                  size")
			for _, in := range infos {
				fmt.Printf("  %-6d%s   %s\n",
					in.Ver,
					in.Time.Format("2006-01-02 15:04:05"),
					bakon.HumanSize(in.Size))
			}
			return nil
		},
	}
}

func diffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <file> [v1] [v2]",
		Short: "Diff two versions; defaults to the two most recent",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp()
			if err != nil {
				return err
			}
			out, err := app.Diff(args[0], args[1:])
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(out)
			if err != nil {
				return err
			}
			if len(out) > 0 {
				return &diffFoundError{}
			}
			return nil
		},
	}
}

type diffFoundError struct{}

func (*diffFoundError) Error() string { return "differences found" }
func (*diffFoundError) Unwrap() error { return ErrDiffFound }

func showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <file> <v>",
		Short: "Print the content of version v",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ver, err := atoiVer(args[1])
			if err != nil {
				return err
			}
			app, err := newApp()
			if err != nil {
				return err
			}
			content, err := app.Show(args[0], ver)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(content)
			return err
		},
	}
}

func revertCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revert <file> <v>",
		Short: "Restore the file to version v, committed as a new version",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ver, err := atoiVer(args[1])
			if err != nil {
				return err
			}
			app, err := newApp()
			if err != nil {
				return err
			}
			newVer, err := app.Revert(args[0], ver)
			if err != nil {
				return err
			}
			if newVer == 0 {
				fmt.Printf("bakon: %s already matches version %d, no new version\n",
					args[0], ver)
				return nil
			}
			fmt.Printf("bakon: reverted %s to version %d, saved as version %d\n",
				args[0], ver, newVer)
			return nil
		},
	}
}

func lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List all managed files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp()
			if err != nil {
				return err
			}
			paths, err := app.Ls()
			if err != nil {
				return err
			}
			for _, p := range paths {
				fmt.Println(p)
			}
			return nil
		},
	}
}

func mvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mv <old> <new>",
		Short: "Update the path mapping of a managed file, keeping history",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp()
			if err != nil {
				return err
			}
			return app.Mv(args[0], args[1])
		},
	}
}

func pruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune [<file>]",
		Short: "Trim history to the retention limit (all files or one)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp()
			if err != nil {
				return err
			}
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			n, err := app.Prune(target)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Println("bakon: nothing to prune")
			} else {
				fmt.Printf("bakon: pruned %d version(s)\n", n)
			}
			return nil
		},
	}
}

func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Manage the change hook of a managed file",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "set <file> <cmd>",
			Short: "Set the hook command run after each new version",
			Args:  cobra.ExactArgs(2),
			RunE: func(c *cobra.Command, args []string) error {
				app, err := newApp()
				if err != nil {
					return err
				}
				return app.HookSet(args[0], args[1])
			},
		},
		&cobra.Command{
			Use:   "unset <file>",
			Short: "Clear the hook command",
			Args:  cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				app, err := newApp()
				if err != nil {
					return err
				}
				return app.HookUnset(args[0])
			},
		},
		&cobra.Command{
			Use:   "show <file>",
			Short: "Show the hook command",
			Args:  cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				app, err := newApp()
				if err != nil {
					return err
				}
				hook, err := app.HookShow(args[0])
				if err != nil {
					return err
				}
				if hook == "" {
					fmt.Println("(not set)")
				} else {
					fmt.Println(hook)
				}
				return nil
			},
		},
	)
	return cmd
}

func pathCmd() *cobra.Command {
	return &cobra.Command{Use: "path <file>", Short: "Print the stable internal path mapping", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp()
		if err != nil {
			return err
		}
		info, err := app.Path(args[0])
		if err != nil {
			return err
		}
		if formatFlag == "json" {
			return json.NewEncoder(os.Stdout).Encode(info)
		}
		fmt.Printf("%s\t%s\t%s\n", info.Path, info.ID, info.Repo)
		return nil
	}}
}

func verifyCmd() *cobra.Command {
	return &cobra.Command{Use: "verify", Short: "Check repository consistency", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp()
		if err != nil {
			return err
		}
		if err := app.Verify(); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil
	}}
}

func dumpCmd() *cobra.Command {
	return &cobra.Command{Use: "dump <file> <v>", Short: "Write a version as binary data", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		ver, err := atoiVer(args[1])
		if err != nil {
			return err
		}
		app, err := newApp()
		if err != nil {
			return err
		}
		content, err := app.Dump(args[0], ver)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(content)
		return err
	}}
}

// ExitCode 将错误分类为进程退出码：0 成功、2 钩子失败、1 其他。
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, bakon.ErrHookFailed) {
		return 5
	}
	var diffErr *diffFoundError
	if errors.As(err, &diffErr) {
		return 1
	}
	if errors.Is(err, bakon.ErrUsage) {
		return 2
	}
	if errors.Is(err, bakon.ErrNotFound) {
		return 3
	}
	if errors.Is(err, bakon.ErrConflict) {
		return 4
	}
	if errors.Is(err, bakon.ErrCorrupt) {
		return 6
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown command") || strings.Contains(msg, "requires") || strings.Contains(msg, "accepts") || strings.Contains(msg, "invalid version") {
		return 2
	}
	if strings.Contains(msg, "not managed") || strings.Contains(msg, "no such file") || strings.Contains(msg, "no version") || strings.Contains(msg, "has been pruned") {
		return 3
	}
	if strings.Contains(msg, "acquire lock") {
		return 4
	}
	return 1
}
