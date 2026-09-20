package bakon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobiNexy/Bakon/internal/index"
)

func indexLoad(app *App) (*index.Index, error) {
	return index.Load(app.IndexPath)
}

// newTestApp 构造指向临时仓库的 App，不触碰真实 ~/.bakon。
func newTestApp(t *testing.T, maxVersions int) (*App, string) {
	t.Helper()
	tmp := t.TempDir()
	store := filepath.Join(tmp, "repo")
	cfg := fmt.Sprintf("store = %q\n\n[retention]\nmax_versions = %d\n", store, maxVersions)
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return app, tmp
}

// overwriteEditor 返回一个编辑器命令：把 content 覆盖写入目标文件。
// 我们以 sh -c "<editor> '<file>'" 调用编辑器，cp 的第二个参数即目标文件。
func overwriteEditor(t *testing.T, tmp, content string) string {
	t.Helper()
	src := filepath.Join(tmp, "newcontent")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return "cp " + quoteArg(src)
}

// noopEditor 编辑器退出且不改动文件。
func noopEditor(t *testing.T, tmp string) string {
	return "true"
}

// mustEdit 执行 edit 并返回本次编辑产生的版本号（Change）。
func mustEdit(t *testing.T, app *App, file string) int {
	t.Helper()
	res, err := app.Edit(file)
	if err != nil {
		t.Fatalf("edit %s: %v", file, err)
	}
	return res.Change
}

func mustSetContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOut(t *testing.T, app *App, args ...string) string {
	t.Helper()
	out, err := app.Repo.Run(args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestEditCreatesVersionsAndLog(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "hosts.txt")
	mustSetContent(t, file, "one\n")
	app.Editor = overwriteEditor(t, tmp, "one\ntwo\n")
	if ver := mustEdit(t, app, file); ver != 1 {
		t.Fatalf("ver = %d, want 1", ver)
	}
	app.Editor = overwriteEditor(t, tmp, "one\ntwo\nthree\n")
	if ver := mustEdit(t, app, file); ver != 2 {
		t.Fatalf("ver = %d, want 2", ver)
	}
	infos, err := app.Log(file)
	if err != nil {
		t.Fatal(err)
	}
	// 基线 0 = "one\n"，之后 1、2 为两次编辑。
	if len(infos) != 3 {
		t.Fatalf("log len = %d", len(infos))
	}
	if infos[0].Ver != 2 || infos[1].Ver != 1 || infos[2].Ver != 0 {
		t.Errorf("log order = %d,%d,%d", infos[0].Ver, infos[1].Ver, infos[2].Ver)
	}
	if infos[0].Size != 14 || infos[1].Size != 8 || infos[2].Size != 4 {
		t.Errorf("sizes = %d,%d,%d", infos[0].Size, infos[1].Size, infos[2].Size)
	}
	if infos[0].Time.IsZero() {
		t.Error("time is zero")
	}
}

func TestFirstEditCreatesBaselineVersion0(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "original\n")
	app.Editor = overwriteEditor(t, tmp, "edited\n")
	res, err := app.Edit(file)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Adopted || res.Change != 1 {
		t.Fatalf("res = %+v, want adopted + change 1", res)
	}
	// ver 0 = 纳管前内容，可读取、可回退。
	got, err := app.Show(file, 0)
	if err != nil || string(got) != "original\n" {
		t.Fatalf("show 0 = %q, %v", got, err)
	}
	if _, err := app.Revert(file, 0); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(file)
	if string(content) != "original\n" {
		t.Errorf("revert 0 content = %q", content)
	}
	// 第二次编辑（无改动）：不再重复纳管。
	app.Editor = noopEditor(t, tmp)
	res, err = app.Edit(file)
	if err != nil {
		t.Fatal(err)
	}
	if res.Adopted || res.Change != 0 {
		t.Errorf("res = %+v, want no adoption and no change", res)
	}
}

func TestAdoptionWithoutChangeRecordsBaselineOnly(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "asis\n")
	app.Editor = noopEditor(t, tmp)
	res, err := app.Edit(file)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Adopted || res.Change != 0 {
		t.Fatalf("res = %+v, want adopted without change", res)
	}
	infos, err := app.Log(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Ver != 0 {
		t.Errorf("log = %+v, want only baseline", infos)
	}
	got, _ := app.Show(file, 0)
	if string(got) != "asis\n" {
		t.Errorf("baseline content = %q", got)
	}
}

