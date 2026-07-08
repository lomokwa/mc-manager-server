package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathStaysInsideServerDir(t *testing.T) {
	base, err := filepath.Abs(ServerDir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"",
		"server.properties",
		"world/level.dat",
		"../etc/passwd",
		"../../secret",
		"/etc/passwd",
		"world/../../../../etc/passwd",
		"..",
		"a/b/../../../c",
		"..\\..\\windows\\system32",
	}
	for _, in := range cases {
		abs, err := resolvePath(in)
		if err != nil {
			// A rejection is also an acceptable way to contain the path.
			continue
		}
		if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
			t.Errorf("resolvePath(%q) = %q escaped base %q", in, abs, base)
		}
	}
}

func TestFileReadWriteList(t *testing.T) {
	relDir := "mc-manager-files-test"
	absDir, err := resolvePath(relDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(absDir, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(absDir)

	rel := relDir + "/note.txt"
	if err := WriteFileText(rel, "hello world"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadFileText(rel)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "hello world" {
		t.Errorf("read = %q, want %q", got, "hello world")
	}

	entries, err := ListFiles(relDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "note.txt" || entries[0].IsDir {
		t.Errorf("list = %+v, want one file note.txt", entries)
	}
}
