package testrun

import (
	"context"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun/mtesting"
	"github.com/podhmo/minigo/runtime"
)

// isolatedSession forks the suite engine and reloads the test package in
// the fork. Parse caches remain shared, while package globals, init state,
// and mutations made by earlier tests stay behind in the parent session.
func isolatedSession(ctx context.Context, e *minigo.Engine, suite *mtesting.Suite, abs string) (*minigo.Engine, *runtime.Package, *runtime.Package, error) {
	se := e.NewSession()
	se.Bind("testing", mtesting.Symbols(suite))
	se.Bind("flag", mtesting.FlagSymbols(suite))
	pkg, err := se.Package(ctx, abs)
	if err != nil {
		return nil, nil, nil, err
	}
	testingPkg, err := se.Package(ctx, "testing")
	if err != nil {
		return nil, nil, nil, err
	}
	return se, pkg, testingPkg, nil
}
