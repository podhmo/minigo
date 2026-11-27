package runtime

import "sync"

var builtinOwnerMu sync.RWMutex

// AdoptBuiltinOwner stamps a package-bound builtin. The first owner wins
// across alias bindings; independent engines may share installer-owned
// BuiltinFunc objects, so the check and write are one critical section.
func AdoptBuiltinOwner(bf *BuiltinFunc, p *Package) {
	builtinOwnerMu.Lock()
	defer builtinOwnerMu.Unlock()
	if bf.Pkg == nil || bf.Pkg.Path == p.Path {
		bf.Pkg = p
	}
}

// BuiltinOwner reads the owner stamped by AdoptBuiltinOwner.
func BuiltinOwner(bf *BuiltinFunc) *Package {
	builtinOwnerMu.RLock()
	defer builtinOwnerMu.RUnlock()
	return bf.Pkg
}
