package crand

import (
	crand "crypto/rand"
	"fmt"
	"io"
	"math/big"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	"github.com/podhmo/minigo/runtime"
)

// Install binds crypto/rand so interpreted callers never reach
// crypto/internal/sysrand -> internal/syscall/unix (linknamed OS
// CSPRNG trampolines).
func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters, typeStr func(runtime.Value) string) {
	// crypto/rand: bound so interpreted callers — mime/multipart's
	// randomBoundary is the reported one (#1084) — never reach
	// crypto/internal/sysrand → internal/syscall/unix, whose GOROOT
	// sources read syscall symbols the bound package does not carry
	// and drive the OS CSPRNG through linknamed trampolines. Packages
	// importing crypto/internal/* directly (crypto/tls, crypto/rsa)
	// still reach interpreted internals — out of scope here.
	bind("crypto/rand", map[string]runtime.Value{
		// Reader is the real host io.Reader: `io.ReadFull(rand.Reader, b)`
		// in interpreted code reads through it via asReaderVM. A script
		// `rand.Reader = r` write is honored only by member reads of
		// Reader — Read/Text below keep the host CSPRNG (real Go
		// redirects them).
		"Reader": &runtime.GoValue{V: crand.Reader},
		"Text":   h.Fn("rand.Text", func(a []any) (any, error) { return crand.Text(), nil }, crand.Text),
		"Read": &runtime.BuiltinFunc{Name: "rand.Read", Target: crand.Read, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("rand.Read needs 1 arg, got %d", len(args))
			}
			bs, commit := stdlib.BorrowBytes(args[0])
			n, rerr := crand.Read(bs)
			commit()
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(rerr)}}, nil
		}},
		"Int": randBig(ad, "rand.Int", crand.Int, func(vc runtime.VMCaller, r io.Reader, args []runtime.Value) (*big.Int, error) {
			max, err := bigIntOf(typeStr, vc, args[1])
			if err != nil {
				return nil, fmt.Errorf("rand.Int: %w", err)
			}
			return crand.Int(r, max)
		}),
		"Prime": randBig(ad, "rand.Prime", crand.Prime, func(_ runtime.VMCaller, r io.Reader, args []runtime.Value) (*big.Int, error) {
			return crand.Prime(r, stdlib.IntOf(hostbridge.GoNative(args[1])))
		}),
	})
}

// randBig adapts crypto/rand's (io.Reader, x) (*big.Int, error) helpers:
// the reader crosses through asReaderVM (the bound rand.Reader or a
// script reader); f gets the raw tail args since Int's max may need the
// VM to marshal a script-built math/big value (see bigIntOf).
func randBig(ad *stdio.Adapters, name string, target any, f func(vc runtime.VMCaller, r io.Reader, args []runtime.Value) (*big.Int, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: target, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		r, err := ad.AsReaderVM(vc, args[0])
		if err != nil {
			return nil, err
		}
		return stdlib.RetErr2(f(vc, r, args))
	}}
}

// bigIntOf resolves a *big.Int argument: a host-produced one (rand.Int's
// or rand.Prime's own result, or a bound helper's) unboxes directly; a
// script-built one rebuilds through the value's public Sign/Bytes
// methods — math/big itself stays source-interpreted, so hostbridge.GoNative only
// sees a struct.
func bigIntOf(typeStr func(runtime.Value) string, vc runtime.VMCaller, v runtime.Value) (*big.Int, error) {
	if bi, ok := hostbridge.GoNative(v).(*big.Int); ok {
		return bi, nil
	}
	if vc != nil {
		signM, okS := vc.Member(v, "Sign")
		bytesM, okB := vc.Member(v, "Bytes")
		if okS && okB {
			signR, err := vc.Call(signM, nil)
			if err != nil {
				return nil, err
			}
			bytesR, err := vc.Call(bytesM, nil)
			if err != nil {
				return nil, err
			}
			z := new(big.Int).SetBytes(stdlib.ByteSlice(bytesR))
			if stdlib.Int64Of(signR) < 0 {
				z.Neg(z)
			}
			return z, nil
		}
	}
	return nil, fmt.Errorf("not usable as a *big.Int: %s", typeStr(v))
}
