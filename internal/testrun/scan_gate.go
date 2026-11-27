package testrun

import (
	"path/filepath"

	"github.com/podhmo/minigo/resolve"
)

// scanForRun applies --tags plus the --tagged gate tag. The returned gate
// is keyed like syntax.File.Name: slash-normalized test file paths.
func scanForRun(dir string, tags []string, tagged string) (*scanFiles, map[string]bool, resolve.BuildConfig, error) {
	cfg := resolve.BuildConfig{Tags: tags}
	buildCfg := cfg
	if tagged != "" {
		buildCfg.Tags = append(append([]string{}, cfg.Tags...), tagged)
	}
	scan, err := scanTests(dir, buildCfg)
	if err != nil || tagged == "" {
		return scan, nil, buildCfg, err
	}
	plain, err := scanTests(dir, cfg)
	if err != nil {
		return nil, nil, buildCfg, err
	}
	inPlain := map[string]bool{}
	for _, f := range plain.all {
		inPlain[f] = true
	}
	gated := map[string]bool{}
	for _, f := range scan.all {
		if !inPlain[f] {
			gated[filepath.ToSlash(f)] = true
		}
	}
	return scan, gated, buildCfg, nil
}
