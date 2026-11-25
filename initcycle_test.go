package minigo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/podhmo/minigo/pkg/gentest"
)

func TestInitCycleUnboundMemberDoesNotDeadlock(t *testing.T) {
	root := gentest.WriteFiles(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"a/a.go": `package a

import "example.com/m/b"

var Y = b.ReadY()
`,
		"b/b.go": `package b

import "example.com/m/a"

func ReadY() int { return a.Y }
`,
		"cmd/main.go": `package main

import "example.com/m/a"

func main() { println(a.Y) }
`,
	})

	e := NewEngine(root)
	_, err := e.Run(context.Background(), filepath.Join(root, "cmd"), "main")
	if err == nil || !strings.Contains(err.Error(), "initialization cycle: a.Y is not bound yet") {
		t.Fatalf("cyclic read of an unbound member must fail loudly, got %v", err)
	}
}
