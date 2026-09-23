```markdown
# Bakon — 单机文件版本管理工具 · 架构设计文档

## 0. 定位与哲学

Bakon 是一个单机命令行工具，用于在编辑关键文件时**无感地保留版本历史**，并支持查看变更、导出历史版本、回退到任意版本。

设计原则：

- **做好一件事**：Bakon 只负责"单文件的编辑—存储—回溯"闭环，不越界做同步、加密、审计。
- **稳定的核心，灵活的边缘**：核心数据模型（对象存储 + 路径索引）稳定不变，命令与钩子作为可扩展的边缘。
- **薄壳原则**：CLI 只是核心库的一个消费者，业务逻辑不依赖终端。
- **管道友好**：尊重 stdin/stdout/stderr 分工；输出对管道友好，同时对人类可读。
- **最小惊讶**：默认行为保守、可预测；破坏性行为需要显式意图。

---

## 1. 架构总览

```text
┌──────────────────────────────────────────────────────────────┐
│                    入口层 (Entry)                            │
│  信号注册 → 参数解析 → 配置层叠 → 日志初始化 → 子命令路由    │
└──────────────────────────┬───────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────┐
│                    命令层 (Commands)                         │
│  子命令：edit / log / diff / show / revert / ls / mv /        │
│         prune / hook / help / version                        │
│  每个子命令：验证输入 → 构建请求 → 调用核心 → 格式化输出     │
└──────────────────────────┬───────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────┐
│                    核心层 (Core / Library)                   │
│  store    索引与对象仓库（内容寻址）                          │
│  version  版本序号分配与映射                                  │
│  editor   编辑器会话（锁、hash、spawn、diff 判定）           │
│  hooks    钩子事件系统                                        │
│  retention 版本裁剪                                           │
└──────────────────────────┬───────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────┐
│                 基础设施层 (Infrastructure)                  │
│  fileio (原子写、锁) │ git 后端 │ shell 执行 │ TTY 检测       │
└──────────────────────────────────────────────────────────────┘

横切关注点：错误分类 · 结构化日志 · 配置层叠 · 信号 · 临时文件
```

**核心层不依赖 CLI 概念**，可被其他程序作为库调用；CLI 层只做"参数 → 请求 → 输出"的翻译。

---

## 2. 入口层：从用户意图到内部请求

### 2.1 命令语法

```text
bakon [全局选项] <子命令> [子命令选项] [--] <操作数>
```

层次划分：

- **全局选项**：影响运行环境（`--config`、`--store`、`-v/--verbose`、`--format`、`--no-color`）
- **子命令**：确定操作类型
- **子命令选项**：调整操作行为
- **操作数**：作用对象（通常是文件路径），`--` 后可安全传入以 `-` 开头的路径

### 2.2 子命令列表

**瓷器（porcelain，面向日常使用）：**

| 命令 | 作用 |
|---|---|
| `bakon edit <file>` | 打开编辑器；无改动静默退出；有改动提交为新版本 |
| `bakon log <file>` | 列出该文件的版本历史 |
| `bakon diff <file> [v1] [v2]` | 对比两个版本；缺省对比最近两个 |
| `bakon show <file> <v>` | 输出某版本内容到 stdout |
| `bakon revert <file> <v>` | 恢复到某版本内容，提交为新版本 |
| `bakon ls` | 列出所有被管理文件 |
| `bakon mv <old> <new>` | 更新路径映射，保留历史 |
| `bakon prune [<file>]` | 按最大版本数裁剪历史 |
| `bakon hook <set\|unset\|show> <file> [cmd]` | 管理文件钩子 |
| `bakon help [<topic>]` | 分层帮助 |
| `bakon version` | 版本与能力查询 |

**管道（plumbing，面向脚本与调试）：**

| 命令 | 作用 |
|---|---|
| `bakon path <file>` | 打印内部 ID 与仓库内路径 |
| `bakon verify` | 检查仓库一致性 |
| `bakon dump <file> <v>` | 与 `show` 等价，但输出保证是二进制原样 |

瓷器/管道的划分明确告诉用户：**瓷器命令的输出格式可能随版本演进；管道命令的输出格式在同一主版本内稳定。**

### 2.3 配置层叠

从低到高优先级：

```text
1. 内置默认值
2. 系统配置        /etc/bakon/config.toml
3. 用户配置        $XDG_CONFIG_HOME/bakon/config.toml
4. 项目/仓库配置   仓库内 config.toml（若指定）
5. 环境变量        BAKON_*
6. 命令行参数
```

未设置层不覆盖已设置层。任何配置项都可以在任一层出现，符合"系统管理员定基线、用户定偏好、单次执行临时覆盖"的经典模型。

### 2.4 帮助系统

分四层，逐级展开：

```text
参数错误    → 简短错误 + did-you-mean 建议 + 用法一行
-h          → 用法摘要 + 常用选项
--help      → 完整子命令文档
help <topic>→ 主题文档（version-scheme / hooks / storage / recovery ...）
```

拼写错误使用编辑距离给出建议：

```text
$ bakon revret /etc/hosts
bakon: unknown command 'revret'.
Did you mean 'revert'?
```

---

## 3. 核心层：稳定的数据模型

### 3.1 内容寻址的对象仓库

Bakon 借助 git 实现内容寻址存储；对上层暴露的抽象只有两个概念：**File**（被管理的文件）与 **Version**（该文件的某次版本）。

```text
File
 ├─ path            ：目标文件绝对路径（用户可见的身份）
 ├─ id              ：内部稳定 ID（与 path 解耦，支持 mv）
 ├─ versions[]      ：按时间顺序的 Version 列表
 ├─ max_versions    ：per-file 保留上限（可选）
 └─ hook            ：per-file 变更钩子（可选）

