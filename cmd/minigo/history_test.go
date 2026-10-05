package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileHistoryPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h := newFileHistory(path, 100)
	h.Add("x := 1")
	h.Add("x * 2")

	// a new session sees the previous session's lines
	h2 := newFileHistory(path, 100)
	if h2.Len() != 2 {
		t.Fatalf("want 2 entries, got %d", h2.Len())
	}
	if h2.At(0) != "x * 2" || h2.At(1) != "x := 1" {
		t.Fatalf("At order wrong: %q %q", h2.At(0), h2.At(1))
	}
}

func TestFileHistoryDedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h := newFileHistory(path, 100)
	h.Add("same")
	h.Add("same")
	h.Add("other")
	if h.Len() != 2 {
		t.Fatalf("consecutive duplicates recorded: %d entries", h.Len())
	}
	if data, _ := os.ReadFile(path); string(data) != "same\nother\n" {
		t.Fatalf("file: %q", data)
	}
}

func TestFileHistoryTrim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h := newFileHistory(path, 3)
	for _, e := range []string{"a", "b", "c", "d"} {
		h.Add(e)
	}
	if h.Len() != 3 || h.At(0) != "d" || h.At(2) != "b" {
		t.Fatalf("ring wrong: Len=%d At=%q..%q", h.Len(), h.At(0), h.At(2))
	}
	// the file itself was rewritten down to the tail
	h2 := newFileHistory(path, 100)
	if h2.Len() != 3 || h2.At(0) != "d" {
		t.Fatalf("reloaded: Len=%d At=%q", h2.Len(), h2.At(0))
	}
}

func TestFileHistoryMissing(t *testing.T) {
	h := newFileHistory(filepath.Join(t.TempDir(), "nonexistent"), 100)
	if h.Len() != 0 {
		t.Fatalf("want empty, got %d", h.Len())
	}
	h.Add("first") // creates the file
	if data, err := os.ReadFile(h.path); err != nil || string(data) != "first\n" {
		t.Fatalf("file: %q err=%v", data, err)
	}
	if h.Len() != 1 || h.At(0) != "first" {
		t.Fatal("add after missing file")
	}
}
