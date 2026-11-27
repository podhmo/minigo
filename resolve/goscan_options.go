package resolve

import (
	"fmt"

	"github.com/podhmo/minigo/pkg/locator"
)

// GoScanOption customizes [NewGoScanResolver].
type GoScanOption func(*goScanOptions)

type goScanOptions struct{ goRoot string }

// WithGOROOT resolves standard-library packages under goRoot instead of
// the host toolchain's `go env GOROOT` answer. It lets an in-process
// caller use a scratch or symlink-farm GOROOT without mutating the
// process environment.
func WithGOROOT(goRoot string) GoScanOption {
	return func(o *goScanOptions) { o.goRoot = goRoot }
}

// NewGoScanResolver creates a resolver for the module whose go.mod is
// nearest at or above startDir. Without such a go.mod it anchors at
// startDir and still resolves GOROOT and module-cache packages.
func NewGoScanResolver(startDir string, cfg BuildConfig, opts ...GoScanOption) (*GoScanResolver, error) {
	var scanOpts goScanOptions
	for _, opt := range opts {
		opt(&scanOpts)
	}
	locatorOpts := []locator.Option{locator.WithGoModuleResolver()}
	if scanOpts.goRoot != "" {
		locatorOpts = append(locatorOpts, locator.WithGOROOT(scanOpts.goRoot))
	}
	l, err := locator.New(startDir, locatorOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating locator at %s: %w", startDir, err)
	}
	return &GoScanResolver{loc: l, cfg: cfg, goRoot: scanOpts.goRoot}, nil
}
