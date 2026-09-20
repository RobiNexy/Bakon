// Package bakon 编排 bakon 的核心流程：edit / revert / prune / hook / 版本解析。
// 并发模型：所有写操作（edit/revert/prune/mv/hook set）在 repo/.lock 的
// flock 下串行执行；读操作依赖 git 对象模型的不可变性，不取锁。
package bakon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/RobiNexy/Bakon/internal/config"
	"github.com/RobiNexy/Bakon/internal/gitx"
	"github.com/RobiNexy/Bakon/internal/index"

	"github.com/gofrs/flock"
)

// ErrHookFailed 标记"版本已提交但钩子执行失败"。
// 版本操作不受影响；进程以非零码退出以提醒用户处理钩子事务。
var ErrHookFailed = errors.New("version saved, but hook failed")

type App struct {
	ConfigPath string
	Repo       *gitx.Repo
	IndexPath  string
	Editor     string
	GlobalMax  int
}

func NewApp(cfgPath string) (*App, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	store := config.ExpandHome(cfg.Store)
	if store == "" {
		store = config.ExpandHome("~/.bakon/repo")
	}
	store = filepath.Clean(store)
	return &App{
		ConfigPath: cfgPath,
		Repo:       gitx.New(store),
		IndexPath:  filepath.Join(store, "index.json"),
		Editor:     cfg.EffectiveEditor(),
		GlobalMax:  cfg.Retention.MaxVersions,
	}, nil
}

// isManaged 仅用于错误分流：判断路径是否已在 index 中。
func (a *App) isManaged(abs string) bool {
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return false
	}
	return idx.Get(abs) != nil
}

// Version 是单个历史版本的解析结果。
type Version struct {
	Ver  int
	Hash string
	Time time.Time
}

// VersionInfo 是 log 输出所需的展示数据。
type VersionInfo struct {
	Ver  int
	Time time.Time
	Size int64
}

// ---- 身份与路径 ----

// IDFor 由目标文件绝对路径派生稳定的内部 ID。
// 取 sha256 前 6 字节（12 hex 字符）：单机规模下碰撞可忽略，
// 比文档示例的 8 字符多出碰撞余量。
func IDFor(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:6])
}

func (a *App) relPath(id string) string {
	// git 路径恒用正斜杠，不做 filepath.Join，保证跨平台 git 参数正确。
	return "files/" + id
}

func cleanAbs(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// 不做 symlink 解析：目标文件可能是 /etc/hosts 这类符号链接，
	// realpath 会让键随链接目标漂移。
	return filepath.Clean(abs), nil
}

func notManaged(abs string) error {
	return fmt.Errorf("not managed: %s (run bakon edit %s once to start tracking)", abs, abs)
}

// ---- 锁 ----

// lock 阻塞获取仓库文件锁，返回释放函数。
// 锁覆盖整个编辑器会话，串行化并发编辑与钩子执行。
func (a *App) lock() (func(), error) {
	if err := os.MkdirAll(a.Repo.Dir, 0o755); err != nil {
		return nil, err
	}
	fl := flock.New(filepath.Join(a.Repo.Dir, ".lock"))
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// ---- 版本解析 ----

// versions 返回 rel 的 bakon 版本列表，旧→新。
// 主题不符合 "bakon ver N" 的提交视为外部提交，不参与版本编号。
func (a *App) versions(rel string) ([]Version, error) {
	cs, err := a.Repo.FileCommits(rel)
	if err != nil {
		return nil, err
	}
	var vs []Version
	for _, c := range cs {
		n, ok := ParseVer(c.Subject)
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339, c.Time)
		if err != nil {
			return nil, fmt.Errorf("parse commit time of %s: %w", c.Hash, err)
		}
		vs = append(vs, Version{Ver: n, Hash: c.Hash, Time: t})
	}
	return vs, nil
}

