// hostslice.go delegates the host-slice operations that moved to
// internal/hostbridge: the exported names keep their vm.* spelling for
// embedders (slices helpers, sort intrinsics, append/copy builtins).
package vm

import (
	"reflect"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// HostSliceOf reports whether x — a bare box, a variable cell holding
// one, or a Named tag over either — carries a host slice, and returns the
// box and the slice itself.
func HostSliceOf(x runtime.Value) (*runtime.GoValue, reflect.Value, bool) {
	return hostbridge.HostSliceOf(x)
}

// UnboxHostSlice reads a boxed host slice as the script slice of the same
// shape (an unnamed []E, as for any host slice that is not named): a
// snapshot — element writes into it do not reach the host. A nil host
// slice reads as a typed nil. ok is false when x holds no host slice.
func UnboxHostSlice(x runtime.Value) (runtime.Value, bool) {
	return hostbridge.UnboxHostSlice(x)
}

// StoreHostSlice writes the elements of snap — a snapshot UnboxHostSlice
// returned for host that an in-place operation reordered — back into the
// host slice's own backing.
func StoreHostSlice(vc runtime.VMCaller, host, snap runtime.Value) error {
	return marshalHost.StoreHostSlice(vc, host, snap)
}

// ReboxHostSlice gives res, a slice result an intrinsic computed from
// snap (the snapshot of host), the host slice type again when it is a
// slice of the snapshot's own type: slices.Clone(ip) is a net.IP. A result
// that shares the snapshot's backing (slices.Compact, Delete) views the
// host backing the same way, so later writes reach it; any other result
// is a fresh host slice. Everything else passes through.
func ReboxHostSlice(vc runtime.VMCaller, host, snap, res runtime.Value) runtime.Value {
	return marshalHost.ReboxHostSlice(vc, host, snap, res)
}

// AppendHostSlice is append(host, add...) on a boxed host slice: it grows
// the host slice itself, so an append within capacity writes the shared
// backing as in Go, and the result keeps the host type.
func AppendHostSlice(vc runtime.VMCaller, host runtime.Value, add []runtime.Value) (runtime.Value, error) {
	return marshalHost.AppendHostSlice(vc, host, add)
}

// CopyIntoHostSlice is copy(host, src) with src already read as elements:
// it writes the host backing and reports how many elements it copied.
func CopyIntoHostSlice(vc runtime.VMCaller, host runtime.Value, src []runtime.Value) (int, error) {
	return marshalHost.CopyIntoHostSlice(vc, host, src)
}

// hostSliceConv converts x to the host slice type of td (a bound named
// slice such as net.IP).
func (v *VM) hostSliceConv(td *runtime.TypeDef, x runtime.Value) (runtime.Value, bool, error) {
	return marshalHost.HostSliceConv(v, td, x)
}

// hostSliceLiteral builds the literal td{...} of a host slice type.
func (v *VM) hostSliceLiteral(f *frame, td *runtime.TypeDef, rt reflect.Type, n int, kv bool, raw []runtime.Value) runtime.Value {
	return marshalHost.HostSliceLiteral(v.indexHooks(f), td, rt, n, kv, raw)
}