func TestEditNoChangeIsSilent(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "same\n")
	if ver := mustEdit(t, app, file); ver != 1 {
		t.Fatalf("ver = %d", ver)
	}
	app.Editor = noopEditor(t, tmp)
	res, err := app.Edit(file)
	if err != nil {
		t.Fatal(err)
	}
	if res.Adopted || res.Change != 0 {
		t.Errorf("res = %+v, want plain no-change", res)
	}
	infos, _ := app.Log(file)
	// 基线 0 + 一次变更 = 2 版。
	if len(infos) != 2 {
		t.Errorf("log len = %d, want 2", len(infos))
	}
}

func TestEditMissingFileFails(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	app.Editor = noopEditor(t, tmp)
	_, err := app.Edit(filepath.Join(tmp, "gone.txt"))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("err = %v, want no-such-file guidance", err)
	}
}

func TestEditDeletedTrackedFileSuggestsRevert(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "v1\n")
	mustEdit(t, app, file)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err := app.Edit(file)
	if err == nil {
		t.Fatal("expected error for deleted tracked file")
	}
	if !strings.Contains(err.Error(), "deleted") || !strings.Contains(err.Error(), "bakon revert") {
		t.Errorf("err = %v, want deleted + revert guidance", err)
	}
}

func TestEditDirectoryFails(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	app.Editor = noopEditor(t, tmp)
	dir := filepath.Join(tmp, "d")
	os.Mkdir(dir, 0o755)
	if _, err := app.Edit(dir); err == nil {
		t.Error("expected error for directory")
	}
}

func TestEditorFailureFails(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "x\n")
	app.Editor = "exit 3"
	if _, err := app.Edit(file); err == nil {
		t.Error("expected editor error")
	}
}

func TestShow(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "start\n")
	for _, c := range []string{"alpha\n", "beta\n", "gamma\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	for ver, want := range map[int]string{0: "start\n", 1: "alpha\n", 2: "beta\n", 3: "gamma\n"} {
		got, err := app.Show(file, ver)
		if err != nil {
			t.Fatalf("show %d: %v", ver, err)
		}
		if string(got) != want {
			t.Errorf("show %d = %q, want %q", ver, got, want)
		}
	}
	if _, err := app.Show(file, 9); err == nil {
		t.Error("expected error for unknown version")
	}
}