// ParseVer 解析提交主题 "bakon ver <N>"。
func ParseVer(subject string) (int, bool) {
	const prefix = "bakon ver "
	if !strings.HasPrefix(subject, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(subject[len(prefix):]))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// resolveVer 定位版本号，错误信息区分三种情形：
//   - ver 早于最旧保留版本 → 已被 prune 裁剪（不可恢复，max_versions 的预期行为）
//   - ver 大于最新版本 → 从未存在
//   - 位于两者之间却无匹配 → 序号空洞（仅历史被外部工具改写时可能出现）
func resolveVer(vs []Version, ver int) (Version, error) {
	if len(vs) == 0 {
		return Version{}, fmt.Errorf("file has no versions yet")
	}
	if ver < vs[0].Ver {
		return Version{}, fmt.Errorf("version %d has been pruned (oldest retained version is %d)",
			ver, vs[0].Ver)
	}
	if ver > vs[len(vs)-1].Ver {
		return Version{}, fmt.Errorf("no version %d (latest is %d)", ver, vs[len(vs)-1].Ver)
	}
	for _, v := range vs {
		if v.Ver == ver {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("no version %d", ver)
}

// ---- 提交 ----

func (a *App) commitVersion(abs string, entry *index.Entry, content []byte) (int, error) {
	rel := a.relPath(entry.ID)
	vs, err := a.versions(rel)
	if err != nil {
		return 0, err
	}
	// 取最大值+1 而非计数：prune 不回收序号，且能容忍历史中混入外部提交。
	next := 1
	for _, v := range vs {
		if v.Ver >= next {
			next = v.Ver + 1
		}
	}
	msg := fmt.Sprintf("bakon ver %d\n\npath: %s\n", next, abs)
	if err := a.Repo.CommitVersion(rel, content, msg); err != nil {
		return 0, err
	}
	return next, nil
}

// ---- edit ----

// Edit 打开编辑器；内容有变化则提交新版本并按需裁剪、执行钩子。
// 返回新版本号；0 表示无改动（静默退出）。
// 与设计文档的步骤差异：旧内容 hash 在取锁之后计算，
// 消除"等锁期间文件被上一操作改变 → 幽灵提交"的竞态。
func (a *App) Edit(path string) (int, error) {
	if err := a.Repo.Init(); err != nil {
		return 0, err
	}
	release, err := a.lock()
	if err != nil {
		return 0, err
	}
	defer release()

	abs, err := cleanAbs(path)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			if a.isManaged(abs) {
				// 已跟踪文件被删：指向 revert 恢复路径（revert 不要求文件存在）。
				return 0, fmt.Errorf("tracked file was deleted: %s (restore with: bakon revert %s <ver>)", abs, abs)
			}
			// 不存在的未跟踪文件：报错而非创建——自动创建会让
			// 路径打错字时凭空纳管一个空文件。
			return 0, fmt.Errorf("no such file: %s (bakon edit only tracks existing files; create it first)", abs)
		}
		return 0, fmt.Errorf("cannot edit %s: %w", abs, err)
	}
	if fi.IsDir() {
		return 0, fmt.Errorf("%s is a directory", abs)
	}

	old, err := os.ReadFile(abs)
	if err != nil {
		return 0, err
	}

	if err := a.runEditor(abs); err != nil {
		return 0, fmt.Errorf("editor: %w", err)
	}

	cur, err := os.ReadFile(abs)
	if err != nil {
		return 0, err
	}
	if bytes.Equal(old, cur) {
		return 0, nil
	}

	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return 0, err
	}
	entry := idx.Get(abs)
	if entry == nil {
		entry = &index.Entry{ID: IDFor(abs)}
		idx.Set(abs, entry)
		if err := idx.Save(a.IndexPath); err != nil {
			return 0, err
		}
	}

	ver, err := a.commitVersion(abs, entry, cur)
	if err != nil {
		return 0, err
	}
	if _, err := a.prunePaths(idx, []string{abs}); err != nil {
		return ver, fmt.Errorf("version %d saved, but prune failed: %w", ver, err)
	}
	if entry.Hook != "" {
		if err := a.runHook(entry.Hook); err != nil {
			return ver, fmt.Errorf("%w: %v", ErrHookFailed, err)
		}
	}
	return ver, nil
}