Version
 ├─ ver             ：递增整数序号，首次分配后不回收
 ├─ committed_at    ：提交时间
 ├─ size            ：字节大小
 ├─ backend_ref     ：git commit hash（对上层不可见）
 └─ source          ：来源枚举 { edit, revert }
```

**关键约束**：
- 用户操作只以 `ver` 引用版本，永不暴露 `backend_ref`。
- `ver` 序号是"用户契约"，裁剪不回收序号，保证外部脚本引用长期稳定。
- `path` 与 `id` 解耦，让重命名不断历史。

### 3.2 存储布局

遵循 XDG 基础目录规范：

```text
$XDG_CONFIG_HOME/bakon/
└── config.toml               # 全局配置（用户偏好）

$XDG_DATA_HOME/bakon/
└── repo/                     # 版本仓库（持久数据，需备份）
    ├── .git/
    ├── files/
    │   └── <id>/             # 每个文件一个哈希 ID 目录
    └── index.json            # 路径与 per-file 配置

$XDG_STATE_HOME/bakon/
└── logs/                     # 运行日志（可清理）

$XDG_CACHE_HOME/bakon/        # 保留位（暂无内容）
```

区分数据/状态/缓存的意义：**备份时只需 `$XDG_DATA_HOME/bakon/`**，其余目录随时可重建。

### 3.3 配置格式

选择 TOML：

- 语法简单，最坏编辑条件下（生产环境紧急修复）不容易出错；
- 原生支持注释；
- 层次结构清晰，比 INI 更强，比 YAML 更少陷阱。

```toml
# $XDG_CONFIG_HOME/bakon/config.toml
editor = "vim"                # 默认编辑器；空则依次尝试 $VISUAL、$EDITOR、vi

[retention]
max_versions = 100            # 每个文件保留上限；0 = 不限制

