package atomic_test

import (
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/atomic"
)

func TestWriteFileNoOverwriteByDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.bin")
	if err := atomic.WriteFile(p, []byte("one"), atomic.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := atomic.WriteFile(p, []byte("two"), atomic.Options{}); err == nil {
		t.Fatal("overwrote without permission")
	}
	if data, _ := os.ReadFile(p); string(data) != "one" {
		t.Fatalf("content changed: %q", data)
	}
	if err := atomic.WriteFile(p, []byte("two"), atomic.Options{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != "two" {
		t.Fatalf("content not replaced: %q", data)
	}
	// no temporary files may be left behind
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != "out.bin" {
			t.Fatalf("stray file left behind: %s", e.Name())
		}
	}
}

func TestWriteJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "x.json")
	if err := atomic.WriteJSON(p, map[string]int{"a": 1}, atomic.Options{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("json output not newline-terminated")
	}
}

func TestEmptyDestination(t *testing.T) {
	if err := atomic.WriteFile("", []byte("x"), atomic.Options{}); err == nil {
		t.Fatal("empty path accepted")
	}
}
