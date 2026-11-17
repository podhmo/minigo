// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"go/token"
	"os"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// trace appends a traceback entry (PR #3's `File "...", line N, in f`
// plus the source line under it) to an unwinding Trap/Panic.
func (v *VM) trace(f *frame, r any) {
	pos := f.pos()
	if !pos.IsValid() && f.fn.Decl != nil {
		// the frame failed before its first instruction (e.g. argument
		// coercion) — point at the function's declaration instead
		pos = f.fn.Decl.Pos()
	}
	entry := v.frameLine(f, pos, v.frameName(f))
	switch e := r.(type) {
	case *runtime.Trap:
		e.Frames = append(e.Frames, entry)
	case *runtime.Panic:
		e.Frames = append(e.Frames, entry)
	}
}

// frameName renders f's function for a traceback entry — `Id[int]` for an
// instantiated generic, `T.M` for methods.
func (v *VM) frameName(f *frame) string {
	name := f.fn.Name
	if len(f.fn.TParams) > 0 {
		args := make([]string, len(f.fn.TParams))
		for i, tp := range f.fn.TParams {
			args[i] = "?"
			if td, ok := f.fn.Binds[tp].(*runtime.TypeDef); ok {
				args[i] = td.Name
			}
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name + "()"
}

// frameLine formats one traceback entry — `File "<file>", line <n>, in
// <name>` followed by the source text under it when available.
func (v *VM) frameLine(f *frame, pos token.Pos, name string) string {
	if !pos.IsValid() || f.fn.Pkg == nil || f.fn.Pkg.Fset == nil {
		return name
	}
	p := f.fn.Pkg.Fset.Position(pos)
	entry := fmt.Sprintf("File %q, line %d, in %s", p.Filename, p.Line, name)
	if l := v.sourceLine(f.fn.Pkg, p.Filename, p.Line); l != "" {
		entry += "\n\t\t" + l
	}
	return entry
}

// sourceLine returns the trimmed text of filename:line for traceback
// snippets. In-memory sources (REPL, generated files) come from
// syntax.File.Src; others are read from disk, but only when filename is a
// file the package actually loaded — never an arbitrary path — so an
// error message cannot leak text outside what the script already ran.
// Failures are silent — a traceback must never itself fail.
func (v *VM) sourceLine(pkg *runtime.Package, filename string, line int) string {
	if pkg == nil || pkg.FileByName == nil {
		return ""
	}
	sf := pkg.FileByName[filename]
	if sf == nil {
		return ""
	}
	if sf.Src != nil {
		return nthLine(sf.Src, line)
	}
	if v.srcCache == nil {
		v.srcCache = map[string][]string{}
	}
	lines, ok := v.srcCache[filename]
	if !ok {
		data, err := os.ReadFile(filename)
		if err != nil {
			v.srcCache[filename] = nil
			return ""
		}
		lines = strings.Split(string(data), "\n")
		v.srcCache[filename] = lines
	}
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// nthLine returns src's line-th line, trimmed; "" when out of range.
func nthLine(src []byte, line int) string {
	if line < 1 {
		return ""
	}
	for i, l := range strings.Split(string(src), "\n") {
		if i == line-1 {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// CallerPCs implements [runtime.VMCaller]. Frames the in-flight panic
// already unwound appear where Go's traceback lists them: between the
// deferred-call chain and the still-live frames below.
func (v *VM) CallerPCs() []uintptr {
	sites := make([]runtime.CallSite, 0, len(v.frames)+len(v.unwinding))
	depth := len(v.frames)
	// unwinding stays populated through the drain even after the panic is
	// consumed, so the splice point is unwindDepth whenever it is non-empty.
	if len(v.unwinding) > 0 && v.unwindDepth < depth {
		depth = v.unwindDepth
	}
	for i := len(v.frames) - 1; i >= depth; i-- {
		sites = append(sites, v.callSite(v.frames[i]))
	}
	// unwound frames list grouped by their panic, newest unwind first:
	// a superseding panic's frames precede the superseded panic's
	// leftovers, matching Go's per-panic traceback order. Groups are
	// keyed by panic and ordered by their last pop.
	seen := map[*runtime.Panic]bool{}
	for i := len(v.unwinding) - 1; i >= 0; i-- {
		pn := v.unwinding[i].pn
		if seen[pn] {
			continue
		}
		seen[pn] = true
		// a deferred call's caller is the unwinder itself — Go's stack
		// lists `runtime.gopanic` between the deferred chain and the
		// panicking frames, so runtime.Caller(2) inside a deferred
		// call resolves to the panicking frame (issue5856).
		sites = append(sites, gopanicCallSite)
		for _, e := range v.unwinding {
			if e.pn == pn {
				sites = append(sites, v.callSite(e.f))
			}
		}
	}
	for i := depth - 1; i >= 0; i-- {
		sites = append(sites, v.callSite(v.frames[i]))
	}
	base := len(v.pcSites)
	v.pcSites = append(v.pcSites, sites...)
	pcs := make([]uintptr, len(sites))
	for i := range pcs {
		pcs[i] = uintptr(base + i + 1)
	}
	return pcs
}

// CallerFrame implements [runtime.VMCaller].
func (v *VM) CallerFrame(pc uintptr) (runtime.CallSite, bool) {
	if pc == 0 || int(pc) > len(v.pcSites) {
		return runtime.CallSite{}, false
	}
	return v.pcSites[pc-1], true
}

// callSite renders one frame for runtime.Callers: the function's Go
// symbol name and the source position it is (or was) executing.
func (v *VM) callSite(f *frame) runtime.CallSite {
	name := f.fn.Name
	if f.fn.PtrRecv && f.fn.Recv != "" && strings.HasPrefix(name, f.fn.Recv+".") {
		// gc spells a pointer-receiver method (*T).M in Frame.Function.
		name = "(*" + f.fn.Recv + ")" + name[len(f.fn.Recv):]
	}
	// runtime.Frame.Function names the fully-qualified symbol —
	// `main.main`, `main.call.name` for methods (Name already carries
	// the receiver). Synthesized frames (`main.__init__`) are
	// pre-qualified.
	if f.fn.Pkg != nil && f.fn.Pkg.Name != "" && !strings.HasPrefix(name, f.fn.Pkg.Name+".") {
		name = f.fn.Pkg.Name + "." + name
	}
	site := runtime.CallSite{Name: name}
	pos := f.pos()
	if !pos.IsValid() && f.fn.Decl != nil {
		pos = f.fn.Decl.Pos()
	}
	if pos.IsValid() && f.fn.Pkg != nil && f.fn.Pkg.Fset != nil {
		p := f.fn.Pkg.Fset.Position(pos)
		site.File = p.Filename
		if site.File == "" {
			// a `//line :N` directive records no filename — gc renders
			// the unrecorded file as "??" in Frame.File.
			site.File = "??"
		}
		site.Line = p.Line
	}
	return site
}