[output]
color = "auto"                # auto | always | never
format = "human"              # human | json | plain（默认 auto 检测 TTY）
```

### 3.4 索引：per-file 配置

`index.json` 存路径映射与文件私有属性：

```json
{
  "/etc/nginx/nginx.conf": {
    "id": "a1b2c3d4",
    "max_versions": 200,
    "hook": "systemctl reload nginx"
  },
  "/etc/hosts": {
    "id": "e5f6a7b8"
  }
}
```

字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `id` | 是 | 内部稳定 ID |
| `max_versions` | 否 | 覆盖全局 `retention.max_versions` |
| `hook` | 否 | 变更钩子命令（see §5） |

**设计契约**：per-file 配置属于"文件的属性"，随文件一同存储在索引中，不放入全局配置。这样即使更换配置文件、跨用户使用同一仓库，钩子和保留策略也不会丢失。

---

## 4. 编辑管道：edit 与 revert 的统一流水线

edit 和 revert 是产生新版本的两条入口，但它们在核心层共享同一条流水线：

```text
                   ┌───────────────┐
edit  <file>  ──▶ │  Prepare      │  校验 · 加锁 · 读取当前 hash
                   └──────┬────────┘
                          │
                   ┌──────▼────────┐
                   │  Mutate       │  edit: 调用编辑器
                   │               │  revert: 从旧版本写回
                   └──────┬────────┘
                          │
                   ┌──────▼────────┐
                   │  Detect       │  比较新旧 hash
                   │               │  未变 → 结束（no-op）
                   └──────┬────────┘
                          │
                   ┌──────▼────────┐
                   │  Commit       │  写入 files/<id>/、分配 ver、
                   │               │  记录到 backend
                   └──────┬────────┘
                          │
                   ┌──────▼────────┐
                   │  Retain       │  超出 max_versions → prune
                   └──────┬────────┘
                          │
                   ┌──────▼────────┐
                   │  Notify       │  触发 hook（若配置）
                   └──────┬────────┘
                          │
                   ┌──────▼────────┐
                   │  Release      │  释放锁、写日志、返回结果
                   └───────────────┘
```

**关键约束：**

- **原地编辑**：编辑器直接在目标文件上工作，Bakon 不通过 cp/mv 覆写源文件，避免破坏 inode、ACL、SELinux 标签等元数据。
- **无变更即无副作用**：未修改的 edit 不产生版本、不触发钩子。
- **锁在钩子执行期间持有**：串行化并发操作，避免钩子交叉。
- **提交与钩子解耦**：钩子失败不回滚版本；版本已提交是既定事实。

---

## 5. 钩子：文件级事件系统

### 5.1 语义

钩子绑定到文件，在**该文件产生新版本时**触发，无论触发源是 `edit` 还是 `revert`。

```text
产生新版本的路径：
  bakon edit   ──▶ (有变更) ──▶ [触发 hook]
  bakon revert ──▶ (提交后) ──▶ [触发 hook]
```

### 5.2 执行约定

- **执行方式**：`sh -c "<hook>"`，继承父进程环境。
- **上下文注入**：通过环境变量传入：

  ```text
  BAKON_FILE       目标文件绝对路径
  BAKON_VERSION    新版本号
  BAKON_SOURCE     "edit" | "revert"
  BAKON_PREV       上一个版本号（首次为空）
  ```

- **失败处理**：钩子返回非零不回滚版本；非零码通过 stderr 上报，Bakon 主命令的最终退出码遵循 §6 规则。
- **不阻塞过久**：钩子应尽量幂等且短时；长时任务建议在钩子中派发到独立进程。
- **绕过机制**：`--no-hook` 全局选项可临时跳过（对应急场景）。

### 5.3 管理命令

```text
bakon hook set   <file> "<cmd>"    # 写入 index.json
bakon hook unset <file>
bakon hook show  <file>
```

---

## 6. 错误处理与退出码

Bakon 的退出码是与外部脚本、CI、自动化的正式契约。

| 退出码 | 名称 | 含义 |
|---|---|---|
| `0` | Success | 命令成功 |
| `1` | Generic | 一般失败（预留） |
| `2` | Usage | 参数错误、未知子命令 |
| `3` | NotFound | 文件不存在、版本不存在 |
| `4` | Conflict | 锁竞争、状态冲突 |
| `5` | HookFailed | 版本已提交，但钩子执行失败 |
| `6` | StoreCorrupt | 仓库损坏或不一致 |
| `70` | Internal | 内部错误 / Bug |

`bakon diff` 的返回值参考 grep 惯例：**发现差异 = 1，无差异 = 0，出错 = 2**。这让脚本可以自然使用：

```bash
if ! bakon diff /etc/hosts; then
    echo "hosts 有历史变更"
