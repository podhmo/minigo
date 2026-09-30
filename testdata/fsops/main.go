package main

// Filesystem/process intrinsics: os file APIs, path/filepath, os/exec,
// and host-value field access (exec.Cmd fields).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// WriteThenRead exercises MkdirAll + WriteFile + ReadFile + string(b).
func WriteThenRead(dir string) string {
	p := filepath.Join(dir, "sub", "a.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return "mkdir: " + err.Error()
	}
	if err := os.WriteFile(p, "hello", 0644); err != nil {
		return "write: " + err.Error()
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "read: " + err.Error()
	}
	return string(data)
}

// StatSize exercises os.Stat and FileInfo.Size through a GoValue.
func StatSize(dir string) int64 {
	p := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(p, "abc", 0644); err != nil {
		return -1
	}
	fi, err := os.Stat(p)
	if err != nil {
		return -2
	}
	return fi.Size()
}

// IsNotExistHit exercises the error sentinel + errors.Is path.
func IsNotExistHit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "missing.txt"))
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrNotExist) && os.IsNotExist(err)
}

// ListDir exercises os.ReadDir + DirEntry.Name/IsDir via reflection.
func ListDir(dir string) string {
	os.WriteFile(filepath.Join(dir, "x.txt"), "x", 0644)
	os.WriteFile(filepath.Join(dir, "y.txt"), "y", 0644)
	os.MkdirAll(filepath.Join(dir, "inner"), 0755)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "readdir: " + err.Error()
	}
	var names []string
	var dirs int
	for _, e := range entries {
		names = append(names, e.Name())
		if e.IsDir() {
			dirs = dirs + 1
		}
	}
	return strings.Join(names, ",") + "|dirs=" + strconv.Itoa(dirs)
}

// ChdirRoundtrip exercises the engine's virtual cwd: Chdir moves it,
// relative writes then anchor at the new cwd, and the host process's
// real cwd never changes.
func ChdirRoundtrip(dir string) string {
	before, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		return "chdir: " + err.Error()
	}
	here, _ := os.Getwd()
	if here != dir {
		return "chdir did not move the virtual cwd: " + here
	}
	if err := os.WriteFile("rel.txt", "c", 0644); err != nil {
		return "relwrite: " + err.Error()
	}
	p, _ := filepath.Abs("rel.txt")
	os.Chdir(before)
	back, _ := os.Getwd()
	if back != before {
		return "chdir back failed"
	}
	return p
}

// GlobMatch exercises filepath.Glob (absolute pattern -> absolute results).
func GlobMatch(dir string) string {
	os.WriteFile(filepath.Join(dir, "a.txt"), "a", 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), "b", 0644)
	m, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		return "glob: " + err.Error()
	}
	return strings.Join(m, ",")
}

// WalkCollect exercises filepath.WalkDir with a script callback and the
// DirEntry methods arriving as boxed host values.
func WalkCollect(dir string) int64 {
	os.MkdirAll(filepath.Join(dir, "deep"), 0755)
	os.WriteFile(filepath.Join(dir, "deep", "w.txt"), "w", 0644)
	n := 0
	err := filepath.WalkDir(dir, func(p string, d any, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n = n + 1
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return int64(n)
}

// ExecEcho exercises exec.Command + Cmd.Output + []byte marshalling.
func ExecEcho() string {
	out, err := exec.Command("echo", "-n", "hi").Output()
	if err != nil {
		return "exec: " + err.Error()
	}
	return string(out)
}

// CmdDirField reads the exec.Cmd.Dir field — defaults to the engine cwd.
func CmdDirField() string {
	cmd := exec.Command("echo")
	return cmd.Dir
}

// ExecDirField writes cmd.Dir and runs pwd there: field set + cwd default.
func ExecDirField(dir string) string {
	cmd := exec.Command("pwd")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "exec: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

// FileWrite exercises os.Create + (*os.File).Write with a script []byte-ish
// arg and Close via reflective method dispatch.
func FileWrite(dir string) string {
	p := filepath.Join(dir, "w.txt")
	f, err := os.Create(p)
	if err != nil {
		return "create: " + err.Error()
	}
	if _, err := f.Write("rw"); err != nil {
		return "write: " + err.Error()
	}
	if err := f.Close(); err != nil {
		return "close: " + err.Error()
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "read: " + err.Error()
	}
	return string(data)
}

// MatchHit exercises filepath.Match with its real two-argument call.
func MatchHit() bool {
	ok, err := filepath.Match("*.txt", "a.txt")
	if err != nil {
		return false
	}
	return ok
}

// LookPathLocal resolves a separator-bearing executable name against the
// engine's virtual cwd, not the host process's.
func LookPathLocal(dir string) string {
	old, err := os.Getwd()
	if err != nil {
		return "wd: " + err.Error()
	}
	if err := os.Chdir(dir); err != nil {
		return "chdir: " + err.Error()
	}
	defer os.Chdir(old)
	p, err := exec.LookPath("./tool.bin")
	if err != nil {
		return "lookpath: " + err.Error()
	}
	// Go's contract: the found name keeps the caller's (relative) shape
	return p
}

// GlobStar widens via *: in a restricted engine a match that escapes the
// roots through an in-root symlink must fail, not be returned.
func GlobStar() (string, error) {
	m, err := filepath.Glob("*/e.txt")
	if err != nil {
		return "", err
	}
	return strings.Join(m, ","), nil
}