func (a *App) runEditor(abs string) error {
	if strings.TrimSpace(a.Editor) == "" {
		return fmt.Errorf("no editor configured (set editor in %s)", a.ConfigPath)
	}
	// 编辑器命令可能自带参数（如 "code -w"），经 shell 拼接目标文件。
	cmd := shellCmd(a.Editor + " " + quoteArg(abs))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// shellCmd 按平台选择 shell：unix 用 sh，Windows 用 cmd /C。
// 编辑器与钩子命令的作者需自行保证其在目标平台可执行；
// bakon 不做跨平台脚本翻译——这是刻意不覆盖的能力边界。
func shellCmd(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-c", command)
}

// quoteArg 拼接 shell 参数：unix 单引号转义，Windows 双引号包裹。
// cmd 对路径中 %、^、& 等元字符的转义不完整，含此类字符的路径
// 在 Windows 上可能出错——单机场景可接受，已文档化。
func quoteArg(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---- revert ----

// Revert 将目标文件恢复为 ver 版本内容，并把恢复动作本身提交为新版本。
// revert 不要求目标文件当前存在（可恢复被删除的文件）。
// 返回新版本号；0 表示目标内容与最新版本相同，未产生新版本。
func (a *App) Revert(path string, ver int) (int, error) {
	release, err := a.lock()
	if err != nil {
		return 0, err
	}
	defer release()

	abs, err := cleanAbs(path)
	if err != nil {
		return 0, err
	}
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return 0, err
	}
	entry := idx.Get(abs)
	if entry == nil {
		return 0, notManaged(abs)
	}
	rel := a.relPath(entry.ID)
	vs, err := a.versions(rel)
	if err != nil {
		return 0, err
	}
	v, err := resolveVer(vs, ver)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", abs, err)
	}
	content, err := a.Repo.BlobAt(v.Hash, rel)
	if err != nil {
		return 0, err
	}
	// 与设计文档的偏离：目标内容与最新版本相同（如 revert 到当前版）时
	// 不产生新提交。git 拒绝空提交，而"每个提交恰好触碰一个路径"是
	// prune 历史重放的关键不变量，不值得为其引入空提交特例。
	// 此时仍写回目标文件（目标可能已偏离仓库内容），但不触发钩子。
	if len(vs) > 0 {
		head, err := a.Repo.BlobAt(vs[len(vs)-1].Hash, rel)
		if err != nil {
			return 0, err
		}
		if bytes.Equal(content, head) {
			if err := writeBack(abs, content); err != nil {
				return 0, err
			}
			return 0, nil
		}
	}
	if err := writeBack(abs, content); err != nil {
		return 0, err
	}
	newVer, err := a.commitVersion(abs, entry, content)
	if err != nil {
		return 0, err
	}
	if _, err := a.prunePaths(idx, []string{abs}); err != nil {
		return newVer, fmt.Errorf("version %d saved, but prune failed: %w", newVer, err)
	}
	if entry.Hook != "" {
		if err := a.runHook(entry.Hook); err != nil {
			return newVer, fmt.Errorf("%w: %v", ErrHookFailed, err)
		}
	}
	return newVer, nil
}

// writeBack 原子写回目标文件，保留原权限位。
func writeBack(abs string, content []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(abs); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".bakon-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		return err
	}
	tmpPath = ""
	return nil
}

// ---- log / show / diff ----