fi
```

**stderr / stdout 分工严格执行**：

- `stdout`：数据（`show`/`dump` 的文件内容、`log`/`ls` 的记录）
- `stderr`：诊断信息、进度、警告、错误
- 错误信息永远不污染管道

---

## 7. 输出格式与终端友好

### 7.1 格式协商

```text
--format human   # 默认（TTY 时）：对齐、着色、单位人性化
--format plain   # 默认（非 TTY 时）：无色、稳定分隔符
--format json    # 结构化，字段稳定，供脚本消费
```

`bakon log --format json` 示例：

```json
{"ver":3,"time":"2025-01-15T10:23:41Z","size":1234,"source":"edit"}
{"ver":2,"time":"2025-01-10T09:01:12Z","size":1156,"source":"revert"}
{"ver":1,"time":"2025-01-03T18:47:00Z","size":1024,"source":"edit"}
```

采用 JSON Lines 而非单一 JSON 数组，允许流式消费与 `jq` 逐行处理。

### 7.2 颜色规范

- 默认 `auto`：TTY 时着色，管道时自动关闭。
- 遵循传统语义：红=删除/错误，绿=新增/成功，黄=修改/警告，蓝=信息。
- **颜色只用于强化信息，从不作为信息的唯一载体**。

### 7.3 进度反馈

`prune` 与批量 `edit` 场景下，在 stderr 输出单行滚动进度：

```text
prune: 7/23 files (30.4%) | 42 versions removed | ETA 00:05
```

非 TTY 时自动降级为里程碑式输出（每完成 N 条打印一行）。

---

## 8. 版本裁剪：prune

- **触发时机**：
  - `edit`/`revert` 提交后，若版本数超过 `max_versions`，自动裁剪最旧。
  - `bakon prune [<file>]` 手动触发。
- **策略**：保留最新 N 个版本，删除更旧。
- **约定**：
  - 已分配的 `ver` 序号**永不回收**；裁剪后 `log` 从"最旧保留版本"开始显示。
  - 被裁剪版本不可恢复——这是由 `max_versions` 显式声明的预期行为。
- **实现**：重写 git 历史移除相关对象；操作前打印将要移除的版本清单，需 `--yes` 或交互确认。

---

## 9. 信号处理与故障恢复

### 9.1 信号语义

| 信号 | 行为 |
|---|---|
| `SIGINT` (Ctrl+C) | 若在编辑器交互中，交由编辑器处理；若在提交阶段，等当前提交完成后退出 |
| `SIGTERM` | 同 SIGINT，尝试完成当前原子操作 |
| `SIGPIPE` | 静默退出（下游关闭是合法状态，非错误） |
| `SIGHUP` | 完成当前操作后退出，不做后台化 |

### 9.2 锁与残留清理

- 锁文件 `repo/.lock` 使用 `O_CREAT|O_EXCL` 原子创建，写入持有者 PID。
- 启动时若发现锁存在但 PID 已死，自动清理并继续。
- 中途崩溃留下的部分写入 `files/<id>/*.tmp`，`bakon verify` 可检测并清理。

### 9.3 原子写

对目标文件与索引 `index.json` 的更新一律采用"写临时 → fsync → rename"三段式，保证任何时刻的读者看到一致状态。

---

## 10. 可测试性

Bakon 的分层设计天然支持测试金字塔：

```text
     端到端测试   通过 shell 脚本调用 bakon 二进制，断言 stdout/stderr/退出码
        ▲
     集成测试     核心层 + 基础设施层，使用临时目录仓库
        ▲
     单元测试     核心层的纯函数（版本序号分配、diff 判定、路径映射）
```

**确定性注入点**（供测试与调试）：

- `BAKON_NOW`：覆盖当前时间戳
- `BAKON_HOME`：覆盖 XDG 目录根
- `BAKON_EDITOR`：测试用假编辑器（读入固定内容后退出）

这些注入点让端到端测试完全可重现。

---

## 11. 演进策略

### 11.1 接口稳定性承诺

| 层级 | 承诺 |
|---|---|
| 瓷器命令语法 | 主版本内保持兼容；变更需两个次版本的废弃期 |
| 管道命令输出 | 主版本内保持字节级稳定 |
| `--format json` 字段 | 只增不删；字段语义不变 |
| `ver` 序号语义 | 永久稳定，不回收 |
| 退出码 | 永久稳定 |
| 存储布局 | 主版本内前向兼容；跨主版本提供 `bakon migrate` |

以上均不做，这个小工具似乎只需要有限的几个版本就够用了。

### 11.2 能力查询

```bash
bakon version            # 版本号 + 编译特性
bakon version --json     # 结构化输出，供脚本判断能力
```

---

## 12. 实现建议

### 12.1 语言与依赖

- **语言**：Go。单二进制、跨平台、启动时间友好，符合 CLI 的启动性能要求。
- **允许使用现成库**，减少自写代码量：

| 用途 | 建议库 |
|---|---|
| 子命令框架 | `spf13/cobra` |
| 配置层叠 | `spf13/viper` 或自研（配合 `BurntSushi/toml`） |
| TOML 解析 | `BurntSushi/toml` 或 `pelletier/go-toml/v2` |
| JSON | 标准库 `encoding/json` |
| Git 后端 | `go-git/go-git`（纯 Go，无外部 `git` 依赖）或 `os/exec` 调用系统 `git` |
| 文件锁 | `gofrs/flock` |
| 终端检测与着色 | `mattn/go-isatty` + `fatih/color` |

### 12.2 Git 后端二选一

- **`go-git` 纯 Go**：真正单二进制、开箱即用；`prune` 的历史重写需自行实现对象清理。
- **系统 `git`**：与手工 git 行为完全一致，`git filter-repo` 等成熟工具可用；要求运行环境有 `git`。

建议直接使用系统 Git，所以需要一个检测是否存在的环节。

### 12.3 项目结构

```text
bakon/
├── cmd/bakon/                # 入口层
│   └── main.go               # 信号 → 参数 → 配置 → 路由
├── commands/                 # 命令层（薄壳）
│   ├── edit.go
│   ├── log.go
│   ├── diff.go
│   ├── show.go
│   ├── revert.go
│   ├── ls.go
│   ├── mv.go
│   ├── prune.go
│   └── hook.go
├── core/                     # 核心层（可作为库）
│   ├── store/                # 索引 + 对象仓库
│   ├── version/              # 序号分配与映射
│   ├── editor/               # 编辑会话
│   ├── hooks/                # 钩子事件
│   └── retention/            # 裁剪策略
├── output/                   # 格式化
│   ├── human.go
│   ├── plain.go
│   └── json.go
├── infra/                    # 基础设施
│   ├── fileio/               # 原子写、锁
│   ├── gitbackend/           # git 抽象
│   ├── shellexec/            # 钩子执行
│   └── tty/                  # TTY 检测
└── config/                   # 配置层叠
    └── config.go
```

核心层不 import `commands/` 或 `output/`，可独立被其他 Go 程序引用。

---

## 13. 边界

**做**：

- 全部子命令（瓷器 + 管道）
- per-file 的 `max_versions` 与 `hook`（edit / revert 均触发）
- 配置层叠、XDG 目录、结构化输出、TTY 感知
- 信号处理、原子写、锁与恢复
- `bakon verify`、`bakon migrate`

**不做**：

- 加密、签名、防篡改审计
- 跨机同步、协作、分布式
- GUI
- 钩子失败重试与事务回滚
- 目录级管理（Bakon 只管理单文件）
```