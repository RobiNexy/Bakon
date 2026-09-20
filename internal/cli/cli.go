// Package cli 将 cobra 命令接线到 bakon 核心流程。
// 错误处理约定：cmd 层不打印错误（SilenceErrors），
// 统一由 main 打印并决定退出码；钩子失败映射到退出码 2。
package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"

	"github.com/RobiNexy/Bakon/internal/bakon"
	"github.com/RobiNexy/Bakon/internal/config"

	"github.com/spf13/cobra"
)

var configFlag string

// 构建信息由 scripts/build.sh 经 -ldflags -X 注入；
// 源码直编（go build）时保持 dev 占位。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func newApp() (*bakon.App, error) {
	return bakon.NewApp(configFlag)
}

func atoiVer(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid version number %q", s)
	}
	return n, nil
}

func Execute() error {
	root := &cobra.Command{
		Use:           "bakon",
		Short:         "Effortless per-file version history backed by git",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configFlag, "config", config.DefaultPath(), "config file path")

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
		versionCmd(),
	)
	return root.Execute()
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			ver, err := app.Edit(args[0])
			if err != nil {
				return err
			}
			if ver > 0 {
				fmt.Printf("bakon: saved version %d of %s\n", ver, args[0])
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
			return err
		},
	}
}

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

// ExitCode 将错误分类为进程退出码：0 成功、2 钩子失败、1 其他。
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, bakon.ErrHookFailed) {
		return 2
	}
	return 1
}
