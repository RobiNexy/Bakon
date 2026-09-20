package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	idx, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 0 {
		t.Errorf("entries = %v", idx.Entries)
	}
}

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.json")
	idx, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	idx.Set("/etc/hosts", &Entry{ID: "e5f6a7b8abcd", Hook: "echo hi"})
	idx.Set("/etc/nginx/nginx.conf", &Entry{ID: "a1b2c3d4e5f6", MaxVersions: 200})
	if err := idx.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Get("/etc/hosts")
	if e == nil || e.ID != "e5f6a7b8abcd" || e.Hook != "echo hi" {
		t.Errorf("hosts entry = %+v", e)
	}
	if got.Paths()[0] != "/etc/hosts" {
		t.Errorf("paths not sorted: %v", got.Paths())
	}
}

func TestOmitEmptyFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.json")
	idx, _ := Load(p)
	idx.Set("/f", &Entry{ID: "abc"})
	if err := idx.Save(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if got := string(data); got != "{\n  \"/f\": {\n    \"id\": \"abc\"\n  }\n}\n" {
		t.Errorf("unexpected json: %q", got)
	}
}

func TestDeleteAndPaths(t *testing.T) {
	idx, _ := Load(filepath.Join(t.TempDir(), "i.json"))
	idx.Set("/b", &Entry{ID: "b"})
	idx.Set("/a", &Entry{ID: "a"})
	idx.Delete("/b")
	paths := idx.Paths()
	if len(paths) != 1 || paths[0] != "/a" {
		t.Errorf("paths = %v", paths)
	}
}
