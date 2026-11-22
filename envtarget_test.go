package minigo

import (
	"context"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

// A BuiltinFunc's Target is not display metadata: funcMapElem hands it
// to a template FuncMap verbatim, and the vm's arg materializers read
// it. An adapter that routes through the engine Env (FsPath, Cwd,
// AllowedRoots) must therefore keep Target nil — attaching the real
// func would hand scripts an unsandboxed call. Keep this list honest:
// any builtin whose adapter consults env belongs on it.
func TestEnvSandboxedBuiltinsHaveNoTarget(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	want := map[string][]string{
		"os": {
			"Stat", "Lstat", "ReadFile", "WriteFile",
			"Mkdir", "MkdirAll", "Remove", "RemoveAll",
			"Open", "Create", "Getwd", "Chdir", "Exit",
			"Link", "Symlink", "Readlink",
		},
		"path/filepath": {"Abs"},
		"os/exec":       {"Command", "CommandContext", "LookPath"},
	}
	for ref, names := range want {
		pkg, err := e.Package(ctx, ref)
		if err != nil {
			t.Fatalf("Package(%q): %v", ref, err)
		}
		for _, name := range names {
			gv, ok := pkg.Globals.Get(name)
			if !ok {
				t.Errorf("%s.%s: not bound", ref, name)
				continue
			}
			bf, ok := gv.(*runtime.BuiltinFunc)
			if !ok {
				t.Errorf("%s.%s: %T is not a BuiltinFunc", ref, name, gv)
				continue
			}
			if bf.Target != nil {
				t.Errorf("%s.%s: Target=%T — env-sandboxed adapters must stay targetless (funcMapElem leaks the raw func)", ref, name, bf.Target)
			}
		}
	}
}
