package testrun

import (
	"context"
	"fmt"
	"time"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun/mtesting"
	"github.com/podhmo/minigo/runtime"
)

// callAsync runs fn in a goroutine so the deadline can stop waiting.
// After expiry it still waits for the killed interpreter to finish
// unwinding, so deferred work cannot overlap the next test.
func callAsync(ctx context.Context, fn func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		<-ch
		return fmt.Errorf("test timed out: %w", ctx.Err())
	}
}

// callTest invokes the test function through callAsync.
func callTest(ctx context.Context, e *minigo.Engine, pkg *runtime.Package, name string, t *mtesting.T) error {
	return callAsync(ctx, func() error {
		start := time.Now()
		_, err := e.Call(ctx, pkg, name, &runtime.GoValue{V: t})
		t.SetElapsed(time.Since(start))
		return err
	})
}

// callCleanup runs one top-level cleanup through the bound __cleanup
// member of the testing package in a fresh Engine.Call.
func callCleanup(ctx context.Context, e *minigo.Engine, testingPkg *runtime.Package, t *mtesting.T) error {
	return callAsync(ctx, func() error {
		_, err := e.Call(ctx, testingPkg, "__cleanup", &runtime.GoValue{V: t})
		return err
	})
}
