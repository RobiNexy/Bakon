package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Entry 是单个被管理文件的元数据，以目标文件的绝对路径为键。
// ID 是仓库内 files/<id> 的目录名，与路径解耦，mv 不改变 ID。
type Entry struct {
	ID          string `json:"id"`
	MaxVersions int    `json:"max_versions,omitempty"`
	Hook        string `json:"hook,omitempty"`
}

type Index struct {
	// Entries 的键为经过 Clean 的绝对路径。
	Entries map[string]*Entry
}

// Load 读取索引。文件不存在时返回空索引，不视为错误。
func Load(path string) (*Index, error) {
	idx := &Index{Entries: map[string]*Entry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &idx.Entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return idx, nil
}

// Save 原子写索引：先写临时文件再 rename，读者不会看到半截 JSON。
func (idx *Index) Save(path string) error {
	data, err := json.MarshalIndent(idx.Entries, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	tmpPath = "" // rename 成功，取消 defer 中的清理
	return nil
}

func (idx *Index) Get(path string) *Entry {
	return idx.Entries[path]
}

func (idx *Index) Set(path string, e *Entry) {
	idx.Entries[path] = e
}

func (idx *Index) Delete(path string) {
	delete(idx.Entries, path)
}

// Paths 返回排序后的全部路径，供 ls 与全量遍历使用。
func (idx *Index) Paths() []string {
	paths := make([]string, 0, len(idx.Entries))
	for p := range idx.Entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