func TestDiff(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "start\n")
	for _, c := range []string{"alpha\n", "beta\n", "gamma\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	// 缺省：最近两版 (beta → gamma)
	out, err := app.Diff(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "-beta") || !strings.Contains(string(out), "+gamma") {
		t.Errorf("default diff = %q", out)
	}
	// 单参数：v-1 vs v（此处 v=2 → alpha vs beta）
	out, err = app.Diff(file, []string{"2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "+beta") {
		t.Errorf("single diff = %q", out)
	}
	// 双参数
	out, err = app.Diff(file, []string{"1", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "+gamma") {
		t.Errorf("v1..v3 diff = %q", out)
	}
	// 基线 0 没有前驱
	if _, err := app.Diff(file, []string{"0"}); err == nil {
		t.Error("expected error diffing baseline alone")
	}
	// 少于两版
	solo := filepath.Join(tmp, "solo.txt")
	mustSetContent(t, solo, "only\n")
	app.Editor = noopEditor(t, tmp)
	mustEdit(t, app, solo)
	if _, err := app.Diff(solo, nil); err == nil {
		t.Error("expected error for single-version diff")
	}
}

func TestRevert(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "start\n")
	for _, c := range []string{"alpha\n", "beta\n", "gamma\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	newVer, err := app.Revert(file, 1)
	if err != nil {
		t.Fatal(err)
	}
	if newVer != 4 {
		t.Fatalf("revert ver = %d, want 4", newVer)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "alpha\n" {
		t.Errorf("file content = %q", got)
	}
	v4, err := app.Show(file, 4)
	if err != nil || string(v4) != "alpha\n" {
		t.Errorf("show v4 = %q (%v)", v4, err)
	}
	infos, _ := app.Log(file)
	// 0=seed, 1..3 三次编辑, 4=revert → 5 版
	if len(infos) != 5 || infos[0].Ver != 4 {
		t.Errorf("log after revert = %+v", infos)
	}
}

func TestRevertUnknownVersionFails(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "a\n")
	app.Editor = noopEditor(t, tmp)
	mustEdit(t, app, file)
	if _, err := app.Revert(file, 5); err == nil {
		t.Error("expected error for unknown version")
	}
}

func TestVersionErrorDistinguishesPrunedFromMissing(t *testing.T) {
	app, tmp := newTestApp(t, 2)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	for _, c := range []string{"v1\n", "v2\n", "v3\n", "v4\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	// 现存版本：3、4。ver 1/2 已被裁剪，ver 5 从未存在。
	_, err := app.Revert(file, 1)
	if err == nil || !strings.Contains(err.Error(), "has been pruned") {
		t.Errorf("ver 1 err = %v, want pruned", err)
	}
	_, err = app.Show(file, 2)
	if err == nil || !strings.Contains(err.Error(), "has been pruned") {
		t.Errorf("ver 2 err = %v, want pruned", err)
	}
	_, err = app.Show(file, 5)
	if err == nil || !strings.Contains(err.Error(), "no version 5") {
		t.Errorf("ver 5 err = %v, want missing", err)
	}
}

func TestRevertRestoresDeletedFile(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "keep\n")
	app.Editor = overwriteEditor(t, tmp, "keep\nchanged\n")
	mustEdit(t, app, file)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Revert(file, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "keep\nchanged\n" {
		t.Errorf("restored = %q", got)
	}
}

func TestAutoPruneKeepsNewestAndDropsObjects(t *testing.T) {
	app, tmp := newTestApp(t, 2)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	contents := []string{"v1\n", "v2\n", "v3\n", "v4\n"}
	for _, c := range contents {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	infos, err := app.Log(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].Ver != 4 || infos[1].Ver != 3 {
		t.Fatalf("log after prune = %+v", infos)
	}
	if _, err := app.Show(file, 1); err == nil {
		t.Error("expected pruned version 1 to be gone")
	}
	// 2 commits + 2×(根树 + files/ 子树) + 2 blobs = 8 个对象；
	// 旧版本对象应已被 gc 清除。
	count := gitOut(t, app, "count-objects", "-v")
	found := false
	for _, line := range strings.Split(count, "\n") {
		if strings.TrimSpace(line) == "in-pack: 8" {
			found = true
		}
	}
	if !found {
		t.Errorf("in-pack != 8: %s", count)
	}
	// 序号不回收：下一版仍是 5
	app.Editor = overwriteEditor(t, tmp, "v5\n")
	if ver := mustEdit(t, app, file); ver != 5 {
		t.Errorf("ver after prune = %d, want 5", ver)
	}
}

func TestManualPruneAllFiles(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	for _, c := range []string{"v1\n", "v2\n", "v3\n", "v4\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	// 文件级覆盖：max_versions = 2（已有 5 版：0..4，裁掉最旧 3 版）
	idx, err := indexLoad(app)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := cleanAbs(file)
	idx.Get(abs).MaxVersions = 2
	if err := idx.Save(app.IndexPath); err != nil {
		t.Fatal(err)
	}
	n, err := app.Prune("")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("pruned = %d, want 3", n)
	}
	infos, _ := app.Log(file)
	if len(infos) != 2 || infos[0].Ver != 4 {
		t.Errorf("log = %+v", infos)
	}
	// 已达上限，再裁剪无事发生
	if n, err := app.Prune(""); err != nil || n != 0 {
		t.Errorf("second prune = %d, %v", n, err)
	}
}

func TestPruneUnlimitedIsNoop(t *testing.T) {
	app, tmp := newTestApp(t, 0) // 0 = 不限制
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	for _, c := range []string{"v1\n", "v2\n", "v3\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, file)
	}
	n, err := app.Prune("")
	if err != nil || n != 0 {
		t.Errorf("prune = %d, %v; want noop", n, err)
	}
	infos, _ := app.Log(file)
	// 0=seed + 3 次编辑 = 4 版
	if len(infos) != 4 {
		t.Errorf("log = %+v", infos)
	}
}

func TestPruneSingleFile(t *testing.T) {
	// 全局不限制，仅给 b 设 per-file 上限：验证单文件手动裁剪
	// 与文件级 max_versions 覆盖，且不影响其他文件。
	app, tmp := newTestApp(t, 0)
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	mustSetContent(t, a, "seed\n")
	mustSetContent(t, b, "seed\n")
	for _, c := range []string{"a1\n", "a2\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, a)
	}
	for _, c := range []string{"b1\n", "b2\n", "b3\n"} {
		app.Editor = overwriteEditor(t, tmp, c)
		mustEdit(t, app, b)
	}
	idx, err := indexLoad(app)
	if err != nil {
		t.Fatal(err)
	}
	idx.Get(b).MaxVersions = 2
	if err := idx.Save(app.IndexPath); err != nil {
		t.Fatal(err)
	}
	// b 有 4 版（0..3），裁掉最旧 2 版
	n, err := app.Prune(b)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned = %d, want 2", n)
	}
	ia, _ := app.Log(a)
	ib, _ := app.Log(b)
	// a 未裁剪：0..2 共 3 版
	if len(ia) != 3 {
		t.Errorf("a log = %+v", ia)
	}
	if len(ib) != 2 || ib[0].Ver != 3 {
		t.Errorf("b log = %+v", ib)
	}
	// 裁剪后，两文件保留版本均可用
	if _, err := app.Show(a, 1); err != nil {
		t.Errorf("show a1: %v", err)
	}
	if _, err := app.Show(b, 2); err != nil {
		t.Errorf("show b2: %v", err)
	}
}

func TestLs(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	mustSetContent(t, a, "seed\n")
	mustSetContent(t, b, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "a\n")
	mustEdit(t, app, a)
	app.Editor = overwriteEditor(t, tmp, "b\n")
	mustEdit(t, app, b)
	paths, err := app.Ls()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{a, b}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("ls = %v, want %v", paths, want)
	}
}

func TestMV(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	oldPath := filepath.Join(tmp, "old.txt")
	newPath := filepath.Join(tmp, "new.txt")
	mustSetContent(t, oldPath, "h0\n")
	app.Editor = overwriteEditor(t, tmp, "h1\n")
	mustEdit(t, app, oldPath)
	if err := app.Mv(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	// 模拟用户已把文件移动到新位置
	mustSetContent(t, newPath, "h1\n")
	paths, _ := app.Ls()
	if len(paths) != 1 || paths[0] != newPath {
		t.Fatalf("ls = %v", paths)
	}
	// 新路径下历史延续：下一版是 2
	app.Editor = overwriteEditor(t, tmp, "h2\n")
	if ver := mustEdit(t, app, newPath); ver != 2 {
		t.Errorf("ver after mv = %d, want 2", ver)
	}
	infos, _ := app.Log(newPath)
	// 0=h0(基线), 1=h1, 2=h2 → 历史随 mv 延续
	if len(infos) != 3 || infos[2].Ver != 0 {
		t.Errorf("log after mv = %+v", infos)
	}
}

func TestMVFailures(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	mustSetContent(t, a, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "a\n")
	mustEdit(t, app, a)
	if err := app.Mv(a, a); err == nil {
		t.Error("expected same-path error")
	}
	mustSetContent(t, b, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "b\n")
	mustEdit(t, app, b)
	if err := app.Mv(a, b); err == nil {
		t.Error("expected already-managed error")
	}
	if err := app.Mv(filepath.Join(tmp, "gone.txt"), b); err == nil {
		t.Error("expected not-managed error")
	}
}

func TestHookLifecycle(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	marker := filepath.Join(tmp, "marker")
	mustSetContent(t, file, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "v1\n")
	mustEdit(t, app, file)

	if err := app.HookSet(file, "touch "+marker); err != nil {
		t.Fatal(err)
	}
	hook, err := app.HookShow(file)
	if err != nil || hook == "" {
		t.Fatalf("hook show = %q, %v", hook, err)
	}
	// 无改动 → 不触发钩子
	app.Editor = noopEditor(t, tmp)
	if _, err := app.Edit(file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook fired without content change")
	}
	// 有改动 → 触发
	app.Editor = overwriteEditor(t, tmp, "v2\n")
	if _, err := app.Edit(file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook did not fire: %v", err)
	}
	// revert 产生新版本 → 触发
	os.Remove(marker)
	if _, err := app.Revert(file, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("hook did not fire on revert")
	}
	// unset
	if err := app.HookUnset(file); err != nil {
		t.Fatal(err)
	}
	if hook, _ := app.HookShow(file); hook != "" {
		t.Errorf("hook after unset = %q", hook)
	}
	// 持久化到 index.json
	data, _ := os.ReadFile(app.IndexPath)
	if strings.Contains(string(data), "hook") {
		t.Errorf("hook field not removed: %s", data)
	}
}

func TestHookFailureKeepsVersion(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "v1\n")
	mustEdit(t, app, file)
	if err := app.HookSet(file, "exit 7"); err != nil {
		t.Fatal(err)
	}
	app.Editor = overwriteEditor(t, tmp, "v2\n")
	res, err := app.Edit(file)
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if !errors.Is(err, ErrHookFailed) {
		t.Fatalf("err = %v, want ErrHookFailed", err)
	}
	if res.Change != 2 {
		t.Fatalf("change = %d, want 2", res.Change)
	}
	infos, _ := app.Log(file)
	// 0=seed, 1=v1, 2=v2（钩子失败不回滚）→ 3 版
	if len(infos) != 3 {
		t.Errorf("version not saved: %+v", infos)
	}
}

func TestHookSetEmptyFails(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "seed\n")
	app.Editor = overwriteEditor(t, tmp, "v1\n")
	mustEdit(t, app, file)
	if err := app.HookSet(file, "   "); err == nil {
		t.Error("expected empty-hook error")
	}
}

func TestUnmanagedOperationsFail(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	for _, fn := range []func() error{
		func() error { _, err := app.Log(file); return err },
		func() error { _, err := app.Show(file, 1); return err },
		func() error { _, err := app.Diff(file, nil); return err },
		func() error { return app.HookSet(file, "x") },
		func() error { _, err := app.HookShow(file); return err },
		func() error { _, err := app.Revert(file, 1); return err },
		func() error { _, err := app.Prune(file); return err },
	} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "not managed") {
			t.Errorf("expected not-managed error, got %v", err)
		}
	}
}

