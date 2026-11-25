package minigo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCycleUnboundMemberDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
	write(filepath.Join(root, "a", "a.go"), `package a

import "example.com/m/b"

var Y = b.ReadY()
`)
	write(filepath.Join(root, "b", "b.go"), `package b

import "example.com/m/a"

func ReadY() int { return a.Y }
`)
	write(filepath.Join(root, "cmd", "main.go"), `package main

import "example.com/m/a"

func main() { println(a.Y) }
`)

	e := NewEngine(root)
	_, err := e.Run(context.Background(), filepath.Join(root, "cmd"), "main")
	if err == nil || !strings.Contains(err.Error(), "initialization cycle: a.Y is not bound yet") {
		t.Fatalf("cyclic read of an unbound member must fail loudly, got %v", err)
	}
}
