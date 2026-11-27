package testrun

import (
	"context"
	"errors"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun/mtesting"
	"github.com/podhmo/minigo/runtime"
)

// runTopCleanups drains top-level cleanups one Engine.Call each. A
// cleanup's own stop error is flag state rather than an execution error;
// keeping it would hide a panic raised by a later cleanup. Once the
// suite context is done, no further call starts: the timed-out cleanup
// was abandoned rather than stopped, so a dependent cleanup must not run
// concurrently with it.
func runTopCleanups(ctx context.Context, e *minigo.Engine, testingPkg *runtime.Package, t *mtesting.T) error {
	var first error
	for t.CleanupCount() > 0 {
		if ctx.Err() != nil {
			break
		}
		err := callCleanup(ctx, e, testingPkg, t)
		if err == nil {
			continue
		}
		if st, ok := mtesting.StopOf(err); ok && st == t {
			continue
		}
		if first == nil {
			first = err
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			break
		}
	}
	return first
}
