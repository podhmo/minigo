package runtime

import (
	goruntime "runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"weak"
)

// synthImports binds compiler-minted `.imp:` qualifiers to their
// import refs: a deferred eval compiles the qualifier in a fresh pass
// whose own synthImports map is empty, and the VM's global resolution
// meets the name with no file-scope import to answer it. Values are
// weak and entries are removed by cleanup: the compiled chunk pins the
// refs it may resolve, but this process-wide registry must not pin a
// session after that chunk becomes unreachable.
var synthImports sync.Map // string -> weak.Pointer[ImportRef]

// synthSeq makes every minted name unique: two sessions compiling the
// same path must never share a qualifier, or one's global resolution
// would read the other's package.
var synthSeq atomic.Uint64

// MintSynthImportName returns the qualifier a re-scoped expression uses
// for ref — globally unique and already bound in the registry.
func MintSynthImportName(ref *ImportRef) string {
	name := ".imp:" + ref.Path + "#" + strconv.FormatUint(synthSeq.Add(1), 16)
	synthImports.Store(name, weak.Make(ref))
	goruntime.AddCleanup(ref, forgetSynthImport, name)
	return name
}

// forgetSynthImport drops a minted qualifier once its import ref is
// unreachable. The name itself is globally unique, so deleting by name
// can never remove a later session's entry.
func forgetSynthImport(name string) { synthImports.Delete(name) }

// SynthImportRef returns the import ref a `.imp:` qualifier was minted
// for, or nil when the name is not one of ours.
func SynthImportRef(name string) *ImportRef {
	if r, ok := synthImports.Load(name); ok {
		return r.(weak.Pointer[ImportRef]).Value()
	}
	return nil
}
