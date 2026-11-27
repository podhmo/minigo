package main

import "github.com/podhmo/minigo"

// testPackageModes converts the repeated --deny/--src flag values into
// the engine package-mode map.
func testPackageModes(denys, srcs []string) map[string]minigo.PackageMode {
	modes := map[string]minigo.PackageMode{}
	for _, p := range denys {
		modes[p] = minigo.ModeDeny
	}
	for _, p := range srcs {
		modes[p] = minigo.ModeSource
	}
	return modes
}