// Log 返回版本列表（最新在前）。不取锁：git 读操作自洽，
// 且不应被他人挂起的编辑会话阻塞。
func (a *App) Log(path string) ([]VersionInfo, error) {
	rel, err := a.relFor(path)
	if err != nil {
		return nil, err
	}
	vs, err := a.versions(rel)
	if err != nil {
		return nil, err
	}
	out := make([]VersionInfo, 0, len(vs))
	for i := len(vs) - 1; i >= 0; i-- {
		size, err := a.Repo.BlobSize(vs[i].Hash, rel)
		if err != nil {
			return nil, err
		}
		out = append(out, VersionInfo{Ver: vs[i].Ver, Time: vs[i].Time, Size: size})
	}
	return out, nil
}

// relFor 解析路径并返回该文件在仓库内的相对路径；未纳管则报错。
func (a *App) relFor(path string) (string, error) {
	abs, err := cleanAbs(path)
	if err != nil {
		return "", err
	}
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return "", err
	}
	entry := idx.Get(abs)
	if entry == nil {
		return "", notManaged(abs)
	}
	return a.relPath(entry.ID), nil
}

// Show 输出 ver 版本的完整内容。
func (a *App) Show(path string, ver int) ([]byte, error) {
	rel, err := a.relFor(path)
	if err != nil {
		return nil, err
	}
	vs, err := a.versions(rel)
	if err != nil {
		return nil, err
	}
	v, err := resolveVer(vs, ver)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a.Repo.BlobAt(v.Hash, rel)
}

// Diff 比较版本差异，返回 git diff 原始输出。
// 参数语义：0 个 → 最近两版；1 个 v → v 的前一版 vs v；2 个 → v1 vs v2。
// "1 个参数 = 与前一版比较"是设计文档未定义行为的暂定约定。
func (a *App) Diff(path string, verArgs []string) ([]byte, error) {
	rel, err := a.relFor(path)
	if err != nil {
		return nil, err
	}
	vs, err := a.versions(rel)
	if err != nil {
		return nil, err
	}
	pick := func(s string) (Version, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return Version{}, fmt.Errorf("invalid version number %q", s)
		}
		v, err := resolveVer(vs, n)
		if err != nil {
			return Version{}, fmt.Errorf("%s: %w", path, err)
		}
		return v, nil
	}
	var c1, c2 Version
	switch len(verArgs) {
	case 0:
		if len(vs) < 2 {
			return nil, fmt.Errorf("%s: need at least 2 versions to diff", path)
		}
		c1, c2 = vs[len(vs)-2], vs[len(vs)-1]
	case 1:
		v, err := pick(verArgs[0])
		if err != nil {
			return nil, err
		}
		i := -1
		for j := range vs {
			if vs[j].Ver == v.Ver {
				i = j
			}
		}
		if i == 0 {
			return nil, fmt.Errorf("%s: no version before %d", path, v.Ver)
		}
		c1, c2 = vs[i-1], vs[i]
	case 2:
		var err error
		if c1, err = pick(verArgs[0]); err != nil {
			return nil, err
		}
		if c2, err = pick(verArgs[1]); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("diff takes at most 2 version arguments")
	}
	return a.Repo.Diff(c1.Hash, c2.Hash, rel)
}

// ---- ls / mv / hook ----

// Ls 返回全部被管理路径（已排序）。只读，不取锁。
func (a *App) Ls() ([]string, error) {
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return nil, err
	}
	return idx.Paths(), nil
}

// Mv 更新路径映射，内部 ID 与 git 历史不变。
func (a *App) Mv(oldPath, newPath string) error {
	release, err := a.lock()
	if err != nil {
		return err
	}
	defer release()

	absOld, err := cleanAbs(oldPath)
	if err != nil {
		return err
	}
	absNew, err := cleanAbs(newPath)
	if err != nil {
		return err
	}
	if absOld == absNew {
		return fmt.Errorf("source and target are the same: %s", absOld)
	}
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return err
	}
	entry := idx.Get(absOld)
	if entry == nil {
		return notManaged(absOld)
	}
	if idx.Get(absNew) != nil {
		return fmt.Errorf("already managed: %s", absNew)
	}
	idx.Set(absNew, entry)
	idx.Delete(absOld)
	return idx.Save(a.IndexPath)
}

