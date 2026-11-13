package vm

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func TestProcessCallerDoesNotFollowReusedVM(t *testing.T) {
	v := &VM{}
	var saved runtime.VMCaller
	capture := &runtime.BuiltinFunc{Fn: func(c runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		saved = c
		return "captured", nil
	}}
	if _, err := v.Call(capture, nil); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	callback := &runtime.BuiltinFunc{Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
		fired.Add(1)
		return "called", nil
	}}
	assertDead := func() error {
		if _, err := saved.Call(callback, nil); !IsProcExit(err) {
			return fmt.Errorf("expired Call error = %v; want process exit", err)
		}
		cc, ok := saved.(interface {
			CallCatching(runtime.Value, []runtime.Value) (runtime.Value, error)
		})
		if !ok {
			return fmt.Errorf("retained caller lost CallCatching")
		}
		if _, err := cc.CallCatching(callback, nil); !IsProcExit(err) {
			return fmt.Errorf("expired CallCatching error = %v; want process exit", err)
		}
		if err := saved.Spawn(callback, nil).Wait(); !IsProcExit(err) {
			return fmt.Errorf("expired Spawn error = %v; want process exit", err)
		}
		return nil
	}
	// An idle VM must not let the old callback open a new root process.
	if err := assertDead(); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	root := &runtime.BuiltinFunc{Fn: func(c runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		close(started)
		<-release
		// Also reject the old handle on the new root's own goroutine.
		if err := assertDead(); err != nil {
			return nil, err
		}
		return c.Call(callback, nil)
	}}
	go func() {
		_, err := v.Call(root, nil)
		result <- err
	}()
	<-started
	// Nor may a foreign callback join the process of a later root Call.
	err := assertDead()
	close(release)
	if rootErr := <-result; rootErr != nil {
		t.Fatal(rootErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	// Deliberate public root entry remains reusable after both processes die.
	got, err := v.Call(callback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("called", got); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff(int32(2), fired.Load()); diff != "" {
		t.Errorf("callback side effects (-want +got):\n%s", diff)
	}
}

func TestProcessCallerHeldScopeAndReentry(t *testing.T) {
	v := &VM{}
	v.EnsureProc()
	v.EnsureProc()
	var saved runtime.VMCaller
	root := &runtime.BuiltinFunc{Fn: func(c runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		saved = c
		return c.Call(&runtime.BuiltinFunc{Fn: func(nested runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			// Ordinary synchronous reentry keeps the owner's frame context.
			return vmOf(nested) == v, nil
		}}, nil)
	}}
	got, err := v.Call(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(true, got); diff != "" {
		t.Errorf("same-owner reentry (-want +got):\n%s", diff)
	}
	callback := &runtime.BuiltinFunc{Fn: func(c runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		// An idle held scope accepts a callback on private execution state.
		return vmOf(c) != v && c.Task() != nil, nil
	}}
	for hold := 0; hold < 2; hold++ {
		got, err := saved.Call(callback, nil)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(true, got); diff != "" {
			t.Errorf("held-scope callback (-want +got):\n%s", diff)
		}
		v.ReleaseProc()
	}
	if _, err := saved.Call(callback, nil); !IsProcExit(err) {
		t.Fatalf("released scope callback error = %v; want process exit", err)
	}
}

func TestReflectedCallbackKeepsProcess(t *testing.T) {
	v := &VM{}
	var callback func() string
	var fired atomic.Int32
	fn := &runtime.BuiltinFunc{Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
		fired.Add(1)
		return "called", nil
	}}
	root := &runtime.BuiltinFunc{Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
		// A raw VM entering a reflected host call must bind arguments before
		// the host can retain them, independently of the BuiltinFunc path.
		_, err := callReflectFunc("capture", reflect.ValueOf(func(f func() string) {
			callback = f
		}), v, []runtime.Value{fn})
		if err != nil {
			return nil, err
		}
		return callback(), nil
	}}
	got, err := v.Call(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("called", got); diff != "" {
		t.Error(diff)
	}
	late := &runtime.BuiltinFunc{Fn: func(runtime.VMCaller, []runtime.Value) (result runtime.Value, err error) {
		defer func() {
			if p := recover(); p != nil {
				if e, ok := p.(error); ok && IsProcExit(e) {
					return
				}
				err = fmt.Errorf("unexpected host callback panic: %v", p)
			} else {
				err = fmt.Errorf("expired reflected callback executed")
			}
		}()
		callback()
		return nil, nil
	}}
	// Check both an idle owner and a later live root on its own goroutine.
	if _, err := late.Fn(nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Call(late, nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int32(1), fired.Load()); diff != "" {
		t.Errorf("reflected callback side effects (-want +got):\n%s", diff)
	}
}

func TestFieldFactoryRemainsReusableAcrossRootCalls(t *testing.T) {
	v := &VM{}
	pool := &sync.Pool{}
	factory := &runtime.BuiltinFunc{Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
		return "made", nil
	}}
	initialize := &runtime.BuiltinFunc{Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
		// Generic value/field adaptation is deliberately outside the initial
		// retained host-argument boundary. Source packages use persistent
		// Pool.New factories (notably go/printer's package-global pool).
		fv, err := adaptFunc(factory, reflect.TypeFor[func() any](), v)
		if err != nil {
			return nil, err
		}
		pool.New = fv.Interface().(func() any)
		return nil, nil
	}}
	if _, err := v.Call(initialize, nil); err != nil {
		t.Fatal(err)
	}
	get := &runtime.BuiltinFunc{Fn: func(c runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		return callOpaqueStore("*sync.Pool", "Get", reflect.ValueOf(pool).MethodByName("Get"), c, nil)
	}}
	for range 2 {
		got, err := v.Call(get, nil)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("made", got); diff != "" {
			t.Errorf("reused field factory (-want +got):\n%s", diff)
		}
	}
}
