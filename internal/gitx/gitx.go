// Package gitx 封装 bakon 依赖的 git 子命令。
// 选择调用系统 git 而非 go-git：prune 需要重写历史并清理对象，
// 官方 git 的 commit-tree/gc 链路是现成的；go-git 无 gc、无法删除对象。
// 前提：运行环境已安装 git。
package gitx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Repo struct {
	// Dir 是 git 仓库根目录（~/.bakon/repo）。
	Dir string
}

func New(dir string) *Repo { return &Repo{Dir: dir} }

func (r *Repo) git(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	return cmd
}

func (r *Repo) run(args ...string) ([]byte, error) {
	return r.runEnv(nil, args...)
}

func (r *Repo) runEnv(env []string, args ...string) ([]byte, error) {
	cmd := r.git(args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out.Bytes(), nil
}

// Init 幂等初始化仓库。repo-local 身份配置避免依赖用户全局 git 配置，
// 否则无全局 user.name 的机器上 commit 会直接失败。
func (r *Repo) Init() error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	if _, err := r.Run("rev-parse", "--is-inside-work-tree"); err == nil {
		return nil
	}
	if _, err := r.Run("init", "-q"); err != nil {
		return err
	}
	if _, err := r.Run("config", "user.name", "bakon"); err != nil {
		return err
	}
	_, err := r.Run("config", "user.email", "bakon@localhost")
	return err
}

func (r *Repo) Run(args ...string) ([]byte, error) {
	return r.run(args...)
}

func (r *Repo) HeadExists() bool {
	_, err := r.Run("rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// CommitInfo 是一条提交的可观察信息。Time 为 git 的 ISO 8601 输出，
// 重建历史时原样回填 GIT_AUTHOR_DATE / GIT_COMMITTER_DATE。
type CommitInfo struct {
	Hash    string
	Time    string
	Subject string
}

// AllCommits 返回全仓库提交，旧→新。
func (r *Repo) AllCommits() ([]CommitInfo, error) {
	return r.logCommits(nil)
}

// FileCommits 返回触碰过 rel 的提交，旧→新。
// 空仓库返回 nil 而非错误，调用方以此区分"首版"与异常。
func (r *Repo) FileCommits(rel string) ([]CommitInfo, error) {
	return r.logCommits([]string{"--", rel})
}

func (r *Repo) logCommits(pathspec []string) ([]CommitInfo, error) {
	if !r.HeadExists() {
		return nil, nil
	}
	args := []string{"log", "--reverse", "--format=%H%x1f%cI%x1f%s"}
	args = append(args, pathspec...)
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	var cs []CommitInfo
	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected git log line: %q", line)
		}
		cs = append(cs, CommitInfo{Hash: parts[0], Time: parts[1], Subject: parts[2]})
	}
	return cs, nil
}

// ChangedBlob 返回提交中发生变更的路径及其新 blob sha。
// bakon 的每次提交恰好触碰一个路径（锁串行化保证），因此取第一个差异行即可。
// --root 使首个提交也能输出差异。
func (r *Repo) ChangedBlob(commit string) (path, blob string, err error) {
	out, err := r.run("diff-tree", "--root", "--no-commit-id", "-r", commit)
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" || strings.HasPrefix(line, "0{0,40}") {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(line[:tab])
		if len(meta) < 4 {
			continue
		}
		return line[tab+1:], meta[3], nil
	}
	return "", "", fmt.Errorf("commit %s has no file change", commit)
}

// BlobAt 读取 rel 在 commit 时的完整内容（二进制安全）。
func (r *Repo) BlobAt(commit, rel string) ([]byte, error) {
	return r.run("cat-file", "blob", commit+":"+rel)
}

// BlobSize 返回 rel 在 commit 时的字节数。
func (r *Repo) BlobSize(commit, rel string) (int64, error) {
	out, err := r.run("cat-file", "-s", commit+":"+rel)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// Diff 输出两个提交中 rel 内容的差异，原样透传 git diff 的输出。
func (r *Repo) Diff(c1, c2, rel string) ([]byte, error) {
	return r.run("diff", c1+":"+rel, c2+":"+rel)
}

// CommitVersion 将内容写入 files/<id> 并提交。
// --only 限定只提交该路径，防御仓库被外部操作污染时夹带其他变更。
func (r *Repo) CommitVersion(rel string, content []byte, msg string) error {
	dst := r.Dir + "/" + rel
	if err := os.MkdirAll(parentDir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		return err
	}
	if _, err := r.run("add", "--", rel); err != nil {
		return err
	}
	_, err := r.run("commit", "-m", msg, "--only", "--", rel)
	return err
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// Rebuild 重写历史：按时间正序重放 decide 允许的提交，
// 每个保留提交沿用原 message 与作者/提交时间。
// 前提：历史为线性链且每个提交只触碰一个路径（bakon 的不变量）。
// 返回新 HEAD；由调用方决定是否 gc。
func (r *Repo) Rebuild(decide func(c CommitInfo, path string) (bool, error)) (string, error) {
	commits, err := r.AllCommits()
	if err != nil || len(commits) == 0 {
		return "", err
	}

	type step struct {
		info CommitInfo
		path string
		blob string
	}
	steps := make([]step, 0, len(commits))
	for _, c := range commits {
		path, blob, err := r.ChangedBlob(c.Hash)
		if err != nil {
			return "", err
		}
		steps = append(steps, step{info: c, path: path, blob: blob})
	}

	// 独立临时索引，不碰仓库现有 index 文件。
	tmpIndex := r.Dir + "/.git/bakon-rebuild-index"
	defer os.Remove(tmpIndex)

	var head string
	for _, s := range steps {
		keep, err := decide(s.info, s.path)
		if err != nil {
			return "", err
		}
		if !keep {
			continue
		}
		env := []string{"GIT_INDEX_FILE=" + tmpIndex}
		if _, err := r.runEnv(env, "update-index", "--add", "--cacheinfo",
			"100644,"+s.blob+","+s.path); err != nil {
			return "", err
		}
		treeOut, err := r.runEnv(env, "write-tree")
		if err != nil {
			return "", err
		}
		tree := strings.TrimSpace(string(treeOut))
		args := []string{"commit-tree", tree}
		if head != "" {
			args = append(args, "-p", head)
		}
		args = append(args, "-m", s.info.Subject)
		dateEnv := []string{
			"GIT_AUTHOR_DATE=" + s.info.Time,
			"GIT_COMMITTER_DATE=" + s.info.Time,
		}
		out, err := r.runEnv(dateEnv, args...)
		if err != nil {
			return "", err
		}
		head = strings.TrimSpace(string(out))
	}
	if head == "" {
		// 全部提交被裁剪；bakon 保证每文件至少保留 max>=1 版，不应到达此处。
		return "", fmt.Errorf("rebuild dropped all commits")
	}

	branch, err := r.CurrentBranch()
	if err != nil {
		return "", err
	}
	if _, err := r.run("update-ref", "refs/heads/"+branch, head); err != nil {
		return "", err
	}
	return head, nil
}

// CurrentBranch 返回当前分支短名。bakon 不做 detach 操作，HEAD 恒指向分支。
func (r *Repo) CurrentBranch() (string, error) {
	out, err := r.run("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GC 清理 reflog 与不可达对象，使被裁剪版本真正从对象库移除。
func (r *Repo) GC() error {
	if _, err := r.run("reflog", "expire", "--expire=now", "--all"); err != nil {
		return err
	}
	_, err := r.run("gc", "--prune=now", "-q")
	return err
}
