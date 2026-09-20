# Bakon

> 编辑关键文件时，无感地保留版本历史。

[![CI](https://github.com/RobiNexy/Bakon/actions/workflows/ci.yml/badge.svg)](https://github.com/RobiNexy/Bakon/actions/workflows/ci.yml)

Bakon 是一个单机命令行工具：用你熟悉的编辑器修改 `nginx.conf`、`hosts`、crontab 这类关键文件时，Bakon 在背后把每次变更存入一个 git 仓库，支持查看差异、导出历史版本、一键回退，并可在文件变更后自动执行钩子（如 `systemctl reload nginx`）。

- **单机、单人、无协作**——没有远端、没有分支、没有冲突
- **底层只依赖 `git`** 作为版本存储引擎
- **不动你的工作流**——编辑器原地编辑目标文件，Bakon 只在仓库里留副本

## 特性

- `edit`：前台打开编辑器；内容没变则静默退出，变了就自动提交为新版本
- 每个文件独立的**整数版本号**（ver），append-only，回退不覆盖历史
- per-file 的**保留上限**（超出自动裁剪最旧版本）与**变更钩子**
- 并发安全：文件锁串行化所有写操作
- 产物为静态二进制，覆盖 Linux / macOS / Windows / Android Termux

## 安装

**方式一：下载预编译二进制**（推荐）

从 [Releases](../../releases) 直接下载对应平台的二进制，重命名为 `bakon` 放入 `PATH`。所有产物可在 `checksums.txt` 中核对 sha256。

| 产物 | 平台 |
|---|---|
| `bakon_<ver>_linux-amd64` | Linux x86-64 |
| `bakon_<ver>_linux-arm64` | Linux arm64 / **Android Termux (aarch64)** |
| `bakon_<ver>_linux-armv7` | Linux armv7 / Android Termux (32 位) |
| `bakon_<ver>_darwin-amd64` | macOS (Intel) |
| `bakon_<ver>_darwin-arm64` | macOS (Apple Silicon) |
| `bakon_<ver>_windows-amd64.exe` | Windows x86-64 |
| `bakon_<ver>_windows-arm64.exe` | Windows arm64 |

```sh
# Linux / macOS（BAKON_VER 换成最新 tag）：
BAKON_VER=v0.1.0
curl -fLo /usr/local/bin/bakon \
  "https://github.com/RobiNexy/Bakon/releases/download/${BAKON_VER}/bakon_${BAKON_VER}_linux-amd64"
chmod +x /usr/local/bin/bakon

# Windows (PowerShell)：
Invoke-WebRequest `
  "https://github.com/RobiNexy/Bakon/releases/download/v0.1.0/bakon_v0.1.0_windows-amd64.exe" `
  -OutFile bakon.exe
```

**方式二：go install**

```sh
go install github.com/RobiNexy/Bakon@latest
```

**方式三：源码构建**

```sh
git clone https://github.com/RobiNexy/Bakon && cd bakon
scripts/build.sh          # 全平台二进制在 dist/
go build .                # 或仅构建本机
```

**Android Termux**（需运行时安装 git）：

```sh
pkg install git
BAKON_VER=v0.1.0   # 换成最新 tag
curl -fLo $PREFIX/bin/bakon \
  "https://github.com/RobiNexy/Bakon/releases/download/${BAKON_VER}/bakon_${BAKON_VER}_linux-arm64"
chmod +x $PREFIX/bin/bakon
```

> 运行依赖：`git`（Bakon 调用系统 git 作为存储引擎）与 shell（编辑器与钩子经 shell 启动：unix 用 `sh`，Windows 用 `cmd /C`——钩子脚本需自行保证目标平台可执行）。

## 快速开始

```sh
# 1. 编辑任意文件，Bakon 自动接管版本历史
bakon edit /etc/nginx/nginx.conf

# 2. 查看历史
bakon log /etc/nginx/nginx.conf
#   #  ver   time                  size
#     3     2025-01-15 10:23:41   1.2K
#     2     2025-01-10 09:01:12   1.1K
#     1     2025-01-03 18:47:00   1.0K

# 3. 看差异（缺省比较最近两版）
bakon diff /etc/nginx/nginx.conf

# 4. 导出某版本内容
bakon show /etc/nginx/nginx.conf 2

# 5. 回退到版本 2（历史不动，生成新版本）
bakon revert /etc/nginx/nginx.conf 2

# 6. 文件改动后自动 reload
bakon hook set /etc/nginx/nginx.conf "systemctl reload nginx"
```

## 命令一览

| 命令 | 作用 |
|---|---|
| `bakon edit <file>` | 打开编辑器；无改动静默退出，有改动提交新版本 |
| `bakon log <file>` | 列出历史版本（序号、时间、大小） |
| `bakon diff <file> [v1] [v2]` | 比较版本差异；缺省最近两版；单参数比较前一版 |
| `bakon show <file> <v>` | 输出某版本完整内容 |
| `bakon revert <file> <v>` | 恢复到某版本，提交为新版本 |
| `bakon ls` | 列出所有被管理的文件 |
| `bakon mv <old> <new>` | 更新路径映射，保留历史 |
| `bakon prune [<file>]` | 按上限裁剪历史；缺省所有文件 |
| `bakon hook set/unset/show` | 管理 per-file 变更钩子 |
| `bakon version` | 打印版本信息 |

## 配置

全局配置 `~/.bakon/config.toml`（不存在时使用默认值）：

```toml
editor = "vim"              # 编辑器，优先级：配置 > $EDITOR > vi
store  = "~/.bakon/repo"    # 版本仓库位置

[retention]
max_versions = 100          # 每文件保留上限；0 = 不限制
```

per-file 配置（`max_versions`、`hook`）存放在仓库的 `index.json` 中，通过 `bakon hook` 命令管理，不进全局配置。

## 钩子与权限（sudo）

钩子是**任意 shell 命令**：写 `sudo systemctl reload nginx` 也会原样执行，Bakon 不拦截、不解释、不代为提权——需要权衡的是运行身份：

| 运行身份 | 建议 |
|---|---|
| root（编辑 `/etc` 文件的典型方式） | 钩子**不要写 sudo**，直接 `systemctl reload nginx`；Bakon 已是 root，套 sudo 在部分 sudoers 配置（如 `requiretty`）下反而失败 |
| 普通用户，钩子需 root | `sudo` 读不到密码时（无 TTY，且钩子的 stdin 接 `/dev/null`）会**直接失败而非挂起**，错误输出可见，Bakon 以退出码 2 提示，版本已保存。正确做法：sudoers 精确白名单 |

sudoers 精确白名单示例（最小权限，仅放行一条命令）：

```text
samphi ALL=(root) NOPASSWD: /usr/bin/systemctl reload nginx
```

替代方案：polkit（`pkexec`）、或用 systemd path unit 监听文件变化替代钩子。Bakon 不缓存凭证、不做提权——这是"不做权限加固"边界的一部分。

> sudo 在无 TTY 环境的确切行为随版本与 sudoers 配置而异 [基于模式推断]，以 `sudo -l -n` 在目标环境实测为准。

## 语义要点

- **ver 序号不回收**：裁剪后 `log` 从最旧保留版本开始显示，已分配序号不复用。
- **revert 产生新版本**：回退是追加一次提交，历史永不改写；若目标内容与当前版本相同则不产生新版本。
- **被裁剪的版本不可恢复**：`show`/`revert` 一个已裁剪的序号会明确报 `has been pruned`（区别于从未存在的 `no version N`）。
- **崩溃无数据丢失**：编辑器退出后、提交完成前进程中断时，目标文件已改而仓库无该版本——下一次 `bakon edit` 会把当前内容补提交为新版本，无需恢复机制。
- **并发模型**：写操作（edit/revert/prune/mv/hook set）由仓库文件锁串行化；只读命令（ls/log/diff/show）**不取锁**——index 原子写入 + git 对象不可变保证无半截状态，代价是读到的快照可能落后一次提交，不会被挂起的编辑会话阻塞。
- **钩子在产生新版本时触发**（edit 或 revert 提交后），在文件锁内执行，失败不影响已完成的版本提交，进程以退出码 2 提示。
- **退出码**：`0` 成功；`1` 操作失败；`2` 版本已保存但钩子失败。

## 开发

```sh
go test -race ./...        # 测试（含真实 git 仓库的集成测试）
go vet ./...
scripts/build.sh           # 全平台交叉编译
```

发布流程：推送 tag 即由 GitHub Actions 构建并发布：

```sh
git tag v0.1.0 && git push origin v0.1.0
```

设计文档见 [docs/design.md](docs/design.md)。

## License

[MIT](LICENSE)