func (a *App) HookSet(path, hook string) error {
	if strings.TrimSpace(hook) == "" {
		return fmt.Errorf("hook command is empty")
	}
	release, err := a.lock()
	if err != nil {
		return err
	}
	defer release()
	return a.mutateHook(path, func(e *index.Entry) { e.Hook = hook })
}

func (a *App) HookUnset(path string) error {
	release, err := a.lock()
	if err != nil {
		return err
	}
	defer release()
	return a.mutateHook(path, func(e *index.Entry) { e.Hook = "" })
}

// HookShow 返回钩子命令；未设置时为空串。
func (a *App) HookShow(path string) (string, error) {
	abs, err := cleanAbs(path)
	if err != nil {
		return "", err
	}
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return "", err
	}
	entry := idx.Get(abs)
	if entry == nil {
		return "", notManaged(abs)
	}
	return entry.Hook, nil
}

func (a *App) mutateHook(path string, fn func(*index.Entry)) error {
	abs, err := cleanAbs(path)
	if err != nil {
		return err
	}
	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return err
	}
	entry := idx.Get(abs)
	if entry == nil {
		return notManaged(abs)
	}
	fn(entry)
	return idx.Save(a.IndexPath)
}

// ---- prune ----

// effectiveMax 生效的最大版本数：文件级覆盖全局；0 表示不限制。
func (a *App) effectiveMax(e *index.Entry) int {
	if e.MaxVersions > 0 {
		return e.MaxVersions
	}
	return a.GlobalMax
}

// Prune 裁剪指定文件（或全部文件）超出保留额度的最旧版本。
// 返回被裁剪的版本数。
func (a *App) Prune(path string) (int, error) {
	release, err := a.lock()
	if err != nil {
		return 0, err
	}
	defer release()

	idx, err := index.Load(a.IndexPath)
	if err != nil {
		return 0, err
	}
	var paths []string
	if path != "" {
		abs, err := cleanAbs(path)
		if err != nil {
			return 0, err
		}
		if idx.Get(abs) == nil {
			return 0, notManaged(abs)
		}
		paths = []string{abs}
	} else {
		paths = idx.Paths()
	}
	return a.prunePaths(idx, paths)
}

// prunePaths 收集待裁剪版本并一次重建历史。要求调用方已持有仓库锁。
func (a *App) prunePaths(idx *index.Index, paths []string) (int, error) {
	dropped := map[string]bool{}
	total := 0
	for _, p := range paths {
		entry := idx.Get(p)
		if entry == nil {
			continue
		}
		rel := a.relPath(entry.ID)
		vs, err := a.versions(rel)
		if err != nil {
			return 0, err
		}
		max := a.effectiveMax(entry)
		if max <= 0 || len(vs) <= max {
			continue
		}
		// vs 旧→新；裁掉最旧的 len(vs)-max 个。
		for _, v := range vs[:len(vs)-max] {
			dropped[v.Hash] = true
			total++
		}
	}
	if total == 0 {
		return 0, nil
	}
	if _, err := a.Repo.Rebuild(func(c gitx.CommitInfo, p string) (bool, error) {
		return !dropped[c.Hash], nil
	}); err != nil {
		return 0, err
	}
	if err := a.Repo.GC(); err != nil {
		return 0, err
	}
	return total, nil
}

// ---- 钩子执行 ----

// runHook 经系统 shell 执行钩子，输出直通终端。
// 失败不回滚版本——文件已正确保存/恢复，这是设计文档的显式取舍。
func (a *App) runHook(hook string) error {
	cmd := shellCmd(hook)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook exited with error: %w", err)
	}
	return nil
}

// ---- 展示辅助 ----

func HumanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
	}
}
