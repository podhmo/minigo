package main

import (
	"flag"
	"time"
)

var (
	pkg      = flag.String("pkg", "text/template", "stdlib package under test")
	src      = flag.String("src", "", "value for minigo --src (default: -pkg)")
	tests    = flag.String("tests", "exec_test.go,multi_test.go", "upstream test files to include, comma-separated")
	examples = flag.Bool("examples", false, "treat -tests as external <pkg>_test example files and run their Example funcs with // Output: checking")
	only     = flag.String("only", "", "run only tests whose name matches this regexp")
	work     = flag.String("work", "", "scratch work directory (default: mkdtemp)")
	keep     = flag.Bool("keep", false, "keep the work directory")
	bin      = flag.String("minigo", "", "prebuilt minigo binary (default: build ./cmd/minigo)")
	timeout  = flag.Duration("timeout", 60*time.Second, "per-test timeout")
	report   = flag.String("report", "", "also write the report to this file")
	runner   = flag.String("runner", "auto", "test runner: auto, process, or testrun (testrun is for internal TestXxx files only)")
	goroot   = flag.String("goroot", "", "real GOROOT (default: build.Default.GOROOT)")
	repoRoot = flag.String("repo", "../..", "repository root holding cmd/minigo")
)