func TestParseVer(t *testing.T) {
	cases := []struct {
		subj string
		n    int
		ok   bool
	}{
		{"bakon ver 3", 3, true}, {"bakon ver 12", 12, true}, {"bakon ver 0", 0, true},
		{"bakon ver x", 0, false}, {"bakon ver -1", 0, false}, {"Merge branch", 0, false}, {"bakon ver", 0, false},
	}
	for _, want := range cases {
		n, ok := ParseVer(want.subj)
		if ok != want.ok || (ok && n != want.n) {
			t.Errorf("ParseVer(%q) = %d,%v", want.subj, n, ok)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0: "0B", 999: "999B", 1024: "1.0K", 1536: "1.5K",
		1048576: "1.0M", 3 * 1024 * 1024 * 1024: "3.0G",
	}
	for n, want := range cases {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestIDForStable(t *testing.T) {
	a := IDFor("/etc/hosts")
	b := IDFor("/etc/hosts")
	c := IDFor("/etc/hosts2")
	if a != b || len(a) != 12 || a == c {
		t.Errorf("ids: %q %q %q", a, b, c)
	}
}

func TestConcurrentEdits(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "base\n")
	app.Editor = overwriteEditor(t, tmp, "v1\n")
	mustEdit(t, app, file)

	// 两个独立 App 并发编辑：旧 hash 在锁内计算，编辑串行生效，
	// 恰好产生两个新版本（v2、v3），无幽灵提交。
	appA, err := NewApp(app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	srcA := filepath.Join(tmp, "contentA")
	os.WriteFile(srcA, []byte("contentA\n"), 0o644)
	appA.Editor = "cp " + quoteArg(srcA)

	appB, err := NewApp(app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	srcB := filepath.Join(tmp, "contentB")
	os.WriteFile(srcB, []byte("contentB\n"), 0o644)
	appB.Editor = "cp " + quoteArg(srcB)

	errs := make(chan error, 2)
	go func() {
		_, err := appA.Edit(file)
		errs <- err
	}()
	go func() {
		_, err := appB.Edit(file)
		errs <- err
	}()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	infos, _ := app.Log(file)
	// 0=base + v1 + 并发的两次变更 = 4 版
	if len(infos) != 4 || infos[0].Ver != 3 || infos[3].Ver != 0 {
		t.Errorf("log = %+v, want 4 versions", infos)
	}
}

func TestGitHistoryUnchangedByReadOps(t *testing.T) {
	app, tmp := newTestApp(t, 100)
	file := filepath.Join(tmp, "f.txt")
	mustSetContent(t, file, "a\n")
	app.Editor = overwriteEditor(t, tmp, "b\n")
	mustEdit(t, app, file)
	app.Editor = overwriteEditor(t, tmp, "c\n")
	mustEdit(t, app, file)
	before := gitOut(t, app, "rev-list", "--all", "--oneline")
	for _, fn := range []func() error{
		func() error { _, err := app.Log(file); return err },
		func() error { _, err := app.Show(file, 1); return err },
		func() error { _, err := app.Diff(file, nil); return err },
		func() error { _, err := app.Ls(); return err },
	} {
		if err := fn(); err != nil {
			t.Fatal(err)
		}
	}
	after := gitOut(t, app, "rev-list", "--all", "--oneline")
	if before != after {
		t.Errorf("history changed by read ops: %q -> %q", before, after)
	}
	// 仓库工作区只应留下设计内的未跟踪文件（.lock、index.json）
	st, err := exec.Command("git", "-C", app.Repo.Dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(st), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, ".lock") && !strings.Contains(line, "index.json") {
			t.Errorf("unexpected repo state: %q", line)
		}
	}
}
