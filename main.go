// Command bakon 是单机文件版本管理 CLI。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/RobiNexy/Bakon/internal/cli"
)

func main() {
	err := cli.Execute()
	if err == nil {
		return
	}
	if errors.Is(err, cli.ErrDiffFound) {
		os.Exit(cli.ExitCode(err))
	}
	// 唯一的错误输出点：cmd 层已静默，这里统一打印并分类退出码。
	// 钩子失败时版本已保存，exit 2 提示调用方处理钩子事务。
	fmt.Fprintln(os.Stderr, "bakon:", err.Error())
	os.Exit(cli.ExitCode(err))
}
